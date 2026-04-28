package jetmonv1

import (
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func TestNormalize(t *testing.T) {
	a := New("jetmon-v1", "http://localhost:7400", "tok", false)

	cases := map[string]string{
		"down":       "http_failure",
		"seems_down": "http_failure",
		"degraded":   "http_failure",
		"up":         "recovered",
		"unknown":    "unknown",
		"":           adapter.UnrecognizedClassification,
		"flapping":   adapter.UnrecognizedClassification,
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestCapabilities(t *testing.T) {
	c := New("jetmon-v1", "http://localhost:7400", "tok", false).Capabilities()
	if c.MinCheckFrequency != time.Minute {
		t.Errorf("MinCheckFrequency = %v, want 1m", c.MinCheckFrequency)
	}
	if c.SupportsKeyword {
		t.Error("SupportsKeyword should be false until jetmon-bridge supports body checks")
	}
	if c.SupportsInvertedKeyword {
		t.Error("SupportsInvertedKeyword should be false until jetmon-bridge supports body checks")
	}
	if !c.SupportsAgentChecks {
		t.Error("SupportsAgentChecks should be true")
	}
	if c.SupportsMaintenanceWindows {
		t.Error("SupportsMaintenanceWindows should be false until jetmon-bridge supports maintenance windows")
	}
	if c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be false in read-only mode")
	}
	if c.DefaultMaxCallsPerRun != 0 {
		t.Errorf("DefaultMaxCallsPerRun = %d, want 0 for self-hosted bridge", c.DefaultMaxCallsPerRun)
	}
}

func TestCapabilities_WriteModeSupportsCooldownReset(t *testing.T) {
	c := New("jetmon-v1", "http://localhost:7400", "tok", true).Capabilities()
	if !c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be true in write mode")
	}
}

// TestImplementsAdapterInterface ensures the adapter satisfies the
// interface — a static check that the compiler enforces, but the
// explicit assertion makes the contract obvious.
func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}
