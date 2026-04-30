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

[[domains]]
name = "example.com"
nameservers = ["ns-01"]
nameserver_hosts = ["ns1.example.net"]
ttl = 30
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Nameservers[0].Hosts
	if len(got) != 2 || got[0] != "ns1.example.com" || got[1] != "ns1.other.example" {
		t.Fatalf("Hosts = %v, want [ns1.example.com ns1.other.example]", got)
	}
	domainHosts := cfg.Domains[0].NameserverHosts
	if len(domainHosts) != 1 || domainHosts[0] != "ns1.example.net" {
		t.Fatalf("Domain.NameserverHosts = %v, want [ns1.example.net]", domainHosts)
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

func TestParse_GeneratedSitesRoundTrip(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000

  [[targets.generated_sites]]
  id = "load"
  host_pattern = "site-%07d.load.example.com"
  start = 0
  count = 1000000
  paths = ["/", "/health"]
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Targets[0].GeneratedSites
	if len(got) != 1 {
		t.Fatalf("GeneratedSites len = %d, want 1", len(got))
	}
	if got[0].ID != "load" ||
		got[0].HostPattern != "site-%07d.load.example.com" ||
		got[0].Start != 0 ||
		got[0].Count != 1_000_000 ||
		len(got[0].Paths) != 2 ||
		got[0].Paths[1] != "/health" {
		t.Fatalf("GeneratedSites[0] = %+v", got[0])
	}
}

func TestParse_GeneratedSitesDefaultStart(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000

  [[targets.generated_sites]]
  id = "load"
  host_pattern = "site-%07d.load.example.com"
  count = 10
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := cfg.Targets[0].GeneratedSites[0].Start; got != 1 {
		t.Fatalf("Start = %d, want default 1", got)
	}
}

func TestParse_RejectsInvalidGeneratedSites(t *testing.T) {
	for name, in := range map[string]string{
		"missing id": `
[control]
auth_token_file = "/tmp/tok"
[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000
  [[targets.generated_sites]]
  host_pattern = "site-%07d.load.example.com"
  count = 10
`,
		"missing pattern": `
[control]
auth_token_file = "/tmp/tok"
[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000
  [[targets.generated_sites]]
  id = "load"
  count = 10
`,
		"bad count": `
[control]
auth_token_file = "/tmp/tok"
[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000
  [[targets.generated_sites]]
  id = "load"
  host_pattern = "site-%07d.load.example.com"
  count = 0
`,
		"bad start": `
[control]
auth_token_file = "/tmp/tok"
[[targets]]
id = "target-01"
address = "10.0.0.10"
control_port = 9000
  [[targets.generated_sites]]
  id = "load"
  host_pattern = "site-%07d.load.example.com"
  start = -1
  count = 10
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(in)); err == nil {
				t.Fatal("Parse returned nil error")
			}
		})
	}
}

func TestParse_CertmintRoundTrip(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"

[certmint]
id           = "certmint-01"
address      = "10.0.0.30"
library_port = 9200
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Certmint == nil {
		t.Fatal("Certmint = nil, want populated")
	}
	if cfg.Certmint.ID != "certmint-01" {
		t.Fatalf("ID = %q", cfg.Certmint.ID)
	}
	if got := cfg.Certmint.LibraryURL(); got != "http://10.0.0.30:9200" {
		t.Fatalf("LibraryURL = %q", got)
	}
}

func TestParse_CertmintOptional(t *testing.T) {
	in := `
[control]
auth_token_file = "/tmp/tok"
`
	cfg, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Certmint != nil {
		t.Fatalf("Certmint = %+v, want nil when section omitted", cfg.Certmint)
	}
	// Methods on nil should be safe — used in code paths that
	// branch on whether certmint is configured.
	if cfg.Certmint.LibraryURL() != "" {
		t.Fatal("nil Certmint.LibraryURL() should return empty string")
	}
}

func TestParse_CertmintRequiresIDAndAddress(t *testing.T) {
	missingID := `
[control]
auth_token_file = "/tmp/tok"

[certmint]
address = "10.0.0.30"
`
	if _, err := Parse([]byte(missingID)); err == nil || !strings.Contains(err.Error(), "certmint.id") {
		t.Fatalf("missing id err = %v", err)
	}

	missingAddr := `
[control]
auth_token_file = "/tmp/tok"

[certmint]
id = "certmint-01"
`
	if _, err := Parse([]byte(missingAddr)); err == nil || !strings.Contains(err.Error(), "address is required") {
		t.Fatalf("missing address err = %v", err)
	}
}

func TestCertmint_LibraryURLDefaultsPort(t *testing.T) {
	c := &Certmint{ID: "x", Address: "10.0.0.30"}
	if got := c.LibraryURL(); got != "http://10.0.0.30:9200" {
		t.Fatalf("LibraryURL = %q, want default port 9200", got)
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
