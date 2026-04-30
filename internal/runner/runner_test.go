package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

// recordingAdapter captures the ctx.Err() it sees on Deprovision so the
// regression test can assert the cleanup path uses a fresh, non-cancelled
// context — not the runner's outer context that may have been cancelled.
type recordingAdapter struct {
	id           string
	deprovCtxErr error
	deprovCalled bool
	deprovCalls  int
	failNext     error
}

func (a *recordingAdapter) ServiceID() string                  { return a.id }
func (a *recordingAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (a *recordingAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *recordingAdapter) Provision(ctx context.Context, _ adapter.Target, _ adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{}, nil
}
func (a *recordingAdapter) Retrieve(ctx context.Context, _ adapter.MonitorHandle, _ adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}
func (a *recordingAdapter) Deprovision(ctx context.Context, _ adapter.MonitorHandle) error {
	a.deprovCalled = true
	a.deprovCalls++
	a.deprovCtxErr = ctx.Err()
	return a.failNext
}

// TestDeprovisionAll_UsesFreshContext is the regression test for the bug
// where the runner deferred Deprovision calls using the run's outer ctx.
// When SIGINT cancelled that ctx mid-grace-period, every Deprovision call
// failed immediately with context.Canceled — so adapters with monitors that
// needed an HTTP DELETE to clean up leaked resources on every aborted run.
//
// The fix: deprovisionAll creates its own context (derived from
// context.Background()) so cleanup runs even when the outer ctx is gone.
func TestDeprovisionAll_UsesFreshContext(t *testing.T) {
	a1 := &recordingAdapter{id: "a1"}
	a2 := &recordingAdapter{id: "a2"}
	handles := []provisioned{
		{a: a1, handle: adapter.MonitorHandle{}},
		{a: a2, handle: adapter.MonitorHandle{}},
	}

	// Simulate the runner's outer ctx being cancelled before cleanup runs.
	outer, cancel := context.WithCancel(context.Background())
	cancel()
	if outer.Err() == nil {
		t.Fatal("test setup: expected outer ctx to be cancelled")
	}

	deprovisionAll(handles)

	for _, a := range []*recordingAdapter{a1, a2} {
		if !a.deprovCalled {
			t.Errorf("%s.Deprovision was not called", a.id)
		}
		if a.deprovCtxErr != nil {
			t.Errorf("%s.Deprovision saw ctx err %v — cleanup ctx is not fresh", a.id, a.deprovCtxErr)
		}
	}
}

// TestDeprovisionAll_ContinuesAfterError verifies one adapter failing does
// not skip the others. Each adapter's monitor must be torn down even if
// a sibling errors first.
func TestDeprovisionAll_ContinuesAfterError(t *testing.T) {
	prevAttempts := deprovisionAttempts
	deprovisionAttempts = 1
	defer func() { deprovisionAttempts = prevAttempts }()

	a1 := &recordingAdapter{id: "a1", failNext: errors.New("boom")}
	a2 := &recordingAdapter{id: "a2"}
	handles := []provisioned{
		{a: a1, handle: adapter.MonitorHandle{}},
		{a: a2, handle: adapter.MonitorHandle{}},
	}

	deprovisionAll(handles)

	if !a1.deprovCalled || !a2.deprovCalled {
		t.Fatalf("expected both Deprovisions called; got a1=%v a2=%v", a1.deprovCalled, a2.deprovCalled)
	}
}

// TestDeprovisionAll_RespectsTimeout ensures the cleanup context is bounded
// — a slow or hanging Deprovision call must not stall fleet shutdown forever.
func TestDeprovisionAll_RespectsTimeout(t *testing.T) {
	prev := deprovisionTimeout
	deprovisionTimeout = 100 * time.Millisecond
	defer func() { deprovisionTimeout = prev }()
	prevAttempts := deprovisionAttempts
	deprovisionAttempts = 1
	defer func() { deprovisionAttempts = prevAttempts }()

	slow := &slowDeprovisionAdapter{id: "slow"}
	handles := []provisioned{{a: slow, handle: adapter.MonitorHandle{}}}

	start := time.Now()
	deprovisionAll(handles)
	elapsed := time.Since(start)

	if elapsed > deprovisionTimeout+500*time.Millisecond {
		t.Fatalf("deprovisionAll took %v, expected ≤ %v", elapsed, deprovisionTimeout+500*time.Millisecond)
	}
	if !slow.sawDeadline {
		t.Fatal("slow adapter did not observe a context deadline; cleanup ctx is unbounded")
	}
}

func TestDeprovisionAll_RetriesFailures(t *testing.T) {
	prevAttempts := deprovisionAttempts
	deprovisionAttempts = 3
	defer func() { deprovisionAttempts = prevAttempts }()

	flaky := &flakyDeprovisionAdapter{id: "flaky", failCount: 1}
	handles := []provisioned{{a: flaky, handle: adapter.MonitorHandle{}}}

	if errs := deprovisionAll(handles); errs != 0 {
		t.Fatalf("deprovisionAll errors = %d, want 0 after retry success", errs)
	}
	if flaky.calls != 2 {
		t.Fatalf("Deprovision calls = %d, want 2", flaky.calls)
	}
}

type slowDeprovisionAdapter struct {
	id          string
	sawDeadline bool
}

func (a *slowDeprovisionAdapter) ServiceID() string                  { return a.id }
func (a *slowDeprovisionAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (a *slowDeprovisionAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *slowDeprovisionAdapter) Provision(ctx context.Context, _ adapter.Target, _ adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{}, nil
}
func (a *slowDeprovisionAdapter) Retrieve(ctx context.Context, _ adapter.MonitorHandle, _ adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}
func (a *slowDeprovisionAdapter) Deprovision(ctx context.Context, _ adapter.MonitorHandle) error {
	if _, ok := ctx.Deadline(); ok {
		a.sawDeadline = true
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * deprovisionTimeout):
		return nil
	}
}

type flakyDeprovisionAdapter struct {
	id        string
	failCount int
	calls     int
}

func (a *flakyDeprovisionAdapter) ServiceID() string                  { return a.id }
func (a *flakyDeprovisionAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }
func (a *flakyDeprovisionAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *flakyDeprovisionAdapter) Provision(ctx context.Context, _ adapter.Target, _ adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{}, nil
}
func (a *flakyDeprovisionAdapter) Retrieve(ctx context.Context, _ adapter.MonitorHandle, _ adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}
func (a *flakyDeprovisionAdapter) Deprovision(context.Context, adapter.MonitorHandle) error {
	a.calls++
	if a.calls <= a.failCount {
		return errors.New("temporary delete failure")
	}
	return nil
}

// TestScheduleFailureEvents_NoOffsets — every failure activates at start
// and deactivates at start+duration. Order within the activate / deactivate
// halves is preserved.
func TestScheduleFailureEvents_NoOffsets(t *testing.T) {
	start := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	duration := time.Minute
	failures := []scenario.Failure{
		{Type: "http_status"},
		{Type: "tcp_refused"},
	}
	events := scheduleFailureEvents(start, duration, failures)

	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}
	// First two are activates at start.
	for i := 0; i < 2; i++ {
		if !events[i].activate || !events[i].at.Equal(start) {
			t.Fatalf("events[%d] = (%v, activate=%v), want activate at start", i, events[i].at, events[i].activate)
		}
	}
	// Next two are deactivates at start+duration.
	for i := 2; i < 4; i++ {
		if events[i].activate || !events[i].at.Equal(start.Add(duration)) {
			t.Fatalf("events[%d] = (%v, activate=%v), want deactivate at start+duration", i, events[i].at, events[i].activate)
		}
	}
}

