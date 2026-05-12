// Package runner orchestrates scenario execution: provisioning monitors,
// driving failure injection on the target fleet, recording ground-truth
// events, waiting out the grace period, and collecting adapter results.
package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// recorder is the subset of *db.DB the runner uses. Defined here (not in
// internal/db) so tests can inject a fake without depending on the real
// MySQL driver. *db.DB satisfies it structurally.
type recorder interface {
	InsertRun(ctx context.Context, r db.RunRecord) error
	CloseRun(ctx context.Context, runID string, endedAt time.Time, reason string) error
	InsertGroundTruthEvent(ctx context.Context, e db.GroundTruthEvent) error
	InsertMonitorReport(ctx context.Context, r db.MonitorReportRow) error
}

// RunOption is a functional option for Run. Used by RunCampaign to
// stamp campaign_run_id onto each scenario_runs row without changing
// the public Run signature for direct (non-campaign) callers.
type RunOption func(*runOpts)

type runOpts struct {
	campaignRunID        string
	requireCooldownReset bool
	parameters           map[string]any
}

// WithCampaignRunID stamps the given campaign_run_id on the run row so
// downstream queries can group every replay back to its parent campaign.
func WithCampaignRunID(id string) RunOption {
	return func(o *runOpts) { o.campaignRunID = id }
}

// withRequireCooldownReset gates adapters that cannot clear alert
// cooldown state between runs. It is used by campaign mode, where many
// repeated same-target replays make cooldown carry-over a real bias.
func withRequireCooldownReset() RunOption {
	return func(o *runOpts) { o.requireCooldownReset = true }
}

func withRunParameters(params map[string]any) RunOption {
	return func(o *runOpts) { o.parameters = params }
}

