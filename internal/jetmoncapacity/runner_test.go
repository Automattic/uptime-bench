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
			report: scrapeUpReport("jetmon-v1", "jetmon-v2"),
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
			report: scrapeUpReport("jetmon-v1", "jetmon-v2"),
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
			report: scrapeUpReport("jetmon-v1", "jetmon-v2"),
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

func TestRunBatchContinuesWhenPrometheusCaptureFails(t *testing.T) {
	cfgPath := writeRunnerPrometheusConfig(t)
	outDir := filepath.Join(t.TempDir(), "out")
	exec := &fakeSQLExecutor{
		activeByDSN:  map[string]int64{},
		staleByDSN:   map[string]int64{"v2-dsn": 8},
		historyByDSN: map[string]int64{"v2-dsn": 2},
	}
	collector := &sequenceCollector{
		reports: []capacitybench.Report{scrapeUpReport("jetmon-v1", "jetmon-v2")},
		errs:    []error{nil, errors.New("prometheus unavailable")},
	}
	manifest, err := (Runner{
		Executor:  exec,
		Clock:     fixedClock{},
		Sleeper:   noSleep{},
		Collector: collector,
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      outDir,
		Apply:       true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if manifest.PrometheusStatus != "fail" {
		t.Fatalf("PrometheusStatus = %q, want fail", manifest.PrometheusStatus)
	}
	if manifest.CleanupStatus != "pass" {
		t.Fatalf("CleanupStatus = %q, want pass", manifest.CleanupStatus)
	}
	if manifest.HealthStatus != "fail" {
		t.Fatalf("HealthStatus = %q, want fail", manifest.HealthStatus)
	}
	if !manifest.StopRecommended {
		t.Fatalf("StopRecommended = false, want true; thresholds: %#v", manifest.Thresholds)
	}
	var sawPromNotMeasured, sawMissedFail bool
	for _, finding := range manifest.Thresholds {
		if finding.Name == "prometheus" && finding.Status == "not_measured" {
			sawPromNotMeasured = true
		}
		if finding.Name == "missed_check_percent" && finding.Series == "jetmon-v2" && finding.Status == "fail" {
			sawMissedFail = true
		}
	}
	if !sawPromNotMeasured || !sawMissedFail {
		t.Fatalf("thresholds missing prometheus not_measured or missed-check fail: %#v", manifest.Thresholds)
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(data)
	for _, want := range []string{"Prometheus Status: fail", "Cleanup Status: pass", "Health Status: fail"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestRunBatchPreflightRejectsExamplePrometheusForApply(t *testing.T) {
	cfgPath := writeRunnerConfig(t, withPrometheus("http://prometheus.example.com:9090", []string{"jetmon-v1.example.com", "jetmon-v2.example.com"}))
	exec := &fakeSQLExecutor{activeByDSN: map[string]int64{}}
	manifest, err := (Runner{
		Executor:  exec,
		Clock:     fixedClock{},
		Sleeper:   noSleep{},
		Collector: fakeCollector{report: scrapeUpReport("jetmon-v1.example.com", "jetmon-v2.example.com")},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      filepath.Join(t.TempDir(), "out"),
		Apply:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "example Prometheus configuration") {
		t.Fatalf("Run error = %v, want example Prometheus preflight error", err)
	}
	if manifest.PrometheusStatus != "preflight_failed" {
		t.Fatalf("PrometheusStatus = %q, want preflight_failed", manifest.PrometheusStatus)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("SQL ran despite Prometheus preflight failure: %#v", exec.calls)
	}
}

func TestRunBatchPreflightRequiresPrometheusForApply(t *testing.T) {
	cfgPath := writeRunnerConfig(t, withPrometheus("", nil))
	exec := &fakeSQLExecutor{activeByDSN: map[string]int64{}}
	manifest, err := (Runner{
		Executor: exec,
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:  cfgPath,
		Mode:        "run-batch",
		ActiveCount: 10,
		OutDir:      filepath.Join(t.TempDir(), "out"),
		Apply:       true,
	})
	if err == nil || !strings.Contains(err.Error(), "prometheus_url is required") {
		t.Fatalf("Run error = %v, want missing Prometheus preflight error", err)
	}
	if manifest.PrometheusStatus != "preflight_failed" {
		t.Fatalf("PrometheusStatus = %q, want preflight_failed", manifest.PrometheusStatus)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("SQL ran despite Prometheus preflight failure: %#v", exec.calls)
	}
}

func TestServiceHealthIncludesFreshnessDetails(t *testing.T) {
	service := ServiceLifecycle{ID: "jetmon-v2", Config: Config{Schema: SchemaV2}}
	result := SQLExecutionResult{
		Statements: []SQLStatementResult{
			{Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", "10"}}},
			{Columns: []string{"stale_active_sites"}, Rows: [][]string{{"2"}}},
			{Columns: []string{"recent_check_history_rows"}, Rows: [][]string{{"25"}}},
			{Columns: []string{"freshness_samples", "freshest_check_age_sec", "average_check_age_sec", "p50_check_age_sec", "p95_check_age_sec", "p99_check_age_sec", "oldest_check_age_sec"}, Rows: [][]string{{"8", "1", "12.5", "9", "30", "42", "60"}}},
			{Columns: []string{"bucket_no", "active_sites", "stale_active_sites"}, Rows: [][]string{{"3", "5", "1"}, {"4", "5", "1"}}},
		},
	}
	health := serviceHealthFromVerify(service, "window-end-verify", result, 10)
	if health.MissedCheckPercent == nil || *health.MissedCheckPercent != 20 {
		t.Fatalf("MissedCheckPercent = %v, want 20", health.MissedCheckPercent)
	}
	if health.RecentChecksPerMinute == nil || *health.RecentChecksPerMinute != 5 {
		t.Fatalf("RecentChecksPerMinute = %v, want 5", health.RecentChecksPerMinute)
	}
	if health.P95CheckAgeSec == nil || *health.P95CheckAgeSec != 30 {
		t.Fatalf("P95CheckAgeSec = %v, want 30", health.P95CheckAgeSec)
	}
	if len(health.StaleBuckets) != 2 || health.StaleBuckets[0].StalePercent != 20 {
		t.Fatalf("StaleBuckets = %#v, want two 20%% buckets", health.StaleBuckets)
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

func TestRunSuiteDefaultsToLastCompletedBatchState(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")
	if err := writeSuiteState(statePath, SuiteState{
		ID:                 "capacity-test",
		LastCompletedBatch: 20,
		LastCompletedAt:    time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		LastRunDir:         filepath.Join(dir, "previous"),
		LastBatchDir:       filepath.Join(dir, "previous", "batch-0000020"),
	}); err != nil {
		t.Fatalf("write suite state: %v", err)
	}

	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:       cfgPath,
		Mode:             "run-suite",
		OutDir:           filepath.Join(dir, "out"),
		SuiteStatePath:   statePath,
		DurationOverride: 2 * time.Minute,
		Apply:            false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if manifest.SuiteStartSource != "state" {
		t.Fatalf("SuiteStartSource = %q, want state", manifest.SuiteStartSource)
	}
	if manifest.SuiteStartCount != 20 {
		t.Fatalf("SuiteStartCount = %d, want 20", manifest.SuiteStartCount)
	}
	if got := joinInts(manifest.BatchSizes); got != "20" {
		t.Fatalf("BatchSizes = %s, want 20", got)
	}
	if manifest.BatchCount != 1 || manifest.TotalBatchCount != 2 {
		t.Fatalf("batch counts = %d/%d, want 1 selected / 2 total", manifest.BatchCount, manifest.TotalBatchCount)
	}
	if manifest.EstimatedRuntime != "2m0s" {
		t.Fatalf("EstimatedRuntime = %q, want 2m0s", manifest.EstimatedRuntime)
	}
	data, err := os.ReadFile(filepath.Join(dir, "out", "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	summary := string(data)
	for _, want := range []string{"Batch Count: 1", "Total Configured Batches: 2", "Batch Sizes: 20", "Suite Start Source: state"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestRunSuiteFullSuiteIgnoresState(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")
	if err := writeSuiteState(statePath, SuiteState{
		ID:                 "capacity-test",
		LastCompletedBatch: 20,
		LastCompletedAt:    time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("write suite state: %v", err)
	}

	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:     cfgPath,
		Mode:           "run-suite",
		OutDir:         filepath.Join(dir, "out"),
		SuiteStatePath: statePath,
		FullSuite:      true,
		Apply:          false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if manifest.SuiteStartSource != "full_suite" {
		t.Fatalf("SuiteStartSource = %q, want full_suite", manifest.SuiteStartSource)
	}
	if got := joinInts(manifest.BatchSizes); got != "10,20" {
		t.Fatalf("BatchSizes = %s, want 10,20", got)
	}
}

func TestRunSuiteExplicitBatchSizesAndStartCount(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath:       cfgPath,
		Mode:             "run-suite",
		OutDir:           filepath.Join(t.TempDir(), "out"),
		BatchSizes:       []int{10, 50, 90},
		SuiteStartCount:  40,
		DurationOverride: time.Minute,
		CooldownOverride: 2 * time.Second,
		Apply:            false,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if manifest.SuiteStartSource != "explicit" {
		t.Fatalf("SuiteStartSource = %q, want explicit", manifest.SuiteStartSource)
	}
	if manifest.SuiteStartCount != 50 {
		t.Fatalf("SuiteStartCount = %d, want next available batch 50", manifest.SuiteStartCount)
	}
	if got := joinInts(manifest.BatchSizes); got != "50,90" {
		t.Fatalf("BatchSizes = %s, want 50,90", got)
	}
	if manifest.EstimatedRuntime != "2m2s" {
		t.Fatalf("EstimatedRuntime = %q, want 2m2s", manifest.EstimatedRuntime)
	}
}

func TestRunSuiteRejectsUnsortedBatchSizes(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	_, err := (Runner{
		Executor: &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:    fixedClock{},
		Sleeper:  noSleep{},
	}).Run(context.Background(), RunOptions{
		ConfigPath: cfgPath,
		Mode:       "run-suite",
		OutDir:     filepath.Join(t.TempDir(), "out"),
		BatchSizes: []int{10, 50, 20},
		Apply:      false,
	})
	if err == nil || !strings.Contains(err.Error(), "strictly increasing") {
		t.Fatalf("Run error = %v, want strictly increasing batch size error", err)
	}
}

func TestRunSuiteApplyWritesSuiteStateAfterSuccessfulBatch(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")

	_, err := (Runner{
		Executor:  &fakeSQLExecutor{},
		Clock:     fixedClock{},
		Sleeper:   noSleep{},
		Collector: fakeCollector{report: scrapeUpReport("jetmon-v1", "jetmon-v2")},
	}).Run(context.Background(), RunOptions{
		ConfigPath:     cfgPath,
		Mode:           "run-suite",
		OutDir:         filepath.Join(dir, "out"),
		SuiteStatePath: statePath,
		BatchSizes:     []int{10},
		Apply:          true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	state, err := readSuiteState(statePath)
	if err != nil {
		t.Fatalf("read suite state: %v", err)
	}
	if state == nil {
		t.Fatal("suite state was not written")
	}
	if state.LastCompletedBatch != 10 {
		t.Fatalf("LastCompletedBatch = %d, want 10", state.LastCompletedBatch)
	}
	if got := joinInts(state.CompletedBatchSequence); got != "10" {
		t.Fatalf("CompletedBatchSequence = %s, want 10", got)
	}
}

type runnerConfigOption func(*runnerConfigSpec)

type runnerConfigSpec struct {
	prometheusURL string
	instances     []string
}

func withPrometheus(prometheusURL string, instances []string) runnerConfigOption {
	return func(spec *runnerConfigSpec) {
		spec.prometheusURL = prometheusURL
		spec.instances = append([]string(nil), instances...)
	}
}

func writeRunnerPrometheusConfig(t *testing.T) string {
	t.Helper()
	return writeRunnerConfig(t)
}

func writeRunnerConfig(t *testing.T, opts ...runnerConfigOption) string {
	t.Helper()
	t.Setenv("JETMON_V1_DB_DSN", "v1-dsn")
	t.Setenv("JETMON_V2_DB_DSN", "v2-dsn")
	spec := runnerConfigSpec{
		prometheusURL: "http://prometheus:9090",
		instances:     []string{"jetmon-v1", "jetmon-v2"},
	}
	for _, opt := range opts {
		opt(&spec)
	}
	var promConfig string
	if spec.prometheusURL != "" || len(spec.instances) > 0 {
		promConfig = "prometheus_url = " + strconv.Quote(spec.prometheusURL) + "\ninstances = ["
		for i, instance := range spec.instances {
			if i > 0 {
				promConfig += ", "
			}
			promConfig += strconv.Quote(instance)
		}
		promConfig += "]\n"
	}
	path := filepath.Join(t.TempDir(), "capacity.toml")
	content := `
id = "capacity-test"
` + promConfig + `

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
	staleByDSN      map[string]int64
	historyByDSN    map[string]int64
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
		stale := e.staleByDSN[dsn]
		if stale > active {
			stale = active
		}
		history := e.historyByDSN[dsn]
		return SQLExecutionResult{
			StatementCount: 7,
			Statements: []SQLStatementResult{
				{Index: 1, Keyword: "SELECT", Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", intString(active)}}},
				{Index: 2, Keyword: "SELECT", Columns: []string{"bucket_no", "benchmark_sites", "active_sites"}, Rows: [][]string{{"10", "100", intString(active)}}},
				{Index: 3, Keyword: "SELECT", Columns: []string{"stale_active_sites"}, Rows: [][]string{{intString(stale)}}},
				{Index: 4, Keyword: "SELECT", Columns: []string{"open_events"}, Rows: [][]string{{"0"}}},
				{Index: 5, Keyword: "SELECT", Columns: []string{"recent_check_history_rows"}, Rows: [][]string{{intString(history)}}},
				{Index: 6, Keyword: "SELECT", Columns: []string{"freshness_samples", "freshest_check_age_sec", "average_check_age_sec", "p50_check_age_sec", "p95_check_age_sec", "p99_check_age_sec", "oldest_check_age_sec"}, Rows: [][]string{{intString(active - stale), "1", "10.5", "10", "20", "30", "40"}}},
				{Index: 7, Keyword: "SELECT", Columns: []string{"bucket_no", "active_sites", "stale_active_sites"}, Rows: [][]string{{"10", intString(active), intString(stale)}}},
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

type sequenceCollector struct {
	reports []capacitybench.Report
	errs    []error
	calls   int
}

func (c *sequenceCollector) Collect(context.Context, string, []string, time.Time, time.Time, time.Duration, time.Duration) (capacitybench.Report, error) {
	i := c.calls
	c.calls++
	var report capacitybench.Report
	if i < len(c.reports) {
		report = c.reports[i]
	}
	var err error
	if i < len(c.errs) {
		err = c.errs[i]
	}
	return report, err
}

func scrapeUpReport(instances ...string) capacitybench.Report {
	report := capacitybench.Report{Instances: instances}
	for _, instance := range instances {
		report.Summaries = append(report.Summaries, capacitybench.SeriesSummary{
			Query:   "scrape_up",
			Labels:  map[string]string{"instance": instance, "job": "node"},
			Samples: 1,
			Min:     1,
			Avg:     1,
			P50:     1,
			P95:     1,
			Max:     1,
			Last:    1,
		})
	}
	return report
}
