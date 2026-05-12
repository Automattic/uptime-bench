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
	"sort"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
)

const (
	failureHTTPMethodStatus   = "http_method_status"
	failureHTTPTimeout        = "http_timeout"
	failureHTTPLatency        = "http_latency"
	failureTLSDeprecated      = "tls_deprecated"
	failureTLSExpiring        = "tls_expiring"
	classificationTLSAdvisory = "tls_advisory"
)

type failureWindow struct {
	kind       string
	start, end time.Time
	details    map[string]any
}

type failureStart struct {
	at      time.Time
	details map[string]any
}

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
	startsByType := make(map[string]failureStart)
	var maintenanceStart time.Time
	for _, e := range events {
		switch e.EventType {
		case "failure_start":
			startsByType[e.FailureType] = failureStart{at: e.OccurredAt, details: asStringMap(e.Details)}
		case "failure_end":
			if s, ok := startsByType[e.FailureType]; ok {
				failureWindows = append(failureWindows, failureWindow{kind: e.FailureType, start: s.at, end: e.OccurredAt, details: s.details})
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

	normalWindows, tlsAdvisoryWindows := splitWindows(windows)

	// Single pass over alerts: classify each one as in-window (true positive,
	// candidate detection-latency sample), TLS advisory detection, TLS false
	// outage report, or out-of-window false positive.
	truePositive := false
	falseNegative := len(normalWindows) > 0
	falsePositive := false
	tlsAdvisoryDetected := false
	tlsAdvisoryMissed := len(tlsAdvisoryWindows) > 0
	tlsAdvisoryFalseOutage := false
	var detectionLatency *float64

	for _, alert := range sr.alerts {
		if alert.ReportedAt == nil {
			continue
		}
		inNormalWindow := false
		for _, w := range normalWindows {
			if containsTime(w, *alert.ReportedAt) {
				inNormalWindow = true
				if detectionLatency == nil {
					latency := alert.ReportedAt.Sub(w.start).Seconds()
					detectionLatency = &latency
				}
				break
			}
		}
		inTLSAdvisoryWindow := false
		for _, w := range tlsAdvisoryWindows {
			if containsTime(w, *alert.ReportedAt) {
				inTLSAdvisoryWindow = true
				break
			}
		}
		switch {
		case inTLSAdvisoryWindow && alert.NormalizedClassification == classificationTLSAdvisory:
			tlsAdvisoryDetected = true
			tlsAdvisoryMissed = false
		case inTLSAdvisoryWindow:
			tlsAdvisoryFalseOutage = true
			tlsAdvisoryMissed = false
		}
		if inNormalWindow {
			truePositive = true
			falseNegative = false
		} else if !inTLSAdvisoryWindow {
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
	out["tls_advisory_detected"] = db.DerivedMetricRow{MetricValue: boolVal(tlsAdvisoryDetected)}
	out["tls_advisory_missed"] = db.DerivedMetricRow{MetricValue: boolVal(tlsAdvisoryMissed)}
	out["tls_advisory_false_outage"] = db.DerivedMetricRow{MetricValue: boolVal(tlsAdvisoryFalseOutage)}

	if detectionLatency != nil {
		out["detection_latency_s"] = db.DerivedMetricRow{MetricValue: detectionLatency}
	}

	return out
}

func splitWindows(windows []failureWindow) (normal []failureWindow, tlsAdvisory []failureWindow) {
	for _, w := range windows {
		if isTLSAdvisoryFailure(w.kind) {
			tlsAdvisory = append(tlsAdvisory, w)
			continue
		}
		if isHealthyGETMethodTrap(w) {
			continue
		}
		normal = append(normal, w)
	}
	return normal, tlsAdvisory
}

func isTLSAdvisoryFailure(kind string) bool {
	switch kind {
	case failureTLSDeprecated, failureTLSExpiring:
		return true
	default:
		return false
	}
}

func isHealthyGETMethodTrap(w failureWindow) bool {
	if w.kind != failureHTTPMethodStatus {
		return false
	}
	method := rawString(w.details["method"])
	return method != "" && !strings.EqualFold(method, "GET")
}

func containsTime(w failureWindow, t time.Time) bool {
	return !t.Before(w.start) && !t.After(effectiveFailureEnd(w))
}

func effectiveFailureEnd(w failureWindow) time.Time {
	if w.kind != failureHTTPTimeout && w.kind != failureHTTPLatency {
		return w.end
	}
	delay, err := time.ParseDuration(rawString(w.details["delay"]))
	if err != nil || delay <= 0 {
		return w.end
	}
	return w.end.Add(delay)
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
		if value := rawString(metadata[key]); value != "" {
			return value
		}
	}
	return ""
}

func asStringMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func rawString(value any) string {
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func normalizeMetadataString(value any) string {
	return strings.ToLower(rawString(value))
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
// campaign generator can produce layered escalations with overlapping
// failure windows, this first merges windows before computing coverage.
func overlapFraction(windows []failureWindow, maintenance failureWindow) float64 {
	if len(windows) == 0 {
		return 0
	}

	merged := mergeWindows(windows)
	var failureTotal time.Duration
	var overlap time.Duration
	for _, w := range merged {
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

func mergeWindows(windows []failureWindow) []failureWindow {
	normalized := make([]failureWindow, 0, len(windows))
	for _, w := range windows {
		if w.end.After(w.start) {
			normalized = append(normalized, w)
		}
	}
	if len(normalized) == 0 {
		return nil
	}

	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].start.Equal(normalized[j].start) {
			return normalized[i].end.Before(normalized[j].end)
		}
		return normalized[i].start.Before(normalized[j].start)
	})

	merged := []failureWindow{normalized[0]}
	for _, w := range normalized[1:] {
		last := &merged[len(merged)-1]
		if !w.start.After(last.end) {
			if w.end.After(last.end) {
				last.end = w.end
			}
			continue
		}
		merged = append(merged, w)
	}
	return merged
}
