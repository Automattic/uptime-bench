package adapter

import "testing"

func TestCleanupScopeMatchesURLAndHost(t *testing.T) {
	scope := CleanupScope{
		TargetHosts: []string{"bench-a.example"},
		TargetURLs:  []string{"http://bench-a.example/status"},
	}

	cases := []struct {
		raw  string
		want bool
	}{
		{"http://bench-a.example/status/", true},
		{"https://bench-a.example/other", true},
		{"bench-a.example", true},
		{"http://bench-b.example/status", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := scope.MatchesURL(tc.raw); got != tc.want {
			t.Errorf("MatchesURL(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestEmptyCleanupScopeMatchesEverything(t *testing.T) {
	var scope CleanupScope
	if !scope.MatchesURL("http://anything.example/") {
		t.Fatal("empty cleanup scope should match any URL")
	}
	if !scope.MatchesHost("anything.example") {
		t.Fatal("empty cleanup scope should match any host")
	}
}

func TestCapabilitiesSupportsMonitorKind(t *testing.T) {
	var httpOnly Capabilities
	if !httpOnly.SupportsMonitorKind(MonitorKindHTTP) {
		t.Fatal("empty MonitorKinds should support HTTP")
	}
	if httpOnly.SupportsMonitorKind(MonitorKindDNS) {
		t.Fatal("empty MonitorKinds should not support DNS")
	}

	caps := Capabilities{MonitorKinds: []string{MonitorKindHTTP, MonitorKindDNS}}
	if !caps.SupportsMonitorKind(MonitorKindDNS) {
		t.Fatal("explicit MonitorKinds should support DNS")
	}
	if caps.SupportsMonitorKind(MonitorKindHeartbeat) {
		t.Fatal("explicit MonitorKinds should not support heartbeat unless listed")
	}
}
