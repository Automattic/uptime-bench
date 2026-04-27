// Package runner orchestrates scenario execution: provisioning monitors,
// driving failure injection on the target fleet, recording ground-truth
// events, waiting out the grace period, and collecting adapter results.
package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
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
func Run(ctx context.Context, sc *scenario.Scenario, fl *fleet.Config, database recorder, adapters []adapter.Adapter, svcCfg *serviceconfig.Config) (string, error) {
	runID := newRunID()
	startedAt := time.Now()

	var seed int64
	if sc.Seed != nil {
		seed = *sc.Seed
	} else {
		seed = startedAt.UnixNano()
	}

	target, err := resolveTarget(fl, sc.Target)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}

	token, err := readFleetToken(fl)
	if err != nil {
		return "", fmt.Errorf("runner: %w", err)
	}
	targetClient := control.NewClient(
		fmt.Sprintf("http://%s:%d", target.Address, target.ControlPort),
		token, &http.Client{Timeout: fl.Control.Timeout},
	)

	params := map[string]any{
		"check_frequency": sc.CheckFrequency.String(),
		"grace_period":    sc.GracePeriod.String(),
		"duration":        sc.Duration.String(),
		"failures":        len(sc.Failures),
	}
	if err := database.InsertRun(ctx, db.RunRecord{
		ID:              runID,
		ScenarioID:      sc.ID,
		ScenarioVersion: sc.Version,
		Seed:            seed,
		TargetID:        sc.Target,
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

	// Provision adapters; skip those that fail capability checks.
	var handles []provisioned
	for _, a := range adapters {
		caps := a.Capabilities()
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

		// Use the first site's hostname as the monitor URL so adapters register
		// against the domain name (e.g. http://bench.local/) rather than the
		// infrastructure address. Monitoring services check by domain, not by IP.
		targetURL := fmt.Sprintf("http://%s", target.Address)
		if len(target.Sites) > 0 {
			targetURL = fmt.Sprintf("http://%s/", target.Sites[0].Host)
		}
		tgt := adapter.Target{ID: sc.Target, URL: targetURL}
		cfg := adapter.ProvisionConfig{
			CheckFrequency: sc.CheckFrequency,
			Keyword:        sc.Keyword,
			KeywordCheck:   sc.KeywordCheck,
		}
		cfg.MaintenanceWindow = maintenanceWindowFor(sc, startedAt)
		handle, err := a.Provision(ctx, tgt, cfg)
		if err != nil {
			log.Printf("runner: provision %s: %v", a.ServiceID(), err)
			resolutionReason = "adapter_error"
			continue
		}
		handles = append(handles, provisioned{a: a, handle: handle})
		log.Printf("runner: provisioned %s (monitor %s)", a.ServiceID(), handle.MonitorID)
	}

	// Deprovision unconditionally on exit, even on abort.
	// Uses a fresh context (see deprovisionAll) because the run's outer ctx
	// may already be cancelled when this defer fires (e.g. SIGINT during
	// the failure window). With the outer ctx, Deprovision calls would
	// fail immediately with context.Canceled and leak monitors.
	defer func() {
		if errs := deprovisionAll(handles); errs > 0 && resolutionReason == "planned_completion" {
			resolutionReason = "adapter_error"
		}
	}()

	// Walk the failure event timeline. Each failure produces one activate
	// event at start+offset and one deactivate event at start+offset+duration;
	// scheduleFailureEvents sorts them all into a single time-ordered list
	// so staggered scenarios just fall out of the same loop as simultaneous
	// ones (offset = 0).
	earliestStart := time.Now()
	events := scheduleFailureEvents(earliestStart, sc.Duration, sc.Failures)

	// Target-side auto-expiry timer: must outlast the latest deactivate
	// plus a generous safety margin in case the harness's deactivate is
	// delayed or fails. Without this, a long-offset failure could expire
	// on the target before the harness gets to it.
	maxOffset := time.Duration(0)
	for _, f := range sc.Failures {
		if f.Offset > maxOffset {
			maxOffset = f.Offset
		}
	}
	failureDuration := maxOffset + sc.Duration + sc.GracePeriod + 30*time.Second

	var failureStarted, failureEnded time.Time
	for _, e := range events {
		if waitFor := time.Until(e.at); waitFor > 0 {
			select {
			case <-time.After(waitFor):
			case <-ctx.Done():
				resolutionReason = "aborted"
				return runID, ctx.Err()
			}
		}

		f := e.failure
		if e.activate {
			fp := failureParams(sc, f)
			sourceCIDRs := collectCIDRs(f.Regions, svcCfg)
			if len(f.Regions) > 0 && len(sourceCIDRs) == 0 {
				log.Printf("runner: warning: failure %s has regions %v but no matching probe_ranges found in services.toml", f.Type, f.Regions)
			}
			req := control.ActivateRequest{
				RunID: runID,
				Seed:  seed,
				Failure: control.FailureSpec{
					Type:        f.Type,
					Host:        targetHostForFailure(target, f),
					Duration:    failureDuration,
					Rate:        f.Rate,
					Params:      fp,
					SourceCIDRs: sourceCIDRs,
				},
			}
			if err := targetClient.Activate(ctx, req); err != nil {
				log.Printf("runner: activate %s: %v", f.Type, err)
				resolutionReason = "adapter_error"
				return runID, fmt.Errorf("runner: activate %s: %w", f.Type, err)
			}
			if err := logEvent(ctx, database, runID, sc.Target, "failure_start", f.Type, fp); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, err
			}
			log.Printf("runner: activated %s on %s", f.Type, sc.Target)
			if failureStarted.IsZero() {
				failureStarted = time.Now()
			}
		} else {
			req := control.DeactivateRequest{
				RunID:       runID,
				FailureType: f.Type,
				Host:        targetHostForFailure(target, f),
			}
			if err := targetClient.Deactivate(ctx, req); err != nil {
				log.Printf("runner: deactivate %s: %v", f.Type, err)
			}
			if err := logEvent(ctx, database, runID, sc.Target, "failure_end", f.Type, nil); err != nil {
				resolutionReason = "ground_truth_log_failure"
				return runID, err
			}
			log.Printf("runner: deactivated %s", f.Type)
			failureEnded = time.Now()
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
			resolutionReason = "adapter_error"
			continue
		}
		logMonitorReport(ctx, database, runID, p.a, result)
		log.Printf("runner: retrieved %s: status=%s reports=%d", p.a.ServiceID(), result.Status, len(result.Reports))
	}

	return runID, nil
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
	if result.Status == adapter.RetrieveUnknown && len(result.Reports) == 0 {
		if err := database.InsertMonitorReport(ctx, db.MonitorReportRow{
			RunID:                 runID,
			ServiceID:             serviceID,
			RetrieveStatus:        string(result.Status),
			RetrieveUnknownReason: result.Reason,
			ReasonCode:            result.ReasonCode,
			RetrievedAt:           now,
		}); err != nil {
			log.Printf("runner: insert monitor_report (unknown): %v", err)
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

func failureParams(sc *scenario.Scenario, f scenario.Failure) map[string]any {
	p := map[string]any{"rate": f.Rate}
	if f.StatusCode != 0 {
		p["status_code"] = f.StatusCode
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

func targetHostForFailure(t fleet.Target, f scenario.Failure) string {
	switch f.Type {
	case "tcp_refused", "tcp_timeout",
		"dns_nxdomain", "dns_servfail", "dns_timeout",
		"dns_cname_nxdomain", "dns_latency", "dns_ns_unavailable":
		return ""
	}
	if len(t.Sites) > 0 {
		return t.Sites[0].Host
	}
	return ""
}

// failureEvent is one activate or deactivate that should fire at a specific
// wall-clock time during a scenario run.
type failureEvent struct {
	at       time.Time
	activate bool // true = activate, false = deactivate
	failure  scenario.Failure
}

// scheduleFailureEvents builds a time-ordered list of activate/deactivate
// events for a scenario's failures. Each failure contributes:
//
//	activate   at start + offset
//	deactivate at start + offset + duration
//
// Stable sort by (time, activate-before-deactivate) so two events at the
// same instant always activate first — keeps the registry in a sensible
// state across simultaneous starts/ends.
func scheduleFailureEvents(start time.Time, duration time.Duration, failures []scenario.Failure) []failureEvent {
	events := make([]failureEvent, 0, 2*len(failures))
	for _, f := range failures {
		activateAt := start.Add(f.Offset)
		deactivateAt := activateAt.Add(duration)
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

// deprovisionTimeout bounds how long we'll wait for adapter cleanup. Each
// adapter's HTTP DELETE is short, but a hung remote service must not block
// fleet shutdown forever. It's a var (not const) so tests can shrink it.
var deprovisionTimeout = 30 * time.Second

// deprovisionAll tears down every monitor handle, returning the number of
// errors encountered. It deliberately uses a fresh context derived from
// context.Background() — the run's outer ctx is often already cancelled
// when this runs (defer on abort path), and reusing it would make every
// Deprovision call fail immediately with context.Canceled.
func deprovisionAll(handles []provisioned) int {
	if len(handles) == 0 {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), deprovisionTimeout)
	defer cancel()
	errs := 0
	for _, p := range handles {
		if err := p.a.Deprovision(ctx, p.handle); err != nil {
			log.Printf("runner: deprovision %s: %v", p.a.ServiceID(), err)
			errs++
		}
	}
	return errs
}
