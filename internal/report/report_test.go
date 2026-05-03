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

func reason(runID, failureType, serviceID, code string) db.CampaignReasonRow {
	return db.CampaignReasonRow{
		RunID:       runID,
		FailureType: failureType,
		ServiceID:   serviceID,
		ReasonCode:  code,
	}
}

func reasonDetail(failureType, serviceID, code, detail string, runs int) db.CampaignReasonDetailRow {
	return db.CampaignReasonDetailRow{
		FailureType: failureType,
		ServiceID:   serviceID,
		ReasonCode:  code,
		Detail:      detail,
		Runs:        runs,
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
		metric("run-7", "http_status", "svc-a", "cooldown_suppressed", 1),
		metric("run-8", "http_status", "svc-a", "cooldown_uncertain", 1),
		metric("run-9", "http_status", "svc-a", "tls_advisory_detected", 1),
		metric("run-10", "http_status", "svc-a", "tls_advisory_missed", 1),
		metric("run-11", "http_status", "svc-a", "tls_advisory_false_outage", 1),
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
	if http.Samples != 10 {
		t.Fatalf("Samples = %d, want 10", http.Samples)
	}
	if http.TruePositive != 2 || http.FalseNegative != 1 || http.FalsePositive != 1 {
		t.Fatalf("TP/FN/FP = %d/%d/%d, want 2/1/1", http.TruePositive, http.FalseNegative, http.FalsePositive)
	}
	if http.Unknown != 1 || http.MaintenanceSuppressed != 1 {
		t.Fatalf("Unknown/MaintenanceSuppressed = %d/%d, want 1/1", http.Unknown, http.MaintenanceSuppressed)
	}
	if http.CooldownSuppressed != 1 || http.CooldownUncertain != 1 {
		t.Fatalf("CooldownSuppressed/CooldownUncertain = %d/%d, want 1/1", http.CooldownSuppressed, http.CooldownUncertain)
	}
	if http.TLSAdvisoryDetected != 1 || http.TLSAdvisoryMissed != 1 || http.TLSAdvisoryFalseOutage != 1 {
		t.Fatalf("TLS advisory counts = %d/%d/%d, want 1/1/1",
			http.TLSAdvisoryDetected, http.TLSAdvisoryMissed, http.TLSAdvisoryFalseOutage)
	}
	if http.DetectionRate == nil || *http.DetectionRate != 2.0/3.0 {
		t.Fatalf("DetectionRate = %v, want 2/3", http.DetectionRate)
	}
	if http.DetectionRateCI95 == nil {
		t.Fatal("DetectionRateCI95 should be populated when TP/FN denominator exists")
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
	if http.LatencyP50CI95Seconds == nil || http.LatencyP95CI95Seconds == nil {
		t.Fatal("latency percentile confidence intervals should be populated")
	}
}

func TestSummarize_IncludesReasonCodeCounts(t *testing.T) {
	rows := []db.CampaignMetricRow{
		metric("run-1", "content", "svc-a", "unknown", 1),
		metric("run-2", "content", "svc-a", "unknown", 1),
		metric("run-3", "content", "svc-b", "true_positive", 1),
	}
	reasons := []db.CampaignReasonRow{
		reason("run-1", "content", "svc-a", "capability_mismatch"),
		reason("run-1", "content", "svc-a", "capability_mismatch"), // duplicate row should still count one run
		reason("run-2", "content", "svc-a", "auth_failed"),
	}

	got := Summarize(rows, reasons)
	var svcA Summary
	for _, s := range got {
		if s.ServiceID == "svc-a" {
			svcA = s
			break
		}
	}
	if svcA.ServiceID == "" {
		t.Fatalf("svc-a summary missing: %+v", got)
	}
	if svcA.CapabilityMismatch != 1 {
		t.Fatalf("CapabilityMismatch = %d, want 1", svcA.CapabilityMismatch)
	}
	if svcA.ReasonCodes["capability_mismatch"] != 1 {
		t.Fatalf("capability_mismatch count = %d, want 1", svcA.ReasonCodes["capability_mismatch"])
	}
	if svcA.ReasonCodes["auth_failed"] != 1 {
		t.Fatalf("auth_failed count = %d, want 1", svcA.ReasonCodes["auth_failed"])
	}
}

func TestSummarizeReasonDetails_NormalizesAndSorts(t *testing.T) {
	rows := []db.CampaignReasonDetailRow{
		reasonDetail("http_status", "svc-b", "adapter_error", "api timed\nout", 1),
		reasonDetail("http_status", "svc-a", "adapter_error", "maintenance invalid_parameter", 2),
		reasonDetail("http_status", "svc-a", "adapter_error", "", 2),
		reasonDetail("http_status", "svc-a", "", "ignored", 2),
	}

	got := SummarizeReasonDetails(rows)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	if got[0].ServiceID != "svc-a" || got[0].Runs != 2 {
		t.Fatalf("first detail = %+v, want svc-a with most runs", got[0])
	}
	if got[1].Detail != "api timed out" {
		t.Fatalf("normalized detail = %q, want newline collapsed", got[1].Detail)
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
	if !strings.Contains(out, "http_status\tsvc\t2\t0.500\t") {
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

func TestAnalyzeBias_FlagsImbalanceAndUnknowns(t *testing.T) {
	summaries := []Summary{
		{FailureType: "http_status", ServiceID: "svc-a", Samples: 100, Unknown: 1},
		{
			FailureType:        "http_status",
			ServiceID:          "svc-b",
			Samples:            80,
			Unknown:            2,
			CapabilityMismatch: 1,
			ReasonCodes:        map[string]int{"capability_mismatch": 1, "auth_failed": 1},
		},
		{
			FailureType:        "content",
			ServiceID:          "svc-a",
			Samples:            100,
			Unknown:            3,
			CapabilityMismatch: 3,
			ReasonCodes:        map[string]int{"capability_mismatch": 3},
		},
		{FailureType: "content", ServiceID: "svc-b", Samples: 100},
	}

	got := AnalyzeBias(summaries)
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4", len(got))
	}
	byName := map[string]BiasCheck{}
	for _, check := range got {
		byName[check.Name] = check
	}
	if byName["service_sample_balance"].Status != "warn" {
		t.Fatalf("service_sample_balance = %+v, want warn", byName["service_sample_balance"])
	}
	if byName["cell_sample_balance"].Status != "warn" {
		t.Fatalf("cell_sample_balance = %+v, want warn", byName["cell_sample_balance"])
	}
	if byName["capability_mismatch"].Status != "info" {
		t.Fatalf("capability_mismatch = %+v, want info", byName["capability_mismatch"])
	}
	if byName["uncategorized_unknown"].Status != "warn" {
		t.Fatalf("uncategorized_unknown = %+v, want warn", byName["uncategorized_unknown"])
	}
	if !strings.Contains(byName["uncategorized_unknown"].Message, "http_status/svc-a=1") {
		t.Fatalf("uncategorized_unknown message = %q, want uncategorized svc-a row", byName["uncategorized_unknown"].Message)
	}
	if strings.Contains(byName["uncategorized_unknown"].Message, "http_status/svc-b") {
		t.Fatalf("reason-coded unknown should not be treated as uncategorized: %q", byName["uncategorized_unknown"].Message)
	}
}

func TestAnalyzeBias_FlagsMissingServiceCells(t *testing.T) {
	summaries := []Summary{
		{FailureType: "http_status", ServiceID: "svc-a", Samples: 5},
		{FailureType: "content", ServiceID: "svc-b", Samples: 5},
	}

	got := AnalyzeBias(summaries)
	byName := map[string]BiasCheck{}
	for _, check := range got {
		byName[check.Name] = check
	}
	check := byName["cell_sample_balance"]
	if check.Status != "warn" {
		t.Fatalf("cell_sample_balance = %+v, want warn", check)
	}
	if !strings.Contains(check.Message, "content=svc-a=0,svc-b=5") ||
		!strings.Contains(check.Message, "http_status=svc-a=5,svc-b=0") {
		t.Fatalf("cell_sample_balance message = %q, want missing cells rendered as zero", check.Message)
	}
}

func TestScoreServicesReportsSampleAndNormalizedRates(t *testing.T) {
	summaries := []Summary{
		{FailureType: "http_status", ServiceID: "svc-a", Samples: 10, TruePositive: 9, FalseNegative: 1},
		{FailureType: "tls_expired", ServiceID: "svc-a", Samples: 2, TruePositive: 0, FalseNegative: 2},
		{FailureType: "http_status", ServiceID: "svc-b", Samples: 10, TruePositive: 5, FalseNegative: 5, Unknown: 1},
		{FailureType: "tls_deprecated", ServiceID: "svc-b", Samples: 2, TLSAdvisoryDetected: 1, TLSAdvisoryFalseOutage: 1},
		{FailureType: "http_body", ServiceID: "svc-b", Samples: 1, CapabilityMismatch: 1, ReasonCodes: map[string]int{"capability_mismatch": 1}},
	}

	got := ScoreServices(summaries)
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2: %+v", len(got), got)
	}
	a := got[0]
	if a.ServiceID != "svc-a" {
		t.Fatalf("first service = %q, want svc-a", a.ServiceID)
	}
	if a.Passed != 9 || a.Failed != 3 || a.Comparable != 12 {
		t.Fatalf("svc-a score = %+v, want passed=9 failed=3 comparable=12", a)
	}
	if a.SampleWeightedPassRate == nil || *a.SampleWeightedPassRate != 0.75 {
		t.Fatalf("svc-a sample rate = %v, want 0.75", a.SampleWeightedPassRate)
	}
	if a.ScenarioNormalizedPassRate == nil || diff(*a.ScenarioNormalizedPassRate, 0.45) > 0.000001 {
		t.Fatalf("svc-a scenario-normalized = %v, want average of 0.9 and 0", a.ScenarioNormalizedPassRate)
	}
	b := got[1]
	if b.Passed != 6 || b.Failed != 6 || b.Unknown != 1 || b.CapabilityMismatch != 1 {
		t.Fatalf("svc-b score = %+v, want passed=6 failed=6 unknown=1 cap=1", b)
	}
	if b.CategoryNormalizedPassRate == nil || *b.CategoryNormalizedPassRate != 0.5 {
		t.Fatalf("svc-b category-normalized = %v, want average of http 0.5 and tls 0.5", b.CategoryNormalizedPassRate)
	}
}

func TestScoreMetricsScoresSamplesWithFalsePositiveAsFailure(t *testing.T) {
	rows := []db.CampaignMetricRow{
		metric("run-1", "http_status", "svc", "true_positive", 1),
		metric("run-1", "http_status", "svc", "false_positive", 1),
		metric("run-2", "http_status", "svc", "true_positive", 1),
		metric("run-3", "http_status", "svc", "unknown", 1),
		metric("run-4", "http_body", "svc", "unknown", 1),
	}
	reasons := []db.CampaignReasonRow{
		reason("run-4", "http_body", "svc", "capability_mismatch"),
	}

	got := ScoreMetrics(rows, reasons)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	score := got[0]
	if score.TotalSamples != 4 {
		t.Fatalf("TotalSamples = %d, want 4", score.TotalSamples)
	}
	if score.Passed != 1 || score.Failed != 1 || score.Comparable != 2 {
		t.Fatalf("score = %+v, want one clean pass and one failed mixed sample", score)
	}
	if score.Excluded != 2 || score.Unknown != 2 || score.CapabilityMismatch != 1 {
		t.Fatalf("excluded counts = %+v, want excluded=2 unknown=2 cap=1", score)
	}
	if score.SampleWeightedPassRate == nil || *score.SampleWeightedPassRate != 0.5 {
		t.Fatalf("SampleWeightedPassRate = %v, want 0.5", score.SampleWeightedPassRate)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestWriteMarkdownIncludesServiceScores(t *testing.T) {
	rate := 0.5
	r := Report{
		ServiceScores: []ServiceScore{
			{ServiceID: "svc", TotalSamples: 4, Passed: 1, Failed: 1, Comparable: 2, Excluded: 2, Unknown: 1, CapabilityMismatch: 1, SampleWeightedPassRate: &rate},
		},
		ReasonDetails: []ReasonDetail{
			{FailureType: "http_status", ServiceID: "svc", ReasonCode: "adapter_error", Detail: "newMonitor already_exists", Runs: 3},
		},
		Summaries: []Summary{{
			FailureType: "http_status",
			ServiceID:   "svc",
			Samples:     2,
			ReasonCodes: map[string]int{"adapter_error": 1},
		}},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "markdown", r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "## Service Scores") || !strings.Contains(out, "| svc | 4 | 1 | 1 | 2 | 2 | 1 | 1 | 50.0% |") {
		t.Fatalf("markdown missing service score table: %q", out)
	}
	if !strings.Contains(out, "## Failure-Type Details") {
		t.Fatalf("markdown missing detail table: %q", out)
	}
	if !strings.Contains(out, "## Reason Codes") || !strings.Contains(out, "| http_status | svc | adapter_error | 1 |") {
		t.Fatalf("markdown missing reason-code table: %q", out)
	}
	if !strings.Contains(out, "## Reason Details") || !strings.Contains(out, "| http_status | svc | adapter_error | newMonitor already_exists | 3 |") {
		t.Fatalf("markdown missing reason-detail table: %q", out)
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
// from docs/roadmap.md surfaced in CLI output, so a published table can
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
		BiasChecks: []BiasCheck{
			{Name: "service_sample_balance", Status: "ok", Message: "per-service samples: svc=1"},
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
	if !strings.Contains(out, "# bias service_sample_balance=ok") {
		t.Fatalf("table output missing bias check line: %q", out)
	}
	if strings.Index(out, "# bias") > strings.Index(out, "failure_type") {
		t.Fatalf("bias checks should be printed before data header: %q", out)
	}
}

func TestWriteTSV_EmitsTLSAdvisoryCounts(t *testing.T) {
	r := Report{
		Summaries: []Summary{
			{
				FailureType:            "tls_deprecated",
				ServiceID:              "svc",
				Samples:                3,
				TLSAdvisoryDetected:    1,
				TLSAdvisoryMissed:      1,
				TLSAdvisoryFalseOutage: 1,
			},
		},
	}

	var buf bytes.Buffer
	if err := Write(&buf, "tsv", r); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "tls_adv_detected\ttls_adv_missed\ttls_adv_false_outage") {
		t.Fatalf("TSV header missing TLS advisory columns: %q", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("TSV lines = %d, want 2: %q", len(lines), out)
	}
	fields := strings.Split(lines[1], "\t")
	if len(fields) < 16 {
		t.Fatalf("TSV row has %d fields, want at least 16: %q", len(fields), lines[1])
	}
	if fields[13] != "1" || fields[14] != "1" || fields[15] != "1" {
		t.Fatalf("TSV row missing TLS advisory counts: %q", out)
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
