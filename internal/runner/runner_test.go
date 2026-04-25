package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
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
