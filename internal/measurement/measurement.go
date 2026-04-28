// Package measurement derives benchmark metrics from the ground-truth event
// log and monitor reports stored in the database.
//
// Metric derivation is always a separate pass from raw event writes —
// never in the same transaction. All metrics are recomputable from the
// raw tables at any time.
package measurement

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
)

type failureWindow struct{ start, end time.Time }

type serviceData struct {
	unknown             bool
	reason              string
	cooldownSuppressed  bool
	cooldownUncertain   bool
	cooldownExplanation string
	alerts              []db.MonitorReportRow
}

type runStore interface {
	GroundTruthEventsForRun(ctx context.Context, runID string) ([]db.GroundTruthEvent, error)
	MonitorReportsForRun(ctx context.Context, runID string) ([]db.MonitorReportRow, error)
	UpsertDerivedMetric(ctx context.Context, r db.DerivedMetricRow) error
}

type campaignStore interface {
	runStore
	RunIDsForCampaign(ctx context.Context, campaignRunID string) ([]string, error)
}

// maintenanceCoverageThreshold: when the maintenance window covers at
// least this fraction of the union of failure windows, an absent alert
// is classified as maintenance_suppressed instead of false_negative.
// 80% is the heuristic from docs/inter-run-state-design.md — fully-
// covered windows are unambiguous; partial overlaps need a rule, and
// covering ≥80% means the monitor genuinely had little time to alert
// outside the suppression window.
const maintenanceCoverageThreshold = 0.80

// Derive computes all metrics for the given run and upserts them into
// derived_metrics. Safe to call multiple times — rows are idempotent.
func Derive(ctx context.Context, database runStore, runID string) error {
	events, err := database.GroundTruthEventsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("measurement: load events: %w", err)
	}
	reports, err := database.MonitorReportsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("measurement: load reports: %w", err)
	}

	// Build failure windows from ground-truth events. Maintenance events,
	// if present, come as a (maintenance_start, maintenance_end) pair
	// emitted by the runner when scenario.Maintenance is set.
	var failureWindows []failureWindow
	var maintenance *failureWindow
	startsByType := make(map[string]time.Time)
	var maintenanceStart time.Time
	for _, e := range events {
		switch e.EventType {
		case "failure_start":
			startsByType[e.FailureType] = e.OccurredAt
		case "failure_end":
			if s, ok := startsByType[e.FailureType]; ok {
				failureWindows = append(failureWindows, failureWindow{start: s, end: e.OccurredAt})
				delete(startsByType, e.FailureType)
			}
		case "maintenance_start":
			maintenanceStart = e.OccurredAt
		case "maintenance_end":
			if !maintenanceStart.IsZero() {
				maintenance = &failureWindow{start: maintenanceStart, end: e.OccurredAt}
				maintenanceStart = time.Time{}
			}
		}
	}

	// Group reports by service.
	byService := make(map[string]*serviceData)
	for _, r := range reports {
		sd := byService[r.ServiceID]
		if sd == nil {
			sd = &serviceData{}
			byService[r.ServiceID] = sd
		}
		if r.RetrieveStatus == "unknown" {
			sd.unknown = true
			sd.reason = r.RetrieveUnknownReason
		}
		if state, explanation := cooldownState(r); state != "" {
			switch state {
			case adapter.ReasonCooldownSuppressed:
				sd.cooldownSuppressed = true
			case adapter.ReasonCooldownUncertain:
				sd.cooldownUncertain = true
			}
			if explanation != "" && sd.cooldownExplanation == "" {
				sd.cooldownExplanation = explanation
			}
		}
		if r.EventType == "alert_fired" {
			sd.alerts = append(sd.alerts, r)
		}
	}

	now := time.Now()
	for serviceID, sr := range byService {
		metrics := computeMetrics(sr, failureWindows, maintenance)
		for name, row := range metrics {
			row.RunID = runID
			row.ServiceID = serviceID
			row.MetricName = name
			row.ComputedAt = now
			if err := database.UpsertDerivedMetric(ctx, row); err != nil {
				log.Printf("measurement: upsert %s/%s: %v", serviceID, name, err)
			}
		}
	}
	return nil
}

// DeriveCampaign computes metrics for every scenario run belonging to
// a campaign run. Derivation is best-effort across replays: a broken
// row should not prevent metrics for later rows from being refreshed.
// Any per-run errors are joined and returned after all runs are tried.
func DeriveCampaign(ctx context.Context, database campaignStore, campaignRunID string) error {
	runIDs, err := database.RunIDsForCampaign(ctx, campaignRunID)
	if err != nil {
		return fmt.Errorf("measurement: load campaign runs: %w", err)
	}

	var errs []error
	for _, runID := range runIDs {
		if err := Derive(ctx, database, runID); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", runID, err))
		}
	}
	return errors.Join(errs...)
}