// TestScheduleFailureEvents_StaggeredOffsets verifies the timeline
// for the canonical "DNS issue at t=0, HTTP error at t=30s" pattern.
// Each failure runs for the scenario duration from its individual
// activate unless it declares a per-failure duration override.
func TestScheduleFailureEvents_StaggeredOffsets(t *testing.T) {
	start := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	duration := 100 * time.Second
	failures := []scenario.Failure{
		{Type: "dns_latency", Offset: 0},
		{Type: "http_status", Offset: 30 * time.Second},
	}
	events := scheduleFailureEvents(start, duration, failures)

	want := []struct {
		at       time.Time
		activate bool
		typ      string
	}{
		{start.Add(0 * time.Second), true, "dns_latency"},
		{start.Add(30 * time.Second), true, "http_status"},
		{start.Add(100 * time.Second), false, "dns_latency"},
		{start.Add(130 * time.Second), false, "http_status"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i, e := range events {
		if !e.at.Equal(want[i].at) || e.activate != want[i].activate || e.failure.Type != want[i].typ {
			t.Fatalf("events[%d] = (%v, activate=%v, %s), want (%v, %v, %s)",
				i, e.at, e.activate, e.failure.Type,
				want[i].at, want[i].activate, want[i].typ)
		}
	}
}

func TestScheduleFailureEvents_PerFailureDurationOverridesScenarioDuration(t *testing.T) {
	start := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	scenarioDuration := 100 * time.Second
	failures := []scenario.Failure{
		{Type: "http_status", Offset: 0, Duration: 20 * time.Second},
		{Type: "tcp_refused", Offset: 30 * time.Second, Duration: 15 * time.Second},
		{Type: "dns_latency", Offset: 60 * time.Second},
	}
	events := scheduleFailureEvents(start, scenarioDuration, failures)

	want := []struct {
		at       time.Time
		activate bool
		typ      string
	}{
		{start.Add(0 * time.Second), true, "http_status"},
		{start.Add(20 * time.Second), false, "http_status"},
		{start.Add(30 * time.Second), true, "tcp_refused"},
		{start.Add(45 * time.Second), false, "tcp_refused"},
		{start.Add(60 * time.Second), true, "dns_latency"},
		{start.Add(160 * time.Second), false, "dns_latency"},
	}
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d", len(events), len(want))
	}
	for i, e := range events {
		if !e.at.Equal(want[i].at) || e.activate != want[i].activate || e.failure.Type != want[i].typ {
			t.Fatalf("events[%d] = (%v, activate=%v, %s), want (%v, %v, %s)",
				i, e.at, e.activate, e.failure.Type,
				want[i].at, want[i].activate, want[i].typ)
		}
	}
	if got, want := latestFailureEndOffset(scenarioDuration, failures), 160*time.Second; got != want {
		t.Fatalf("latestFailureEndOffset = %v, want %v", got, want)
	}
}

// TestScheduleFailureEvents_OverlappingDeactivateAndActivate — when the
// deactivate of failure A and the activate of failure B fall on the same
// instant, the activate must come first so the registry never momentarily
// has zero failures active. Stable sort with activate-before-deactivate
// pinned in the comparator.
func TestScheduleFailureEvents_OverlappingDeactivateAndActivate(t *testing.T) {
	start := time.Date(2026, 4, 25, 0, 0, 0, 0, time.UTC)
	duration := 10 * time.Second
	failures := []scenario.Failure{
		{Type: "first", Offset: 0},
		{Type: "second", Offset: duration}, // activate of "second" == deactivate of "first"
	}
	events := scheduleFailureEvents(start, duration, failures)

	// Expected order:
	//   t=0:  activate first
	//   t=10: activate second   (must come before deactivate at the same instant)
	//   t=10: deactivate first
	//   t=20: deactivate second
	want := []struct {
		activate bool
		typ      string
	}{
		{true, "first"},
		{true, "second"},
		{false, "first"},
		{false, "second"},
	}
	for i, e := range events {
		if e.activate != want[i].activate || e.failure.Type != want[i].typ {
			t.Fatalf("events[%d] = (activate=%v, %s), want (%v, %s)",
				i, e.activate, e.failure.Type, want[i].activate, want[i].typ)
		}
	}
}

// TestScheduleFailureEvents_Empty — no failures, no events.
func TestScheduleFailureEvents_Empty(t *testing.T) {
	events := scheduleFailureEvents(time.Now(), time.Minute, nil)
	if len(events) != 0 {
		t.Fatalf("got %d events for empty input, want 0", len(events))
	}
}

// ─── logEvent + recorder seam ───────────────────────────────────────────────

// fakeRecorder is a recorder that records every call and can be configured
// to fail any of the four methods on demand. Lets tests verify both that
// the runner respects the database errors AND that it stops calling the
// database after one fatal failure.
type fakeRecorder struct {
	insertRunErr            error
	closeRunErr             error
	insertEventErr          error  // applies to all InsertGroundTruthEvent calls
	failEventOfType         string // if set, only events of this type fail
	insertReportErr         error
	groundTruthEventsLogged []string              // event types in call order
	monitorReportsLogged    int                   // count of InsertMonitorReport calls
	monitorReportRows       []db.MonitorReportRow // captured rows for assertions
	closeRunReason          string
}

func (f *fakeRecorder) InsertRun(ctx context.Context, r db.RunRecord) error {
	return f.insertRunErr
}
func (f *fakeRecorder) CloseRun(ctx context.Context, runID string, endedAt time.Time, reason string) error {
	f.closeRunReason = reason
	return f.closeRunErr
}
func (f *fakeRecorder) InsertGroundTruthEvent(ctx context.Context, e db.GroundTruthEvent) error {
	f.groundTruthEventsLogged = append(f.groundTruthEventsLogged, e.EventType)
	if f.failEventOfType != "" && e.EventType == f.failEventOfType {
		return f.insertEventErr
	}
	if f.failEventOfType == "" && f.insertEventErr != nil {
		return f.insertEventErr
	}
	return nil
}
func (f *fakeRecorder) InsertMonitorReport(ctx context.Context, r db.MonitorReportRow) error {
	f.monitorReportsLogged++
	f.monitorReportRows = append(f.monitorReportRows, r)
	return f.insertReportErr
}

// TestLogEvent_PropagatesDBError is the basic seam: the helper must surface
// the underlying error so callers can decide whether to abort. The error
// message must be tagged with "ground_truth_log_failure" because that's the
// resolution_reason callers set on it.
func TestLogEvent_PropagatesDBError(t *testing.T) {
	rec := &fakeRecorder{insertEventErr: errors.New("connection lost")}
	err := logEvent(context.Background(), rec, "run-1", "t", "failure_start", "http_status", nil)
	if err == nil {
		t.Fatal("logEvent returned nil despite DB error")
	}
	if !strings.Contains(err.Error(), "ground_truth_log_failure") {
		t.Errorf("error %q should be tagged with ground_truth_log_failure", err)
	}
	if !strings.Contains(err.Error(), "failure_start") {
		t.Errorf("error %q should mention the event type", err)
	}
}

// TestLogEvent_HappyPath — no DB error means no Go error; the row was
// recorded.
func TestLogEvent_HappyPath(t *testing.T) {
	rec := &fakeRecorder{}
	if err := logEvent(context.Background(), rec, "run-1", "t", "run_start", "", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.groundTruthEventsLogged) != 1 {
		t.Fatalf("got %d events logged, want 1", len(rec.groundTruthEventsLogged))
	}
	if rec.groundTruthEventsLogged[0] != "run_start" {
		t.Fatalf("logged %q, want run_start", rec.groundTruthEventsLogged[0])
	}
}

// TestLogMonitorReport_LogsErrorButContinues — monitor reports are
// observations, not ground truth. A DB failure here is logged but does
// not abort the run (the runner has no path to recover from a partial
// observation set anyway). This test pins the contract so a future
// refactor that escalates monitor-report errors to fatal is a deliberate
// choice, not an accident.
func TestLogMonitorReport_LogsErrorButContinues(t *testing.T) {
	rec := &fakeRecorder{insertReportErr: errors.New("connection lost")}
	a := &recordingAdapter{id: "svc"}
	logMonitorReport(context.Background(), rec, "run-1", a, adapter.RetrieveResult{
		Status: adapter.RetrieveUnknown,
		Reason: "rate-limited",
	})
	if rec.monitorReportsLogged != 1 {
		t.Fatalf("InsertMonitorReport called %d times, want 1", rec.monitorReportsLogged)
	}
	// No assertion on Go error: this function intentionally swallows.
}

func TestLogMonitorReport_WritesKnownNoEventRow(t *testing.T) {
	rec := &fakeRecorder{}
	a := &recordingAdapter{id: "svc"}
	logMonitorReport(context.Background(), rec, "run-1", a, adapter.RetrieveResult{
		Status: adapter.RetrieveKnown,
		Metadata: map[string]any{
			"cooldown_state": "suppressed",
		},
	})

	if rec.monitorReportsLogged != 1 {
		t.Fatalf("InsertMonitorReport called %d times, want 1", rec.monitorReportsLogged)
	}
	row := rec.monitorReportRows[0]
	if row.RetrieveStatus != string(adapter.RetrieveKnown) {
		t.Fatalf("RetrieveStatus = %q, want known", row.RetrieveStatus)
	}
	if row.EventType != "" {
		t.Fatalf("EventType = %q, want empty no-event audit row", row.EventType)
	}
	meta, ok := row.Metadata.(map[string]any)
	if !ok || meta["cooldown_state"] != "suppressed" {
		t.Fatalf("Metadata = %#v, want cooldown_state marker", row.Metadata)
	}
}

// TestEffectiveSeed_ExplicitSeedWins pins the CLAUDE.md invariant that
// scenarios with an explicit Seed record exactly that value on the run,
// not the wall-clock fallback. Reproducibility depends on this — a
// regression would silently break replay-from-recorded-seed.
func TestEffectiveSeed_ExplicitSeedWins(t *testing.T) {
	explicit := int64(12345)
	sc := &scenario.Scenario{Seed: &explicit}
	startedAt := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	if got := effectiveSeed(sc, startedAt); got != explicit {
		t.Errorf("effectiveSeed = %d, want %d (explicit scenario seed)", got, explicit)
	}
}

// TestEffectiveSeed_FallbackToStartedAt — when the scenario omits Seed,
// the run records startedAt's nanosecond clock. Recording the fallback
// is what lets an operator replay an unseeded run by copying the
// recorded value into the scenario file.
func TestEffectiveSeed_FallbackToStartedAt(t *testing.T) {
	sc := &scenario.Scenario{Seed: nil}
	startedAt := time.Date(2026, 4, 27, 12, 0, 0, 999, time.UTC)
	want := startedAt.UnixNano()
	if got := effectiveSeed(sc, startedAt); got != want {
		t.Errorf("effectiveSeed = %d, want %d (startedAt.UnixNano)", got, want)
	}
}

// TestEffectiveSeed_ExplicitZeroIsRespected pins a subtle case: if the
// scenario sets seed = 0 explicitly (Seed != nil pointing at zero), the
// run records 0, NOT the wall-clock fallback. Zero is a valid seed,
// distinguishable from "unspecified" by the pointer being non-nil.
func TestEffectiveSeed_ExplicitZeroIsRespected(t *testing.T) {
	zero := int64(0)
	sc := &scenario.Scenario{Seed: &zero}
	startedAt := time.Date(2026, 4, 27, 12, 0, 0, 999, time.UTC)
	if got := effectiveSeed(sc, startedAt); got != 0 {
		t.Errorf("effectiveSeed = %d, want 0 (explicit zero, not the wall-clock fallback)", got)
	}
}

// ─── provisionAdapters: capability gates ─────────────────────────────────────
//
// These tests pin the runner's capability-gating behaviour: when a
// scenario requires a feature an adapter doesn't support, the runner
// must record a `monitor_reports` row tagged with reason_code =
// "capability_mismatch" and skip that adapter's Provision call entirely.
// The wire shape was already tested via logMonitorReport's reason_code
// round-trip; these tests close the loop by exercising the gate logic
// itself.

// gateTestAdapter is a configurable Adapter used to drive the gate
// tests. Each test sets the capabilities the adapter claims and the
// optional Provision error, then inspects whether Provision was called
// and what config it received.
type gateTestAdapter struct {
	id            string
	caps          adapter.Capabilities
	provisionedAs *adapter.ProvisionConfig // captures call; nil if Provision wasn't called
	provisionErr  error                    // returned from Provision when non-nil
}

func (a *gateTestAdapter) ServiceID() string                  { return a.id }
func (a *gateTestAdapter) Capabilities() adapter.Capabilities { return a.caps }
func (a *gateTestAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *gateTestAdapter) Retrieve(context.Context, adapter.MonitorHandle, adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}
func (a *gateTestAdapter) Deprovision(context.Context, adapter.MonitorHandle) error {
	return nil
}
func (a *gateTestAdapter) Provision(_ context.Context, _ adapter.Target, c adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	cfg := c
	a.provisionedAs = &cfg
	if a.provisionErr != nil {
		return adapter.MonitorHandle{}, a.provisionErr
	}
	return adapter.MonitorHandle{ServiceID: a.id, MonitorID: "fake-" + a.id}, nil
}

// gateTestTarget returns a fleet.Target sufficient for provisionAdapters
// to construct a target URL. The adapter never connects to it (Provision
// is mocked), so the address can be anything that round-trips through
// monitorTargetURL.
func gateTestTarget() fleet.Target {
	return fleet.Target{
		ID:      "bench",
		Address: "192.0.2.1",
		Sites:   []fleet.Site{{ID: "bench-a", Host: "bench-a.example"}},
	}
}

func TestMonitorTargetURLUsesHTTPSForTLSScenarios(t *testing.T) {
	tlsTypes := []string{
		"tls_expired",
		"tls_expiring",
		"tls_invalid",
		"tls_handshake",
		"tls_deprecated",
	}
	for _, typ := range tlsTypes {
		t.Run(typ, func(t *testing.T) {
			sc := &scenario.Scenario{
				Target:   "bench",
				Failures: []scenario.Failure{{Type: typ}},
			}
			if got, want := monitorTargetURL(sc, gateTestTarget()), "https://bench-a.example/"; got != want {
				t.Fatalf("monitorTargetURL = %q, want %q", got, want)
			}
		})
	}
}

func TestMonitorTargetURLUsesHTTPForNonTLSScenarios(t *testing.T) {
	sc := &scenario.Scenario{
		Target:   "bench",
		Failures: []scenario.Failure{{Type: "http_status"}},
	}
	if got, want := monitorTargetURL(sc, gateTestTarget()), "http://bench-a.example/"; got != want {
		t.Fatalf("monitorTargetURL = %q, want %q", got, want)
	}
}

func TestMonitorTargetURLFallsBackToAddress(t *testing.T) {
	target := fleet.Target{ID: "bench", Address: "192.0.2.1"}
	sc := &scenario.Scenario{
		Target:   "bench",
		Failures: []scenario.Failure{{Type: "tls_expired"}},
	}
	if got, want := monitorTargetURL(sc, target), "https://192.0.2.1"; got != want {
		t.Fatalf("monitorTargetURL = %q, want %q", got, want)
	}
}

// TestProvisionAdapters_MinCheckFrequencyGate — adapter requires
// MinCheckFrequency = 5m; scenario asks for 30s. Adapter should not be
// provisioned; one capability_mismatch row should be written.
func TestProvisionAdapters_MinCheckFrequencyGate(t *testing.T) {
	a := &gateTestAdapter{
		id: "svc",
		caps: adapter.Capabilities{
			MinCheckFrequency: 5 * time.Minute,
		},
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{Target: "bench", CheckFrequency: 30 * time.Second}

	handles, provisionErr := provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), false)

	if provisionErr {
		t.Errorf("provisionErr = true, want false (gate skip is not an adapter error)")
	}
	if len(handles) != 0 {
		t.Errorf("handles = %d, want 0 (adapter was gated)", len(handles))
	}
	if a.provisionedAs != nil {
		t.Errorf("Provision was called despite gate; got config %+v", a.provisionedAs)
	}
	if len(rec.monitorReportRows) != 1 {
		t.Fatalf("expected 1 capability_mismatch row, got %d", len(rec.monitorReportRows))
	}
	row := rec.monitorReportRows[0]
	if row.ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("row.ReasonCode = %q, want %q", row.ReasonCode, adapter.ReasonCapabilityMismatch)
	}
	if !strings.Contains(row.RetrieveUnknownReason, "check_frequency") {
		t.Errorf("row.RetrieveUnknownReason = %q, should mention check_frequency", row.RetrieveUnknownReason)
	}
}

