package jetmoncapacity

import (
	"os"
	"path/filepath"
	"testing"
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
