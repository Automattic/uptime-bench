package jetmonv1

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestRetrieveMapsSiteDownAsAlertFired(t *testing.T) {
	reportedAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			t.Fatalf("path = %q, want /events", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"id": 1,
			"blog_id": 42,
			"event_type": "status_transition",
			"source": "veriflier",
			"old_status": 1,
			"new_status": 0,
			"created_at": "` + reportedAt.Format(time.RFC3339) + `"
		}]`))
	}))
	defer srv.Close()

	a := New("jetmon-v1", srv.URL, "tok", false)
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{
		Fields: map[string]string{"blog_id": "42"},
	}, adapter.RunWindow{
		FailureStarted: reportedAt.Add(-time.Minute),
		GracePeriodEnd: reportedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %s, want %s", res.Status, adapter.RetrieveKnown)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("Reports = %d, want 1", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("EventType = %s, want %s", res.Reports[0].EventType, adapter.EventAlertFired)
	}
	if res.Reports[0].RawClassification != "down" {
		t.Errorf("RawClassification = %q, want down", res.Reports[0].RawClassification)
	}
}

func TestRetrieveMapsRunningAsAlertResolved(t *testing.T) {
	reportedAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{
			"id": 1,
			"blog_id": 42,
			"event_type": "status_transition",
			"source": "jetmon",
			"old_status": 0,
			"new_status": 1,
			"created_at": "` + reportedAt.Format(time.RFC3339) + `"
		}]`))
	}))
	defer srv.Close()

	a := New("jetmon-v1", srv.URL, "tok", false)
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{
		Fields: map[string]string{"blog_id": "42"},
	}, adapter.RunWindow{
		FailureStarted: reportedAt.Add(-time.Minute),
		GracePeriodEnd: reportedAt.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("Reports = %d, want 1", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertResolved {
		t.Errorf("EventType = %s, want %s", res.Reports[0].EventType, adapter.EventAlertResolved)
	}
	if res.Reports[0].RawClassification != "up" {
		t.Errorf("RawClassification = %q, want up", res.Reports[0].RawClassification)
	}
}

// TestImplementsAdapterInterface ensures the adapter satisfies the
// interface — a static check that the compiler enforces, but the
// explicit assertion makes the contract obvious.
func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}