// TestProvisionAdapters_KeywordGate — adapter SupportsKeyword=false,
// scenario sets a keyword. Skip + capability_mismatch row.
func TestProvisionAdapters_KeywordGate(t *testing.T) {
	a := &gateTestAdapter{
		id:   "svc",
		caps: adapter.Capabilities{SupportsKeyword: false},
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{Target: "bench", Keyword: "uptime-bench-canary", KeywordCheck: "present"}

	_, _ = provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), false)

	if a.provisionedAs != nil {
		t.Errorf("Provision should be skipped when keyword required and SupportsKeyword=false")
	}
	if len(rec.monitorReportRows) != 1 || rec.monitorReportRows[0].ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("expected one capability_mismatch row, got %+v", rec.monitorReportRows)
	}
	if !strings.Contains(rec.monitorReportRows[0].RetrieveUnknownReason, "SupportsKeyword") {
		t.Errorf("Reason should mention SupportsKeyword, got %q", rec.monitorReportRows[0].RetrieveUnknownReason)
	}
}

// TestProvisionAdapters_InvertedKeywordGate — adapter SupportsKeyword=true
// but SupportsInvertedKeyword=false; scenario uses keyword_check=absent.
// Skip + capability_mismatch.
func TestProvisionAdapters_InvertedKeywordGate(t *testing.T) {
	a := &gateTestAdapter{
		id: "svc",
		caps: adapter.Capabilities{
			SupportsKeyword:         true,
			SupportsInvertedKeyword: false,
		},
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{Target: "bench", Keyword: "HACKED", KeywordCheck: adapter.KeywordCheckAbsent}

	_, _ = provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), false)

	if a.provisionedAs != nil {
		t.Error("Provision should be skipped when keyword_check=absent and SupportsInvertedKeyword=false")
	}
	if len(rec.monitorReportRows) != 1 || rec.monitorReportRows[0].ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("expected one capability_mismatch row, got %+v", rec.monitorReportRows)
	}
	if !strings.Contains(rec.monitorReportRows[0].RetrieveUnknownReason, "SupportsInvertedKeyword") {
		t.Errorf("Reason should mention SupportsInvertedKeyword, got %q", rec.monitorReportRows[0].RetrieveUnknownReason)
	}
}

