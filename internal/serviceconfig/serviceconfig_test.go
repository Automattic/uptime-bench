package serviceconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleParses asserts services.example.toml parses cleanly so the
// reference operators copy from stays valid.
func TestExampleParses(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "services.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if len(cfg.Services) == 0 {
		t.Fatalf("example has no [[services]] entries")
	}
	// Every example service should at minimum have id and type.
	for i, s := range cfg.Services {
		if s.ID == "" {
			t.Errorf("services[%d]: empty ID", i)
		}
		if s.Type == "" {
			t.Errorf("services[%d] (%s): empty Type", i, s.ID)
		}
	}
}

func TestParse_RequiresID(t *testing.T) {
	_, err := Parse([]byte(`
[[services]]
type = "jetmon-v1"
`))
	if err == nil || !strings.Contains(err.Error(), "id is required") {
		t.Fatalf("got err=%v, want one mentioning id", err)
	}
}

func TestParse_RequiresType(t *testing.T) {
	_, err := Parse([]byte(`
[[services]]
id = "x"
`))
	if err == nil || !strings.Contains(err.Error(), "type is required") {
		t.Fatalf("got err=%v, want one mentioning type", err)
	}
}

func TestParse_RoundTripsAuthAndProbeRanges(t *testing.T) {
	in := `
[[services]]
id      = "p"
type    = "pingdom"
enabled = true
auth    = { token = "abc" }

[services.probe_ranges]
us-east = ["1.2.3.0/24", "4.5.6.0/24"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	s := cfg.Services[0]
	if s.Auth["token"] != "abc" {
		t.Fatalf("auth.token = %q, want abc", s.Auth["token"])
	}
	got := s.ProbeRanges["us-east"]
	if len(got) != 2 || got[0] != "1.2.3.0/24" || got[1] != "4.5.6.0/24" {
		t.Fatalf("probe_ranges.us-east = %v", got)
	}
}
