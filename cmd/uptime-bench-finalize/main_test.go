package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
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

	if err := writeReportFiles(dir, report, &capacityReport); err != nil {
		t.Fatalf("writeReportFiles: %v", err)
	}
	for _, name := range []string{"report.md", "report.json", "capacity.md", "capacity.json", "manifest.json"} {
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
	if !contains(manifest.Files, "capacity.md") || !contains(manifest.Files, "capacity.json") {
		t.Fatalf("manifest files = %#v, want capacity artifacts", manifest.Files)
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