// TestProvisionAdapters_MaintenanceWindowGate — adapter
// SupportsMaintenanceWindows=false; scenario has [maintenance]. Skip +
// capability_mismatch.
func TestProvisionAdapters_MaintenanceWindowGate(t *testing.T) {
	a := &gateTestAdapter{
		id:   "svc",
		caps: adapter.Capabilities{SupportsMaintenanceWindows: false},
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{
		Target:      "bench",
		Maintenance: &scenario.Maintenance{StartOffset: 0, Duration: 5 * time.Minute},
	}

	_, _ = provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), false)

	if a.provisionedAs != nil {
		t.Error("Provision should be skipped when [maintenance] requested and SupportsMaintenanceWindows=false")
	}
	if len(rec.monitorReportRows) != 1 || rec.monitorReportRows[0].ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("expected one capability_mismatch row, got %+v", rec.monitorReportRows)
	}
	if !strings.Contains(rec.monitorReportRows[0].RetrieveUnknownReason, "SupportsMaintenanceWindows") {
		t.Errorf("Reason should mention SupportsMaintenanceWindows, got %q", rec.monitorReportRows[0].RetrieveUnknownReason)
	}
}

// TestProvisionAdapters_CooldownResetGate — campaign mode requires
// clean alert state between repeated replays. When the caller asks for
// cooldown reset support and an adapter cannot provide it, the adapter
// is gated as a capability_mismatch rather than producing biased
// campaign data.
func TestProvisionAdapters_CooldownResetGate(t *testing.T) {
	a := &gateTestAdapter{
		id:   "svc",
		caps: adapter.Capabilities{SupportsCooldownReset: false},
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{Target: "bench", CheckFrequency: time.Minute}

	handles, provisionErr := provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), true)

	if provisionErr {
		t.Errorf("provisionErr = true, want false (cooldown gate skip is not an adapter error)")
	}
	if len(handles) != 0 {
		t.Errorf("handles = %d, want 0 (adapter was gated)", len(handles))
	}
	if a.provisionedAs != nil {
		t.Errorf("Provision was called despite cooldown gate; got config %+v", a.provisionedAs)
	}
	if len(rec.monitorReportRows) != 1 || rec.monitorReportRows[0].ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Fatalf("expected one capability_mismatch row, got %+v", rec.monitorReportRows)
	}
	if !strings.Contains(rec.monitorReportRows[0].RetrieveUnknownReason, "SupportsCooldownReset") {
		t.Errorf("Reason should mention SupportsCooldownReset, got %q", rec.monitorReportRows[0].RetrieveUnknownReason)
	}
}

