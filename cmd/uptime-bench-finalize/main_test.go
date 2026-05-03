package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
	"github.com/Automattic/uptime-bench/internal/db"
	benchreport "github.com/Automattic/uptime-bench/internal/report"
)

func TestWriteReportFilesIncludesCapacityArtifacts(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	report := benchreport.Report{
		Meta: benchreport.Meta{
			Input:             "campaign-a",
			MatchedAsConfigID: true,
			CampaignRuns:      1,
			EarliestStartedAt: &start,
			LatestEndedAt:     &end,
		},
	}
	capacityReport := capacitybench.Report{
		PrometheusURL: "http://prometheus.example.com:9090",
		Start:         start,
		End:           end,
		Step:          "15s",
		Instances:     []string{"jetmon-v1.example.com"},
		Summaries: []capacitybench.SeriesSummary{
			{
				Query:   "host_cpu_used",
				Unit:    "percent",
				Labels:  map[string]string{"instance": "jetmon-v1.example.com"},
				Samples: 3,
				Min:     10,
				Avg:     15,
				P50:     15,
				P95:     20,
				Max:     21,
				Last:    12,
			},
		},
	}

	if err := writeReportFiles(dir, report, &capacityReport, nil); err != nil {
		t.Fatalf("writeReportFiles: %v", err)
	}
	for _, name := range []string{"report.md", "report.json", "capacity.md", "capacity.json", "capacity.txt", "manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
	capacityMD, err := os.ReadFile(filepath.Join(dir, "capacity.md"))
	if err != nil {
		t.Fatalf("read capacity.md: %v", err)
	}
	if !strings.Contains(string(capacityMD), "## Raw Window Summary") {
		t.Fatalf("capacity.md missing raw summary:\n%s", string(capacityMD))
	}

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !contains(manifest.Files, "capacity.md") || !contains(manifest.Files, "capacity.json") || !contains(manifest.Files, "capacity.txt") {
		t.Fatalf("manifest files = %#v, want capacity artifacts", manifest.Files)
	}
}

func TestWriteReportFilesIncludesStandardArtifacts(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "harness.log"), []byte("harness log\n"), 0o644); err != nil {
		t.Fatalf("write harness log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target-status-after.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write target status: %v", err)
	}
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	report := benchreport.Report{
		Meta: benchreport.Meta{
			Input:             "tiny-run",
			MatchedAsRunID:    true,
			CampaignRuns:      1,
			EarliestStartedAt: &start,
			LatestEndedAt:     &end,
		},
	}
	artifacts := &finalizeArtifacts{
		CampaignRuns: []db.CampaignRunDetail{{
			ID:               "tiny-run",
			CampaignID:       "tiny-campaign",
			ConfigTOML:       tinyCampaignTOML,
			MasterSeed:       42,
			StartedAt:        start,
			EndedAt:          &end,
			ResolutionReason: "planned_completion",
		}},
		Tables: []db.ExportTable{
			{
				Name:   "campaign_runs",
				Header: []string{"id", "campaign_id"},
				Rows:   [][]string{{"tiny-run", "tiny-campaign"}},
			},
			{
				Name:   "scenario_runs",
				Header: []string{"id", "scenario_id", "campaign_id", "started_at", "ended_at", "resolution_reason"},
				Rows: [][]string{{
					"scenario-run-1",
					"tiny-campaign-d-0000-r0",
					"tiny-run",
					start.Format(time.RFC3339Nano),
					end.Format(time.RFC3339Nano),
					"planned_completion",
				}},
			},
			{Name: "ground_truth_events", Header: []string{"id", "run_id"}, Rows: [][]string{{"1", "scenario-run-1"}}},
			{Name: "monitor_reports", Header: []string{"id", "run_id"}, Rows: [][]string{{"1", "scenario-run-1"}}},
			{Name: "derived_metrics", Header: []string{"id", "run_id"}, Rows: [][]string{{"1", "scenario-run-1"}}},
		},
	}

	if err := writeReportFiles(dir, report, nil, artifacts); err != nil {
		t.Fatalf("writeReportFiles: %v", err)
	}
	for _, name := range []string{
		"run.meta.tsv",
		"campaign_runs.tsv",
		"scenario_runs.tsv",
		"ground_truth_events.tsv",
		"monitor_reports.tsv",
		"derived_metrics.tsv",
		"scenario-plan.tsv",
		"schedule.tsv",
		"campaigns/tiny-run.toml",
	} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}

	schedule, err := os.ReadFile(filepath.Join(dir, "schedule.tsv"))
	if err != nil {
		t.Fatalf("read schedule.tsv: %v", err)
	}
	if !strings.Contains(string(schedule), "scenario-run-1") {
		t.Fatalf("schedule.tsv missing actual run mapping:\n%s", string(schedule))
	}

	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Files []string `json:"files"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	for _, name := range []string{"run.meta.tsv", "scenario-plan.tsv", "schedule.tsv", "campaigns/tiny-run.toml", "logs/harness.log", "target-status-after.json"} {
		if !contains(manifest.Files, name) {
			t.Fatalf("manifest files = %#v, want %s", manifest.Files, name)
		}
	}
}

func TestEscapeTSVField(t *testing.T) {
	got := escapeTSVField("a\tb\nc\rd\\e")
	if got != `a\tb\nc\rd\\e` {
		t.Fatalf("escapeTSVField = %q", got)
	}
}

func TestCollectCapacityReportRequiresCompletedCampaign(t *testing.T) {
	start := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	_, err := collectCapacityReport(t.Context(), benchreport.Meta{
		EarliestStartedAt: &start,
	}, capacityOptions{
		prometheusURL: "http://prometheus.example.com:9090",
		instancesRaw:  defaultCapacityInstances,
		step:          15 * time.Second,
		rateWindow:    2 * time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "still in progress") {
		t.Fatalf("err = %v, want in-progress error", err)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

const tinyCampaignTOML = `
id              = "tiny-campaign"
description     = "x"
duration        = "1h"
seed            = 42
check_frequency = "60s"
grace_period    = "180s"

[targets]
pool     = ["bench-a"]
patterns = ["single"]

[duration_buckets]
brief = { min = "2m", max = "2m" }

[sampling]
samples_per_cell_default = 1

[[failure_types]]
type = "http_status"
status_code_choices = [503]

[cooldown]
per_target_minimum = "1m"
`