// Run executes a scenario end-to-end and returns the run ID.
//
//  1. Provision each adapter.
//  2. Activate failures on target fleet; record failure_start events.
//  3. Wait for scenario duration.
//  4. Deactivate failures; record failure_end events.
//  5. Wait for grace period.
//  6. Retrieve results from each adapter; write monitor_reports.
//  7. Deprovision all adapters (unconditionally).
//  8. Close the run with a resolution_reason.
//
// Ground-truth event log writes are treated as fatal: if the harness can't
// record a failure_start / failure_end, the run is aborted with
// resolution_reason = "ground_truth_log_failure" because metrics are
// recomputed from the log and a missed write silently corrupts the record.
func Run(ctx context.Context, sc *scenario.Scenario, fl *fleet.Config, database recorder, adapters []adapter.Adapter, svcCfg *serviceconfig.Config, opts ...RunOption) (string, error) {
	var o runOpts
	for _, opt := range opts {
		opt(&o)
	}

	runID := newRunID()
	startedAt := time.Now()

	seed := effectiveSeed(sc, startedAt)

	target, err := resolveTarget(fl, sc.Target)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}
	endpoint, err := targetEndpointForRun(sc, target, runID, seed)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}

	dnsBaseline := newDNSBaselinePlan(sc, fl, svcCfg, endpoint.host)

	token, err := readFleetToken(fl)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}
	monitorURL := monitorTargetURLForRun(sc, endpoint, runID)

	params := map[string]any{
		"check_frequency": sc.CheckFrequency.String(),
		"grace_period":    sc.GracePeriod.String(),
		"duration":        sc.Duration.String(),
		"failures":        len(sc.Failures),
		"monitor_url":     monitorURL,
	}
	if sc.FreshHostname {
		params["fresh_hostname"] = true
	}
	if dnsBaseline != nil {
		params["dns_baseline"] = "tls_only"
	}
	for k, v := range o.parameters {
		params[k] = v
	}
	if err := database.InsertRun(ctx, db.RunRecord{
		ID:              runID,
		ScenarioID:      sc.ID,
		ScenarioVersion: sc.Version,
		Seed:            seed,
		TargetID:        sc.Target,
		CampaignID:      o.campaignRunID,
		Parameters:      params,
		StartedAt:       startedAt,
	}); err != nil {
		return "", fmt.Errorf("runner: insert run: %w", err)
	}

	resolutionReason := "planned_completion"
	defer func() {
		// run_end is fire-and-forget: we're already exiting. CloseRun is
		// the canonical run-end record; if it fails too the operator
		// sees both errors in the log.
		if err := logEvent(ctx, database, runID, sc.Target, "run_end", "", map[string]any{"reason": resolutionReason}); err != nil {
			log.Printf("runner: log run_end: %v", err)
		}
		if err := database.CloseRun(ctx, runID, time.Now(), resolutionReason); err != nil {
			log.Printf("runner: close run: %v", err)
		}
		log.Printf("runner: run %s closed: %s", runID, resolutionReason)
	}()

	if err := logEvent(ctx, database, runID, sc.Target, "run_start", "", nil); err != nil {
		resolutionReason = "ground_truth_log_failure"
		return runID, err
	}
	if dnsBaseline != nil {
		if err := dnsBaseline.record(ctx, database, runID, sc.Target, "pre_provision"); err != nil {
			resolutionReason = "ground_truth_log_failure"
			return runID, err
		}
	}

	// If the scenario declares a maintenance window, persist it as a
	// pair of ground-truth events (maintenance_start at window.Start,
	// maintenance_end at window.End) so the measurement engine can read
	// the window back the same way it reads failure_start / failure_end.
	// Both rows are written now, at startedAt; their occurred_at fields
	// carry the *configured* window times (which may be in the future).
	if sc.Maintenance != nil {
		mw := maintenanceWindowFor(sc, startedAt)
		if mw != nil {
			if err := database.InsertGroundTruthEvent(ctx, db.GroundTruthEvent{
				RunID: runID, EventType: "maintenance_start", TargetID: sc.Target, OccurredAt: mw.Start,
			}); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, fmt.Errorf("ground_truth_log_failure: maintenance_start: %w", err)
			}
			if err := database.InsertGroundTruthEvent(ctx, db.GroundTruthEvent{
				RunID: runID, EventType: "maintenance_end", TargetID: sc.Target, OccurredAt: mw.End,
			}); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, fmt.Errorf("ground_truth_log_failure: maintenance_end: %w", err)
			}
		}
	}

	// Determine effective call budget per adapter.
	budgets := make(map[string]int, len(adapters))
	for _, a := range adapters {
		limit := fl.Adapters[a.ServiceID()].MaxCallsPerRun
		if limit == 0 {
			limit = a.Capabilities().DefaultMaxCallsPerRun
		}
		budgets[a.ServiceID()] = limit
	}

	handles, provisionErr := provisionAdapters(ctx, sc, endpoint, adapters, database, runID, startedAt, o.requireCooldownReset)
	if provisionErr {
		resolutionReason = "adapter_error"
	}

	// Deprovision unconditionally on exit, even on abort.
	// Uses a fresh context (see deprovisionAll) because the run's outer ctx
	// may already be cancelled when this defer fires (e.g. SIGINT during
	// the failure window). With the outer ctx, Deprovision calls would
	// fail immediately with context.Canceled and leak monitors.
	defer func() {
		if errs := deprovisionAll(handles); errs > 0 && resolutionReason == "planned_completion" {
			resolutionReason = "cleanup_error"
		}
	}()

	if len(handles) == 0 {
		log.Printf("runner: no provisioned adapters; skipping failure injection")
		return runID, nil
	}

	// Walk the failure event timeline. Each failure produces one activate
	// event at start+offset and one deactivate event after its effective
	// duration; scheduleFailureEvents sorts them all into a single
	// time-ordered list so staggered scenarios just fall out of the same
	// loop as simultaneous ones (offset = 0).
	earliestStart := time.Now()
	events := scheduleFailureEvents(earliestStart, sc.Duration, sc.Failures)

	// Target-side auto-expiry timer: must outlast the latest deactivate
	// plus a generous safety margin in case the harness's deactivate is
	// delayed or fails. Without this, a long-offset failure could expire
	// on the target before the harness gets to it.
	targetAutoExpiry := latestFailureEndOffset(sc.Duration, sc.Failures) + sc.GracePeriod + 30*time.Second

	var failureStarted, failureEnded time.Time
	dnsBaselineActive := 0
	for _, e := range events {
		if waitFor := time.Until(e.at); waitFor > 0 {
			if dnsBaseline != nil && dnsBaselineActive > 0 {
				if err := dnsBaseline.wait(ctx, database, runID, sc.Target, waitFor); err != nil {
					if ctx.Err() != nil {
						resolutionReason = "aborted"
						return runID, ctx.Err()
					}
					resolutionReason = "ground_truth_log_failure"
					return runID, err
				}
			} else {
				select {
				case <-time.After(waitFor):
				case <-ctx.Done():
					resolutionReason = "aborted"
					return runID, ctx.Err()
				}
			}
		}

		f := e.failure
		if e.activate {
			fp := failureParams(sc, f)
			sourceCIDRs := collectCIDRs(f.Regions, svcCfg)
			if len(f.Regions) > 0 && len(sourceCIDRs) == 0 {
				log.Printf("runner: warning: failure %s has regions %v but no matching probe_ranges found in services.toml", f.Type, f.Regions)
			}
			host, path := targetHostPathForFailure(endpoint, f)
			members, err := controlMembersForFailure(fl, target, f, seed)
			if err != nil {
				log.Printf("runner: select control members for %s: %v", f.Type, err)
				if isDNSFailureType(f.Type) {
					exposure := dnsExposureResult{
						Host:        endpoint.host,
						FailureType: f.Type,
						Observable:  false,
						Reason:      err.Error(),
					}
					fp["dns_exposure"] = exposure
					if err := logEvent(ctx, database, runID, sc.Target, "setup_exposure_failure", f.Type, fp); err != nil {
						resolutionReason = "ground_truth_log_failure"
						return runID, err
					}
					logFailureNotObservable(ctx, database, runID, handles, f, exposure)
					resolutionReason = "setup_exposure_failure"
					return runID, nil
				}
				resolutionReason = "adapter_error"
				return runID, fmt.Errorf("runner: select control members for %s: %w", f.Type, err)
			}
			req := control.ActivateRequest{
				RunID: runID,
				Seed:  seed,
				Failure: control.FailureSpec{
					Type:        f.Type,
					Host:        host,
					Path:        path,
					Duration:    targetAutoExpiry,
					Rate:        f.Rate,
					Params:      fp,
					SourceCIDRs: sourceCIDRs,
				},
			}
			if f.Type == "dns_ns_unavailable" {
				// For nameserver-availability scenarios, rate chooses the
				// fraction of DNS members to affect. Once a member is selected,
				// every query to that member should see the configured mode.
				req.Failure.Rate = 1.0
			}
			if err := activateFailure(ctx, members, token, fl.Control.Timeout, req); err != nil {
				log.Printf("runner: activate %s: %v", f.Type, err)
				resolutionReason = "adapter_error"
				return runID, fmt.Errorf("runner: activate %s: %w", f.Type, err)
			}
			fp["control_members"] = controlMemberIDs(members)
			if isDNSFailureType(f.Type) {
				exposure := checkDNSFailureExposure(ctx, f, endpoint.host, members, fl.Control.Timeout)
				fp["dns_exposure"] = exposure
				if !exposure.Observable {
					if err := deactivateFailure(ctx, members, token, fl.Control.Timeout, control.DeactivateRequest{
						RunID:       runID,
						FailureType: f.Type,
						Host:        host,
						Path:        path,
					}); err != nil {
						log.Printf("runner: deactivate %s after exposure failure: %v", f.Type, err)
					}
					if err := logEvent(ctx, database, runID, sc.Target, "setup_exposure_failure", f.Type, fp); err != nil {
						resolutionReason = "ground_truth_log_failure"
						return runID, err
					}
					logFailureNotObservable(ctx, database, runID, handles, f, exposure)
					resolutionReason = "setup_exposure_failure"
					return runID, nil
				}
			}
			if err := logEvent(ctx, database, runID, sc.Target, "failure_start", f.Type, fp); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, err
			}
			log.Printf("runner: activated %s on %s", f.Type, sc.Target)
			if failureStarted.IsZero() {
				failureStarted = time.Now()
			}
			if dnsBaseline != nil {
				dnsBaselineActive++
				if err := dnsBaseline.record(ctx, database, runID, sc.Target, "active_start"); err != nil {
					resolutionReason = "ground_truth_log_failure"
					return runID, err
				}
			}
		} else {
			host, path := targetHostPathForFailure(endpoint, f)
			members, err := controlMembersForFailure(fl, target, f, seed)
			if err != nil {
				log.Printf("runner: select control members for deactivate %s: %v", f.Type, err)
				resolutionReason = "adapter_error"
				return runID, fmt.Errorf("runner: select control members for deactivate %s: %w", f.Type, err)
			}
			req := control.DeactivateRequest{
				RunID:       runID,
				FailureType: f.Type,
				Host:        host,
				Path:        path,
			}
			if err := deactivateFailure(ctx, members, token, fl.Control.Timeout, req); err != nil {
				log.Printf("runner: deactivate %s: %v", f.Type, err)
			}
			if err := logEvent(ctx, database, runID, sc.Target, "failure_end", f.Type, nil); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, err
			}
			log.Printf("runner: deactivated %s", f.Type)
			failureEnded = time.Now()
			if dnsBaseline != nil {
				if err := dnsBaseline.record(ctx, database, runID, sc.Target, "post_deactivation"); err != nil {
					resolutionReason = "ground_truth_log_failure"
					return runID, err
				}
				if dnsBaselineActive > 0 {
					dnsBaselineActive--
				}
			}
		}
	}

	// Wait for grace period.
	log.Printf("runner: grace period %v", sc.GracePeriod)
	select {
	case <-time.After(sc.GracePeriod):
	case <-ctx.Done():
		resolutionReason = "aborted"
		return runID, ctx.Err()
	}
	gracePeriodEnds := time.Now()
	if dnsBaseline != nil {
		if err := dnsBaseline.record(ctx, database, runID, sc.Target, "post_recovery"); err != nil {
			resolutionReason = "ground_truth_log_failure"
			return runID, err
		}
		if dnsBaseline.unstable {
			if resolutionReason == "planned_completion" {
				resolutionReason = adapter.ReasonSetupEnvironmentDNSUnstable
			}
			logDNSBaselineUnstable(ctx, database, runID, handles, dnsBaseline)
		}
	}

	// Retrieve results.
	window := adapter.RunWindow{
		RunID:          runID,
		FailureStarted: failureStarted,
		FailureEnded:   failureEnded,
		GracePeriodEnd: gracePeriodEnds,
	}
	callsMade := make(map[string]int, len(handles))
	for _, p := range handles {
		svcID := p.a.ServiceID()
		budget := budgets[svcID]
		if budget > 0 && callsMade[svcID] >= budget {
			log.Printf("runner: %s: budget exceeded (%d calls limit)", svcID, budget)
			resolutionReason = "budget_exceeded"
			logMonitorReport(ctx, database, runID, p.a, adapter.RetrieveResult{
				Status: adapter.RetrieveUnknown,
				Reason: fmt.Sprintf("budget_exceeded: limit %d", budget),
			})
			continue
		}
		callsMade[svcID]++

		result, err := p.a.Retrieve(ctx, p.handle, window)
		if err != nil {
			log.Printf("runner: retrieve %s: %v", p.a.ServiceID(), err)
			logMonitorReport(ctx, database, runID, p.a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     fmt.Sprintf("retrieve %s: %v", p.a.ServiceID(), err),
				ReasonCode: adapter.ReasonAdapterError,
			})
			resolutionReason = "adapter_error"
			continue
		}
		logMonitorReport(ctx, database, runID, p.a, result)
		log.Printf("runner: retrieved %s: status=%s reports=%d", p.a.ServiceID(), result.Status, len(result.Reports))
	}

	return runID, nil
}