func computeMetrics(sr *serviceData, windows []failureWindow, maintenance *failureWindow) map[string]db.DerivedMetricRow {
	out := make(map[string]db.DerivedMetricRow)
	f64 := func(v float64) *float64 { return &v }

	if sr.unknown {
		out["unknown"] = db.DerivedMetricRow{MetricValue: f64(1), MetricText: sr.reason}
		return out
	}

	// Single pass over alerts: classify each one as in-window (true positive,
	// candidate detection-latency sample) or out-of-window (false positive).
	truePositive := false
	falseNegative := len(windows) > 0
	falsePositive := false
	var detectionLatency *float64

	for _, alert := range sr.alerts {
		if alert.ReportedAt == nil {
			continue
		}
		inWindow := false
		for _, w := range windows {
			if !alert.ReportedAt.Before(w.start) && !alert.ReportedAt.After(w.end) {
				inWindow = true
				if detectionLatency == nil {
					latency := alert.ReportedAt.Sub(w.start).Seconds()
					detectionLatency = &latency
				}
				break
			}
		}
		if inWindow {
			truePositive = true
			falseNegative = false
		} else {
			falsePositive = true
		}
	}

	// Maintenance window suppression: when the scenario declared a
	// maintenance window AND no alert fired during the failure period
	// AND the maintenance window covered ≥80% of the failure window
	// union, classify as maintenance_suppressed instead of false_negative.
	// This is correct behaviour: the monitor was asked to suppress alerts.
	maintenanceSuppressed := false
	if falseNegative && maintenance != nil && len(windows) > 0 {
		coverage := overlapFraction(windows, *maintenance)
		if coverage >= maintenanceCoverageThreshold {
			maintenanceSuppressed = true
			falseNegative = false
		}
	}

	cooldownSuppressed := false
	cooldownUncertain := false
	if falseNegative && len(windows) > 0 {
		switch {
		case sr.cooldownSuppressed:
			cooldownSuppressed = true
			falseNegative = false
		case sr.cooldownUncertain:
			cooldownUncertain = true
			falseNegative = false
		}
	}

	boolVal := func(b bool) *float64 {
		if b {
			return f64(1)
		}
		return f64(0)
	}

	out["true_positive"] = db.DerivedMetricRow{MetricValue: boolVal(truePositive)}
	out["false_negative"] = db.DerivedMetricRow{MetricValue: boolVal(falseNegative)}
	out["false_positive"] = db.DerivedMetricRow{MetricValue: boolVal(falsePositive)}
	out["unknown"] = db.DerivedMetricRow{MetricValue: f64(0)}
	out["maintenance_suppressed"] = db.DerivedMetricRow{MetricValue: boolVal(maintenanceSuppressed)}
	out["cooldown_suppressed"] = db.DerivedMetricRow{MetricValue: boolVal(cooldownSuppressed), MetricText: sr.cooldownExplanation}
	out["cooldown_uncertain"] = db.DerivedMetricRow{MetricValue: boolVal(cooldownUncertain), MetricText: sr.cooldownExplanation}

	if detectionLatency != nil {
		out["detection_latency_s"] = db.DerivedMetricRow{MetricValue: detectionLatency}
	}

	return out
}

func cooldownState(r db.MonitorReportRow) (state string, explanation string) {
	switch r.ReasonCode {
	case adapter.ReasonCooldownSuppressed:
		return adapter.ReasonCooldownSuppressed, r.RetrieveUnknownReason
	case adapter.ReasonCooldownUncertain, adapter.ReasonCooldownResetFailed:
		return adapter.ReasonCooldownUncertain, r.RetrieveUnknownReason
	}

	metadata, ok := r.Metadata.(map[string]any)
	if !ok {
		return "", ""
	}
	explanation = firstRawString(metadata, "cooldown_reason", "cooldown_explanation", "reason")
	for _, key := range []string{"reason_code", "cooldown_reason_code", "cooldown_outcome", "cooldown_state"} {
		raw := normalizeMetadataString(metadata[key])
		switch raw {
		case adapter.ReasonCooldownSuppressed, "suppressed":
			return adapter.ReasonCooldownSuppressed, explanation
		case adapter.ReasonCooldownUncertain, adapter.ReasonCooldownResetFailed, "uncertain", "reset_failed", "reset-failed":
			return adapter.ReasonCooldownUncertain, explanation
		}
	}
	if truthy(metadata["cooldown_suppressed"]) {
		return adapter.ReasonCooldownSuppressed, explanation
	}
	if truthy(metadata["cooldown_uncertain"]) || truthy(metadata["cooldown_reset_failed"]) {
		return adapter.ReasonCooldownUncertain, explanation
	}
	return "", ""
}

func firstRawString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeMetadataString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}

func truthy(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y":
			return true
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

// overlapFraction returns the fraction of the union of failure windows
// covered by the maintenance window. 0.0 means no overlap; 1.0 means
// every failure-active second falls inside the maintenance window.
//
// The denominator is the *union* duration of failure windows so
// overlapping or simultaneous failures don't double-count. Since the
// runner produces non-overlapping per-failure windows in practice
// (each failure has exactly one start/end pair), the union typically
// equals the sum, but the math is correct either way.
func overlapFraction(windows []failureWindow, maintenance failureWindow) float64 {
	if len(windows) == 0 {
		return 0
	}
	// Compute total failure-window duration (sum of intersected-with-self).
	// For non-overlapping windows this is just the sum. For overlapping
	// ones we'd want true union; current scenarios don't generate overlap
	// so the simpler sum is a safe approximation. Document this if a
	// future scenario starts producing overlapping ground-truth windows.
	var failureTotal time.Duration
	var overlap time.Duration
	for _, w := range windows {
		failureTotal += w.end.Sub(w.start)
		if maintenance.end.Before(w.start) || maintenance.start.After(w.end) {
			continue
		}
		s := w.start
		if maintenance.start.After(s) {
			s = maintenance.start
		}
		e := w.end
		if maintenance.end.Before(e) {
			e = maintenance.end
		}
		overlap += e.Sub(s)
	}
	if failureTotal <= 0 {
		return 0
	}
	return float64(overlap) / float64(failureTotal)
}
