package runtest_test

// Tests in this file pin the CLAUDE.md invariant:
//
//	Every scenario run records a resolution_reason on close — no run
//	ends without one.
//
// "Every run" means every code path through runner.Run() must either
// return *before* InsertRun is called (no scenario_runs row created;
// nothing to record) or *after* InsertRun is called (deferred
// CloseRun fires with a meaningful reason). This file walks each exit
// path and asserts which side of the invariant it falls on.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/runner/runtest"
)

// TestRun_HappyPath_RecordsPlannedCompletion is the baseline. A run
// that proceeds to its natural end records resolution_reason =
// "planned_completion".
func TestRun_HappyPath_RecordsPlannedCompletion(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Scenario.Monitors = []string{"test-svc"}
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:   "test-svc",
			Caps: adapter.Capabilities{MinCheckFrequency: 30 * time.Second},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := f.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.Recorder.CloseRunCalls != 1 {
		t.Errorf("CloseRun called %d times, want 1", f.Recorder.CloseRunCalls)
	}
	if f.Recorder.CloseRunReason != "planned_completion" {
		t.Errorf("CloseRunReason = %q, want planned_completion", f.Recorder.CloseRunReason)
	}
	if len(f.Recorder.Runs) != 1 {
		t.Errorf("InsertRun called %d times, want 1", len(f.Recorder.Runs))
	}
}

// TestRun_ResolveTargetFailure_NoRowCreated — when the scenario
// references a target that doesn't exist in the fleet, Run() returns
// before InsertRun. No scenario_runs row is created, so the
// resolution_reason invariant doesn't apply.
func TestRun_ResolveTargetFailure_NoRowCreated(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Scenario.Target = "nonexistent-target"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.Run(ctx)
	if err == nil {
		t.Fatal("expected error from resolveTarget, got nil")
	}
	if len(f.Recorder.Runs) != 0 {
		t.Errorf("Runs = %d, want 0 (early exit before InsertRun)", len(f.Recorder.Runs))
	}
	if f.Recorder.CloseRunCalls != 0 {
		t.Errorf("CloseRun called %d times, want 0", f.Recorder.CloseRunCalls)
	}
}

// TestRun_ReadFleetTokenFailure_NoRowCreated — same shape as
// resolveTarget: pre-InsertRun failure, no row, no CloseRun.
func TestRun_ReadFleetTokenFailure_NoRowCreated(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Fleet.Control.AuthTokenFile = "/nonexistent/control-token"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.Run(ctx)
	if err == nil {
		t.Fatal("expected error from readFleetToken, got nil")
	}
	if len(f.Recorder.Runs) != 0 {
		t.Errorf("Runs = %d, want 0", len(f.Recorder.Runs))
	}
	if f.Recorder.CloseRunCalls != 0 {
		t.Errorf("CloseRun called %d times, want 0", f.Recorder.CloseRunCalls)
	}
}

// TestRun_InsertRunFailure_NoCloseRun — InsertRun's failure happens
// just before the deferred CloseRun is set up, so CloseRun shouldn't
// fire. The runner attempted the InsertRun call (the recorder
// captured the row argument before returning the error).
func TestRun_InsertRunFailure_NoCloseRun(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Recorder.InsertRunErr = errors.New("simulated InsertRun failure")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.Run(ctx)
	if err == nil {
		t.Fatal("expected error from InsertRun, got nil")
	}
	if len(f.Recorder.Runs) != 1 {
		t.Errorf("Runs = %d, want 1 (InsertRun was attempted)", len(f.Recorder.Runs))
	}
	if f.Recorder.CloseRunCalls != 0 {
		t.Errorf("CloseRun called %d times, want 0 (defer not set up yet)", f.Recorder.CloseRunCalls)
	}
}

// TestRun_RunStartEventFailure_RecordsGroundTruthLogFailure — the
// first ground-truth event (run_start) fails to insert. By that point
// the run row exists and the defer is set up, so CloseRun fires with
// resolution_reason = "ground_truth_log_failure". This is the
// canonical "failure mid-run" path.
func TestRun_RunStartEventFailure_RecordsGroundTruthLogFailure(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Recorder.FailEventOfType = "run_start"
	f.Recorder.InsertEventErr = errors.New("simulated event log failure")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := f.Run(ctx)
	if err == nil {
		t.Fatal("expected error from logEvent(run_start), got nil")
	}
	if len(f.Recorder.Runs) != 1 {
		t.Errorf("Runs = %d, want 1", len(f.Recorder.Runs))
	}
	if f.Recorder.CloseRunCalls != 1 {
		t.Fatalf("CloseRun called %d times, want 1", f.Recorder.CloseRunCalls)
	}
	if f.Recorder.CloseRunReason != "ground_truth_log_failure" {
		t.Errorf("CloseRunReason = %q, want ground_truth_log_failure", f.Recorder.CloseRunReason)
	}
}

