package measurement

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
)

// alertAt returns a MonitorReportRow representing one alert_fired event.
func alertAt(t time.Time) db.MonitorReportRow {
	return db.MonitorReportRow{EventType: "alert_fired", ReportedAt: &t}
}

func window(start, end time.Time) failureWindow {
	return failureWindow{start: start, end: end}
}

func typedWindow(kind string, start, end time.Time) failureWindow {
	return failureWindow{kind: kind, start: start, end: end}
}

func timeoutWindow(delay string, start, end time.Time) failureWindow {
	return failureWindow{
		kind:    failureHTTPTimeout,
		start:   start,
		end:     end,
		details: map[string]any{"delay": delay},
	}
}

func methodWindow(method string, start, end time.Time) failureWindow {
	return failureWindow{
		kind:    failureHTTPMethodStatus,
		start:   start,
		end:     end,
		details: map[string]any{"method": method},
	}
}

// TestComputeMetrics_TruePositive — alert inside the failure window.
func TestComputeMetrics_TruePositive(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(start.Add(15 * time.Second))}}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["true_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("true_positive = %v, want 1", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_positive = %v, want 0", v)
	}
	if v := out["detection_latency_s"].MetricValue; v == nil || *v != 15 {
		t.Fatalf("detection_latency_s = %v, want 15", v)
	}
}

// TestComputeMetrics_FalseNegative — failure window with no alerts.
func TestComputeMetrics_FalseNegative(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: nil}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_negative = %v, want 1", v)
	}
	if _, has := out["detection_latency_s"]; has {
		t.Fatal("detection_latency_s should not be set when no alert fired")
	}
}

// TestComputeMetrics_FalsePositive — alert outside any failure window.
func TestComputeMetrics_FalsePositive(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	// Alert fires 5 minutes before the failure window.
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(start.Add(-5 * time.Minute))}}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["false_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_positive = %v, want 1", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
}

func TestComputeMetrics_HTTPTimeoutAlertInDelayTailIsTruePositive(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(end.Add(5 * time.Second))}}

	out := computeMetrics(sr, []failureWindow{timeoutWindow("35s", start, end)}, nil)

	if v := out["true_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("true_positive = %v, want 1", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_positive = %v, want 0", v)
	}
	if v := out["detection_latency_s"].MetricValue; v == nil || *v != 65 {
		t.Fatalf("detection_latency_s = %v, want 65", v)
	}
}

func TestComputeMetrics_HTTPTimeoutAlertAfterDelayTailIsStillLate(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(end.Add(40 * time.Second))}}

	out := computeMetrics(sr, []failureWindow{timeoutWindow("35s", start, end)}, nil)

	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_negative = %v, want 1", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_positive = %v, want 1", v)
	}
}

func TestComputeMetrics_HEADFailureWithHealthyGETIsNotFalseNegative(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{}

	out := computeMetrics(sr, []failureWindow{methodWindow("HEAD", start, end)}, nil)

	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (GET-visible page is healthy)", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_positive = %v, want 0", v)
	}
}

func TestComputeMetrics_HEADFailureAlertIsFalsePositive(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(start.Add(15 * time.Second))}}

	out := computeMetrics(sr, []failureWindow{methodWindow("HEAD", start, end)}, nil)

	if v := out["false_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_positive = %v, want 1 (HEAD-only failure is false-down for visitor-visible GET)", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0", v)
	}
}

func TestComputeMetrics_GETFailureRemainsVisitorVisibleOutage(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{}

	out := computeMetrics(sr, []failureWindow{methodWindow("GET", start, end)}, nil)

	if v := out["false_negative"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_negative = %v, want 1 (GET failure is visitor-visible)", v)
	}
}

func TestComputeMetrics_TLSDeprecatedMissedAdvisory(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{}

	out := computeMetrics(sr, []failureWindow{typedWindow(failureTLSDeprecated, start, end)}, nil)

	if v := out["tls_advisory_missed"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("tls_advisory_missed = %v, want 1", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (tls_deprecated is advisory, not outage)", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0", v)
	}
}

