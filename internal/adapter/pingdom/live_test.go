//go:build live

// Package pingdom live test. Run with:
//
//	PINGDOM_TOKEN=<your_pingdom_api_token> \
//	PINGDOM_TARGET_URL=http://your-target.example/ \
//	go test -tags live -run Live ./internal/adapter/pingdom/ -v
//
// Without the `live` build tag this file is skipped entirely.
package pingdom

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	tok := os.Getenv("PINGDOM_TOKEN")
	target := os.Getenv("PINGDOM_TARGET_URL")
	if tok == "" || target == "" {
		t.Skip("set PINGDOM_TOKEN and PINGDOM_TARGET_URL to run live test")
	}
	return New("pingdom-live", "", tok), target
}

// TestLiveProvisionRetrieveDeprovision exercises the full adapter contract
// against api.pingdom.com. Same shape as the UptimeRobot live test:
// Provision a check, Retrieve outage history (which will be empty for a
// fresh check), then Deprovision via best-effort cleanup.
func TestLiveProvisionRetrieveDeprovision(t *testing.T) {
	a, targetURL := liveAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target := adapter.Target{ID: "smoketest-bench-a", URL: targetURL}
	cfg := adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute}

	t.Logf("Provision: creating check for %s", targetURL)
	handle, err := a.Provision(ctx, target, cfg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision: handle = %+v", handle)

	defer func() {
		t.Logf("Deprovision: deleting check %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("Provision returned empty MonitorID")
	}

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
