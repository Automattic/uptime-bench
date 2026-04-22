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
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

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
func Run(ctx context.Context, sc *scenario.Scenario, fl *fleet.Config, database *db.DB, adapters []adapter.Adapter) (string, error) {
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

	logEvent(ctx, database, runID, sc.Target, "run_start", "", nil)

	resolutionReason := "planned_completion"
	defer func() {
		logEvent(ctx, database, runID, sc.Target, "run_end", "", map[string]any{"reason": resolutionReason})
		if err := database.CloseRun(ctx, runID, time.Now(), resolutionReason); err != nil {
			log.Printf("runner: close run: %v", err)
		}
		log.Printf("runner: run %s closed: %s", runID, resolutionReason)
	}()

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
	type provisioned struct {
		a      adapter.Adapter
		handle adapter.MonitorHandle
	}
	var handles []provisioned
	for _, a := range adapters {
		caps := a.Capabilities()
		if sc.CheckFrequency < caps.MinCheckFrequency {
			log.Printf("runner: skip %s: check_frequency %v < min %v", a.ServiceID(), sc.CheckFrequency, caps.MinCheckFrequency)
			logMonitorReport(ctx, database, runID, a.ServiceID(), adapter.RetrieveResult{
				Status: adapter.RetrieveUnknown,
				Reason: fmt.Sprintf("capability_mismatch: check_frequency %v < min %v", sc.CheckFrequency, caps.MinCheckFrequency),
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
		cfg := adapter.ProvisionConfig{CheckFrequency: sc.CheckFrequency}
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
	defer func() {
		for _, p := range handles {
			if err := p.a.Deprovision(ctx, p.handle); err != nil {
				log.Printf("runner: deprovision %s: %v", p.a.ServiceID(), err)
				if resolutionReason == "planned_completion" {
					resolutionReason = "adapter_error"
				}
			}
		}
	}()

	// Activate failures.
	failureStarted := time.Now()
	failureDuration := sc.Duration + sc.GracePeriod + 30*time.Second // safety margin for auto-expiry
	for _, f := range sc.Failures {
		fp := failureParams(f)
		req := control.ActivateRequest{
			RunID: runID,
			Seed:  seed,
			Failure: control.FailureSpec{
				Type:     f.Type,
				Host:     targetHostForFailure(target, f),
				Duration: failureDuration,
				Rate:     f.Rate,
				Params:   fp,
			},
		}
		if err := targetClient.Activate(ctx, req); err != nil {
			log.Printf("runner: activate %s: %v", f.Type, err)
			resolutionReason = "adapter_error"
			return runID, fmt.Errorf("runner: activate %s: %w", f.Type, err)
		}
		logEvent(ctx, database, runID, sc.Target, "failure_start", f.Type, fp)
		log.Printf("runner: activated %s on %s", f.Type, sc.Target)
	}
	failureEnded := failureStarted

	// Wait for scenario duration.
	log.Printf("runner: failure active for %v", sc.Duration)
	select {
	case <-time.After(sc.Duration):
	case <-ctx.Done():
		resolutionReason = "aborted"
		return runID, ctx.Err()
	}
	failureEnded = time.Now()

	// Deactivate failures.
	for _, f := range sc.Failures {
		req := control.DeactivateRequest{
			RunID:       runID,
			FailureType: f.Type,
			Host:        targetHostForFailure(target, f),
		}
		if err := targetClient.Deactivate(ctx, req); err != nil {
			log.Printf("runner: deactivate %s: %v", f.Type, err)
		}
		logEvent(ctx, database, runID, sc.Target, "failure_end", f.Type, nil)
		log.Printf("runner: deactivated %s", f.Type)
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
			logMonitorReport(ctx, database, runID, svcID, adapter.RetrieveResult{
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
		logMonitorReport(ctx, database, runID, p.a.ServiceID(), result)
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

func logEvent(ctx context.Context, database *db.DB, runID, targetID, eventType, failureType string, details any) {
	if err := database.InsertGroundTruthEvent(ctx, db.GroundTruthEvent{
		RunID:       runID,
		EventType:   eventType,
		TargetID:    targetID,
		FailureType: failureType,
		OccurredAt:  time.Now(),
		Details:     details,
	}); err != nil {
		log.Printf("runner: log event %s: %v", eventType, err)
	}
}

func logMonitorReport(ctx context.Context, database *db.DB, runID, serviceID string, result adapter.RetrieveResult) {
	now := time.Now()
	if result.Status == adapter.RetrieveUnknown && len(result.Reports) == 0 {
		database.InsertMonitorReport(ctx, db.MonitorReportRow{
			RunID:                 runID,
			ServiceID:             serviceID,
			RetrieveStatus:        string(result.Status),
			RetrieveUnknownReason: result.Reason,
			RetrievedAt:           now,
		})
		return
	}
	for _, r := range result.Reports {
		normalized := adapter.Normalize(serviceID, r.RawClassification)
		var reportedAt *time.Time
		if !r.ReportedAt.IsZero() {
			t := r.ReportedAt
			reportedAt = &t
		}
		database.InsertMonitorReport(ctx, db.MonitorReportRow{
			RunID:                    runID,
			ServiceID:                serviceID,
			RetrieveStatus:           string(result.Status),
			RetrieveUnknownReason:    result.Reason,
			EventType:                string(r.EventType),
			RawClassification:        r.RawClassification,
			NormalizedClassification: normalized,
			ReportedAt:               reportedAt,
			RetrievedAt:              now,
			Metadata:                 r.Metadata,
		})
	}
}

func failureParams(f scenario.Failure) map[string]any {
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
	if f.Keyword != "" {
		p["keyword"] = f.Keyword
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
	return p
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

func newRunID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
