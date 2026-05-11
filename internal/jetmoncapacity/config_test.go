package jetmoncapacity

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadRunConfigNormalizesServiceLifecycles(t *testing.T) {
	t.Setenv("JETMON_V1_DB_DSN", "v1-dsn")
	dir := t.TempDir()
	path := filepath.Join(dir, "capacity.toml")
	content := `
id = "capacity-test"
prometheus_url = "http://prometheus:9090"
instances = ["jetmon-v1.example.com", "jetmon-v2.example.com"]

[targets]
url_pattern = "http://site-%07d.load.example.test/"
count = 100

[checks]
interval = "1m"

[batches]
sizes = [10, 50]
duration = "5m"
cooldown = "1m"

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
	t.Setenv("JETMON_V2_DB_DSN", "v2-dsn")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadRunConfig(path)
	if err != nil {
		t.Fatalf("LoadRunConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	services, err := cfg.ServiceLifecycles([]string{"v1", "jetmon-v2"})
	if err != nil {
		t.Fatalf("ServiceLifecycles: %v", err)
	}
	if len(services) != 2 {
		t.Fatalf("services = %d, want 2", len(services))
	}
	if services[0].ID != "jetmon-v1" || services[0].DSN != "v1-dsn" {
		t.Fatalf("v1 service = %#v", services[0])
	}
	if services[1].ID != "jetmon-v2" || services[1].DSN != "v2-dsn" {
		t.Fatalf("v2 service = %#v", services[1])
	}
	if services[0].Config.CheckIntervalMinutes != 1 {
		t.Fatalf("check interval = %d, want 1", services[0].Config.CheckIntervalMinutes)
	}
}

func TestRunConfigRejectsOversizedBatch(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		Batches: BatchesConfig{Sizes: []int{11}, Duration: "5m", Cooldown: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate succeeded, want error")
	}
}

func TestRunConfigRejectsUnsortedBatches(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 100},
		Checks:  ChecksConfig{Interval: "1m"},
		Batches: BatchesConfig{Sizes: []int{10, 50, 20}, Duration: "5m", Cooldown: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       100,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       100,
		}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate succeeded, want error")
	}
}

func TestRunConfigRejectsTargetPatternMismatch(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{
			Domain:     "steadycadence.example",
			URLPattern: "http://site-%07d.load.steadycadence.example/",
			Count:      100,
		},
		Checks:  ChecksConfig{Interval: "1m"},
		Batches: BatchesConfig{Sizes: []int{10}, Duration: "5m", Cooldown: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       100,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       100,
		}},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "does not match targets.host_pattern") {
		t.Fatalf("Validate error = %v, want target pattern mismatch", err)
	}
}

func TestRunConfigDefaultsURLPatternFromHostPattern(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{
			HostPattern: "site-%07d.capacity.example",
			Count:       100,
		},
	}
	got := cfg.Normalize()
	if got.Targets.URLPattern != "http://site-%07d.capacity.example/" {
		t.Fatalf("URLPattern = %q, want derived URL pattern", got.Targets.URLPattern)
	}
}

func TestRunConfigDefaultsAndDedupesTargetPreflightSources(t *testing.T) {
	defaulted := RunConfig{}.Normalize()
	if !reflect.DeepEqual(defaulted.TargetPreflight.CheckSources, []string{"runner"}) {
		t.Fatalf("default check sources = %#v, want runner", defaulted.TargetPreflight.CheckSources)
	}

	cfg := RunConfig{
		TargetPreflight: TargetPreflightConfig{
			CheckSources: []string{" runner ", "", "jetmon-service-host-1", "runner"},
		},
	}
	got := cfg.Normalize()
	want := []string{"runner", "jetmon-service-host-1"}
	if !reflect.DeepEqual(got.TargetPreflight.CheckSources, want) {
		t.Fatalf("check sources = %#v, want %#v", got.TargetPreflight.CheckSources, want)
	}
}

func TestRunConfigNormalizesReplayDetectionDefaults(t *testing.T) {
	cfg := validCapacityConfigForTest()
	cfg.CapacityReplay = CapacityReplayConfig{
		Enabled:          true,
		TargetControlURL: "http://target.example.test:9000",
		Events: []CapacityReplayEvent{{
			ID:       "http-503-sample",
			Offset:   "3m",
			Duration: "7m",
		}},
	}
	cfg.ReplayDetection = ReplayDetectionConfig{Enabled: true}
	cfg.JetmonV1.BridgeURL = "http://jetmon-v1.example.test:7400"

	got := cfg.Normalize()
	if got.ReplayDetection.V1BridgeURL != cfg.JetmonV1.BridgeURL {
		t.Fatalf("v1 bridge url = %q, want %q", got.ReplayDetection.V1BridgeURL, cfg.JetmonV1.BridgeURL)
	}
	if got.ReplayDetection.Timeout != "15s" {
		t.Fatalf("timeout = %q, want 15s", got.ReplayDetection.Timeout)
	}
	if got.ReplayDetection.WindowPadding != "30s" {
		t.Fatalf("window padding = %q, want 30s", got.ReplayDetection.WindowPadding)
	}
	if timeout, err := got.ReplayDetectionTimeout(); err != nil || timeout != 15*time.Second {
		t.Fatalf("ReplayDetectionTimeout = %v, %v; want 15s", timeout, err)
	}
	if padding, err := got.ReplayDetectionWindowPadding(); err != nil || padding != 30*time.Second {
		t.Fatalf("ReplayDetectionWindowPadding = %v, %v; want 30s", padding, err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestRunConfigNormalizesStreamingTelemetryDefaults(t *testing.T) {
	cfg := validCapacityConfigForTest()
	cfg.NetworkBuckets = NetworkBucketsConfig{
		SSHConfig: "/tmp/ssh-config",
		Timeout:   "3s",
	}
	cfg.StreamingTelemetry = StreamingTelemetryConfig{
		Enabled: true,
		Hosts: []StreamingTelemetryHostConfig{{
			Service:      " jetmon-v2 ",
			SSHHost:      " jetmon-v2.example.test ",
			DashboardURL: "http://127.0.0.1:8080/",
		}},
	}
	cfg.JetmonV2.SchedulerEngine = " Streaming "

	got := cfg.Normalize()
	if got.StreamingTelemetry.SSHConfig != "/tmp/ssh-config" {
		t.Fatalf("SSHConfig = %q, want network bucket default", got.StreamingTelemetry.SSHConfig)
	}
	if got.StreamingTelemetry.Timeout != "3s" {
		t.Fatalf("Timeout = %q, want network bucket default", got.StreamingTelemetry.Timeout)
	}
	host := got.StreamingTelemetry.Hosts[0]
	if host.Service != "jetmon-v2" || host.SSHHost != "jetmon-v2.example.test" || host.Unit != "jetmon2" || host.DashboardURL != "http://127.0.0.1:8080" {
		t.Fatalf("host = %#v, want trimmed defaults", host)
	}
	if got.JetmonV2.SchedulerEngine != "streaming" {
		t.Fatalf("SchedulerEngine = %q, want streaming", got.JetmonV2.SchedulerEngine)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if timeout, err := got.StreamingTelemetryTimeout(); err != nil || timeout != 3*time.Second {
		t.Fatalf("StreamingTelemetryTimeout = %v, %v; want 3s", timeout, err)
	}
}

func TestRunConfigRejectsInvalidSchedulerEngine(t *testing.T) {
	cfg := validCapacityConfigForTest()
	cfg.JetmonV2.SchedulerEngine = "eventual"

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "jetmon_v2.scheduler_engine") {
		t.Fatalf("Validate error = %v, want scheduler engine validation error", err)
	}
}

func TestRunConfigRejectsReplayDetectionWithoutReplay(t *testing.T) {
	cfg := validCapacityConfigForTest()
	cfg.ReplayDetection = ReplayDetectionConfig{Enabled: true}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "replay_detection.enabled requires capacity_replay.enabled=true") {
		t.Fatalf("Validate error = %v, want replay detection dependency error", err)
	}
}

func TestRunConfigRejectsInvalidExpectedRequestRatioRange(t *testing.T) {
	cfg := validCapacityConfigForTest()
	cfg.TargetObserver = TargetObserverConfig{
		Enabled:                 true,
		TargetControlURL:        "http://target.example.test:9000",
		MinExpectedRequestRatio: 1.2,
		MaxExpectedRequestRatio: 1.0,
	}

	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "max_expected_request_ratio") {
		t.Fatalf("Validate error = %v, want max ratio validation error", err)
	}
}

func TestServiceLifecyclesRejectsUnknownService(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	if _, err := cfg.ServiceLifecycles([]string{"jetmon-v1", "typo"}); err == nil {
		t.Fatal("ServiceLifecycles succeeded, want error")
	}
}

func TestServiceLifecyclesRejectsInlineDSN(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
			DSN:         "user:pass@tcp(localhost:3306)/jetmon_db",
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	if _, err := cfg.ServiceLifecycles([]string{"jetmon-v1"}); err == nil {
		t.Fatal("ServiceLifecycles succeeded, want inline dsn error")
	}
}

func TestServiceLifecyclesReadsDSNFile(t *testing.T) {
	dir := t.TempDir()
	dsnPath := filepath.Join(dir, "dsn")
	if err := os.WriteFile(dsnPath, []byte("file-dsn\n"), 0o600); err != nil {
		t.Fatalf("write dsn file: %v", err)
	}
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
			DSNFile:     dsnPath,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	services, err := cfg.ServiceLifecycles([]string{"jetmon-v1"})
	if err != nil {
		t.Fatalf("ServiceLifecycles: %v", err)
	}
	if got := services[0].DSN; got != "file-dsn" {
		t.Fatalf("DSN = %q, want file-dsn", got)
	}
}

func TestServiceLifecyclesAllowsMissingDSNFileForPlanning(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
			DSNFile:     filepath.Join(t.TempDir(), "missing-dsn"),
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	services, err := cfg.ServiceLifecycles([]string{"jetmon-v1"})
	if err != nil {
		t.Fatalf("ServiceLifecycles: %v", err)
	}
	if services[0].HasDSN {
		t.Fatalf("HasDSN = true, want false for missing dsn_file")
	}
	if services[0].DSNFile == "" {
		t.Fatalf("DSNFile was not preserved: %#v", services[0])
	}
}

func TestServiceLifecyclesRejectsLooseDSNFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dsnPath := filepath.Join(dir, "dsn")
	if err := os.WriteFile(dsnPath, []byte("file-dsn\n"), 0o644); err != nil {
		t.Fatalf("write dsn file: %v", err)
	}
	cfg := RunConfig{
		Targets: TargetConfig{URLPattern: "http://site-%d.example.test/", Count: 10},
		Checks:  ChecksConfig{Interval: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       10,
			DSNFile:     dsnPath,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       10,
		}},
	}
	if _, err := cfg.ServiceLifecycles([]string{"jetmon-v1"}); err == nil {
		t.Fatal("ServiceLifecycles succeeded, want dsn_file permission error")
	}
}

func validCapacityConfigForTest() RunConfig {
	return RunConfig{
		ID: "capacity-test",
		Targets: TargetConfig{
			URLPattern: "http://site-%07d.example.test/",
			Count:      100,
		},
		Checks:  ChecksConfig{Interval: "1m"},
		Batches: BatchesConfig{Sizes: []int{10}, Duration: "5m", Cooldown: "1m"},
		JetmonV1: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV1,
			BlogIDStart: 100,
			Count:       100,
		}},
		JetmonV2: ServiceConfig{Lifecycle: LifecycleConfig{
			Schema:      SchemaV2,
			BlogIDStart: 200,
			Count:       100,
		}},
	}
}
