// Package runtest provides shared scaffolding for tests that drive
// runner.Run() end-to-end. The runner reaches out to three external
// dependencies — an on-disk control token, an HTTP control plane on
// the target, and the configured Adapter implementations — that
// individually need fakes/fixtures to be testable.
//
// A Fixture wraps a temp control-token file, an httptest server
// standing in for the target's control plane, a fleet.Config wired to
// that server, and a FakeRecorder that satisfies the runner's
// recorder interface (structurally — the interface itself is
// unexported in internal/runner). Tests populate the Scenario,
// Adapters, and any per-test recorder hooks, then call Fixture.Run().
//
// The package lives at internal/runner/runtest/ rather than inline in
// the runner test file because cross-package consumers (campaigns
// Phase 4, future integration tests) will reuse the same scaffolding.
// Same-package fakeRecorder etc. in runner_test.go stay where they
// are; this package is the export-shaped sibling.
package runtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/runner"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// Fixture bundles every external dependency runner.Run() needs into
// one configurable object. Construct with NewFixture(t); set Scenario
// and Adapters; call Run().
type Fixture struct {
	t *testing.T

	Scenario     *scenario.Scenario
	Adapters     []adapter.Adapter
	Fleet        *fleet.Config
	Services     *serviceconfig.Config
	Recorder     *FakeRecorder
	TargetServer *httptest.Server // exposed so tests can attach custom handlers

	tokenFile string
}

// NewFixture builds a ready-to-run Fixture and registers cleanup so
// tests don't have to remember to close the httptest server or remove
// the temp token file.
//
// The default scenario has a single http_status failure, runs for ~1
// second with a short grace period, and targets the fixture's
// "bench-a" site. Tests that need a different scenario shape should
// replace Scenario wholesale.
func NewFixture(t *testing.T) *Fixture {
	t.Helper()

	tokenDir := t.TempDir()
	tokenPath := filepath.Join(tokenDir, "control-token")
	if err := os.WriteFile(tokenPath, []byte("test-token\n"), 0o600); err != nil {
		t.Fatalf("runtest: write token file: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Control plane uses 204 No Content for successful Activate /
	// Deactivate (see internal/control/client.go); 200 is treated as
	// an unexpected response and surfaces as an error.
	mux.HandleFunc("/activate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/deactivate", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	host, port := splitHostPort(t, srv.URL)

	fl := &fleet.Config{
		Control: fleet.ControlConfig{
			Timeout:       5 * time.Second,
			AuthTokenFile: tokenPath,
		},
		Adapters: map[string]fleet.AdapterConfig{},
		Targets: []fleet.Target{
			{
				ID:          "bench",
				Address:     host,
				ControlPort: port,
				Sites: []fleet.Site{
					{ID: "bench-a", Host: "bench-a.example", Paths: []string{"/"}},
				},
			},
		},
	}

	seed := int64(1)
	sc := &scenario.Scenario{
		ID:             "runtest-default",
		Version:        "1",
		Target:         "bench",
		Monitors:       []string{},
		CheckFrequency: 30 * time.Second,
		GracePeriod:    1 * time.Second,
		Duration:       1 * time.Second,
		Seed:           &seed,
		Failures: []scenario.Failure{
			{Type: "http_status", Rate: 1.0, StatusCode: 503},
		},
	}

	return &Fixture{
		t:            t,
		Scenario:     sc,
		Adapters:     nil,
		Fleet:        fl,
		Services:     &serviceconfig.Config{},
		Recorder:     &FakeRecorder{},
		TargetServer: srv,
		tokenFile:    tokenPath,
	}
}

// Run invokes runner.Run() with the fixture's wiring.
func (f *Fixture) Run(ctx context.Context) (string, error) {
	return runner.Run(ctx, f.Scenario, f.Fleet, f.Recorder, f.Adapters, f.Services)
}

func splitHostPort(t *testing.T, srvURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(srvURL)
	if err != nil {
		t.Fatalf("runtest: parse httptest URL %q: %v", srvURL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("runtest: parse httptest port from %q: %v", srvURL, err)
	}
	return u.Hostname(), port
}

// ─── FakeRecorder ───────────────────────────────────────────────────────────

// FakeRecorder satisfies the runner's unexported recorder interface
// structurally. Tests inspect the captured rows after Run.
type FakeRecorder struct {
	InsertRunErr    error
	CloseRunErr     error
	InsertEventErr  error
	FailEventOfType string
	InsertReportErr error

	Runs                []db.RunRecord
	GroundTruthEvents   []db.GroundTruthEvent
	MonitorReports      []db.MonitorReportRow
	CloseRunReason      string
	CloseRunCalls       int
	GroundTruthEventLog []string
}

func (f *FakeRecorder) InsertRun(_ context.Context, r db.RunRecord) error {
	f.Runs = append(f.Runs, r)
	return f.InsertRunErr
}

func (f *FakeRecorder) CloseRun(_ context.Context, _ string, _ time.Time, reason string) error {
	f.CloseRunReason = reason
	f.CloseRunCalls++
	return f.CloseRunErr
}

func (f *FakeRecorder) InsertGroundTruthEvent(_ context.Context, e db.GroundTruthEvent) error {
	f.GroundTruthEvents = append(f.GroundTruthEvents, e)
	f.GroundTruthEventLog = append(f.GroundTruthEventLog, e.EventType)
	if f.FailEventOfType != "" && e.EventType == f.FailEventOfType {
		return f.InsertEventErr
	}
	if f.FailEventOfType == "" && f.InsertEventErr != nil {
		return f.InsertEventErr
	}
	return nil
}

func (f *FakeRecorder) InsertMonitorReport(_ context.Context, r db.MonitorReportRow) error {
	f.MonitorReports = append(f.MonitorReports, r)
	return f.InsertReportErr
}

// ─── Test adapter ───────────────────────────────────────────────────────────

// SimpleAdapter is a minimal Adapter implementation for fixture tests.
type SimpleAdapter struct {
	ID            string
	Caps          adapter.Capabilities
	RetrieveValue adapter.RetrieveResult
}

func (a *SimpleAdapter) ServiceID() string                  { return a.ID }
func (a *SimpleAdapter) Capabilities() adapter.Capabilities { return a.Caps }
func (a *SimpleAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *SimpleAdapter) Provision(context.Context, adapter.Target, adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{ServiceID: a.ID, MonitorID: "fake-" + a.ID}, nil
}
func (a *SimpleAdapter) Retrieve(context.Context, adapter.MonitorHandle, adapter.RunWindow) (adapter.RetrieveResult, error) {
	return a.RetrieveValue, nil
}
func (a *SimpleAdapter) Deprovision(context.Context, adapter.MonitorHandle) error { return nil }
