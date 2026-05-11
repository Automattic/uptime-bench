package jetmoncapacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
	"github.com/Automattic/uptime-bench/internal/targetserver"
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

func TestRunBatchCapturesTargetObserverWindow(t *testing.T) {
	cfgPath := writeRunnerConfig(t, withTargetObserver("http://target-control.test:9000"))
	outDir := filepath.Join(t.TempDir(), "out")
	observer := &fakeTargetObserverClient{}
	manifest, err := (Runner{
		Executor:       &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:          fixedClock{},
		Sleeper:        noSleep{},
		ObserverClient: observer,
		Collector: fakeCollector{
			report: scrapeUpReport("jetmon-v1", "jetmon-v2"),
		},
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
	if observer.resetCalls != 1 || observer.summaryCalls != 1 {
		t.Fatalf("observer calls reset/summary = %d/%d, want 1/1", observer.resetCalls, observer.summaryCalls)
	}
	if observer.lastReset.ActiveCount != 10 || len(observer.lastReset.Services) != 2 {
		t.Fatalf("reset request = %#v, want active count 10 and two services", observer.lastReset)
	}
	if manifest.TargetObserverStatus != "pass" {
		t.Fatalf("TargetObserverStatus = %q, want pass", manifest.TargetObserverStatus)
	}
	if len(manifest.TargetObservations) != 2 {
		t.Fatalf("TargetObservations = %d, want reset + window", len(manifest.TargetObservations))
	}
	for _, name := range []string{"target-observer-reset.json", "target-observer-window.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(data), "Target Observer:") {
		t.Fatalf("summary missing target observer section:\n%s", string(data))
	}
}

func TestRunBatchCapturesDiskIOAttribution(t *testing.T) {
	cfgPath := writeRunnerConfig(t, withDiskIOAttribution())
	outDir := filepath.Join(t.TempDir(), "out")
	report := scrapeUpReport("jetmon-v1", "jetmon-v2")
	report.Summaries = append(report.Summaries,
		capacitybench.SeriesSummary{Query: "host_disk_read_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2"}, Avg: 2048},
		capacitybench.SeriesSummary{Query: "host_disk_written_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2"}, Avg: 4096},
		capacitybench.SeriesSummary{Query: "docker_container_block_read_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2", "container": "mysql"}, Avg: 512},
		capacitybench.SeriesSummary{Query: "docker_container_block_write_bytes", Unit: "bytes_per_second", Labels: map[string]string{"instance": "jetmon-v2", "container": "mysql"}, Avg: 1024},
	)
	diskCollector := &fakeDiskIOAttributionCollector{run: DiskIOAttributionRun{
		Status: "pass",
		Hosts: []DiskIOAttributionHost{{
			ID:       "jetmon-v2",
			Instance: "jetmon-v2",
			SSHHost:  "jetmon-v2",
			Status:   "pass",
			ProcessStart: []ProcessIOSnapshot{{
				PID:            123,
				StartTimeTicks: 10,
				Comm:           "jetmon2",
				Label:          "jetmon2",
				Counters:       ProcessIOCounters{ReadBytes: 1000, WriteBytes: 2000},
			}},
			ProcessEnd: []ProcessIOSnapshot{{
				PID:            123,
				StartTimeTicks: 10,
				Comm:           "jetmon2",
				Label:          "jetmon2",
				Counters:       ProcessIOCounters{ReadBytes: 2000, WriteBytes: 4000},
			}},
			ProcessDeltas: []ProcessIODelta{{
				PID:                 123,
				Label:               "jetmon2",
				Delta:               ProcessIOCounters{ReadBytes: 1000, WriteBytes: 2000},
				ReadBytesPerSecond:  1000,
				WriteBytesPerSecond: 2000,
			}},
			TopReadProcesses: []ProcessIODelta{{
				PID:                123,
				Label:              "jetmon2",
				ReadBytesPerSecond: 1000,
			}},
			TopWriteProcesses: []ProcessIODelta{{
				PID:                 123,
				Label:               "jetmon2",
				WriteBytesPerSecond: 2000,
			}},
		}},
	}}
	manifest, err := (Runner{
		Executor:          &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:             fixedClock{},
		Sleeper:           noSleep{},
		Collector:         fakeCollector{report: report},
		DiskIOAttribution: diskCollector,
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
	if diskCollector.startCalls != 1 || diskCollector.finishCalls != 1 {
		t.Fatalf("disk collector calls start/finish = %d/%d, want 1/1", diskCollector.startCalls, diskCollector.finishCalls)
	}
	if manifest.DiskIOAttributionStatus != "complete" {
		t.Fatalf("DiskIOAttributionStatus = %q, want complete", manifest.DiskIOAttributionStatus)
	}
	for _, name := range []string{"process-io-start.json", "process-io-end.json", "process-io-delta.json", "mounts-window.json", "pidstat-window.txt", "iostat-window.txt", "disk-io-attribution.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(outDir, "summary.txt"))
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(data), "Disk I/O Attribution:") {
		t.Fatalf("summary missing disk I/O section:\n%s", string(data))
	}
}

func TestRunBatchFailsOnTargetObserverGaps(t *testing.T) {
	cfgPath := writeRunnerConfig(t, withTargetObserver("http://target-control.test:9000"))
	outDir := filepath.Join(t.TempDir(), "out")
	observer := &fakeTargetObserverClient{
		summary: targetserver.CapacityObserveSummary{
			Active:         true,
			RunID:          "batch-10",
			SnapshotAt:     time.Unix(1_700_000_060, 0).UTC(),
			ElapsedSeconds: 60,
			ActiveCount:    10,
			Services: []targetserver.CapacityObserveServiceSummary{{
				ID:                  "jetmon-v1",
				ExpectedSites:       10,
				ObservedSites:       9,
				NeverSeenSites:      1,
				StaleSites:          1,
				CoveragePercent:     90,
				TotalRequests:       9,
				RequestsPerSecond:   0.15,
				RequestsPerSiteMean: 0.9,
			}},
		},
	}
	manifest, err := (Runner{
		Executor:       &fakeSQLExecutor{activeByDSN: map[string]int64{}},
		Clock:          fixedClock{},
		Sleeper:        noSleep{},
		ObserverClient: observer,
		Collector: fakeCollector{
			report: scrapeUpReport("jetmon-v1", "jetmon-v2"),
		},
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
	if manifest.TargetObserverStatus != "fail" {
		t.Fatalf("TargetObserverStatus = %q, want fail", manifest.TargetObserverStatus)
	}
	if !manifest.StopRecommended || !strings.Contains(manifest.StopReason, "target_observer_never_seen_sites") {
		t.Fatalf("stop recommendation = %t %q, want never-seen stop", manifest.StopRecommended, manifest.StopReason)
	}
	if got := suiteBatchStatus(manifest); got != "fail" {
		t.Fatalf("suiteBatchStatus = %q, want fail", got)
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

func TestEvaluateTargetObserverThresholdsFailsOverCheckRatio(t *testing.T) {
	findings := EvaluateTargetObserverThresholds(targetserver.CapacityObserveSummary{
		Services: []targetserver.CapacityObserveServiceSummary{{
			ID:                   "jetmon-v2",
			ExpectedRequestRatio: 2.31,
		}},
	}, TargetObserverConfig{
		Enabled:                 true,
		MinExpectedRequestRatio: 0.8,
		MaxExpectedRequestRatio: 1.25,
	})

	var maxFinding *ThresholdFinding
	for i := range findings {
		if findings[i].Name == "target_observer_expected_request_ratio_max" {
			maxFinding = &findings[i]
			break
		}
	}
	if maxFinding == nil {
		t.Fatalf("max expected request ratio finding missing: %+v", findings)
	}
	if maxFinding.Status != "fail" || !strings.Contains(maxFinding.Reason, "above") {
		t.Fatalf("max finding = %+v, want fail above limit", *maxFinding)
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

func TestRunBatchTargetPreflightRejectsActivatedURLMismatch(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	exec := &fakeSQLExecutor{
		activeByDSN:     map[string]int64{},
		badSampleURLDSN: "v2-dsn",
	}
	manifest, err := (Runner{
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
	if err == nil || !strings.Contains(err.Error(), "target preflight") {
		t.Fatalf("Run error = %v, want target preflight mismatch", err)
	}
	if !exec.sawDeactivation("v1-dsn") || !exec.sawDeactivation("v2-dsn") {
		t.Fatalf("expected cleanup after target preflight failure; calls: %#v", exec.calls)
	}
	if len(manifest.TargetPreflights) != 2 || manifest.TargetPreflights[1].Status != "fail" {
		t.Fatalf("TargetPreflights = %+v, want v2 failure recorded", manifest.TargetPreflights)
	}
}

func TestPreflightServiceTargetsChecksEveryConfiguredSource(t *testing.T) {
	exec := &fakeSQLExecutor{}
	checker := &recordingURLChecker{}
	cfg := RunConfig{
		TargetPreflight: TargetPreflightConfig{
			CheckSources:   []string{"runner", "jetmon-service-host-1"},
			ExpectedStatus: 200,
		},
	}
	service := ServiceLifecycle{
		ID:     "jetmon-v1",
		DSN:    "v1-dsn",
		HasDSN: true,
		Config: Config{
			Schema:               SchemaV1,
			BlogIDStart:          8000000000000000,
			Count:                100,
			BucketMin:            0,
			BucketMax:            9,
			URLPattern:           "http://site-%07d.load.example.test/",
			URLNumberStart:       1,
			CheckIntervalMinutes: 1,
		},
	}

	preflight, err := (Runner{
		Executor:   exec,
		URLChecker: checker,
	}).preflightServiceTargets(context.Background(), service, cfg, 3, time.Second)
	if err != nil {
		t.Fatalf("preflightServiceTargets: %v", err)
	}
	if preflight.Status != "pass" {
		t.Fatalf("preflight status = %q, want pass", preflight.Status)
	}
	if len(preflight.Samples) != 10 {
		t.Fatalf("samples = %d, want 10", len(preflight.Samples))
	}
	for _, sample := range preflight.Samples {
		if len(sample.Checks) != 2 {
			t.Fatalf("sample checks = %#v, want two sources", sample.Checks)
		}
		if sample.Checks[0].Source != "runner" || sample.Checks[1].Source != "jetmon-service-host-1" {
			t.Fatalf("sample checks = %#v, want runner and service host", sample.Checks)
		}
	}
	if len(checker.calls) != 20 {
		t.Fatalf("checker calls = %#v, want 20 calls", checker.calls)
	}
}

func TestPreflightServiceTargetsRetriesTransientURLCheck(t *testing.T) {
	exec := &fakeSQLExecutor{activeURLSampleRows: 1}
	checker := &sequenceURLChecker{
		responses: []TargetURLCheck{
			{Source: "runner", Error: "dns: temporary failure"},
			{Source: "runner", DNSOK: true, HTTPOK: true, HTTPStatus: 200},
		},
	}
	cfg := RunConfig{
		TargetPreflight: TargetPreflightConfig{
			CheckSources:   []string{"runner"},
			ExpectedStatus: 200,
		},
	}
	service := ServiceLifecycle{
		ID:     "jetmon-v1",
		DSN:    "v1-dsn",
		HasDSN: true,
		Config: Config{
			Schema:               SchemaV1,
			BlogIDStart:          8000000000000000,
			Count:                100,
			BucketMin:            0,
			BucketMax:            9,
			URLPattern:           "http://site-%07d.load.example.test/",
			URLNumberStart:       1,
			CheckIntervalMinutes: 1,
		},
	}

	preflight, err := (Runner{
		Executor:   exec,
		URLChecker: checker,
		Sleeper:    noSleep{},
	}).preflightServiceTargets(context.Background(), service, cfg, 1, time.Second)
	if err != nil {
		t.Fatalf("preflightServiceTargets: %v", err)
	}
	if checker.calls != 2 {
		t.Fatalf("checker calls = %d, want 2", checker.calls)
	}
	if got := preflight.Samples[0].Checks[0].Attempts; got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

func TestPreflightServiceTargetsFailsAfterURLCheckRetries(t *testing.T) {
	exec := &fakeSQLExecutor{activeURLSampleRows: 1}
	checker := &sequenceURLChecker{
		responses: []TargetURLCheck{
			{Source: "runner", Error: "dns: temporary failure"},
		},
	}
	cfg := RunConfig{
		TargetPreflight: TargetPreflightConfig{
			CheckSources:   []string{"runner"},
			ExpectedStatus: 200,
		},
	}
	service := ServiceLifecycle{
		ID:     "jetmon-v1",
		DSN:    "v1-dsn",
		HasDSN: true,
		Config: Config{
			Schema:               SchemaV1,
			BlogIDStart:          8000000000000000,
			Count:                100,
			BucketMin:            0,
			BucketMax:            9,
			URLPattern:           "http://site-%07d.load.example.test/",
			URLNumberStart:       1,
			CheckIntervalMinutes: 1,
		},
	}

	preflight, err := (Runner{
		Executor:   exec,
		URLChecker: checker,
		Sleeper:    noSleep{},
	}).preflightServiceTargets(context.Background(), service, cfg, 1, time.Second)
	if err == nil || !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("preflightServiceTargets error = %v, want retry exhaustion", err)
	}
	if checker.calls != 3 {
		t.Fatalf("checker calls = %d, want 3", checker.calls)
	}
	if got := preflight.Samples[0].Checks[0].Attempts; got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}

func TestDefaultTargetURLCheckerRejectsUnsupportedSourceWithoutNetwork(t *testing.T) {
	check := defaultTargetURLChecker{}.CheckURL(context.Background(), "jetmon-service-host-1", "http://example.test/", time.Second, 200)
	if check.Source != "jetmon-service-host-1" {
		t.Fatalf("Source = %q, want configured source", check.Source)
	}
	if check.DNSOK || check.HTTPOK || !strings.Contains(check.Error, "source-aware TargetURLChecker") {
		t.Fatalf("check = %#v, want local unsupported-source error", check)
	}
}

func TestResolvedHTTPClientUsesResolvedAddressAndPreservesHost(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	rawURL := "http://site-0000049.steadycadence.party:" + serverURL.Port() + "/"
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	resp, err := resolvedHTTPClient(parsed, []string{"127.0.0.1"}, time.Second).Get(rawURL)
	if err != nil {
		t.Fatalf("GET through resolved client: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	wantHost := "site-0000049.steadycadence.party:" + serverURL.Port()
	if gotHost != wantHost {
		t.Fatalf("Host header = %q, want %q", gotHost, wantHost)
	}
}

func TestCapacityReportDescription(t *testing.T) {
	got := capacityReportDescription("Jetmon V1/V2 Capacity", "run-batch", 1000, "")
	want := "Jetmon V1/V2 Capacity-run-batch-1000-sites"
	if got != want {
		t.Fatalf("capacityReportDescription = %q, want %q", got, want)
	}
	if got := capacityReportDescription("ignored", "run-suite", 0, "Capacity Scout"); got != "Capacity Scout" {
		t.Fatalf("override description = %q, want Capacity Scout", got)
	}
}

func TestPlannedReportDuration(t *testing.T) {
	duration := 10 * time.Minute
	cooldown := 2 * time.Minute
	if got := plannedReportDuration("run-batch", duration, cooldown, 3); got != duration {
		t.Fatalf("run-batch duration = %s, want %s", got, duration)
	}
	if got := plannedReportDuration("run-suite", duration, cooldown, 3); got != 34*time.Minute {
		t.Fatalf("run-suite duration = %s, want 34m", got)
	}
	if got := plannedReportDuration("plan", duration, cooldown, 3); got != 0 {
		t.Fatalf("plan duration = %s, want 0", got)
	}
}

func TestServiceHealthIncludesFreshnessDetails(t *testing.T) {
	service := ServiceLifecycle{ID: "jetmon-v2", Config: Config{Schema: SchemaV2, CheckIntervalMinutes: 1}}
	result := SQLExecutionResult{
		Statements: []SQLStatementResult{
			{Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", "10"}}},
			{Columns: []string{"check_interval", "active_sites"}, Rows: [][]string{{"1", "10"}}},
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

func TestServiceHealthTreatsStreamingDBFreshnessAsLegacyProjection(t *testing.T) {
	service := ServiceLifecycle{
		ID:              "jetmon-v2",
		Config:          Config{Schema: SchemaV2, CheckIntervalMinutes: 1},
		SchedulerEngine: "streaming",
	}
	result := SQLExecutionResult{
		Statements: []SQLStatementResult{
			{Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", "10"}}},
			{Columns: []string{"check_interval", "active_sites"}, Rows: [][]string{{"1", "10"}}},
			{Columns: []string{"open_events"}, Rows: [][]string{{"3"}}},
			{Columns: []string{"stale_active_sites"}, Rows: [][]string{{"2"}}},
			{Columns: []string{"recent_check_history_rows"}, Rows: [][]string{{"25"}}},
		},
	}

	health := serviceHealthFromVerify(service, "window-end-verify", result, 10)
	if health.FreshnessMeasured {
		t.Fatal("FreshnessMeasured = true, want false for streaming scheduler")
	}
	if health.FreshnessSource != "target_observer_replay_detection_streaming_telemetry" {
		t.Fatalf("FreshnessSource = %q, want streaming telemetry source", health.FreshnessSource)
	}
	if health.MissedCheckPercent != nil || health.StaleActiveSites != nil {
		t.Fatalf("scored DB freshness = stale %v missed %v, want nil", health.StaleActiveSites, health.MissedCheckPercent)
	}
	if health.LegacyProjectionStaleActiveSites == nil || *health.LegacyProjectionStaleActiveSites != 2 {
		t.Fatalf("LegacyProjectionStaleActiveSites = %v, want 2", health.LegacyProjectionStaleActiveSites)
	}
	if health.LegacyProjectionMissedCheckPercent == nil || *health.LegacyProjectionMissedCheckPercent != 20 {
		t.Fatalf("LegacyProjectionMissedCheckPercent = %v, want 20", health.LegacyProjectionMissedCheckPercent)
	}
	if health.OpenEvents == nil || *health.OpenEvents != 3 {
		t.Fatalf("OpenEvents = %v, want 3", health.OpenEvents)
	}
	if health.RecentCheckHistoryRows != nil {
		t.Fatalf("RecentCheckHistoryRows = %v, want nil legacy streaming projection", health.RecentCheckHistoryRows)
	}
	if !strings.Contains(health.Reason, "legacy last_checked_at projection") {
		t.Fatalf("Reason = %q, want legacy projection explanation", health.Reason)
	}
}

func TestServiceHealthFailsCheckIntervalMismatch(t *testing.T) {
	service := ServiceLifecycle{ID: "jetmon-v2", Config: Config{Schema: SchemaV2, CheckIntervalMinutes: 5}}
	result := SQLExecutionResult{Statements: []SQLStatementResult{
		{Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", "10"}}},
		{Columns: []string{"check_interval", "active_sites"}, Rows: [][]string{{"1", "3"}, {"5", "7"}}},
	}}

	health := serviceHealthFromVerify(service, "active-check-interval-verify", result, 10)
	if health.Status != "fail" {
		t.Fatalf("Status = %q, want fail", health.Status)
	}
	if health.CheckIntervalMismatchSites == nil || *health.CheckIntervalMismatchSites != 3 {
		t.Fatalf("CheckIntervalMismatchSites = %v, want 3", health.CheckIntervalMismatchSites)
	}
	if !strings.Contains(health.Reason, "different from 5m") {
		t.Fatalf("Reason = %q, want interval mismatch", health.Reason)
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

func TestRunSuiteDefaultsToLastCleanBatchWhenStateHasProblem(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")
	if err := writeSuiteState(statePath, SuiteState{
		ID:                 "capacity-test",
		LastCompletedBatch: 20,
		LastCleanBatch:     10,
		FirstProblemBatch:  20,
		LastCompletedAt:    time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		LastRunDir:         filepath.Join(dir, "previous"),
		LastBatchDir:       filepath.Join(dir, "previous", "batch-0000020"),
		StopRecommended:    true,
		StopReason:         "missed_check_percent exceeded threshold for jetmon-v2",
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
	if manifest.SuiteStartCount != 10 {
		t.Fatalf("SuiteStartCount = %d, want 10", manifest.SuiteStartCount)
	}
	if got := joinInts(manifest.BatchSizes); got != "10,20" {
		t.Fatalf("BatchSizes = %s, want 10,20", got)
	}
	if !containsString(manifest.Notes, "resuming run-suite from last clean batch 10 recorded in "+statePath) {
		t.Fatalf("manifest notes missing clean resume note: %#v", manifest.Notes)
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
	if state.LastCleanBatch != 10 {
		t.Fatalf("LastCleanBatch = %d, want 10", state.LastCleanBatch)
	}
	if state.FirstProblemBatch != 0 {
		t.Fatalf("FirstProblemBatch = %d, want 0", state.FirstProblemBatch)
	}
	if got := joinInts(state.CompletedBatchSequence); got != "10" {
		t.Fatalf("CompletedBatchSequence = %s, want 10", got)
	}
}

func TestRunSuiteApplyStateRecordsProblemBatch(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")

	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{
			staleByDSN:   map[string]int64{"v2-dsn": 2},
			historyByDSN: map[string]int64{"v2-dsn": 8},
		},
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
	if !manifest.StopRecommended {
		t.Fatalf("StopRecommended = false, want true")
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
	if state.LastCleanBatch != 0 {
		t.Fatalf("LastCleanBatch = %d, want 0", state.LastCleanBatch)
	}
	if state.FirstProblemBatch != 10 {
		t.Fatalf("FirstProblemBatch = %d, want 10", state.FirstProblemBatch)
	}
	if !state.StopRecommended || !strings.Contains(state.StopReason, "missed_check_percent") {
		t.Fatalf("state stop = %t %q, want missed-check stop", state.StopRecommended, state.StopReason)
	}
}

func TestRunSuiteApplyPreservesPriorCleanWhenHigherFirstBatchFails(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "suite-state.json")
	if err := writeSuiteState(statePath, SuiteState{
		ID:                 "capacity-test",
		LastCompletedBatch: 10,
		LastCleanBatch:     10,
		LastCompletedAt:    time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC),
		LastRunDir:         filepath.Join(dir, "previous"),
		LastBatchDir:       filepath.Join(dir, "previous", "batch-0000010"),
	}); err != nil {
		t.Fatalf("write suite state: %v", err)
	}

	manifest, err := (Runner{
		Executor: &fakeSQLExecutor{
			activeByDSN:         map[string]int64{"v1-dsn": 20, "v2-dsn": 20},
			staleByDSN:          map[string]int64{"v2-dsn": 2},
			historyByDSN:        map[string]int64{"v2-dsn": 8},
			activeURLSampleRows: 20,
		},
		Clock:     fixedClock{},
		Sleeper:   noSleep{},
		Collector: fakeCollector{report: scrapeUpReport("jetmon-v1", "jetmon-v2")},
	}).Run(context.Background(), RunOptions{
		ConfigPath:     cfgPath,
		Mode:           "run-suite",
		OutDir:         filepath.Join(dir, "out"),
		SuiteStatePath: statePath,
		BatchSizes:     []int{20},
		Apply:          true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !manifest.StopRecommended {
		t.Fatalf("StopRecommended = false, want true")
	}
	state, err := readSuiteState(statePath)
	if err != nil {
		t.Fatalf("read suite state: %v", err)
	}
	if state == nil {
		t.Fatal("suite state was not written")
	}
	if state.LastCompletedBatch != 20 {
		t.Fatalf("LastCompletedBatch = %d, want 20", state.LastCompletedBatch)
	}
	if state.LastCleanBatch != 10 {
		t.Fatalf("LastCleanBatch = %d, want preserved prior clean 10", state.LastCleanBatch)
	}
	if state.FirstProblemBatch != 20 {
		t.Fatalf("FirstProblemBatch = %d, want 20", state.FirstProblemBatch)
	}
}

func TestRunSuiteWritesCapacityRollup(t *testing.T) {
	cfgPath := writeRunnerConfig(t)
	outDir := filepath.Join(t.TempDir(), "out")
	manifest, err := (Runner{
		Executor:  &fakeSQLExecutor{},
		Clock:     fixedClock{},
		Sleeper:   noSleep{},
		Collector: fakeCollector{report: capacitySuiteReport("jetmon-v1", "jetmon-v2")},
	}).Run(context.Background(), RunOptions{
		ConfigPath: cfgPath,
		Mode:       "run-suite",
		OutDir:     outDir,
		BatchSizes: []int{10},
		Apply:      true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"capacity.md", "capacity.json"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(outDir, "capacity.md"))
	if err != nil {
		t.Fatalf("read capacity.md: %v", err)
	}
	md := string(data)
	for _, want := range []string{"# Jetmon Capacity Suite Report", "## Batch Results", "## Service Health", "## Thresholds", "## Prometheus Highlights", "| 10 | pass |"} {
		if !strings.Contains(md, want) {
			t.Fatalf("capacity.md missing %q:\n%s", want, md)
		}
	}
	data, err = os.ReadFile(filepath.Join(outDir, "capacity.json"))
	if err != nil {
		t.Fatalf("read capacity.json: %v", err)
	}
	var report SuiteReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("unmarshal capacity.json: %v", err)
	}
	if report.CompletedBatches != 1 || report.LastCleanBatch != 10 {
		t.Fatalf("suite report batches = completed %d clean %d, want 1/10", report.CompletedBatches, report.LastCleanBatch)
	}
	if len(report.Batches) != 1 || len(report.Batches[0].PrometheusSummary) == 0 {
		t.Fatalf("suite report missing batch Prometheus summary: %+v", report.Batches)
	}
	if !hasArtifact(manifest.Artifacts, "capacity-report") || !hasArtifact(manifest.Artifacts, "capacity-json") {
		t.Fatalf("manifest missing capacity report artifacts: %#v", manifest.Artifacts)
	}
}

type runnerConfigOption func(*runnerConfigSpec)

type runnerConfigSpec struct {
	prometheusURL string
	instances     []string
	observerURL   string
	diskIO        bool
}

func withPrometheus(prometheusURL string, instances []string) runnerConfigOption {
	return func(spec *runnerConfigSpec) {
		spec.prometheusURL = prometheusURL
		spec.instances = append([]string(nil), instances...)
	}
}

func withTargetObserver(url string) runnerConfigOption {
	return func(spec *runnerConfigSpec) {
		spec.observerURL = url
	}
}

func withDiskIOAttribution() runnerConfigOption {
	return func(spec *runnerConfigSpec) {
		spec.diskIO = true
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
	var observerConfig string
	if spec.observerURL != "" {
		t.Setenv("CONTROL_TOKEN", "target-token")
		observerConfig = `
[target_observer]
enabled = true
target_control_url = ` + strconv.Quote(spec.observerURL) + `
timeout = "1s"
`
	}
	var diskIOConfig string
	if spec.diskIO {
		diskIOConfig = `
[disk_io_attribution]
enabled = true
timeout = "1s"
sample_interval = "1s"

  [[disk_io_attribution.hosts]]
  id = "jetmon-v2"
  instance = "jetmon-v2"
  ssh_host = "jetmon-v2"
`
	}
	path := filepath.Join(t.TempDir(), "capacity.toml")
	content := `
id = "capacity-test"
` + promConfig + `

[targets]
host_pattern = "site-%07d.load.example.test"
url_pattern = "http://site-%07d.load.example.test/"
count = 100

[target_preflight]
skip_http = true
` + observerConfig + `
` + diskIOConfig + `

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
	calls               []fakeSQLCall
	activeByDSN         map[string]int64
	staleByDSN          map[string]int64
	historyByDSN        map[string]int64
	checkIntervalByDSN  map[string]int
	failActivateDSN     string
	badSampleURLDSN     string
	activeURLSampleRows int
	seedTotal           int64
	seedMatching        int64
}

type fakeSQLCall struct {
	dsn string
	sql string
}

type recordingURLChecker struct {
	calls []recordingURLCheckCall
}

type recordingURLCheckCall struct {
	source string
	url    string
}

func (c *recordingURLChecker) CheckURL(ctx context.Context, source string, rawURL string, timeout time.Duration, expectedStatus int) TargetURLCheck {
	c.calls = append(c.calls, recordingURLCheckCall{source: source, url: rawURL})
	return TargetURLCheck{
		Source:     source,
		DNSOK:      true,
		HTTPOK:     true,
		HTTPStatus: expectedStatus,
	}
}

type sequenceURLChecker struct {
	responses []TargetURLCheck
	calls     int
}

func (c *sequenceURLChecker) CheckURL(context.Context, string, string, time.Duration, int) TargetURLCheck {
	c.calls++
	if len(c.responses) == 0 {
		return TargetURLCheck{Source: "runner", DNSOK: true, HTTPOK: true, HTTPStatus: 200}
	}
	index := c.calls - 1
	if index >= len(c.responses) {
		index = len(c.responses) - 1
	}
	return c.responses[index]
}

type fakeTargetObserverClient struct {
	resetCalls   int
	summaryCalls int
	lastReset    targetserver.CapacityObserveResetRequest
	summary      targetserver.CapacityObserveSummary
}

func (c *fakeTargetObserverClient) Reset(ctx context.Context, baseURL, token string, req targetserver.CapacityObserveResetRequest, timeout time.Duration) (targetserver.CapacityObserveSummary, error) {
	c.resetCalls++
	c.lastReset = req
	return targetserver.CapacityObserveSummary{
		Active:      true,
		RunID:       req.RunID,
		StartedAt:   time.Unix(1_700_000_000, 0).UTC(),
		SnapshotAt:  time.Unix(1_700_000_000, 0).UTC(),
		ActiveCount: req.ActiveCount,
		Services: []targetserver.CapacityObserveServiceSummary{{
			ID:            req.Services[0].ID,
			ExpectedSites: req.ActiveCount,
		}},
	}, nil
}

func (c *fakeTargetObserverClient) Summary(ctx context.Context, baseURL, token string, timeout time.Duration) (targetserver.CapacityObserveSummary, error) {
	c.summaryCalls++
	if len(c.summary.Services) > 0 {
		return c.summary, nil
	}
	return targetserver.CapacityObserveSummary{
		Active:         true,
		RunID:          "batch-10",
		SnapshotAt:     time.Unix(1_700_000_060, 0).UTC(),
		ElapsedSeconds: 60,
		ActiveCount:    10,
		Services: []targetserver.CapacityObserveServiceSummary{{
			ID:                  "jetmon-v1",
			ExpectedSites:       10,
			ObservedSites:       10,
			CoveragePercent:     100,
			TotalRequests:       10,
			RequestsPerSecond:   0.16,
			RequestsPerSiteMean: 1,
		}},
	}, nil
}

func (e *fakeSQLExecutor) ExecuteSQL(ctx context.Context, dsn string, sqlText string) (SQLExecutionResult, error) {
	e.calls = append(e.calls, fakeSQLCall{dsn: dsn, sql: sqlText})
	switch {
	case strings.Contains(sqlText, "monitor_url") && strings.Contains(sqlText, "monitor_active = 1"):
		return e.activeURLSamples(dsn), nil
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
	case strings.Contains(sqlText, "check_interval") && strings.Contains(sqlText, "GROUP BY check_interval") && !strings.Contains(sqlText, "benchmark_sites"):
		interval := e.checkIntervalByDSN[dsn]
		if interval == 0 {
			interval = 1
		}
		return singleRowResult([]string{"check_interval", "active_sites"}, []string{intString(int64(interval)), intString(e.activeByDSN[dsn])}), nil
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
			StatementCount: 8,
			Statements: []SQLStatementResult{
				{Index: 1, Keyword: "SELECT", Columns: []string{"benchmark_sites", "active_sites"}, Rows: [][]string{{"100", intString(active)}}},
				{Index: 2, Keyword: "SELECT", Columns: []string{"bucket_no", "benchmark_sites", "active_sites"}, Rows: [][]string{{"10", "100", intString(active)}}},
				{Index: 3, Keyword: "SELECT", Columns: []string{"check_interval", "active_sites"}, Rows: [][]string{{"1", intString(active)}}},
				{Index: 4, Keyword: "SELECT", Columns: []string{"stale_active_sites"}, Rows: [][]string{{intString(stale)}}},
				{Index: 5, Keyword: "SELECT", Columns: []string{"open_events"}, Rows: [][]string{{"0"}}},
				{Index: 6, Keyword: "SELECT", Columns: []string{"recent_check_history_rows"}, Rows: [][]string{{intString(history)}}},
				{Index: 7, Keyword: "SELECT", Columns: []string{"freshness_samples", "freshest_check_age_sec", "average_check_age_sec", "p50_check_age_sec", "p95_check_age_sec", "p99_check_age_sec", "oldest_check_age_sec"}, Rows: [][]string{{intString(active - stale), "1", "10.5", "10", "20", "30", "40"}}},
				{Index: 8, Keyword: "SELECT", Columns: []string{"bucket_no", "active_sites", "stale_active_sites"}, Rows: [][]string{{"10", intString(active), intString(stale)}}},
			},
		}, nil
	default:
		return SQLExecutionResult{StatementCount: 1}, nil
	}
}

func (e *fakeSQLExecutor) activeURLSamples(dsn string) SQLExecutionResult {
	start := int64(8000000000000000)
	bucketMin := 0
	if dsn == "v2-dsn" {
		start = 8000001000000000
		bucketMin = 10
	}
	samples := e.activeURLSampleRows
	if samples <= 0 {
		samples = 10
	}
	var rows [][]string
	for i := 0; i < samples; i++ {
		blogID := start + int64(i)
		rows = append(rows, []string{
			intString(blogID),
			strconv.Itoa(bucketMin + i),
			e.sampleURL(dsn, i+1),
		})
	}
	return SQLExecutionResult{
		StatementCount: 1,
		Statements: []SQLStatementResult{{
			Index:   1,
			Keyword: "SELECT",
			Columns: []string{"blog_id", "bucket_no", "monitor_url"},
			Rows:    rows,
		}},
	}
}

func (e *fakeSQLExecutor) sampleURL(dsn string, number int) string {
	host := "load.example.test"
	if dsn == e.badSampleURLDSN {
		host = "wrong.example.test"
	}
	return "http://site-" + fmt.Sprintf("%07d", number) + "." + host + "/"
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

type fakeDiskIOAttributionCollector struct {
	run         DiskIOAttributionRun
	startCalls  int
	finishCalls int
}

func (c *fakeDiskIOAttributionCollector) Start(context.Context, RunConfig, []ServiceLifecycle, time.Time, time.Duration) (DiskIOAttributionHandle, error) {
	c.startCalls++
	return &fakeDiskIOAttributionHandle{collector: c}, nil
}

type fakeDiskIOAttributionHandle struct {
	collector *fakeDiskIOAttributionCollector
}

func (h *fakeDiskIOAttributionHandle) Finish(_ context.Context, end time.Time) (DiskIOAttributionRun, error) {
	h.collector.finishCalls++
	run := h.collector.run
	run.End = end
	return run, nil
}

func (h *fakeDiskIOAttributionHandle) Cancel() {}

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

func capacitySuiteReport(instances ...string) capacitybench.Report {
	report := scrapeUpReport(instances...)
	report.PrometheusURL = "http://prometheus:9090"
	report.Start = time.Unix(1_700_000_000, 0).UTC()
	report.End = report.Start.Add(time.Minute)
	report.Step = "15s"
	for _, instance := range instances {
		report.Summaries = append(report.Summaries, capacitybench.SeriesSummary{
			Query:   "host_cpu_used",
			Unit:    "percent",
			Labels:  map[string]string{"instance": instance},
			Samples: 4,
			Min:     10,
			Avg:     20,
			P50:     20,
			P95:     30,
			Max:     35,
			Last:    25,
		})
	}
	return report
}

func hasArtifact(artifacts []Artifact, action string) bool {
	for _, artifact := range artifacts {
		if artifact.Action == action {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
