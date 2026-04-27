package measurement

import (
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/db"
)

// alertAt returns a MonitorReportRow representing one alert_fired event.
func alertAt(t time.Time) db.MonitorReportRow {
	return db.MonitorReportRow{EventType: "alert_fired", ReportedAt: &t}
}

func window(start, end time.Time) failureWindow {
	return failureWindow{start: start, end: end}
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
