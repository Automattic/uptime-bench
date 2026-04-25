//go:build live

// Package betteruptime live test. Run with:
//
//	BETTER_UPTIME_TOKEN=<your_better_uptime_api_token> \
//	BETTER_UPTIME_TARGET_URL=http://your-target.example/ \
//	go test -tags live -run Live ./internal/adapter/betteruptime/ -v
//
// Without the `live` build tag this file is skipped entirely. The test
// creates a real monitor for BETTER_UPTIME_TARGET_URL, queries its
// incidents over a 24-hour window (typically empty for a fresh monitor),
// and deletes it. Cleanup is deferred so a failure between Provision and
// Deprovision still removes the monitor.
//
// The free tier is rate-limited to 60 req/min and supports a minimum
// 3-minute check frequency.
package betteruptime

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	tok := os.Getenv("BETTER_UPTIME_TOKEN")
	target := os.Getenv("BETTER_UPTIME_TARGET_URL")
	if tok == "" || target == "" {
		t.Skip("set BETTER_UPTIME_TOKEN and BETTER_UPTIME_TARGET_URL to run live test")
	}
	return New("better-uptime-live", "", tok), target
}

// TestLiveProvisionRetrieveDeprovision exercises the full adapter contract
// against the live Better Uptime API.
func TestLiveProvisionRetrieveDeprovision(t *testing.T) {
	a, targetURL := liveAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target := adapter.Target{ID: "smoketest-bench-a", URL: targetURL}
	cfg := adapter.ProvisionConfig{CheckFrequency: 3 * time.Minute}

	t.Logf("Provision: creating monitor for %s", targetURL)
	handle, err := a.Provision(ctx, target, cfg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision: handle = %+v", handle)

	defer func() {
		t.Logf("Deprovision: deleting monitor %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("Provision returned empty MonitorID")
	}
	if handle.ServiceID != "better-uptime-live" {
		t.Errorf("ServiceID = %q, want better-uptime-live", handle.ServiceID)
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
