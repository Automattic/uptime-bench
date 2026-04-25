//go:build live

// Package uptimerobot live test. Run with:
//
//	UPTIMEROBOT_API_KEY=u123-XXX \
//	UPTIMEROBOT_TARGET_URL=http://your-target.example/ \
//	go test -tags live -run Live ./internal/adapter/uptimerobot/ -v
//
// Without the `live` build tag this file is skipped entirely. Tests are
// structured so a single run does Provision → Retrieve → Deprovision
// against the real API; if Provision succeeds but a later step errors,
// the test attempts a best-effort cleanup so we don't leak monitors.
package uptimerobot

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	key := os.Getenv("UPTIMEROBOT_API_KEY")
	target := os.Getenv("UPTIMEROBOT_TARGET_URL")
	if key == "" || target == "" {
		t.Skip("set UPTIMEROBOT_API_KEY and UPTIMEROBOT_TARGET_URL to run live test")
	}
	return New("uptimerobot-live", "", key), target
}

// TestLiveProvisionRetrieveDeprovision exercises the full adapter contract
// against the real UptimeRobot v2 API. It creates a monitor, briefly waits
// (UptimeRobot takes a few seconds to do its first probe), retrieves the
// log entries, and deletes the monitor. Failures at any step include the
// API's own error text, which is usually enough to diagnose.
func TestLiveProvisionRetrieveDeprovision(t *testing.T) {
	a, targetURL := liveAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target := adapter.Target{ID: "smoketest-bench-a", URL: targetURL}
	cfg := adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute}

	t.Logf("Provision: creating monitor for %s", targetURL)
	handle, err := a.Provision(ctx, target, cfg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision: handle = %+v", handle)

	// Always best-effort delete, even if Retrieve fails.
	defer func() {
		t.Logf("Deprovision: deleting monitor %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("Provision returned empty MonitorID")
	}
	if handle.ServiceID != "uptimerobot-live" {
		t.Errorf("ServiceID = %q, want uptimerobot-live", handle.ServiceID)
	}

	// Retrieve immediately. The window is the last 24 hours, so we'll see
	// any logs UptimeRobot has for this fresh monitor (typically empty or
	// a single "Started" entry).
	window := adapter.RunWindow{
		FailureStarted: time.Now().Add(-24 * time.Hour),
		FailureEnded:   time.Now(),
		GracePeriodEnd: time.Now(),
	}
	res, err := a.Retrieve(ctx, handle, window)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	t.Logf("Retrieve: status=%s reports=%d reason=%q", res.Status, len(res.Reports), res.Reason)
	if res.Status == adapter.RetrieveUnknown {
		t.Errorf("Retrieve returned Unknown: %s", res.Reason)
	}
	for i, r := range res.Reports {
		t.Logf("  report[%d]: %s raw=%q at=%s", i, r.EventType, r.RawClassification, r.ReportedAt.Format(time.RFC3339))
	}
}
