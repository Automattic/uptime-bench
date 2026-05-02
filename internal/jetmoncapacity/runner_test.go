package jetmoncapacity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

func TestRunBatchCleansUpPartialActivation(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	exec := &fakeSQLExecutor{
		activeByDSN:     map[string]int64{"v1-dsn": 10, "v2-dsn": 0},
		failActivateDSN: "v2-dsn",
	}
	_, err := (Runner{
		Executor: exec,
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
		Collector: fakeCollector{
			report: capacitybench.Report{},
		},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      filepath.Join(t.TempDir(), "out"),
		Apply:       true,
	})
	if err == nil {
		t.Fatal("Run succeeded, want v2 activation error")
	}
	if !exec.sawDeactivation("v1-dsn") {
		t.Fatalf("v1 was not deactivated after partial activation; calls: %#v", exec.calls)
	}
}

func TestRunBatchCleansUpAfterInterruptedWindow(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	exec := &fakeSQLExecutor{activeByDSN: map[string]int64{}}
	_, err := (Runner{
		Executor: exec,
		Clock:    fixedClock{},
		Sleeper:  cancelSleep{},
		Collector: fakeCollector{
			report: capacitybench.Report{},
		},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      filepath.Join(t.TempDir(), "out"),
		Apply:       true,
	})
	if err == nil {
		t.Fatal("Run succeeded, want interrupted window error")
	}
	if !exec.sawDeactivation("v1-dsn") || !exec.sawDeactivation("v2-dsn") {
		t.Fatalf("expected both services to be deactivated after interruption; calls: %#v", exec.calls)
	}
}

