package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

func TestRunFetchesSelectedSources(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/pingdom":
			fmt.Fprint(w, "13.232.220.164")
		case "/uptimerobot":
			fmt.Fprint(w, "216.144.248.5")
		case "/datadog":
			fmt.Fprint(w, `{"synthetics":{"prefixes_ipv4_by_location":{"aws:us-east-1":["3.3.3.3/32"]}}}`)
		case "/betterstack":
			fmt.Fprint(w, `<h2>What IPs does Better Stack use?</h2><h4>EU (Europe)</h4><code>65.108.79.0/24</code>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	regionMapPath := filepath.Join(t.TempDir(), "uptimerobot_regions.json")
	if err := os.WriteFile(regionMapPath, []byte(`{"prefixes":[{"region":"us-east","cidrs":["216.144.248.0/24"]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := run(context.Background(), []string{
		"-pingdom-url", srv.URL + "/pingdom",
		"-uptimerobot-url", srv.URL + "/uptimerobot",
		"-datadog-url", srv.URL + "/datadog",
		"-betterstack-url", srv.URL + "/betterstack",
		"-uptimerobot-region-map", regionMapPath,
	}, &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	cfg, err := serviceconfig.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("output should be parseable services TOML: %v\n%s", err, out.String())
	}
	if len(cfg.Services) != 4 {
		t.Fatalf("services = %d, want 4\n%s", len(cfg.Services), out.String())
	}
	got := serviceByID(cfg, "uptimerobot").ProbeRanges["us-east"]
	if len(got) != 1 || got[0] != "216.144.248.5/32" {
		t.Fatalf("uptimerobot us-east = %v", got)
	}
	if _, ok := serviceByID(cfg, "datadog-synthetics").ProbeRanges["us-east"]; !ok {
		t.Fatalf("datadog us-east range missing: %+v", serviceByID(cfg, "datadog-synthetics").ProbeRanges)
	}
}

func TestRunRejectsUnknownService(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"-services", "unknown"}, &out)
	if err == nil {
		t.Fatal("run returned nil error, want unknown service error")
	}
}

func serviceByID(cfg *serviceconfig.Config, id string) serviceconfig.Service {
	for _, svc := range cfg.Services {
		if svc.ID == id {
			return svc
		}
	}
	return serviceconfig.Service{}
}
