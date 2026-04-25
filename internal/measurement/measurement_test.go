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

	out := computeMetrics(sr, []failureWindow{window(start, end)})

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

	out := computeMetrics(sr, []failureWindow{window(start, end)})

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

	out := computeMetrics(sr, []failureWindow{window(start, end)})

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

	out := computeMetrics(sr, []failureWindow{window(start, end)})

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

// TestComputeMetrics_UnknownStops — when the adapter reported Unknown,
// the metrics short-circuit to a single "unknown" row and skip TP/FN/FP.
func TestComputeMetrics_UnknownStops(t *testing.T) {
	sr := &serviceData{unknown: true, reason: "rate limited"}

	out := computeMetrics(sr, nil)

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
