package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

// recordingAdapter captures the ctx.Err() it sees on Deprovision so the
// regression test can assert the cleanup path uses a fresh, non-cancelled
// context — not the runner's outer context that may have been cancelled.
type recordingAdapter struct {
	id           string
	deprovCtxErr error
	deprovCalled bool
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

// TestScheduleFailureEvents_StaggeredOffsets — verifies the timeline
// for the canonical "DNS issue at t=0, HTTP error at t=30s" pattern.
// Each failure runs for `duration` from its individual activate.
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
	groundTruthEventsLogged []string // event types in call order
	monitorReportsLogged    int
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