func TestRunBatchFailsWhenActivationCountDoesNotMatch(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	exec := &fakeSQLExecutor{activeByDSN: map[string]int64{"v1-dsn": 0}}
	_, err := (Runner{
		Executor: exec,
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
		Collector: fakeCollector{
			report: capacitybench.Report{},
		},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      filepath.Join(t.TempDir(), "out"),
		Apply:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "active count") {
		t.Fatalf("Run error = %v, want active count error", err)
	}
	if !exec.sawDeactivation("v1-dsn") {
		t.Fatalf("expected cleanup after active count mismatch; calls: %#v", exec.calls)
	}
}

func TestSeedRequiresForceWhenBenchmarkRowsExist(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	exec := &fakeSQLExecutor{
		seedTotal:    10,
		seedMatching: 10,
	}
	_, err := (Runner{
		Executor: exec,
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath: cfgPath,
		Mode:       "seed",
		OutDir:     filepath.Join(t.TempDir(), "out"),
		Apply:      true,
	})
	if err == nil || !strings.Contains(err.Error(), "force-reseed") {
		t.Fatalf("Run error = %v, want force-reseed error", err)
	}
	if exec.sawSeedMutation() {
		t.Fatalf("seed mutation ran despite preflight failure; calls: %#v", exec.calls)
	}
}

func TestEvaluateThresholdsStopsOnResourceFailure(t *testing.T) {
	report := &capacitybench.Report{
		Summaries: []capacitybench.SeriesSummary{
			{Query: "host_cpu_used", Labels: map[string]string{"instance": "jetmon-v1.example.com"}, Max: 92},
			{Query: "scrape_up", Labels: map[string]string{"instance": "jetmon-v1.example.com", "job": "node"}, Min: 1},
		},
	}
	findings := EvaluateThresholds(nil, "http://prometheus", StopThresholds{
		HostCPUPercent: 85,
		ScrapeUpMin:    1,
	}, report)
	var failed bool
	for _, finding := range findings {
		if finding.Name == "host_cpu_used" && finding.Status == "fail" {
			failed = true
		}
	}
	if !failed {
		t.Fatalf("expected host_cpu_used failure, got %#v", findings)
	}
}

func TestRunWritesOperatorSummary(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	outDir := filepath.Join(t.TempDir(), "out")
	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      outDir,
		Apply:       false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(data)
	for _, want := range []string{"Jetmon Capacity Run", "Mode: run-batch", "Apply: false", "Active Count: 10"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
	var found bool
	for _, artifact := range manifest.Artifacts {
		if artifact.Action == "summary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest did not include summary artifact: %#v", manifest.Artifacts)
	}
}

func TestRunSuiteSummaryIncludesRuntimeEstimate(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	outDir := filepath.Join(t.TempDir(), "out")
	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:       cfgPath,
		Mode:             "run-suite",
		DurationOverride: 2 * time.Minute,
		OutDir:           outDir,
		Apply:            false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if manifest.BatchCount != 2 {
		t.Fatalf("BatchCount = %d, want 2", manifest.BatchCount)
	}
	if manifest.EstimatedRuntime != "4m1s" {
		t.Fatalf("EstimatedRuntime = %q, want 4m1s", manifest.EstimatedRuntime)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(data)
	for _, want := range []string{"Batch Count: 2", "Estimated Runtime: 4m1s"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func writeRunnerConfig(t *testing.T) string {
	t.Helper()
	t.Setenv("JETMON_V1_DB_DSN", "v1-dsn")
	t.Setenv("JETMON_V2_DB_DSN", "v2-dsn")
	path := filepath.Join(t.TempDir(), "capacity.toml")
	content := `
id = "capacity-test"
prometheus_url = "http://prometheus:9090"
instances = ["jetmon-v1.example.com", "jetmon-v2.example.com"]

[targets]
url_pattern = "http://site-%07d.load.example.test/"
count = 100

[checks]
interval = "1m"

[window]
baseline_duration = "1m"
step = "15s"
rate_window = "1m"

[batches]
sizes = [10, 20]
duration = "1m"
cooldown = "1s"

[stop_thresholds]
host_cpu_percent = 85
missed_check_percent = 1

[jetmon_v1.lifecycle]
schema = "v1"
blog_id_start = 8000000000000000
count = 100
bucket_min = 0
bucket_max = 9
dsn_env = "JETMON_V1_DB_DSN"

[jetmon_v2.lifecycle]
schema = "v2"
blog_id_start = 8000001000000000
count = 100
bucket_min = 10
bucket_max = 19
dsn_env = "JETMON_V2_DB_DSN"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

type fakeSQLExecutor struct {
	calls           []fakeSQLCall
	activeByDSN     map[string]int64
	failActivateDSN string
	seedTotal       int64
	seedMatching    int64
}

type fakeSQLCall struct {
	dsn string
	sql string
}

func (e *fakeSQLExecutor) ExecuteSQL(ctx context.Context, dsn string, sqlText string) (SQLExecutionResult, error) {
	e.calls = append(e.calls, fakeSQLCall{dsn: dsn, sql: sqlText})
	switch {
	case strings.Contains(sqlText, "COUNT(*) AS total_rows"):
		return singleRowResult([]string{"total_rows", "matching_url_rows"}, []string{intString(e.seedTotal), intString(e.seedMatching)}), nil
	case strings.Contains(sqlText, "Activate the requested batch prefix."):
		if dsn == e.failActivateDSN {
			return SQLExecutionResult{}, errors.New("activate failed")
		}
		if e.activeByDSN == nil {
			e.activeByDSN = map[string]int64{}
		}
		if _, ok := e.activeByDSN[dsn]; !ok {
			e.activeByDSN[dsn] = 10
		}
		return SQLExecutionResult{StatementCount: 1}, nil
	case strings.Contains(sqlText, "Deactivate every benchmark-owned site row."):
		if e.activeByDSN != nil {
			e.activeByDSN[dsn] = 0
		}
		return SQLExecutionResult{StatementCount: 1}, nil
	case strings.Contains(sqlText, "COUNT(*) AS active_sites") && !strings.Contains(sqlText, "benchmark_sites"):
		return singleRowResult([]string{"active_sites"}, []string{intString(e.activeByDSN[dsn])}), nil
	case strings.Contains(sqlText, "COUNT(*) AS benchmark_sites"):
		active := e.activeByDSN[dsn]
		return SQLExecutionResult{
			StatementCount: 1,
			Statements: []SQLStatementResult{
				{Index: 1, Keyword: "SELECT", Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", intString(active)}}},
				{Index: 2, Keyword: "SELECT", Columns: []string{"stale_active_sites"}, Rows: [][]string{{"0"}}},
			},
		}, nil
	default:
		return SQLExecutionResult{StatementCount: 1}, nil
	}
}

func (e *fakeSQLExecutor) sawDeactivation(dsn string) bool {
	for _, call := range e.calls {
		if call.dsn == dsn && strings.Contains(call.sql, "Deactivate every benchmark-owned site row.") {
			return true
		}
	}
	return false
}

func (e *fakeSQLExecutor) sawSeedMutation() bool {
	for _, call := range e.calls {
		if strings.Contains(call.sql, "DELETE FROM jetpack_monitor_sites") {
			return true
		}
	}
	return false
}

func singleRowResult(columns []string, row []string) SQLExecutionResult {
	return SQLExecutionResult{
		StatementCount: 1,
		Statements: []SQLStatementResult{
			{Index: 1, Keyword: "SELECT", Columns: columns, Rows: [][]string{row}},
		},
	}
}

func intString(value int64) string {
	return strconv.FormatInt(value, 10)
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Unix(1_700_000_000, 0).UTC() }

type noSleep struct{}

func (noSleep) Sleep(context.Context, time.Duration) error { return nil }

type cancelSleep struct{}

func (cancelSleep) Sleep(context.Context, time.Duration) error { return context.Canceled }

type fakeCollector struct {
	report capacitybench.Report
}

func (c fakeCollector) Collect(context.Context, string, []string, time.Time, time.Time, time.Duration, time.Duration) (capacitybench.Report, error) {
	return c.report, nil
}
