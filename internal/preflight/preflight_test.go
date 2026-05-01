package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckScenarioFlagsMissingTargetAndRegionGaps(t *testing.T) {
	dir := t.TempDir()
	fleetPath := writeFile(t, dir, "fleet.toml", `
[[targets]]
id = "bench-a"
address = "127.0.0.1"
control_port = 8082
`)
	servicesPath := writeFile(t, dir, "services.toml", `
[[services]]
id = "svc-a"
type = "jetmon-v1"
url = "http://127.0.0.1:8090"
enabled = true
auth = { token = "tok", write_mode = "true" }
`)
	scenarioPath := writeFile(t, dir, "scenario.toml", `
id = "preflight-scenario"
version = "1"
target = "missing"
monitors = ["svc-a"]
check_frequency = "60s"
grace_period = "180s"
duration = "2m"

[[failures]]
type = "http_status"
status_code = 503
regions = ["us-east"]
`)

	got := Check(fleetPath, servicesPath, []string{scenarioPath}, nil)
	if !got.HasErrors() {
		t.Fatalf("expected missing target to be an error: %+v", got.Diagnostics)
	}
	if !hasDiagnostic(got, "error", "target \"missing\" not found") {
		t.Fatalf("missing target diagnostic not found: %+v", got.Diagnostics)
	}
	if !hasDiagnostic(got, "warn", "region \"us-east\"") {
		t.Fatalf("region probe range warning not found: %+v", got.Diagnostics)
	}
	if len(got.Scenarios) != 1 || got.Scenarios[0].Runtime != "5m0s" {
		t.Fatalf("scenario result = %+v, want runtime 5m0s", got.Scenarios)
	}
}

func TestCheckCampaignReportsTimingAndUnsupportedHostPatterns(t *testing.T) {
	dir := t.TempDir()
	fleetPath := writeFile(t, dir, "fleet.toml", `
[[targets]]
id = "bench-a"
address = "127.0.0.1"
control_port = 8082

[[targets]]
id = "bench-b"
address = "127.0.0.2"
control_port = 8082
`)
	servicesPath := writeFile(t, dir, "services.toml", `
[[services]]
id = "svc-a"
type = "jetmon-v1"
url = "http://127.0.0.1:8090"
enabled = true
auth = { token = "tok", write_mode = "true" }
`)
	campaignPath := writeFile(t, dir, "campaign.toml", `
id = "preflight-campaign"
duration = "30m"
seed = 1
check_frequency = "60s"
grace_period = "60s"

[targets]
pool = ["bench-a", "bench-b"]
patterns = ["single", "two_random"]

[duration_buckets]
brief = { min = "2m", max = "2m" }

[sampling]
samples_per_cell_default = 1

[[failure_types]]
type = "http_status"
status_code_choices = [503]
`)

	got := Check(fleetPath, servicesPath, nil, []string{campaignPath})
	if !hasDiagnostic(got, "error", "host pattern \"two_random\"") {
		t.Fatalf("unsupported host pattern diagnostic not found: %+v", got.Diagnostics)
	}
	if len(got.Campaigns) != 1 {
		t.Fatalf("campaign results = %+v, want one", got.Campaigns)
	}
	if got.Campaigns[0].Replays != 2 || got.Campaigns[0].Designs != 2 {
		t.Fatalf("campaign timing = %+v, want 2 designs/replays", got.Campaigns[0])
	}
}

func hasDiagnostic(r Report, level, substr string) bool {
	for _, d := range r.Diagnostics {
		if d.Level == level && strings.Contains(d.Message, substr) {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