func TestComputeMetrics_TLSDeprecatedAdvisoryDetected(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	alert := alertAt(start.Add(10 * time.Second))
	alert.NormalizedClassification = classificationTLSAdvisory
	sr := &serviceData{alerts: []db.MonitorReportRow{alert}}

	out := computeMetrics(sr, []failureWindow{typedWindow(failureTLSDeprecated, start, end)}, nil)

	if v := out["tls_advisory_detected"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("tls_advisory_detected = %v, want 1", v)
	}
	if v := out["tls_advisory_missed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("tls_advisory_missed = %v, want 0", v)
	}
	if v := out["tls_advisory_false_outage"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("tls_advisory_false_outage = %v, want 0", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0 (advisory is separate from outage TP)", v)
	}
}

func TestComputeMetrics_TLSDeprecatedFalseOutage(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	alert := alertAt(start.Add(10 * time.Second))
	alert.NormalizedClassification = "tls_failure"
	sr := &serviceData{alerts: []db.MonitorReportRow{alert}}

	out := computeMetrics(sr, []failureWindow{typedWindow(failureTLSDeprecated, start, end)}, nil)

	if v := out["tls_advisory_false_outage"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("tls_advisory_false_outage = %v, want 1", v)
	}
	if v := out["tls_advisory_missed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("tls_advisory_missed = %v, want 0", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_positive = %v, want 0 (false outage is reported separately)", v)
	}
}

func TestComputeMetrics_TLSExpiringAdvisoryDetected(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	alert := alertAt(start.Add(10 * time.Second))
	alert.NormalizedClassification = classificationTLSAdvisory
	sr := &serviceData{alerts: []db.MonitorReportRow{alert}}

	out := computeMetrics(sr, []failureWindow{typedWindow(failureTLSExpiring, start, end)}, nil)

	if v := out["tls_advisory_detected"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("tls_advisory_detected = %v, want 1", v)
	}
	if v := out["tls_advisory_missed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("tls_advisory_missed = %v, want 0", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0 (expiring cert is advisory, not outage TP)", v)
	}
	if _, has := out["detection_latency_s"]; has {
		t.Fatal("detection_latency_s should not be set for advisory-only TLS detections")
	}
}

func TestComputeMetrics_TLSExpiringHTTPFailureIsFalseOutageNotTruePositive(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	alert := alertAt(start.Add(10 * time.Second))
	alert.NormalizedClassification = "http_failure"
	sr := &serviceData{alerts: []db.MonitorReportRow{alert}}

	out := computeMetrics(sr, []failureWindow{typedWindow(failureTLSExpiring, start, end)}, nil)

	if v := out["tls_advisory_false_outage"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("tls_advisory_false_outage = %v, want 1", v)
	}
	if v := out["true_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("true_positive = %v, want 0 (wrong-layer HTTP outage must not satisfy TLS expiry)", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (advisory outcomes stay out of outage FN)", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_positive = %v, want 0 (false outage is reported separately while advisory is active)", v)
	}
	if _, has := out["detection_latency_s"]; has {
		t.Fatal("detection_latency_s should not be set from wrong-layer TLS expiry alerts")
	}
}

// TestComputeMetrics_MixedAlerts — verifies the single-pass logic handles
// both true-positive and false-positive alerts correctly. The earlier
// two-loop version effectively did the same thing; this test pins down
// the contract so the refactor can't lose either case.
func TestComputeMetrics_MixedAlerts(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: []db.MonitorReportRow{
		alertAt(start.Add(-time.Minute)),     // false positive
		alertAt(start.Add(20 * time.Second)), // true positive (sets detection latency)
		alertAt(end.Add(time.Minute)),        // false positive
	}}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["true_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("true_positive = %v, want 1", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0", v)
	}
	if v := out["false_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_positive = %v, want 1", v)
	}
	if v := out["detection_latency_s"].MetricValue; v == nil || *v != 20 {
		t.Fatalf("detection_latency_s = %v, want 20", v)
	}
}

