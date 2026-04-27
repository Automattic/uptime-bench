package fleet

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleParses asserts the canonical fleet.example.toml in the repo
// parses without error. It's the reference operators copy from, so keeping
// it valid is non-negotiable.
func TestExampleParses(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "fleet.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(%s): %v", path, err)
	}
	if cfg.Control.AuthTokenFile == "" {
		t.Fatal("control.auth_token_file should be set in the example")
	}
	if len(cfg.Nameservers) == 0 || len(cfg.Targets) == 0 || len(cfg.Domains) == 0 {
		t.Fatalf("example missing required sections: ns=%d targets=%d domains=%d",
			len(cfg.Nameservers), len(cfg.Targets), len(cfg.Domains))
	}
}

func TestParse_RejectsNameserverWithoutControlPort(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[nameservers]]
id = "ns-01"
address = "1.2.3.4"
dns_port = 53
`
	_, err := Parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "control_port is required") {
		t.Fatalf("got err=%v, want one mentioning control_port", err)
	}
}

func TestParse_RejectsTargetWithoutControlPort(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[targets]]
id = "t1"
address = "1.2.3.4"
`
	_, err := Parse([]byte(in))
	if err == nil || !strings.Contains(err.Error(), "control_port is required") {
		t.Fatalf("got err=%v, want one mentioning control_port", err)
	}
}

func TestParse_NameserverDefaultDNSPort(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[nameservers]]
id = "ns-01"
address = "1.2.3.4"
control_port = 9100
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Nameservers[0].DNSPort != 53 {
		t.Fatalf("default DNSPort: got %d, want 53", cfg.Nameservers[0].DNSPort)
	}
}

func TestParse_NameserverHostsRoundTrip(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[nameservers]]
id = "ns-01"
address = "1.2.3.4"
control_port = 9100
domains = ["example.com"]
hosts = ["ns1.example.com", "ns1.other.example"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Nameservers[0].Hosts
	if len(got) != 2 || got[0] != "ns1.example.com" || got[1] != "ns1.other.example" {
		t.Fatalf("Hosts = %v, want [ns1.example.com ns1.other.example]", got)
	}
}

func TestParse_NameserverHostsOptional(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[nameservers]]
id = "ns-01"
address = "1.2.3.4"
control_port = 9100
domains = ["example.com"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cfg.Nameservers[0].Hosts) != 0 {
		t.Fatalf("Hosts = %v, want empty when omitted", cfg.Nameservers[0].Hosts)
	}
}

func TestParse_DefaultControlTimeout(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Control.Timeout.Seconds() != 10 {
		t.Fatalf("default timeout: got %v, want 10s", cfg.Control.Timeout)
	}
}
