package runner

import (
	"context"
	"reflect"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

func TestScenarioIsTLSOnly(t *testing.T) {
	tests := []struct {
		name string
		sc   *scenario.Scenario
		want bool
	}{
		{
			name: "only TLS",
			sc: &scenario.Scenario{Failures: []scenario.Failure{
				{Type: "tls_expiring"},
				{Type: "tls_deprecated"},
			}},
			want: true,
		},
		{
			name: "mixed",
			sc: &scenario.Scenario{Failures: []scenario.Failure{
				{Type: "tls_expiring"},
				{Type: "http_status"},
			}},
			want: false,
		},
		{
			name: "empty",
			sc:   &scenario.Scenario{},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scenarioIsTLSOnly(tc.sc); got != tc.want {
				t.Fatalf("scenarioIsTLSOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDNSBaselineList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "json",
			raw:  `["10.0.0.176:5353","8.8.8.8"]`,
			want: []string{"10.0.0.176:5353", "8.8.8.8"},
		},
		{
			name: "comma",
			raw:  `10.0.0.176:5353, 8.8.8.8`,
			want: []string{"10.0.0.176:5353", "8.8.8.8"},
		},
		{
			name: "quoted bracket list",
			raw:  `["10.0.0.176:5353", "8.8.8.8"]`,
			want: []string{"10.0.0.176:5353", "8.8.8.8"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDNSBaselineList(tc.raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseDNSBaselineList = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNewDNSBaselinePlanIncludesConfiguredAndAuthoritativeResolvers(t *testing.T) {
	sc := &scenario.Scenario{
		Monitors: []string{"jetmon-v2"},
		Failures: []scenario.Failure{
			{Type: "tls_expiring"},
		},
	}
	fl := &fleet.Config{Nameservers: []fleet.Nameserver{
		{ID: "ns-a", Address: "167.99.6.211", DNSPort: 53},
		{ID: "ns-b", Address: "134.122.118.159", DNSPort: 53},
	}}
	svcCfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "jetmon-v2",
			Type:    "jetmon-v2",
			Enabled: true,
			Auth: map[string]string{
				"check_dns_resolvers":   `["10.0.0.176:5353"]`,
				"dns_baseline_ssh_host": "jetmon-service-host-2",
			},
		},
		{
			ID:      "other-jetmon",
			Type:    "jetmon-v2",
			Enabled: true,
			Auth: map[string]string{
				"check_dns_resolvers": "192.0.2.53:53",
			},
		},
	}}

	plan := newDNSBaselinePlan(sc, fl, svcCfg, "probe-a.steadycadence.party")
	if plan == nil {
		t.Fatal("newDNSBaselinePlan returned nil")
	}

	got := map[string]bool{}
	for _, resolver := range plan.resolvers {
		got[resolver.ID] = true
	}
	for _, want := range []string{
		"monitor-system:jetmon-service-host-2",
		"monitor-configured:jetmon-service-host-2:10.0.0.176:5353",
		"authoritative:ns-a",
		"authoritative:ns-b",
	} {
		if !got[want] {
			t.Fatalf("resolver %q not found in %#v", want, plan.resolvers)
		}
	}
	if got["configured:192.0.2.53:53"] {
		t.Fatalf("included resolver for service outside scenario monitors: %#v", plan.resolvers)
	}
	if got["harness-system"] || got["configured:10.0.0.176:5353"] {
		t.Fatalf("included harness-sourced checks even though monitor SSH is configured: %#v", plan.resolvers)
	}
}

func TestNewDNSBaselinePlanFallsBackToHarnessWhenMonitorHostIsNotConfigured(t *testing.T) {
	sc := &scenario.Scenario{
		Monitors: []string{"jetmon-v2"},
		Failures: []scenario.Failure{{Type: "tls_expiring"}},
	}
	svcCfg := &serviceconfig.Config{Services: []serviceconfig.Service{
		{
			ID:      "jetmon-v2",
			Type:    "jetmon-v2",
			Enabled: true,
			Auth: map[string]string{
				"check_dns_resolvers": "10.0.0.176:5353",
			},
		},
	}}
	plan := newDNSBaselinePlan(sc, &fleet.Config{}, svcCfg, "probe-a.steadycadence.party")
	if plan == nil {
		t.Fatal("newDNSBaselinePlan returned nil")
	}

	got := map[string]bool{}
	for _, resolver := range plan.resolvers {
		got[resolver.ID] = true
	}
	for _, want := range []string{"harness-system", "configured:10.0.0.176:5353"} {
		if !got[want] {
			t.Fatalf("resolver %q not found in %#v", want, plan.resolvers)
		}
	}
}

func TestNewDNSBaselinePlanSkipsNonTLSOnlyScenarios(t *testing.T) {
	sc := &scenario.Scenario{
		Monitors: []string{"jetmon-v2"},
		Failures: []scenario.Failure{
			{Type: "tls_expiring"},
			{Type: "http_status"},
		},
	}
	if plan := newDNSBaselinePlan(sc, &fleet.Config{}, &serviceconfig.Config{}, "example.com"); plan != nil {
		t.Fatalf("newDNSBaselinePlan = %#v, want nil for mixed scenario", plan)
	}
}

func TestLogDNSBaselineUnstableWritesUnknownReasonRows(t *testing.T) {
	rec := &fakeRecorder{}
	baseline := &dnsBaselinePlan{
		host:           "probe-a.steadycadence.party",
		unstable:       true,
		unstablePhases: []string{"active_start"},
		samples:        1,
	}
	logDNSBaselineUnstable(context.Background(), rec, "run-1", []provisioned{
		{a: &recordingAdapter{id: "jetmon-v2"}},
	}, baseline)

	if rec.monitorReportsLogged != 1 {
		t.Fatalf("monitor reports logged = %d, want 1", rec.monitorReportsLogged)
	}
	row := rec.monitorReportRows[0]
	if row.RetrieveStatus != string(adapter.RetrieveUnknown) {
		t.Fatalf("RetrieveStatus = %q, want unknown", row.RetrieveStatus)
	}
	if row.ReasonCode != adapter.ReasonSetupEnvironmentDNSUnstable {
		t.Fatalf("ReasonCode = %q, want %q", row.ReasonCode, adapter.ReasonSetupEnvironmentDNSUnstable)
	}
}