// TestComputeMetrics_MaintenanceFullySuppressed — failure with no
// alerts AND a maintenance window covering the entire failure period.
// Outcome flips from false_negative to maintenance_suppressed.
func TestComputeMetrics_MaintenanceFullySuppressed(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Minute)
	maintenance := failureWindow{start: start.Add(-time.Minute), end: end.Add(time.Minute)}
	sr := &serviceData{alerts: nil} // no alerts during failure

	out := computeMetrics(sr, []failureWindow{window(start, end)}, &maintenance)

	if v := out["maintenance_suppressed"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("maintenance_suppressed = %v, want 1 (window covers 100%% of failure)", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (the maintenance window explains the absent alert)", v)
	}
}

// TestComputeMetrics_MaintenancePartialBelowThreshold — maintenance
// window covers only 50% of the failure period; that's below the 80%
// heuristic so the outcome stays false_negative.
func TestComputeMetrics_MaintenancePartialBelowThreshold(t *testing.T) {
	start := time.Now()
	end := start.Add(10 * time.Minute)
	// Maintenance covers t..t+5m (50% of the failure period).
	maintenance := failureWindow{start: start, end: start.Add(5 * time.Minute)}
	sr := &serviceData{alerts: nil}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, &maintenance)

	if v := out["false_negative"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_negative = %v, want 1 (50%% maintenance coverage is below threshold)", v)
	}
	if v := out["maintenance_suppressed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("maintenance_suppressed = %v, want 0", v)
	}
}

// TestComputeMetrics_MaintenancePartialAboveThreshold — maintenance
// covers 90% of the failure period (above 80% threshold) → suppressed.
func TestComputeMetrics_MaintenancePartialAboveThreshold(t *testing.T) {
	start := time.Now()
	end := start.Add(10 * time.Minute)
	// Maintenance covers t+0..t+9m (90% of the failure period).
	maintenance := failureWindow{start: start, end: start.Add(9 * time.Minute)}
	sr := &serviceData{alerts: nil}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, &maintenance)

	if v := out["maintenance_suppressed"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("maintenance_suppressed = %v, want 1 (90%% maintenance coverage exceeds 80%% threshold)", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0", v)
	}
}

func TestOverlapFractionUsesFailureWindowUnion(t *testing.T) {
	start := time.Now()
	windows := []failureWindow{
		window(start, start.Add(10*time.Minute)),
		window(start.Add(5*time.Minute), start.Add(15*time.Minute)),
	}
	maintenance := window(start, start.Add(10*time.Minute))

	got := overlapFraction(windows, maintenance)
	want := 10.0 / 15.0
	if got != want {
		t.Fatalf("overlapFraction = %v, want %v (10m covered over 15m union)", got, want)
	}
}

// TestComputeMetrics_MaintenanceWithAlert — alert fired during the
// failure period regardless of maintenance. true_positive wins;
// maintenance_suppressed stays 0 (an alert means the monitor honoured
// the failure, not the suppression).
func TestComputeMetrics_MaintenanceWithAlert(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Minute)
	maintenance := failureWindow{start: start, end: end}
	sr := &serviceData{alerts: []db.MonitorReportRow{alertAt(start.Add(30 * time.Second))}}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, &maintenance)

	if v := out["true_positive"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("true_positive = %v, want 1", v)
	}
	if v := out["maintenance_suppressed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("maintenance_suppressed = %v, want 0 (alert fired regardless of maintenance)", v)
	}
}

// TestComputeMetrics_NoMaintenanceWindow — sanity: when maintenance is
// nil, the new metric still appears in the output but is 0.
func TestComputeMetrics_NoMaintenanceWindow(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{alerts: nil}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["maintenance_suppressed"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("maintenance_suppressed = %v, want 0 when no maintenance configured", v)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("false_negative = %v, want 1", v)
	}
}

func TestComputeMetrics_CooldownSuppressed(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{
		cooldownSuppressed:  true,
		cooldownExplanation: "prior alert still inside vendor cooldown",
	}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["cooldown_suppressed"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("cooldown_suppressed = %v, want 1", v)
	}
	if out["cooldown_suppressed"].MetricText != "prior alert still inside vendor cooldown" {
		t.Fatalf("cooldown_suppressed text = %q", out["cooldown_suppressed"].MetricText)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (cooldown explains absent alert)", v)
	}
	if v := out["cooldown_uncertain"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("cooldown_uncertain = %v, want 0", v)
	}
}

