package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
	r := Report{
		Summaries: []Summary{
			{
				FailureType:       "http_status",
				ServiceID:         "svc",
				Samples:           2,
				DetectionRate:     &rate,
				TruePositive:      1,
				FalseNegative:     1,
				LatencyMinSeconds: &min,
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "tsv", r); err != nil {
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
	// TSV intentionally has no metadata header — keep it that way so
	// existing pipelines parsing the file format don't have to skip a
	// non-row line.
	if strings.HasPrefix(out, "#") {
		t.Fatalf("TSV output must not carry a metadata header: %q", out)
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "xml", Report{}); err == nil {
		t.Fatal("Write: expected unknown format error")
	}
}

// TestWriteTable_EmitsMetaCommentLine — the table format prefixes the
// data rows with a `#` comment disclosing how the report was scoped
// (which interpretation the input matched, how many campaign runs the
// data spans, the time window). This is the methodology disclosure
// from ROADMAP.md surfaced in CLI output, so a published table can
// stand alone without separately documenting the aggregation depth.
func TestWriteTable_EmitsMetaCommentLine(t *testing.T) {
	started := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 4, 22, 18, 30, 0, 0, time.UTC)
	r := Report{
		Meta: Meta{
			Input:             "weekly-2026-q2",
			MatchedAsConfigID: true,
			CampaignRuns:      3,
			EarliestStartedAt: &started,
			LatestEndedAt:     &ended,
		},
		Summaries: []Summary{
			{FailureType: "http_status", ServiceID: "svc", Samples: 1},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "table", r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "# ") {
		t.Fatalf("table output should start with a # meta line, got %q", out)
	}
	if !strings.Contains(out, `input="weekly-2026-q2"`) {
		t.Fatalf("meta line missing input: %q", out)
	}
	if !strings.Contains(out, "matched_as=config_id") {
		t.Fatalf("meta line missing match interpretation: %q", out)
	}
	if !strings.Contains(out, "campaign_runs=3") {
		t.Fatalf("meta line missing campaign_runs count: %q", out)
	}
	if !strings.Contains(out, "earliest_started_at=2026-04-15") {
		t.Fatalf("meta line missing earliest_started_at: %q", out)
	}
	if !strings.Contains(out, "latest_ended_at=2026-04-22") {
		t.Fatalf("meta line missing latest_ended_at: %q", out)
	}
}

// TestWriteTable_NoMetaWhenEmpty — when nothing matched, the table
// format must not emit a misleading meta line claiming an aggregation
// that doesn't exist.
func TestWriteTable_NoMetaWhenEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, "table", Report{Meta: Meta{Input: "missing"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.HasPrefix(buf.String(), "# ") {
		t.Fatalf("expected no meta line for empty match, got %q", buf.String())
	}
}

// TestWriteJSON_WrapsMetaAndSummaries — JSON output is a Report
// envelope so machine consumers parsing the file get the disclosure
// alongside the data; downstream code that only wants the rows reads
// .summaries.
func TestWriteJSON_WrapsMetaAndSummaries(t *testing.T) {
	r := Report{
		Meta: Meta{
			Input:          "abc123",
			MatchedAsRunID: true,
			CampaignRuns:   1,
		},
		Summaries: []Summary{
			{FailureType: "http_status", ServiceID: "svc", Samples: 5},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "json", r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"meta"`) || !strings.Contains(out, `"summaries"`) {
		t.Fatalf("JSON output missing top-level meta/summaries: %q", out)
	}
	if !strings.Contains(out, `"matched_as_run_id": true`) {
		t.Fatalf("JSON meta missing match flag: %q", out)
	}
}

// TestMetaFromLookup_SuppressesEndedAtWhileAnyRunInFlight — when even
// one matched campaign run hasn't completed, LatestEndedAt must stay
// nil so a published report doesn't claim a closed time window that
// isn't real. This is what makes back-to-back reports during a
// long-running aggregate honest about the data they've seen so far.
func TestMetaFromLookup_SuppressesEndedAtWhileAnyRunInFlight(t *testing.T) {
	t1 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	t1End := t1.Add(2 * time.Hour)
	lookup := &db.CampaignLookup{
		Input:             "weekly",
		MatchedAsConfigID: true,
		Runs: []db.CampaignRunSummary{
			{ID: "a", CampaignID: "weekly", StartedAt: t1, EndedAt: &t1End},
			{ID: "b", CampaignID: "weekly", StartedAt: t2, EndedAt: nil}, // still running
		},
	}

	m := MetaFromLookup(lookup)
	if m.CampaignRuns != 2 {
		t.Fatalf("CampaignRuns = %d, want 2", m.CampaignRuns)
	}
	if m.EarliestStartedAt == nil || !m.EarliestStartedAt.Equal(t1) {
		t.Fatalf("EarliestStartedAt = %v, want %v", m.EarliestStartedAt, t1)
	}
	if m.LatestEndedAt != nil {
		t.Fatalf("LatestEndedAt = %v, want nil while one run is in flight", m.LatestEndedAt)
	}
}

// TestSummarize_NoFailureTypeUsesNonCollidingSentinel — rows with no
// failure_start ground-truth events surface under a sentinel that
// can't collide with any real failure type *or* with the literal
// metric name "unknown" (the Unknown retrieve outcome).
func TestSummarize_NoFailureTypeUsesNonCollidingSentinel(t *testing.T) {
	rows := []db.CampaignMetricRow{
		metric("run-x", "", "svc-a", "true_positive", 1),
	}
	got := Summarize(rows)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].FailureType == "unknown" {
		t.Fatal("sentinel must not equal the metric name 'unknown' — namespace collision risk")
	}
	if got[0].FailureType != "<no_failure>" {
		t.Fatalf("FailureType = %q, want <no_failure>", got[0].FailureType)
	}
}
