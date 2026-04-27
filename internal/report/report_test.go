package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/db"
)

func metric(runID, failureType, serviceID, name string, value float64) db.CampaignMetricRow {
	return db.CampaignMetricRow{
		RunID:       runID,
		FailureType: failureType,
		ServiceID:   serviceID,
		MetricName:  name,
		MetricValue: &value,
	}
}

func TestSummarize_GroupsMetricsByFailureAndService(t *testing.T) {
	rows := []db.CampaignMetricRow{
		metric("run-1", "http_status", "svc-a", "true_positive", 1),
		metric("run-1", "http_status", "svc-a", "false_negative", 0),
		metric("run-1", "http_status", "svc-a", "false_positive", 0),
		metric("run-1", "http_status", "svc-a", "unknown", 0),
		metric("run-1", "http_status", "svc-a", "maintenance_suppressed", 0),
		metric("run-1", "http_status", "svc-a", "detection_latency_s", 20),
		metric("run-2", "http_status", "svc-a", "true_positive", 0),
		metric("run-2", "http_status", "svc-a", "false_negative", 1),
		metric("run-2", "http_status", "svc-a", "false_positive", 1),
		metric("run-3", "http_status", "svc-a", "unknown", 1),
		metric("run-4", "http_status", "svc-a", "maintenance_suppressed", 1),
		metric("run-5", "http_status", "svc-a", "true_positive", 1),
		metric("run-5", "http_status", "svc-a", "false_negative", 0),
		metric("run-5", "http_status", "svc-a", "detection_latency_s", 80),
		metric("run-6", "tcp_refused", "svc-b", "true_positive", 1),
		metric("run-6", "tcp_refused", "svc-b", "detection_latency_s", 5),
	}

	got := Summarize(rows)
	if len(got) != 2 {
		t.Fatalf("got %d summaries, want 2: %+v", len(got), got)
	}

	http := got[0]
	if http.FailureType != "http_status" || http.ServiceID != "svc-a" {
		t.Fatalf("first summary = %+v, want http_status/svc-a", http)
	}
	if http.Samples != 5 {
		t.Fatalf("Samples = %d, want 5", http.Samples)
	}
	if http.TruePositive != 2 || http.FalseNegative != 1 || http.FalsePositive != 1 {
		t.Fatalf("TP/FN/FP = %d/%d/%d, want 2/1/1", http.TruePositive, http.FalseNegative, http.FalsePositive)
	}
	if http.Unknown != 1 || http.MaintenanceSuppressed != 1 {
		t.Fatalf("Unknown/MaintenanceSuppressed = %d/%d, want 1/1", http.Unknown, http.MaintenanceSuppressed)
	}
	if http.DetectionRate == nil || *http.DetectionRate != 2.0/3.0 {
		t.Fatalf("DetectionRate = %v, want 2/3", http.DetectionRate)
	}
	if http.LatencyMinSeconds == nil || *http.LatencyMinSeconds != 20 {
		t.Fatalf("LatencyMinSeconds = %v, want 20", http.LatencyMinSeconds)
	}
	if http.LatencyP50Seconds == nil || *http.LatencyP50Seconds != 20 {
		t.Fatalf("LatencyP50Seconds = %v, want 20", http.LatencyP50Seconds)
	}
	if http.LatencyP95Seconds == nil || *http.LatencyP95Seconds != 80 {
		t.Fatalf("LatencyP95Seconds = %v, want 80", http.LatencyP95Seconds)
	}
}

func TestWriteTSV(t *testing.T) {
	rate := 0.5
	min := 12.25
	summaries := []Summary{
		{
			FailureType:       "http_status",
			ServiceID:         "svc",
			Samples:           2,
			DetectionRate:     &rate,
			TruePositive:      1,
			FalseNegative:     1,
			LatencyMinSeconds: &min,
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "tsv", summaries); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "failure_type\tservice\tn\ttp_rate") {
		t.Fatalf("missing TSV header in %q", out)
	}
	if !strings.Contains(out, "http_status\tsvc\t2\t0.500\t1\t1") {
		t.Fatalf("missing TSV row in %q", out)
	}
	if !strings.Contains(out, "\t12.2\t") {
		t.Fatalf("missing formatted latency in %q", out)
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "xml", nil); err == nil {
		t.Fatal("Write: expected unknown format error")
	}
}