func TestComputeMetrics_CooldownUncertain(t *testing.T) {
	start := time.Now()
	end := start.Add(time.Minute)
	sr := &serviceData{
		cooldownUncertain:   true,
		cooldownExplanation: "reset endpoint returned ambiguous status",
	}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if v := out["cooldown_uncertain"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("cooldown_uncertain = %v, want 1", v)
	}
	if out["cooldown_uncertain"].MetricText != "reset endpoint returned ambiguous status" {
		t.Fatalf("cooldown_uncertain text = %q", out["cooldown_uncertain"].MetricText)
	}
	if v := out["false_negative"].MetricValue; v == nil || *v != 0 {
		t.Fatalf("false_negative = %v, want 0 (cooldown uncertainty keeps row out of FN)", v)
	}
}

func TestCooldownStateReadsMetadata(t *testing.T) {
	state, explanation := cooldownState(db.MonitorReportRow{
		Metadata: map[string]any{
			"cooldown_state":       "suppressed",
			"cooldown_explanation": "previous run alerted 5m ago",
		},
	})
	if state != adapter.ReasonCooldownSuppressed {
		t.Fatalf("state = %q, want %q", state, adapter.ReasonCooldownSuppressed)
	}
	if explanation != "previous run alerted 5m ago" {
		t.Fatalf("explanation = %q", explanation)
	}

	state, _ = cooldownState(db.MonitorReportRow{
		Metadata: map[string]any{"cooldown_reset_failed": true},
	})
	if state != adapter.ReasonCooldownUncertain {
		t.Fatalf("reset failure state = %q, want %q", state, adapter.ReasonCooldownUncertain)
	}
}

// TestComputeMetrics_UnknownStops — when the adapter reported Unknown,
// the metrics short-circuit to a single "unknown" row and skip TP/FN/FP.
func TestComputeMetrics_UnknownStops(t *testing.T) {
	sr := &serviceData{unknown: true, reason: "rate limited"}

	out := computeMetrics(sr, nil, nil)

	if len(out) != 1 {
		t.Fatalf("got %d metrics, want 1 (unknown short-circuit)", len(out))
	}
	if v := out["unknown"].MetricValue; v == nil || *v != 1 {
		t.Fatalf("unknown = %v, want 1", v)
	}
	if out["unknown"].MetricText != "rate limited" {
		t.Fatalf("unknown reason = %q, want %q", out["unknown"].MetricText, "rate limited")
	}
}

// TestComputeMetrics_CapabilityMismatchTreatedLikeUnknown pins the
// docs/events.md invariant that capability_mismatch and Unknown both keep
// the row out of the false-negative count. The runner writes a
// capability_mismatch row with retrieve_status="unknown" and a
// reason_code; Derive folds both API-error Unknown and capability
// mismatch into serviceData.unknown=true. This test ensures the
// short-circuit fires for a capability_mismatch reason just like it
// does for an API-error reason — so a regression that special-cases
// one path and not the other gets caught.
func TestComputeMetrics_CapabilityMismatchTreatedLikeUnknown(t *testing.T) {
	start := time.Now()
	end := start.Add(5 * time.Minute)
	// Even with an active failure window present, capability_mismatch
	// must NOT produce false_negative=1 — the adapter wasn't asked.
	sr := &serviceData{
		unknown: true,
		reason:  "scenario requires keyword monitoring; adapter SupportsKeyword = false",
	}

	out := computeMetrics(sr, []failureWindow{window(start, end)}, nil)

	if _, has := out["false_negative"]; has {
		t.Errorf("false_negative metric must not be emitted for capability_mismatch row; got %+v", out)
	}
	if _, has := out["true_positive"]; has {
		t.Errorf("true_positive metric must not be emitted for capability_mismatch row; got %+v", out)
	}
	if v := out["unknown"].MetricValue; v == nil || *v != 1 {
		t.Errorf("unknown = %v, want 1", v)
	}
	if out["unknown"].MetricText != sr.reason {
		t.Errorf("unknown.MetricText = %q, want the row's reason text preserved (%q)",
			out["unknown"].MetricText, sr.reason)
	}
}