// TestRun_AdapterProvisionFailure_RecordsAdapterError — adapter's
// Provision returns a Go error. The runner doesn't fail the whole run
// (other adapters might still work) but resolution_reason is set to
// "adapter_error" so the operator knows downstream metrics for that
// service are missing.
func TestRun_AdapterProvisionFailure_RecordsAdapterError(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Scenario.Monitors = []string{"failing-svc"}
	f.Adapters = []adapter.Adapter{
		&failingProvisionAdapter{
			id:   "failing-svc",
			caps: adapter.Capabilities{MinCheckFrequency: 30 * time.Second},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := f.Run(ctx); err != nil {
		t.Fatalf("Run should complete despite adapter Provision error; got %v", err)
	}
	if f.Recorder.CloseRunCalls != 1 {
		t.Errorf("CloseRun called %d times, want 1", f.Recorder.CloseRunCalls)
	}
	if f.Recorder.CloseRunReason != "adapter_error" {
		t.Errorf("CloseRunReason = %q, want adapter_error", f.Recorder.CloseRunReason)
	}
}

// TestRun_NonEmptyResolutionReason_AlwaysTrue — meta-check across
// every Run path that reaches CloseRun. The universal claim: when
// CloseRun was called, the recorded reason is non-empty. If a future
// regression introduces a code path that calls CloseRun with "", this
// catches it.
func TestRun_NonEmptyResolutionReason_AlwaysTrue(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *runtest.Fixture)
	}{
		{
			name: "happy path",
			setup: func(f *runtest.Fixture) {
				f.Scenario.Monitors = []string{"svc"}
				f.Adapters = []adapter.Adapter{
					&runtest.SimpleAdapter{ID: "svc", Caps: adapter.Capabilities{MinCheckFrequency: 30 * time.Second}},
				}
			},
		},
		{
			name: "run_start event failure",
			setup: func(f *runtest.Fixture) {
				f.Recorder.FailEventOfType = "run_start"
				f.Recorder.InsertEventErr = errors.New("x")
			},
		},
		{
			name: "adapter Provision failure",
			setup: func(f *runtest.Fixture) {
				f.Scenario.Monitors = []string{"svc"}
				f.Adapters = []adapter.Adapter{
					&failingProvisionAdapter{id: "svc", caps: adapter.Capabilities{MinCheckFrequency: 30 * time.Second}},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := runtest.NewFixture(t)
			tc.setup(f)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = f.Run(ctx)

			if f.Recorder.CloseRunCalls > 0 && f.Recorder.CloseRunReason == "" {
				t.Errorf("CloseRun was called with an empty resolution_reason — invariant broken")
			}
		})
	}
}

func TestRun_AllAdaptersCapabilityMismatchedSkipsFailureInjection(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Scenario.Keyword = "HACKED"
	f.Scenario.KeywordCheck = adapter.KeywordCheckAbsent
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID: "jetmon-v2",
			Caps: adapter.Capabilities{
				MinCheckFrequency:       30 * time.Second,
				SupportsKeyword:         true,
				SupportsInvertedKeyword: false,
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, eventType := range f.Recorder.GroundTruthEventLog {
		if eventType == "failure_start" || eventType == "failure_end" {
			t.Fatalf("ground-truth events = %v, want no failure injection when every adapter is capability-gated", f.Recorder.GroundTruthEventLog)
		}
	}
	if len(f.Recorder.MonitorReports) != 1 {
		t.Fatalf("MonitorReports = %d, want 1 capability_mismatch row", len(f.Recorder.MonitorReports))
	}
	row := f.Recorder.MonitorReports[0]
	if row.ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Fatalf("ReasonCode = %q, want %q", row.ReasonCode, adapter.ReasonCapabilityMismatch)
	}
	if f.Recorder.CloseRunReason != "planned_completion" {
		t.Fatalf("CloseRunReason = %q, want planned_completion", f.Recorder.CloseRunReason)
	}
}

// failingProvisionAdapter satisfies adapter.Adapter but fails its
// Provision call, exercising the resolution_reason="adapter_error"
// path through Run().
type failingProvisionAdapter struct {
	id   string
	caps adapter.Capabilities
}

func (a *failingProvisionAdapter) ServiceID() string                  { return a.id }
func (a *failingProvisionAdapter) Capabilities() adapter.Capabilities { return a.caps }
func (a *failingProvisionAdapter) Normalize(string) string            { return adapter.UnrecognizedClassification }
func (a *failingProvisionAdapter) Provision(context.Context, adapter.Target, adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{}, errors.New("vendor returned 500")
}
func (a *failingProvisionAdapter) Retrieve(context.Context, adapter.MonitorHandle, adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}
func (a *failingProvisionAdapter) Deprovision(context.Context, adapter.MonitorHandle) error {
	return nil
}
