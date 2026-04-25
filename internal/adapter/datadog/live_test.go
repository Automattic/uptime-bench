//go:build live

// Package datadog live test. Run with:
//
//	DATADOG_API_KEY=<DD-API-KEY> \
//	DATADOG_APP_KEY=<DD-APPLICATION-KEY> \
//	DATADOG_TARGET_URL=http://your-target.example/ \
//	go test -tags live -run Live ./internal/adapter/datadog/ -v
//
// For non-US accounts, also set DATADOG_API_URL (e.g.
// https://api.datadoghq.eu, https://api.us3.datadoghq.com,
// https://api.us5.datadoghq.com, https://api.ap1.datadoghq.com).
//
// Without the `live` build tag this file is skipped entirely. The test
// creates a real synthetic test, retrieves results over a 24-hour
// window (typically empty for a fresh test), and deletes it. Cleanup
// is deferred so a failure between Provision and Deprovision still
// removes the monitor.
//
// Note: Datadog's default location is aws:us-east-1. If your account
// is on a region that doesn't include that location, Provision will
// error with the API's location-not-allowed message — adjust the
// adapter's hardcoded location list before re-running.
package datadog

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	apiKey := os.Getenv("DATADOG_API_KEY")
	appKey := os.Getenv("DATADOG_APP_KEY")
	target := os.Getenv("DATADOG_TARGET_URL")
	if apiKey == "" || appKey == "" || target == "" {
		t.Skip("set DATADOG_API_KEY, DATADOG_APP_KEY, and DATADOG_TARGET_URL to run live test")
	}
	apiURL := os.Getenv("DATADOG_API_URL") // optional region override
	return New("datadog-synthetics-live", apiURL, apiKey, appKey), target
}

// TestLiveProvisionRetrieveDeprovision exercises the full adapter contract
// against the live Datadog Synthetics API.
func TestLiveProvisionRetrieveDeprovision(t *testing.T) {
	a, targetURL := liveAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	target := adapter.Target{ID: "smoketest-bench-a", URL: targetURL}
	cfg := adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute}

	t.Logf("Provision: creating synthetic test for %s", targetURL)
	handle, err := a.Provision(ctx, target, cfg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision: handle = %+v", handle)

	defer func() {
		t.Logf("Deprovision: deleting synthetic test %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("Provision returned empty MonitorID")
	}
	if handle.ServiceID != "datadog-synthetics-live" {
		t.Errorf("ServiceID = %q, want datadog-synthetics-live", handle.ServiceID)
	}

	// Datadog takes a moment to schedule the first execution. The 24-hour
	// window will pick up any results that landed; for a fresh test it'll
	// usually be empty.
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