// TestProvisionAdapters_HappyPath — adapter has every capability the
// scenario requires; Provision is called with a fully-populated
// ProvisionConfig and the handle is included in the return value.
func TestProvisionAdapters_HappyPath(t *testing.T) {
	a := &gateTestAdapter{
		id: "svc",
		caps: adapter.Capabilities{
			MinCheckFrequency:          time.Minute,
			SupportsKeyword:            true,
			SupportsInvertedKeyword:    true,
			SupportsMaintenanceWindows: true,
		},
	}
	rec := &fakeRecorder{}
	startedAt := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	sc := &scenario.Scenario{
		Target:         "bench",
		CheckFrequency: time.Minute,
		Keyword:        "uptime-bench-canary",
		KeywordCheck:   adapter.KeywordCheckPresent,
		Maintenance:    &scenario.Maintenance{StartOffset: 0, Duration: 5 * time.Minute},
	}

	handles, provisionErr := provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", startedAt, false)

	if provisionErr {
		t.Errorf("provisionErr = true, want false")
	}
	if len(handles) != 1 || handles[0].handle.MonitorID != "fake-svc" {
		t.Errorf("handles = %+v, want one entry from svc", handles)
	}
	if a.provisionedAs == nil {
		t.Fatal("Provision was not called despite all gates passing")
	}
	if a.provisionedAs.Keyword != "uptime-bench-canary" {
		t.Errorf("ProvisionConfig.Keyword = %q", a.provisionedAs.Keyword)
	}
	if a.provisionedAs.MaintenanceWindow == nil {
		t.Errorf("ProvisionConfig.MaintenanceWindow should be set when scenario.Maintenance != nil")
	}
	if len(rec.monitorReportRows) != 0 {
		t.Errorf("happy path should produce no monitor_reports rows yet (those come from Retrieve), got %d", len(rec.monitorReportRows))
	}
}

