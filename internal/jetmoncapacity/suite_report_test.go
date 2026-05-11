package jetmoncapacity

import (
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

func TestBuildSuiteReportTracksCleanAndProblemBatches(t *testing.T) {
	created := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	windowStart := created.Add(time.Minute)
	windowEnd := created.Add(6 * time.Minute)
	parent := RunManifest{
		ID:               "capacity-test",
		ConfigPath:       "configs/capacity/example.toml",
		OutDir:           "reports/capacity/example",
		Apply:            true,
		CreatedAt:        created,
		BatchDuration:    "5m0s",
		Cooldown:         "1m0s",
		PrometheusURL:    "http://prometheus.example.com:9090",
		Instances:        []string{"jetmon-v1.example.com", "jetmon-v2.example.com"},
		BatchCount:       2,
		TotalBatchCount:  3,
		BatchSizes:       []int{10, 20},
		SuiteStartCount:  10,
		SuiteStartSource: "explicit",
		SuiteStatePath:   "reports/capacity/state.json",
	}
	children := []RunManifest{
		{
			ActiveCount:      10,
			OutDir:           "reports/capacity/example/batch-0000010",
			LifecycleStatus:  "pass",
			HealthStatus:     "pass",
			PrometheusStatus: "pass",
			CleanupStatus:    "pass",
			WindowStart:      &windowStart,
			WindowEnd:        &windowEnd,
		},
		{
			ActiveCount:      20,
			OutDir:           "reports/capacity/example/batch-0000020",
			LifecycleStatus:  "pass",
			HealthStatus:     "fail",
			PrometheusStatus: "pass",
			CleanupStatus:    "pass",
			Health: []ServiceHealth{{
				Service:                "jetmon-v2",
				Action:                 "window-end-verify",
				Status:                 "pass",
				FreshnessSource:        "db_last_checked_at",
				ActiveSites:            int64Ptr(20),
				StaleActiveSites:       int64Ptr(4),
				MissedCheckPercent:     float64Ptr(20),
				RecentChecksPerMinute:  float64Ptr(3),
				FreshnessWindowMinutes: 5,
				CheckIntervals: []CheckIntervalRow{{
					CheckIntervalMinutes: 1,
					ActiveSites:          20,
				}},
			}},
			Thresholds: []ThresholdFinding{{
				Name:   "missed_check_percent",
				Status: "fail",
				Series: "jetmon-v2",
				Value:  20,
				Limit:  5,
				Reason: "above allowed limit",
			}},
			ReplayDetectionStatus: "fail",
			ReplayDetectionError:  "interval mismatch",
			ReplayDetections: []ReplayDetectionRun{{
				Status: "fail",
				Events: []ReplayDetectionEvent{{
					ID: "http-503-sample",
					Services: []ReplayDetectionServiceSummary{{
						Service:                          "jetmon-v2",
						Status:                           "fail",
						Hosts:                            2,
						EligibleHosts:                    1,
						DownDetected:                     1,
						RecoveryDetected:                 1,
						LateDownDetected:                 1,
						PreexistingDownOverlappedFailure: 1,
						ExpectedCheckIntervalSec:         300,
						NormalCheckIntervalMinSec:        intPtr(60),
						NormalCheckIntervalMaxSec:        intPtr(60),
						NextCheckIntervalMinSec:          intPtr(60),
						NextCheckIntervalMaxSec:          intPtr(60),
						CheckIntervalMismatchEvents:      1,
						DownLatencyMinSec:                float64Ptr(30),
						DownLatencyMeanSec:               float64Ptr(45),
						DownLatencyMaxSec:                float64Ptr(60),
						RecoveryLatencyMinSec:            float64Ptr(10),
						RecoveryLatencyMeanSec:           float64Ptr(20),
						RecoveryLatencyMaxSec:            float64Ptr(30),
						Error:                            "interval mismatch",
					}},
				}},
			}},
		},
	}

	report := buildSuiteReport(parent, children)

	if report.CompletedBatches != 2 {
		t.Fatalf("CompletedBatches = %d, want 2", report.CompletedBatches)
	}
	if report.LastCleanBatch != 10 {
		t.Fatalf("LastCleanBatch = %d, want 10", report.LastCleanBatch)
	}
	if report.FirstProblemBatch != 20 {
		t.Fatalf("FirstProblemBatch = %d, want 20", report.FirstProblemBatch)
	}
	if len(report.Batches) != 2 || report.Batches[0].Status != "pass" || report.Batches[1].Status != "fail" {
		t.Fatalf("batch statuses = %+v, want pass then fail", report.Batches)
	}
	if len(report.Batches[1].Health) != 1 || len(report.Batches[1].Thresholds) != 1 {
		t.Fatalf("problem batch details missing: %+v", report.Batches[1])
	}
	if len(report.Batches[1].ThroughputMargins) != 1 {
		t.Fatalf("throughput margins missing: %+v", report.Batches[1])
	}
	if len(report.Batches[1].ReplayDetections) != 1 {
		t.Fatalf("replay detections missing: %+v", report.Batches[1])
	}
	margin := report.Batches[1].ThroughputMargins[0]
	if margin.Status != "fail" || margin.RequiredChecksPerMinute == nil || *margin.RequiredChecksPerMinute != 4 {
		t.Fatalf("throughput margin = %+v, want fail with required/min 4", margin)
	}
	md := formatSuiteReportMarkdown(report)
	for _, want := range []string{
		"## Throughput Margin",
		"## Check Interval Distribution",
		"## Replay Detection",
		"| 20 | jetmon-v2 | window-end-verify | 1 | 20 |",
		"| 20 | jetmon-v2 | pass | fail | db_last_checked_at | 20 | 4 | 20.00 | - | - | 3.00",
		"| 20 | jetmon-v2 | fail | 20 | 5 | 4.00 | 3.00 | -1.00 | -25.00 | - |",
		"| 20 | http-503-sample | jetmon-v2 | fail | 2 | 1 | 1 | 1 | 1 | 1 | 300s | 60 | 60 | 1 | 30.00/45.00/60.00 | 10.00/20.00/30.00 | interval mismatch |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestFormatSuiteReportMarkdownHandlesNoBatches(t *testing.T) {
	report := SuiteReport{
		ID:            "capacity-test",
		ConfigPath:    "configs/capacity/example.toml",
		OutDir:        "reports/capacity/example",
		CreatedAt:     time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		BatchCount:    2,
		Instances:     []string{"jetmon-v1.example.com", "jetmon-v2.example.com"},
		StopReason:    "operator stopped | no completed batch",
		PrometheusURL: "http://prometheus.example.com:9090",
	}

	md := formatSuiteReportMarkdown(report)
	for _, want := range []string{
		"# Jetmon Capacity Suite Report",
		"- Last clean batch: `none`",
		"- First problem batch: `none`",
		"| 0 | none | not recorded | - | - | - | - | - | - | - | - | - | - | false | no completed batches |",
		"operator stopped \\| no completed batch",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestSuitePrometheusRowsFiltersAndSortsHighlights(t *testing.T) {
	report := SuiteReport{Batches: []SuiteBatchReport{
		{
			ActiveCount: 20,
			PrometheusSummary: []capacitybench.SeriesSummary{
				{Query: "uninteresting_metric", Labels: map[string]string{"instance": "jetmon-v2.example.com"}, Avg: 99},
				{Query: "host_memory_used", Labels: map[string]string{"instance": "jetmon-v2.example.com"}, Avg: 70},
			},
		},
		{
			ActiveCount: 10,
			PrometheusSummary: []capacitybench.SeriesSummary{
				{Query: "scrape_up", Labels: map[string]string{"instance": "jetmon-v1.example.com"}, Min: 1},
				{Query: "host_cpu_used", Labels: map[string]string{"instance": "jetmon-v1.example.com"}, Avg: 30},
			},
		},
	}}

	rows := suitePrometheusRows(report)

	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3: %+v", len(rows), rows)
	}
	got := []string{
		rows[0].Summary.Query,
		rows[1].Summary.Query,
		rows[2].Summary.Query,
	}
	want := []string{"host_cpu_used", "scrape_up", "host_memory_used"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d query = %q, want %q; rows=%+v", i, got[i], want[i], rows)
		}
	}
	if rows[0].Batch != 10 || rows[2].Batch != 20 {
		t.Fatalf("row batch order = %d,%d,%d, want 10,10,20", rows[0].Batch, rows[1].Batch, rows[2].Batch)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func float64Ptr(v float64) *float64 {
	return &v
}
