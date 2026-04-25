package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv2"
)

// TestRegistry_KnownTypes asserts the registry has factories for every
// service type uptime-bench currently knows how to instantiate. Adding
// a new adapter should require an entry here, and removing one should
// fail the test loudly rather than silently change behavior.
func TestRegistry_KnownTypes(t *testing.T) {
	want := []string{"jetmon-v1", "jetmon-v2", "uptimerobot", "pingdom", "better-uptime", "datadog-synthetics"}
	for _, typ := range want {
		if _, ok := registry[typ]; !ok {
			t.Errorf("registry missing factory for %q", typ)
		}
	}
	for typ := range registry {
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

// TestRegistry_JetmonV1RequiresURL — the jetmon-v1 factory must reject
// an empty URL because the adapter has no public API endpoint to fall
// back to. Catching this at config parse time saves the operator a
// confusing "no monitor pre-seeded" error mid-run.
func TestRegistry_JetmonV1RequiresURL(t *testing.T) {
	factory := registry["jetmon-v1"]
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
	factory := registry["jetmon-v1"]
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

// TestRegistry_JetmonV2AlwaysErrors locks in the stub contract: the
// factory must return ErrNotImplemented unconditionally, with no
// possibility of building a real adapter, until the Jetmon 2 public
// API lands and the stub is replaced.
func TestRegistry_JetmonV2AlwaysErrors(t *testing.T) {
	factory := registry["jetmon-v2"]
	cases := []struct {
		name string
		url  string
		auth map[string]string
	}{
		{"empty everything", "", nil},
		{"with url and token", "https://api.example.com", map[string]string{"token": "tok"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := factory("jetmon-v2", tc.url, tc.auth)
			if err == nil {
				t.Fatal("expected error from jetmon-v2 stub factory")
			}
			if !errors.Is(err, jetmonv2.ErrNotImplemented) {
				t.Fatalf("err = %v, want ErrNotImplemented", err)
			}
			if a != nil {
				t.Fatal("stub factory returned a non-nil adapter")
			}
		})
	}
}

// TestRegistry_UptimeRobotRequiresAPIKey — the uptimerobot factory must
// reject an empty api_key. The adapter would also catch this on the
// first API call, but failing at construction time means the operator
// sees the error before any monitors are touched.
func TestRegistry_UptimeRobotRequiresAPIKey(t *testing.T) {
	factory := registry["uptimerobot"]
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
	factory := registry["uptimerobot"]
	a, err := factory("ur", "", map[string]string{"api_key": "u123-XXX"})
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

// TestRegistry_PingdomRequiresToken — same fail-fast contract as the
// other adapters: if the operator forgets the token, error before any
// API call instead of producing a confusing 401 mid-run.
func TestRegistry_PingdomRequiresToken(t *testing.T) {
	factory := registry["pingdom"]
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
	factory := registry["pingdom"]
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
	factory := registry["better-uptime"]
	_, err := factory("bu", "", nil)
	if err == nil {
		t.Fatal("expected error from better-uptime factory with empty token")
	}
	if !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want one mentioning token", err)
	}
}

func TestRegistry_BetterUptimeBuilds(t *testing.T) {
	factory := registry["better-uptime"]
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
	factory := registry["datadog-synthetics"]
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
	factory := registry["datadog-synthetics"]
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