type fakeCampaignStore struct {
	campaignRunIDs []string
	campaignErr    error
	events         map[string][]db.GroundTruthEvent
	reports        map[string][]db.MonitorReportRow
	eventErr       map[string]error
	reportErr      map[string]error
	reportCalls    []string
	upserts        []db.DerivedMetricRow
}

func (f *fakeCampaignStore) RunIDsForCampaign(context.Context, string) ([]string, error) {
	if f.campaignErr != nil {
		return nil, f.campaignErr
	}
	return append([]string(nil), f.campaignRunIDs...), nil
}

func (f *fakeCampaignStore) GroundTruthEventsForRun(_ context.Context, runID string) ([]db.GroundTruthEvent, error) {
	if err := f.eventErr[runID]; err != nil {
		return nil, err
	}
	return append([]db.GroundTruthEvent(nil), f.events[runID]...), nil
}

func (f *fakeCampaignStore) MonitorReportsForRun(_ context.Context, runID string) ([]db.MonitorReportRow, error) {
	f.reportCalls = append(f.reportCalls, runID)
	if err := f.reportErr[runID]; err != nil {
		return nil, err
	}
	return append([]db.MonitorReportRow(nil), f.reports[runID]...), nil
}

func (f *fakeCampaignStore) UpsertDerivedMetric(_ context.Context, r db.DerivedMetricRow) error {
	f.upserts = append(f.upserts, r)
	return nil
}

func TestDeriveCampaign_BestEffortAcrossRuns(t *testing.T) {
	store := &fakeCampaignStore{
		campaignRunIDs: []string{"run-1", "run-2", "run-3"},
		events:         map[string][]db.GroundTruthEvent{},
		reports: map[string][]db.MonitorReportRow{
			"run-1": {
				{RunID: "run-1", ServiceID: "svc", RetrieveStatus: "unknown", RetrieveUnknownReason: "rate limited"},
			},
			"run-3": {
				{RunID: "run-3", ServiceID: "svc", RetrieveStatus: "unknown", RetrieveUnknownReason: "auth failed"},
			},
		},
		eventErr:  map[string]error{},
		reportErr: map[string]error{"run-2": errors.New("db offline")},
	}

	err := DeriveCampaign(context.Background(), store, "campaign-run")
	if err == nil {
		t.Fatal("DeriveCampaign: expected joined error from run-2")
	}
	if !strings.Contains(err.Error(), "run-2") || !strings.Contains(err.Error(), "db offline") {
		t.Fatalf("DeriveCampaign error = %v, want run id and underlying error", err)
	}

	wantCalls := []string{"run-1", "run-2", "run-3"}
	if len(store.reportCalls) != len(wantCalls) {
		t.Fatalf("reportCalls = %v, want %v", store.reportCalls, wantCalls)
	}
	for i := range wantCalls {
		if store.reportCalls[i] != wantCalls[i] {
			t.Fatalf("reportCalls = %v, want %v", store.reportCalls, wantCalls)
		}
	}

	upserted := map[string]bool{}
	for _, row := range store.upserts {
		if row.MetricName == "unknown" {
			upserted[row.RunID] = true
		}
	}
	if !upserted["run-1"] || !upserted["run-3"] {
		t.Fatalf("unknown metrics upserted for runs = %v, want run-1 and run-3", upserted)
	}
	if upserted["run-2"] {
		t.Fatal("run-2 should not have metrics because its reports failed to load")
	}
}

func TestDeriveCampaign_RunListErrorStopsBeforeRuns(t *testing.T) {
	store := &fakeCampaignStore{campaignErr: errors.New("campaign lookup failed")}

	err := DeriveCampaign(context.Background(), store, "campaign-run")
	if err == nil {
		t.Fatal("DeriveCampaign: expected error")
	}
	if !strings.Contains(err.Error(), "load campaign runs") {
		t.Fatalf("DeriveCampaign error = %v, want campaign load context", err)
	}
	if len(store.reportCalls) != 0 {
		t.Fatalf("reportCalls = %v, want no per-run derivation after list failure", store.reportCalls)
	}
}