// TestProvisionAdapters_ProvisionErrorSetsFlag — adapter passes all
// gates but Provision returns a Go error. Should set provisionErr=true,
// produce no handle, and write no capability_mismatch row (the adapter
// was tried; it just failed mid-call).
func TestProvisionAdapters_ProvisionErrorSetsFlag(t *testing.T) {
	a := &gateTestAdapter{
		id:           "svc",
		caps:         adapter.Capabilities{MinCheckFrequency: time.Minute},
		provisionErr: errors.New("provider returned 500"),
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{Target: "bench", CheckFrequency: time.Minute}

	handles, provisionErr := provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{a}, rec, "run-1", time.Now(), false)

	if !provisionErr {
		t.Errorf("provisionErr = false, want true (Provision returned an error)")
	}
	if len(handles) != 0 {
		t.Errorf("handles = %+v, want none (provision failed)", handles)
	}
	if len(rec.monitorReportRows) != 0 {
		t.Errorf("no capability_mismatch row should be written for a Provision error; got %+v", rec.monitorReportRows)
	}
}

// TestProvisionAdapters_MixedAdapters — three adapters: one passes all
// gates and provisions, one fails the keyword gate, one passes gates
// but errors in Provision. Each is recorded correctly; the runner
// reports an adapter_error overall.
func TestProvisionAdapters_MixedAdapters(t *testing.T) {
	good := &gateTestAdapter{
		id: "good",
		caps: adapter.Capabilities{
			MinCheckFrequency: time.Minute,
			SupportsKeyword:   true,
		},
	}
	gated := &gateTestAdapter{
		id:   "gated",
		caps: adapter.Capabilities{MinCheckFrequency: time.Minute, SupportsKeyword: false},
	}
	failing := &gateTestAdapter{
		id:           "failing",
		caps:         adapter.Capabilities{MinCheckFrequency: time.Minute, SupportsKeyword: true},
		provisionErr: errors.New("nope"),
	}
	rec := &fakeRecorder{}
	sc := &scenario.Scenario{
		Target:         "bench",
		CheckFrequency: time.Minute,
		Keyword:        "uptime-bench-canary",
		KeywordCheck:   adapter.KeywordCheckPresent,
	}

	handles, provisionErr := provisionAdapters(context.Background(), sc, gateTestTarget(),
		[]adapter.Adapter{good, gated, failing}, rec, "run-1", time.Now(), false)

	if !provisionErr {
		t.Errorf("provisionErr should be true because 'failing' errored")
	}
	if len(handles) != 1 || handles[0].handle.ServiceID != "good" {
		t.Errorf("handles = %+v, want one entry from 'good'", handles)
	}
	// Exactly one capability_mismatch row, from 'gated'.
	if len(rec.monitorReportRows) != 1 || rec.monitorReportRows[0].ServiceID != "gated" {
		t.Errorf("expected one capability_mismatch row from 'gated', got %+v", rec.monitorReportRows)
	}
	// 'good' was provisioned, 'gated' was not, 'failing' was attempted (Provision called).
	if good.provisionedAs == nil {
		t.Error("'good' should have been provisioned")
	}
	if gated.provisionedAs != nil {
		t.Error("'gated' should not have been provisioned (failed gate)")
	}
	if failing.provisionedAs == nil {
		t.Error("'failing' should have been attempted (Provision call) even though it errored")
	}
}

