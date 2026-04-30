package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapterfactory"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// TestRegistry_KnownTypes asserts the registry has factories for every
// service type uptime-bench currently knows how to instantiate. Adding
// a new adapter should require an entry here, and removing one should
// fail the test loudly rather than silently change behavior.
func TestRegistry_KnownTypes(t *testing.T) {
	want := []string{"jetmon-v1", "jetmon-v2", "uptimerobot", "pingdom", "better-uptime", "datadog-synthetics"}
	for _, typ := range want {
		if _, ok := adapterfactory.Registry[typ]; !ok {
			t.Errorf("registry missing factory for %q", typ)
		}
	}
	for typ := range adapterfactory.Registry {
		known := false
		for _, w := range want {
			if typ == w {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("registry has unexpected entry %q — update TestRegistry_KnownTypes if intended", typ)
		}
	}
}

func TestParseMonitorOverride(t *testing.T) {
	got, err := parseMonitorOverride("jetmon-v2, pingdom")
	if err != nil {
		t.Fatalf("parseMonitorOverride: %v", err)
	}
	want := []string{"jetmon-v2", "pingdom"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseMonitorOverride = %#v, want %#v", got, want)
	}
}

func TestParseMonitorOverrideRejectsInvalidLists(t *testing.T) {
	cases := []string{
		"",
		"jetmon-v2,",
		"jetmon-v2,,pingdom",
		"jetmon-v2, jetmon-v2",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseMonitorOverride(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// TestRegistry_JetmonV1RequiresURL — the jetmon-v1 factory must reject
// an empty URL because the adapter has no public API endpoint to fall
// back to. Catching this at config parse time saves the operator a
// confusing "no monitor pre-seeded" error mid-run.
func TestRegistry_JetmonV1RequiresURL(t *testing.T) {
	factory := adapterfactory.Registry["jetmon-v1"]
	_, err := factory("jetmon", "", map[string]string{"token": "tok"})
	if err == nil {
		t.Fatal("expected error from jetmon-v1 factory with empty URL")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("err = %v, want one mentioning url", err)
	}
}

// TestRegistry_JetmonV1Builds — happy path: with a URL and token the
// factory returns a non-nil adapter.
func TestRegistry_JetmonV1Builds(t *testing.T) {
	factory := adapterfactory.Registry["jetmon-v1"]
	a, err := factory("jetmon", "http://localhost:7400", map[string]string{"token": "tok"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil adapter without error")
	}
	if a.ServiceID() != "jetmon" {
		t.Fatalf("ServiceID = %q, want jetmon", a.ServiceID())
	}
}

// TestRegistry_JetmonV2RequiresURLAndToken catches missing v2 API
// configuration before any monitor-management calls are attempted.
func TestRegistry_JetmonV2RequiresURLAndToken(t *testing.T) {
	factory := adapterfactory.Registry["jetmon-v2"]
	cases := []struct {
		name string
		url  string
		auth map[string]string
		want string
	}{
		{"empty everything", "", nil, "url"},
		{"missing token", "https://api.example.com/api/v1", nil, "token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := factory("jetmon-v2", tc.url, tc.auth)
			if err == nil {
				t.Fatal("expected error from jetmon-v2 factory")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tc.want)
			}
			if a != nil {
				t.Fatal("factory returned adapter on invalid config")
			}
		})
	}
}

// TestRegistry_JetmonV2Builds — happy path with API URL and token.
func TestRegistry_JetmonV2Builds(t *testing.T) {
	factory := adapterfactory.Registry["jetmon-v2"]
	a, err := factory("jetmon-v2", "http://localhost:8081/api/v1", map[string]string{
		"token":     "tok",
		"bucket_no": "17",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil")
	}
	if a.ServiceID() != "jetmon-v2" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestRegistry_JetmonV2RejectsInvalidBucket(t *testing.T) {
	factory := adapterfactory.Registry["jetmon-v2"]
	_, err := factory("jetmon-v2", "http://localhost:8081/api/v1", map[string]string{
		"token":     "tok",
		"bucket_no": "nope",
	})
	if err == nil {
		t.Fatal("expected invalid bucket_no error")
	}
	if !strings.Contains(err.Error(), "bucket_no") {
		t.Fatalf("err = %v, want bucket_no", err)
	}
}

// TestRegistry_UptimeRobotRequiresAPIKey — the uptimerobot factory must
// reject an empty api_key. The adapter would also catch this on the
// first API call, but failing at construction time means the operator
// sees the error before any monitors are touched.
func TestRegistry_UptimeRobotRequiresAPIKey(t *testing.T) {
	factory := adapterfactory.Registry["uptimerobot"]
	_, err := factory("ur", "", nil)
	if err == nil {
		t.Fatal("expected error from uptimerobot factory with empty api_key")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err = %v, want one mentioning api_key", err)
	}
}

// TestRegistry_UptimeRobotBuilds — happy path with api_key set.
func TestRegistry_UptimeRobotBuilds(t *testing.T) {
	factory := adapterfactory.Registry["uptimerobot"]
	a, err := factory("ur", "", map[string]string{
		"api_key":             "u123-XXX",
		"http_method":         "GET",
		"min_check_frequency": "60s",
	})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil")
	}
	if a.ServiceID() != "ur" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestRegistry_UptimeRobotRejectsInvalidHTTPMethod(t *testing.T) {
	factory := adapterfactory.Registry["uptimerobot"]
	_, err := factory("ur", "", map[string]string{"api_key": "u123-XXX", "http_method": "TRACE"})
	if err == nil {
		t.Fatal("expected invalid http_method error")
	}
	if !strings.Contains(err.Error(), "http_method") {
		t.Fatalf("err = %v, want http_method", err)
	}
}

func TestRegistry_UptimeRobotRejectsInvalidMinCheckFrequency(t *testing.T) {
	factory := adapterfactory.Registry["uptimerobot"]
	_, err := factory("ur", "", map[string]string{"api_key": "u123-XXX", "min_check_frequency": "soon"})
	if err == nil {
		t.Fatal("expected invalid min_check_frequency error")
	}
	if !strings.Contains(err.Error(), "min_check_frequency") {
		t.Fatalf("err = %v, want min_check_frequency", err)
	}
}

// TestRegistry_PingdomRequiresToken — same fail-fast contract as the
// other adapters: if the operator forgets the token, error before any
// API call instead of producing a confusing 401 mid-run.
func TestRegistry_PingdomRequiresToken(t *testing.T) {
	factory := adapterfactory.Registry["pingdom"]
	_, err := factory("pd", "", nil)
	if err == nil {
		t.Fatal("expected error from pingdom factory with empty token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want one mentioning token", err)
	}
}

// TestRegistry_PingdomBuilds — happy path.
func TestRegistry_PingdomBuilds(t *testing.T) {
	factory := adapterfactory.Registry["pingdom"]
	a, err := factory("pd", "", map[string]string{"token": "tok"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil")
	}
	if a.ServiceID() != "pd" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestRegistry_BetterUptimeRequiresToken(t *testing.T) {
	factory := adapterfactory.Registry["better-uptime"]
	_, err := factory("bu", "", nil)
	if err == nil {
		t.Fatal("expected error from better-uptime factory with empty token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want one mentioning token", err)
	}
}

func TestRegistry_BetterUptimeBuilds(t *testing.T) {
	factory := adapterfactory.Registry["better-uptime"]
	a, err := factory("bu", "", map[string]string{"token": "tok"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil")
	}
	if a.ServiceID() != "bu" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestRegistry_DatadogRequiresBothKeys(t *testing.T) {
	factory := adapterfactory.Registry["datadog-synthetics"]
	cases := []map[string]string{
		nil,
		{"api_key": "ak"},                // missing app_key
		{"app_key": "pk"},                // missing api_key
		{"api_key": "", "app_key": "pk"}, // empty api_key
	}
	for _, auth := range cases {
		_, err := factory("dd", "", auth)
		if err == nil {
			t.Errorf("auth=%v: expected error from datadog factory", auth)
			continue
		}
		if !strings.Contains(err.Error(), "required") {
			t.Errorf("auth=%v: err = %v, want one mentioning required", auth, err)
		}
	}
}

func TestRegistry_DatadogBuilds(t *testing.T) {
	factory := adapterfactory.Registry["datadog-synthetics"]
	a, err := factory("dd", "", map[string]string{"api_key": "ak", "app_key": "pk"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	if a == nil {
		t.Fatal("factory returned nil")
	}
	if a.ServiceID() != "dd" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestAdaptersForScenario_PreservesMonitorOrder(t *testing.T) {
	cfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "jetmon-a",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7400",
			Auth:    map[string]string{"token": "tok-a"},
			Enabled: true,
		},
		{
			ID:      "jetmon-b",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7401",
			Auth:    map[string]string{"token": "tok-b"},
			Enabled: true,
		},
	}}

	adapters, err := adapterfactory.ForScenario(cfg, []string{"jetmon-b", "jetmon-a"})
	if err != nil {
		t.Fatalf("adaptersForScenario: %v", err)
	}
	if len(adapters) != 2 {
		t.Fatalf("len(adapters) = %d, want 2", len(adapters))
	}
	if adapters[0].ServiceID() != "jetmon-b" || adapters[1].ServiceID() != "jetmon-a" {
		t.Fatalf("adapter order = [%s, %s], want [jetmon-b, jetmon-a]",
			adapters[0].ServiceID(), adapters[1].ServiceID())
	}
}

func TestAdaptersForScenario_RejectsDisabledMonitor(t *testing.T) {
	cfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "jetmon-a",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7400",
			Auth:    map[string]string{"token": "tok-a"},
			Enabled: false,
		},
	}}

	_, err := adapterfactory.ForScenario(cfg, []string{"jetmon-a"})
	if err == nil {
		t.Fatal("expected disabled scenario monitor to be rejected")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want one mentioning not found", err)
	}
}

func TestEnabledAdapters_LoadsEnabledServicesOnly(t *testing.T) {
	cfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "disabled",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7400",
			Auth:    map[string]string{"token": "tok-disabled"},
			Enabled: false,
		},
		{
			ID:      "enabled",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7401",
			Auth:    map[string]string{"token": "tok-enabled"},
			Enabled: true,
		},
	}}

	adapters, err := adapterfactory.Enabled(cfg)
	if err != nil {
		t.Fatalf("enabledAdapters: %v", err)
	}
	if len(adapters) != 1 {
		t.Fatalf("len(adapters) = %d, want 1", len(adapters))
	}
	if adapters[0].ServiceID() != "enabled" {
		t.Fatalf("ServiceID = %q, want enabled", adapters[0].ServiceID())
	}
}

func TestEnabledAdapters_RejectsEmptySet(t *testing.T) {
	cfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "disabled",
			Type:    "jetmon-v1",
			URL:     "http://localhost:7400",
			Auth:    map[string]string{"token": "tok-disabled"},
			Enabled: false,
		},
	}}

	_, err := adapterfactory.Enabled(cfg)
	if err == nil {
		t.Fatal("expected error when no services are enabled")
	}
	if !strings.Contains(err.Error(), "no enabled services") {
		t.Fatalf("err = %v, want one mentioning no enabled services", err)
	}
}