// effectiveSeed returns the seed Run() records on the run row. If the
// scenario specifies an explicit seed, that wins (for reproducibility);
// otherwise the seed defaults to startedAt.UnixNano(), which is
// recorded so an operator can replay the same run later by pinning
// `seed = <recorded value>` in the scenario.
//
// The CLAUDE.md invariant is "the seed is recorded in the run record
// for every run" — extracting this helper makes that invariant
// directly testable without spinning up the full Run() machinery.
func effectiveSeed(sc *scenario.Scenario, startedAt time.Time) int64 {
	if sc.Seed != nil {
		return *sc.Seed
	}
	return startedAt.UnixNano()
}

func resolveTarget(fl *fleet.Config, targetID string) (fleet.Target, error) {
	for _, t := range fl.Targets {
		if t.ID == targetID {
			return t, nil
		}
	}
	return fleet.Target{}, fmt.Errorf("target %q not found in fleet config", targetID)
}

func readFleetToken(fl *fleet.Config) (string, error) {
	if fl.Control.AuthTokenFile == "" {
		return "", fmt.Errorf("fleet: control.auth_token_file is required")
	}
	data, err := os.ReadFile(fl.Control.AuthTokenFile)
	if err != nil {
		return "", fmt.Errorf("fleet: read auth_token_file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// logEvent writes one ground-truth event row. Returns the underlying error
// so the caller can decide whether to abort — for the runner, any DB
// failure here is fatal because the event log is the canonical record
// metrics are derived from.
func logEvent(ctx context.Context, database recorder, runID, targetID, eventType, failureType string, details any) error {
	err := database.InsertGroundTruthEvent(ctx, db.GroundTruthEvent{
		RunID:       runID,
		EventType:   eventType,
		TargetID:    targetID,
		FailureType: failureType,
		OccurredAt:  time.Now(),
		Details:     details,
	})
	if err != nil {
		log.Printf("runner: log event %s: %v", eventType, err)
		return fmt.Errorf("ground_truth_log_failure: %s: %w", eventType, err)
	}
	return nil
}

// logMonitorReport writes retrieve results to the database. The adapter is
// passed (rather than just its ID) so the row can include the normalized
// classification: each adapter owns its own raw→normalized mapping.
//
// a may be nil only for Unknown-without-reports results — those rows have
// no classification to normalize.
func logMonitorReport(ctx context.Context, database recorder, runID string, a adapter.Adapter, result adapter.RetrieveResult) {
	now := time.Now()
	serviceID := ""
	if a != nil {
		serviceID = a.ServiceID()
	}
	if len(result.Reports) == 0 {
		if err := database.InsertMonitorReport(ctx, db.MonitorReportRow{
			RunID:                 runID,
			ServiceID:             serviceID,
			RetrieveStatus:        string(result.Status),
			RetrieveUnknownReason: result.Reason,
			ReasonCode:            result.ReasonCode,
			RetrievedAt:           now,
			Metadata:              result.Metadata,
		}); err != nil {
			log.Printf("runner: insert monitor_report (%s/no-events): %v", serviceID, err)
		}
		return
	}
	for _, r := range result.Reports {
		normalized := a.Normalize(r.RawClassification)
		var reportedAt *time.Time
		if !r.ReportedAt.IsZero() {
			t := r.ReportedAt
			reportedAt = &t
		}
		if err := database.InsertMonitorReport(ctx, db.MonitorReportRow{
			RunID:                    runID,
			ServiceID:                serviceID,
			RetrieveStatus:           string(result.Status),
			RetrieveUnknownReason:    result.Reason,
			ReasonCode:               result.ReasonCode,
			EventType:                string(r.EventType),
			RawClassification:        r.RawClassification,
			NormalizedClassification: normalized,
			ReportedAt:               reportedAt,
			RetrievedAt:              now,
			Metadata:                 r.Metadata,
		}); err != nil {
			log.Printf("runner: insert monitor_report (%s/%s): %v", serviceID, r.EventType, err)
		}
	}
}

// provisionAdapters walks the adapter list, applies capability gates,
// and provisions adapters that pass. Capability mismatches produce a
// monitor_reports row with reason_code = "capability_mismatch" and skip
// Provision; adapters whose Provision call returns an error contribute
// to provisionErr and produce a monitor_reports row with reason_code =
// "adapter_error" so provider/API reliability is queryable from the
// database instead of only from runner logs.
//
// Extracted from Run() so the gate logic is unit-testable without
// spinning up the full Run() machinery (target HTTP plane, control
// token files, etc.). Run() consumes the (handles, provisionErr) pair
// and translates provisionErr=true into resolution_reason="adapter_error".
func provisionAdapters(
	ctx context.Context,
	sc *scenario.Scenario,
	endpoint targetEndpoint,
	adapters []adapter.Adapter,
	database recorder,
	runID string,
	startedAt time.Time,
	requireCooldownReset bool,
) (handles []provisioned, provisionErr bool) {
	for _, a := range adapters {
		caps := a.Capabilities()
		if !caps.SupportsMonitorKind(sc.MonitorKind) {
			log.Printf("runner: skip %s: scenario requires monitor_kind=%s (not supported)", a.ServiceID(), sc.MonitorKind)
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     fmt.Sprintf("scenario requires monitor_kind = %s; adapter does not support it", sc.MonitorKind),
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if sc.CheckFrequency < caps.MinCheckFrequency {
			log.Printf("runner: skip %s: check_frequency %v < min %v", a.ServiceID(), sc.CheckFrequency, caps.MinCheckFrequency)
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     fmt.Sprintf("check_frequency %v < min %v", sc.CheckFrequency, caps.MinCheckFrequency),
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if sc.Keyword != "" && !caps.SupportsKeyword {
			log.Printf("runner: skip %s: scenario requires keyword monitoring (not supported)", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "scenario requires keyword monitoring; adapter SupportsKeyword = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if sc.KeywordCheck == adapter.KeywordCheckAbsent && !caps.SupportsInvertedKeyword {
			log.Printf("runner: skip %s: scenario requires inverted keyword check (not supported)", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "scenario requires keyword_check = absent; adapter SupportsInvertedKeyword = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if sc.Maintenance != nil && !caps.SupportsMaintenanceWindows {
			log.Printf("runner: skip %s: scenario requires a maintenance window (not supported)", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "scenario requires a [maintenance] block; adapter SupportsMaintenanceWindows = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if sc.ResponseTimeThreshold > 0 && !caps.SupportsResponseTimeThreshold {
			log.Printf("runner: skip %s: scenario requires response-time threshold (not supported)", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "scenario requires response_time_threshold; adapter SupportsResponseTimeThreshold = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if len(sc.RequestHeaders) > 0 && !caps.SupportsRequestHeaders {
			log.Printf("runner: skip %s: scenario requires custom request headers (not supported)", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "scenario requires request_headers; adapter SupportsRequestHeaders = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}
		if requireCooldownReset && !caps.SupportsCooldownReset {
			log.Printf("runner: skip %s: campaign requires cooldown reset support", a.ServiceID())
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     "campaign replays require clean alert state; adapter SupportsCooldownReset = false",
				ReasonCode: adapter.ReasonCapabilityMismatch,
			})
			continue
		}

		tgt := adapter.Target{ID: sc.Target, URL: monitorTargetURLForRun(sc, endpoint, runID)}
		cfg := adapter.ProvisionConfig{
			CheckFrequency:        sc.CheckFrequency,
			MonitorKind:           sc.MonitorKind,
			Keyword:               sc.Keyword,
			KeywordCheck:          sc.KeywordCheck,
			ForbiddenKeywords:     forbiddenKeywordsForScenario(sc),
			ResponseTimeThreshold: sc.ResponseTimeThreshold,
			RequestHeaders:        sc.RequestHeaders,
		}
		cfg.MaintenanceWindow = maintenanceWindowFor(sc, startedAt)
		handle, err := a.Provision(ctx, tgt, cfg)
		if err != nil {
			log.Printf("runner: provision %s: %v", a.ServiceID(), err)
			logMonitorReport(ctx, database, runID, a, adapter.RetrieveResult{
				Status:     adapter.RetrieveUnknown,
				Reason:     fmt.Sprintf("provision %s: %v", a.ServiceID(), err),
				ReasonCode: adapter.ReasonAdapterError,
			})
			provisionErr = true
			continue
		}
		handles = append(handles, provisioned{a: a, handle: handle})
		log.Printf("runner: provisioned %s (monitor %s)", a.ServiceID(), handle.MonitorID)
	}
	return handles, provisionErr
}

type targetEndpoint struct {
	host string
	path string
}

func monitorTargetURL(sc *scenario.Scenario, target fleet.Target) string {
	return monitorTargetURLForEndpoint(sc, targetEndpointForScenario(sc, target))
}

func monitorTargetURLForEndpoint(sc *scenario.Scenario, endpoint targetEndpoint) string {
	return monitorTargetURLForRun(sc, endpoint, "")
}

func monitorTargetURLForRun(sc *scenario.Scenario, endpoint targetEndpoint, runID string) string {
	// Use the first site's hostname so adapters register against the
	// domain name rather than the infrastructure address. Monitoring
	// services check by domain, not by IP.
	scheme := "http"
	if scenarioUsesTLS(sc) {
		scheme = "https"
	}
	u := url.URL{
		Scheme: scheme,
		Host:   endpoint.host,
		Path:   endpoint.path,
	}
	if token := monitorURLToken(sc, runID); token != "" {
		u.RawQuery = "ub=" + token
	}
	return u.String()
}

func targetEndpointForScenario(sc *scenario.Scenario, target fleet.Target) targetEndpoint {
	endpoint, err := targetEndpointForRun(sc, target, "", 0)
	if err == nil {
		return endpoint
	}
	if len(target.Sites) == 0 {
		return targetEndpoint{host: target.Address}
	}
	site := target.Sites[0]
	return targetEndpoint{
		host: site.Host,
		path: selectMonitorPath(sc, site.Paths),
	}
}

func targetEndpointForRun(sc *scenario.Scenario, target fleet.Target, runID string, seed int64) (targetEndpoint, error) {
	if sc != nil && sc.FreshHostname {
		if len(target.GeneratedSites) == 0 {
			return targetEndpoint{}, fmt.Errorf("scenario %q requested fresh_hostname but target %q has no generated_sites", sc.ID, target.ID)
		}
		rangeIndex := stableRunIndex(sc, runID, seed, "generated-range", len(target.GeneratedSites))
		generated := target.GeneratedSites[rangeIndex]
		siteIndex := generated.Start + stableRunIndex(sc, runID, seed, "generated-host", generated.Count)
		return targetEndpoint{
			host: fmt.Sprintf(generated.HostPattern, siteIndex),
			path: selectMonitorPath(sc, generated.Paths),
		}, nil
	}
	if len(target.Sites) == 0 {
		return targetEndpoint{host: target.Address}, nil
	}
	site := target.Sites[0]
	return targetEndpoint{
		host: site.Host,
		path: selectMonitorPath(sc, site.Paths),
	}, nil
}

func selectMonitorPath(sc *scenario.Scenario, paths []string) string {
	normalized := normalizeSitePaths(paths)
	if len(normalized) == 0 {
		return "/"
	}
	candidates := normalized
	if len(normalized) > 1 {
		nonRoot := make([]string, 0, len(normalized)-1)
		for _, path := range normalized {
			if path != "/" {
				nonRoot = append(nonRoot, path)
			}
		}
		if len(nonRoot) > 0 {
			candidates = nonRoot
		}
	}
	return candidates[stableScenarioIndex(sc, len(candidates))]
}

func normalizeSitePaths(paths []string) []string {
	if len(paths) == 0 {
		return []string{"/"}
	}
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			path = "/"
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		if !seen[path] {
			out = append(out, path)
			seen[path] = true
		}
	}
	if len(out) == 0 {
		return []string{"/"}
	}
	return out
}

func stableScenarioIndex(sc *scenario.Scenario, n int) int {
	if n <= 1 {
		return 0
	}
	key := ""
	if sc != nil {
		key = sc.ID
	}
	sum := sha256.Sum256([]byte(key))
	value := uint64(sum[0])<<56 | uint64(sum[1])<<48 | uint64(sum[2])<<40 | uint64(sum[3])<<32 |
		uint64(sum[4])<<24 | uint64(sum[5])<<16 | uint64(sum[6])<<8 | uint64(sum[7])
	return int(value % uint64(n))
}

func stableRunIndex(sc *scenario.Scenario, runID string, seed int64, salt string, n int) int {
	if n <= 1 {
		return 0
	}
	scID := ""
	if sc != nil {
		scID = sc.ID
	}
	key := fmt.Sprintf("%s\x00%s\x00%d\x00%s", scID, runID, seed, salt)
	sum := sha256.Sum256([]byte(key))
	value := uint64(sum[0])<<56 | uint64(sum[1])<<48 | uint64(sum[2])<<40 | uint64(sum[3])<<32 |
		uint64(sum[4])<<24 | uint64(sum[5])<<16 | uint64(sum[6])<<8 | uint64(sum[7])
	return int(value % uint64(n))
}

func monitorURLToken(sc *scenario.Scenario, runID string) string {
	if sc == nil || strings.TrimSpace(sc.ID) == "" {
		return ""
	}
	key := sc.ID
	if runID != "" {
		key += "\x00" + runID
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:4])
}

func scenarioUsesTLS(sc *scenario.Scenario) bool {
	if sc == nil {
		return false
	}
	for _, f := range sc.Failures {
		if strings.HasPrefix(f.Type, "tls_") {
			return true
		}
	}
	return false
}

// maintenanceWindowFor converts a scenario's relative [maintenance]
// offsets into absolute timestamps anchored at startedAt (the run's
// canonical start). Returns nil when the scenario has no [maintenance]
// block. A few seconds of drift between startedAt and the actual first
// failure activation is acceptable because vendor maintenance APIs are
// minute-grained.
func maintenanceWindowFor(sc *scenario.Scenario, startedAt time.Time) *adapter.MaintenanceWindow {
	if sc == nil || sc.Maintenance == nil {
		return nil
	}
	windowStart := startedAt.Add(sc.Maintenance.StartOffset)
	return &adapter.MaintenanceWindow{
		Start: windowStart,
		End:   windowStart.Add(sc.Maintenance.Duration),
	}
}

func forbiddenKeywordsForScenario(sc *scenario.Scenario) []string {
	if sc == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, failure := range sc.Failures {
		for _, marker := range forbiddenMarkersForContent(failure.Content) {
			if _, ok := seen[marker]; ok {
				continue
			}
			seen[marker] = struct{}{}
			out = append(out, marker)
		}
	}
	return out
}

func forbiddenMarkersForContent(content string) []string {
	switch content {
	case "error_page":
		return []string{"Error establishing a database connection"}
	case "defacement":
		return []string{"H4CK3D"}
	case "ransomware":
		return []string{"DARKLOCK RANSOMWARE v3.1"}
	case "malicious_script":
		return []string{
			"cdn.track-analytics-js.example/v2/t.min.js",
			"metrics.evil-cdn.example/collect.js",
		}
	case "spam_links":
		return []string{
			"buy cheap viagra online no prescription",
			"free casino slots no deposit bonus",
			"bitcoin investment platform guaranteed returns",
		}
	default:
		return nil
	}
}

func failureParams(sc *scenario.Scenario, f scenario.Failure) map[string]any {
	p := map[string]any{"rate": f.Rate}
	if f.StatusCode != 0 {
		p["status_code"] = f.StatusCode
	}
	if f.HeaderName != "" {
		p["header_name"] = f.HeaderName
	}
	if f.HeaderValue != "" {
		p["header_value"] = f.HeaderValue
	}
	if sc != nil && len(sc.RequestHeaders) > 0 {
		p["request_headers"] = sc.RequestHeaders
	}
	if f.Method != "" {
		p["method"] = f.Method
	}
	if f.Phase != "" {
		p["phase"] = f.Phase
	}
	if f.Delay != 0 {
		p["delay"] = f.Delay.String()
	}
	if f.TruncateAfterBytes != nil {
		p["truncate_after_bytes"] = *f.TruncateAfterBytes
	}
	if f.Variant != "" {
		p["variant"] = f.Variant
	}
	if f.ChainLength != 0 {
		p["chain_length"] = f.ChainLength
	}
	if f.Content != "" {
		p["content"] = f.Content
	}
	// Keyword lives at scenario level. Forward it to the target only for
	// content failure types that actually use it (keyword_missing removes
	// the keyword from the body; keyword_injected adds it).
	if sc != nil && sc.Keyword != "" && f.Type == "http_body" &&
		(f.Content == "keyword_missing" || f.Content == "keyword_injected") {
		p["keyword"] = sc.Keyword
	}
	if f.AddedLatency != 0 {
		p["added_latency"] = f.AddedLatency.String()
	}
	if f.Mode != "" {
		p["mode"] = f.Mode
	}
	if f.DaysExpired != 0 {
		p["days_expired"] = f.DaysExpired
	}
	if f.DaysRemaining != 0 {
		p["days_remaining"] = f.DaysRemaining
	}
	if f.Reason != "" {
		p["reason"] = f.Reason
	}
	if len(f.Regions) > 0 {
		p["regions"] = f.Regions
	}
	return p
}

// collectCIDRs expands region names to a deduplicated list of CIDR strings
// by aggregating probe_ranges across all enabled services in svcCfg.
func collectCIDRs(regions []string, svcCfg *serviceconfig.Config) []string {
	if len(regions) == 0 || svcCfg == nil {
		return nil
	}
	seen := make(map[string]bool)
	var cidrs []string
	for _, svc := range svcCfg.Services {
		if !svc.Enabled {
			continue
		}
		for _, region := range regions {
			for _, cidr := range svc.ProbeRanges[region] {
				if !seen[cidr] {
					seen[cidr] = true
					cidrs = append(cidrs, cidr)
				}
			}
		}
	}
	return cidrs
}

func targetHostPathForFailure(endpoint targetEndpoint, f scenario.Failure) (string, string) {
	switch f.Type {
	case "tcp_refused",
		"dns_nxdomain", "dns_servfail", "dns_timeout",
		"dns_cname_nxdomain", "dns_latency", "dns_ns_unavailable":
		return "", ""
	case "tcp_timeout",
		"tls_expired", "tls_expiring", "tls_invalid", "tls_handshake", "tls_deprecated":
		return endpoint.host, ""
	}
	return endpoint.host, endpoint.path
}

// failureEvent is one activate or deactivate that should fire at a specific
// wall-clock time during a scenario run.
type failureEvent struct {
	at       time.Time
	activate bool // true = activate, false = deactivate
	failure  scenario.Failure
}

// effectiveFailureDuration returns the per-failure duration override when
// present, otherwise the scenario's top-level duration.
func effectiveFailureDuration(scenarioDuration time.Duration, f scenario.Failure) time.Duration {
	if f.Duration > 0 {
		return f.Duration
	}
	return scenarioDuration
}

func latestFailureEndOffset(scenarioDuration time.Duration, failures []scenario.Failure) time.Duration {
	var latest time.Duration
	for _, f := range failures {
		end := f.Offset + effectiveFailureDuration(scenarioDuration, f)
		if end > latest {
			latest = end
		}
	}
	return latest
}

// scheduleFailureEvents builds a time-ordered list of activate/deactivate
// events for a scenario's failures. Each failure contributes:
//
//	activate   at start + offset
//	deactivate at start + offset + effective failure duration
//
// Stable sort by (time, activate-before-deactivate) so two events at the
// same instant always activate first — keeps the registry in a sensible
// state across simultaneous starts/ends.
func scheduleFailureEvents(start time.Time, scenarioDuration time.Duration, failures []scenario.Failure) []failureEvent {
	events := make([]failureEvent, 0, 2*len(failures))
	for _, f := range failures {
		activateAt := start.Add(f.Offset)
		deactivateAt := activateAt.Add(effectiveFailureDuration(scenarioDuration, f))
		events = append(events,
			failureEvent{at: activateAt, activate: true, failure: f},
			failureEvent{at: deactivateAt, activate: false, failure: f},
		)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].at.Equal(events[j].at) {
			return events[i].activate && !events[j].activate
		}
		return events[i].at.Before(events[j].at)
	})
	return events
}

func newRunID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// provisioned pairs an adapter with the handle returned by its Provision call.
// Lives at package scope (not inside Run) so deprovisionAll can be tested
// in isolation.
type provisioned struct {
	a      adapter.Adapter
	handle adapter.MonitorHandle
}

// deprovisionTimeout bounds a single adapter cleanup attempt. Each handle gets
// its own fresh timeout so one slow provider cannot consume the whole cleanup
// budget and cause unrelated monitors to leak. It's a var (not const) so tests
// can shrink it.
var deprovisionTimeout = 90 * time.Second

// deprovisionAttempts lets cleanup retry transient provider/network failures.
// Deprovision operations are required to be idempotent, so retrying is safer
// than leaking monitors that can exhaust provider quotas before the next run.
var deprovisionAttempts = 3

// deprovisionAll tears down every monitor handle, returning the number of
// errors encountered. It deliberately uses a fresh context derived from
// context.Background() — the run's outer ctx is often already cancelled
// when this runs (defer on abort path), and reusing it would make every
// Deprovision call fail immediately with context.Canceled.
func deprovisionAll(handles []provisioned) int {
	if len(handles) == 0 {
		return 0
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(handles))
	for _, p := range handles {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := deprovisionOne(p); err != nil {
				log.Printf("runner: deprovision %s: %v", p.a.ServiceID(), err)
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	return len(errCh)
}

func deprovisionOne(p provisioned) error {
	attempts := deprovisionAttempts
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), deprovisionTimeout)
		err := p.a.Deprovision(ctx, p.handle)
		cancel()
		if err == nil {
			if attempt > 1 {
				log.Printf("runner: deprovision %s succeeded on attempt %d", p.a.ServiceID(), attempt)
			}
			return nil
		}
		lastErr = err
		if attempt < attempts {
			log.Printf("runner: deprovision %s attempt %d/%d: %v", p.a.ServiceID(), attempt, attempts, err)
		}
	}
	return lastErr
}
