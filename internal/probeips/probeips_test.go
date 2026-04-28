package probeips

import (
	"slices"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

func TestNormalizeCIDR(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":            "1.2.3.4/32",
		"1.2.3.4/24":         "1.2.3.0/24",
		"`2001:db8::1`":      "2001:db8::1/128",
		"2001:db8::abcd/120": "2001:db8::ab00/120",
	}
	for in, want := range cases {
		got, ok := NormalizeCIDR(in)
		if !ok {
			t.Fatalf("NormalizeCIDR(%q) returned !ok", in)
		}
		if got != want {
			t.Fatalf("NormalizeCIDR(%q) = %q, want %q", in, got, want)
		}
	}
	if got, ok := NormalizeCIDR("not-an-ip"); ok {
		t.Fatalf("NormalizeCIDR(not-an-ip) = %q, want !ok", got)
	}
}

func TestCanonicalRegion(t *testing.T) {
	cases := map[string]string{
		"aws:us-east-1":      "us-east",
		"gcp:europe-west3":   "eu-west",
		"azure:eastus":       "us-east",
		"AS (Asia)":          "ap-sea",
		"NA (North America)": "us-east",
	}
	for in, want := range cases {
		if got := CanonicalRegion(in); got != want {
			t.Fatalf("CanonicalRegion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDatadogUsesLocationMaps(t *testing.T) {
	in := []byte(`{
  "synthetics": {
    "prefixes_ipv4": ["198.51.100.10/32"],
    "prefixes_ipv4_by_location": {
      "aws:us-east-1": ["3.3.3.3/32"],
      "gcp:europe-west3": ["34.159.50.128/29"]
    },
    "prefixes_ipv6_by_location": {
      "aws:ap-southeast-1": ["2001:db8::1/128"]
    }
  }
}`)

	regions, warnings, err := ParseDatadog(in)
	if err != nil {
		t.Fatalf("ParseDatadog: %v", err)
	}
	if !slices.Equal(regions["us-east"], []string{"3.3.3.3/32"}) {
		t.Fatalf("us-east = %v", regions["us-east"])
	}
	if !slices.Equal(regions["eu-west"], []string{"34.159.50.128/29"}) {
		t.Fatalf("eu-west = %v", regions["eu-west"])
	}
	if !slices.Equal(regions["ap-sea"], []string{"2001:db8::1/128"}) {
		t.Fatalf("ap-sea = %v", regions["ap-sea"])
	}
	if len(warnings) == 0 {
		t.Fatal("expected Datadog normalization warning")
	}
	if _, ok := regions["global"]; ok {
		t.Fatalf("global flat list should not be used when location maps exist: %v", regions["global"])
	}
}

func TestParsePingdomPlainText(t *testing.T) {
	regions, warnings, err := ParsePingdom([]byte("13.232.220.164 52.24.42.103\n"))
	if err != nil {
		t.Fatalf("ParsePingdom: %v", err)
	}
	want := []string{"13.232.220.164/32", "52.24.42.103/32"}
	if !slices.Equal(regions["global"], want) {
		t.Fatalf("global = %v, want %v", regions["global"], want)
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "untagged") {
		t.Fatalf("warnings = %v, want untagged warning", warnings)
	}
}

func TestParseUptimeRobotUsesRegionMap(t *testing.T) {
	regionMap := RegionMap{Prefixes: []RegionPrefix{
		{Region: "us-east", CIDRs: []string{"216.144.248.0/24"}},
		{Region: "us-west", CIDRs: []string{"208.115.199.0/24"}},
	}}
	regions, warnings, err := ParseUptimeRobot([]byte("216.144.248.5\n208.115.199.9\n203.0.113.10\n"), regionMap)
	if err != nil {
		t.Fatalf("ParseUptimeRobot: %v", err)
	}
	if !slices.Equal(regions["us-east"], []string{"216.144.248.5/32"}) {
		t.Fatalf("us-east = %v", regions["us-east"])
	}
	if !slices.Equal(regions["us-west"], []string{"208.115.199.9/32"}) {
		t.Fatalf("us-west = %v", regions["us-west"])
	}
	if !slices.Equal(regions["unmapped"], []string{"203.0.113.10/32"}) {
		t.Fatalf("unmapped = %v", regions["unmapped"])
	}
	if !containsWarning(warnings, "unmapped") {
		t.Fatalf("warnings = %v, want unmapped warning", warnings)
	}
}

func TestParseBetterStackProseRegions(t *testing.T) {
	in := []byte(`
<h2>What User-Agent does Better Stack use?</h2>
<code>Chrome/130.0.0.0</code>
<p>Europe phone support is available.</p>
<h2>What IPs does Better Stack use?</h2>
<h4>AS (Asia)</h4>
<code>5.223.56.56</code>
<h4>EU (Europe)</h4>
<code>65.108.79.0/24</code>
`)
	regions, warnings, err := ParseBetterStack(in)
	if err != nil {
		t.Fatalf("ParseBetterStack: %v", err)
	}
	if !slices.Equal(regions["ap-sea"], []string{"5.223.56.56/32"}) {
		t.Fatalf("ap-sea = %v", regions["ap-sea"])
	}
	if !slices.Equal(regions["eu-west"], []string{"65.108.79.0/24"}) {
		t.Fatalf("eu-west = %v", regions["eu-west"])
	}
	if _, ok := regions["global"]; ok {
		t.Fatalf("pre-IP user-agent version leaked into global: %v", regions["global"])
	}
	if !containsWarning(warnings, "broad prose regions") {
		t.Fatalf("warnings = %v, want broad prose warning", warnings)
	}
}

func TestFormatTOMLIsParseableServiceFragment(t *testing.T) {
	out := FormatTOML([]ServiceRanges{
		{
			ServiceID:   ServicePingdom,
			ServiceType: ServicePingdom,
			SourceURL:   DefaultPingdomURL,
			Regions: map[string][]string{
				"us-east": {"1.2.3.4", "1.2.3.0/24"},
			},
			Warnings: []string{"review this"},
		},
	})
	cfg, err := serviceconfig.Parse([]byte(out))
	if err != nil {
		t.Fatalf("generated TOML did not parse: %v\n%s", err, out)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(cfg.Services))
	}
	got := cfg.Services[0].ProbeRanges["us-east"]
	want := []string{"1.2.3.0/24", "1.2.3.4/32"}
	if !slices.Equal(got, want) {
		t.Fatalf("probe ranges = %v, want %v\n%s", got, want, out)
	}
	if !strings.Contains(out, "# warning: review this") {
		t.Fatalf("generated TOML missing warning comment:\n%s", out)
	}
}

func containsWarning(warnings []string, needle string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, needle) {
			return true
		}
	}
	return false
}
