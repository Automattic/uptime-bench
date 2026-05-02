//go:build live

// Package jetmonv1 live test. Run with:
//
//	JETMON_BRIDGE_URL=http://localhost:7400 \
//	JETMON_BRIDGE_TOKEN=test-bench-token \
//	JETMON_TARGET_URL=http://bench-a.example.com/ \
//	go test -tags live -run Live ./internal/adapter/jetmonv1/ -v
//
// Without the `live` build tag this file is skipped entirely.
//
// Setting up the local bridge:
//
//  1. cd ../jetmon-bridge
//
//  2. ensure docker/.env has JETMON_TOKEN and (for the write-mode test)
//     JETMON_WRITE=true with JETMON_BUCKET=0
//
//  3. make up-local
//
//  4. seed the read-mode target into the bridge's MySQL — TestLive_ReadModeProvision
//     requires a row in jetpack_monitor_sites whose monitor_url matches
//     JETMON_TARGET_URL. With the default credentials:
//
//     docker exec docker-mysql-1 mysql -u root -pjetmon_test jetmon_db -e \
//     "INSERT INTO jetpack_monitor_sites \
//     (blog_id, bucket_no, monitor_url, monitor_active, site_status, check_interval, last_status_change) \
//     VALUES (9001, 0, 'http://bench-a.example.com/', 1, 1, 5, NOW());"
//
// The two tests cover both halves of the adapter contract: read mode
// (just the lookup, no DB writes) and write mode (creates and deactivates
// a monitor through POST/DELETE).
package jetmonv1

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func liveBridge(t *testing.T) (url, token, target string) {
	t.Helper()
	url = os.Getenv("JETMON_BRIDGE_URL")
	token = os.Getenv("JETMON_BRIDGE_TOKEN")
	target = os.Getenv("JETMON_TARGET_URL")
	if url == "" || target == "" {
		t.Skip("set JETMON_BRIDGE_URL and JETMON_TARGET_URL to run live test")
	}
	return url, token, target
}

// TestLive_ReadModeProvision verifies that a pre-seeded monitor can be
// looked up via GET /monitors. The test target must already exist in
// jetpack_monitor_sites — when running locally with the seeded bridge,
// see the README for the SQL to insert it.
func TestLive_ReadModeProvision(t *testing.T) {
	url, token, target := liveBridge(t)
	a := New("jetmon-v1-live", url, token, false /* writeMode */)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	handle, err := a.Provision(ctx, adapter.Target{ID: "smoketest", URL: target}, adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Provision (read mode): %v", err)
	}
	t.Logf("Provision read: handle = %+v", handle)
	if handle.MonitorID == "" {
		t.Fatal("handle.MonitorID is empty")
	}
	if handle.Fields["blog_id"] == "" {
		t.Errorf("handle.Fields[blog_id] = %q, expected non-empty", handle.Fields["blog_id"])
	}
}

// TestLive_WriteModeCycle exercises the full Provision/Retrieve/Deprovision
// cycle in write mode. Provision creates a fresh monitor row (or reactivates
// an existing one), Retrieve fetches synthesised events from the bridge,
// Deprovision soft-deletes the row.
func TestLive_WriteModeCycle(t *testing.T) {
	url, token, target := liveBridge(t)
	if token == "" {
		t.Skip("write-mode test requires JETMON_BRIDGE_TOKEN to be set on the bridge")
	}
	a := New("jetmon-v1-live", url, token, true /* writeMode */)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use a unique URL so this test doesn't collide with the seeded data
	// or with the read-mode test running in the same process.
	cycleURL := target + "smoketest-write/"
	handle, err := a.Provision(ctx, adapter.Target{ID: "smoketest-write", URL: cycleURL}, adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute})
	if err != nil {
		t.Fatalf("Provision (write mode): %v", err)
	}
	t.Logf("Provision write: handle = %+v", handle)

	defer func() {
		t.Logf("Deprovision: deactivating monitor %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("handle.MonitorID is empty after write provision")
	}
	if handle.Fields["monitor_url"] != cycleURL {
		t.Errorf("handle.Fields[monitor_url] = %q, want %q", handle.Fields["monitor_url"], cycleURL)
	}

	// Retrieve. With a freshly-created monitor and no run-time activity
	// there will likely be no transition events; the v1 bridge synthesises
	// a single event from last_status_change if it falls in the window.
	window := adapter.RunWindow{
		FailureStarted: time.Now().Add(-24 * time.Hour),
		FailureEnded:   time.Now(),
		GracePeriodEnd: time.Now().Add(time.Hour),
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
		t.Logf("  report[%d]: %s raw=%q at=%s meta=%v", i, r.EventType, r.RawClassification, r.ReportedAt.Format(time.RFC3339), r.Metadata)
	}
}
