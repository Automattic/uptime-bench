package jetmonv1

import (
	"testing"

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

// TestImplementsAdapterInterface ensures the adapter satisfies the
// interface — a static check that the compiler enforces, but the
// explicit assertion makes the contract obvious.
func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}