// TestMaintenanceWindowFor_NilScenario covers the no-op cases.
func TestMaintenanceWindowFor_NilScenario(t *testing.T) {
	if w := maintenanceWindowFor(nil, time.Now()); w != nil {
		t.Errorf("nil scenario should yield nil window, got %+v", w)
	}
	sc := &scenario.Scenario{Maintenance: nil}
	if w := maintenanceWindowFor(sc, time.Now()); w != nil {
		t.Errorf("scenario without [maintenance] should yield nil window, got %+v", w)
	}
}

// TestMaintenanceWindowFor_AbsoluteTimestamps verifies that relative
// scenario offsets convert to absolute timestamps anchored at startedAt.
// This is the bit the runner relies on to call vendor maintenance APIs
// with concrete from/to values.
func TestMaintenanceWindowFor_AbsoluteTimestamps(t *testing.T) {
	startedAt := time.Date(2026, 4, 26, 18, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		offset    time.Duration
		dur       time.Duration
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "zero offset, window opens at startedAt",
			offset:    0,
			dur:       5 * time.Minute,
			wantStart: startedAt,
			wantEnd:   startedAt.Add(5 * time.Minute),
		},
		{
			name:      "positive offset shifts both ends",
			offset:    30 * time.Second,
			dur:       2 * time.Minute,
			wantStart: startedAt.Add(30 * time.Second),
			wantEnd:   startedAt.Add(150 * time.Second),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := &scenario.Scenario{
				Maintenance: &scenario.Maintenance{
					StartOffset: tc.offset,
					Duration:    tc.dur,
				},
			}
			w := maintenanceWindowFor(sc, startedAt)
			if w == nil {
				t.Fatal("got nil window, want non-nil")
			}
			if !w.Start.Equal(tc.wantStart) {
				t.Errorf("Start = %v, want %v", w.Start, tc.wantStart)
			}
			if !w.End.Equal(tc.wantEnd) {
				t.Errorf("End = %v, want %v", w.End, tc.wantEnd)
			}
		})
	}
}

// TestLogMonitorReport_PropagatesReasonCode pins the contract that
// RetrieveResult.ReasonCode reaches the database row. Capability gating
// depends on this — without it, support-matrix queries can't tell
// "the adapter wasn't asked" apart from "the adapter couldn't reach
// its API." See docs/events.md for the reporting rules.
func TestLogMonitorReport_PropagatesReasonCode(t *testing.T) {
	rec := &fakeRecorder{}
	a := &recordingAdapter{id: "svc"}
	logMonitorReport(context.Background(), rec, "run-1", a, adapter.RetrieveResult{
		Status:     adapter.RetrieveUnknown,
		Reason:     "scenario requires keyword monitoring; SupportsKeyword = false",
		ReasonCode: adapter.ReasonCapabilityMismatch,
	})
	if len(rec.monitorReportRows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rec.monitorReportRows))
	}
	row := rec.monitorReportRows[0]
	if row.ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("row.ReasonCode = %q, want %q", row.ReasonCode, adapter.ReasonCapabilityMismatch)
	}
	if row.RetrieveStatus != "unknown" {
		t.Errorf("row.RetrieveStatus = %q, want unknown", row.RetrieveStatus)
	}
	if !strings.Contains(row.RetrieveUnknownReason, "SupportsKeyword") {
		t.Errorf("row.RetrieveUnknownReason = %q, should retain free-form detail", row.RetrieveUnknownReason)
	}
}
