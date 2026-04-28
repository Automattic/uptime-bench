package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/runner"
	"github.com/Automattic/uptime-bench/internal/runner/runtest"
)

// smallCampaign builds a minimal Campaign that produces exactly one
// design × one replay so RunCampaign tests run in well under a second.
// Constructing the struct directly skips the parser; validation lives
// in campaign_test.go and isn't exercised here.
func smallCampaign(t *testing.T, pool []string, patterns []string) *campaign.Campaign {
	t.Helper()
	return &campaign.Campaign{
		ID:             "rc-test",
		Description:    "RunCampaign test",
		Duration:       300 * time.Millisecond,
		Seed:           1,
		CheckFrequency: 30 * time.Second,
		GracePeriod:    50 * time.Millisecond,
		Targets: campaign.Targets{
			Pool:     pool,
			Patterns: patterns,
		},
		DurationBuckets: map[string]campaign.DurationBucket{
			"brief": {Min: 50 * time.Millisecond, Max: 50 * time.Millisecond},
		},
		Sampling: campaign.Sampling{SamplesPerCellDefault: 1},
		FailureTypes: []campaign.FailureType{
			{Type: "http_status", StatusCodeChoices: []int{503}},
		},
	}
}

// TestRunCampaign_HappyPath drives a tiny single-replay campaign through
// RunCampaign and verifies the audit-trail row, the per-replay
// scenario_runs row stamped with campaign_id, and the planned_completion
// close reason.
func TestRunCampaign_HappyPath(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			Caps:          adapter.Capabilities{SupportsCooldownReset: true},
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench"}, []string{"single"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	campaignRunID, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{
		ConfigTOML:         "id = \"rc-test\"\n",
		AdapterVersions:    map[string]string{"svc-a": "abc123"},
		TargetFleetVersion: "deadbeef",
	})
	if err != nil {
		t.Fatalf("RunCampaign: %v", err)
	}
	if campaignRunID == "" {
		t.Fatal("RunCampaign returned empty campaign run ID")
	}

	if len(f.Recorder.CampaignRuns) != 1 {
		t.Fatalf("CampaignRuns = %d, want 1", len(f.Recorder.CampaignRuns))
	}
	cr := f.Recorder.CampaignRuns[0]
	if cr.ID != campaignRunID {
		t.Errorf("CampaignRuns[0].ID = %q, want %q", cr.ID, campaignRunID)
	}
	if cr.CampaignID != "rc-test" {
		t.Errorf("CampaignRuns[0].CampaignID = %q, want rc-test", cr.CampaignID)
	}
	if cr.MasterSeed != c.Seed {
		t.Errorf("CampaignRuns[0].MasterSeed = %d, want %d", cr.MasterSeed, c.Seed)
	}
	if !strings.Contains(cr.ConfigTOML, "rc-test") {
		t.Errorf("CampaignRuns[0].ConfigTOML did not echo passed-in body: %q", cr.ConfigTOML)
	}
	if cr.TargetFleetVersion != "deadbeef" {
		t.Errorf("CampaignRuns[0].TargetFleetVersion = %q", cr.TargetFleetVersion)
	}

	if f.Recorder.CloseCampaignRunCalls != 1 {
		t.Errorf("CloseCampaignRunCalls = %d, want 1", f.Recorder.CloseCampaignRunCalls)
	}
	if f.Recorder.CloseCampaignRunReason != "planned_completion" {
		t.Errorf("CloseCampaignRunReason = %q, want planned_completion", f.Recorder.CloseCampaignRunReason)
	}

	if len(f.Recorder.Runs) != 1 {
		t.Fatalf("Runs = %d, want 1 replay row", len(f.Recorder.Runs))
	}
	if f.Recorder.Runs[0].CampaignID != campaignRunID {
		t.Errorf("Runs[0].CampaignID = %q, want %q (campaign_id must be stamped on every scenario_runs row)",
			f.Recorder.Runs[0].CampaignID, campaignRunID)
	}
	params, ok := f.Recorder.Runs[0].Parameters.(map[string]any)
	if !ok {
		t.Fatalf("Runs[0].Parameters = %T, want map[string]any", f.Recorder.Runs[0].Parameters)
	}
	if params["campaign_design_id"] != "d-0000" || params["campaign_replay_index"] != 0 {
		t.Fatalf("campaign replay params = %#v, want design d-0000 replay 0", params)
	}
	if params["campaign_duration_bucket"] != "brief" || params["campaign_host_pattern"] != "single" {
		t.Fatalf("campaign cell params = %#v, want brief/single", params)
	}
	if params["campaign_failure_label"] != "http_status" {
		t.Fatalf("campaign_failure_label = %#v, want http_status", params["campaign_failure_label"])
	}
	if len(f.Recorder.MonitorReports) != 1 {
		t.Fatalf("MonitorReports = %+v, want one known/no-event audit row", f.Recorder.MonitorReports)
	}
	if got := f.Recorder.MonitorReports[0].RetrieveStatus; got != string(adapter.RetrieveKnown) {
		t.Fatalf("RetrieveStatus = %q, want known", got)
	}
	if got := f.Recorder.MonitorReports[0].EventType; got != "" {
		t.Fatalf("EventType = %q, want empty no-event row", got)
	}
}

// TestRunCampaign_GatesAdaptersWithoutCooldownReset verifies campaign mode
// enforces clean inter-run alert state. Single scenario runs may still use
// adapters without SupportsCooldownReset, but campaign replays record a
// capability_mismatch because repeated samples can otherwise be biased by
// vendor-side cooldown carry-over.
func TestRunCampaign_GatesAdaptersWithoutCooldownReset(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			Caps:          adapter.Capabilities{SupportsCooldownReset: false},
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench"}, []string{"single"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{}); err != nil {
		t.Fatalf("RunCampaign: %v", err)
	}

	if len(f.Recorder.Runs) != 1 {
		t.Fatalf("Runs = %d, want 1 replay row", len(f.Recorder.Runs))
	}
	if len(f.Recorder.MonitorReports) != 1 {
		t.Fatalf("MonitorReports = %+v, want one capability_mismatch row", f.Recorder.MonitorReports)
	}
	row := f.Recorder.MonitorReports[0]
	if row.ServiceID != "svc-a" {
		t.Errorf("ServiceID = %q, want svc-a", row.ServiceID)
	}
	if row.ReasonCode != adapter.ReasonCapabilityMismatch {
		t.Errorf("ReasonCode = %q, want %q", row.ReasonCode, adapter.ReasonCapabilityMismatch)
	}
	if !strings.Contains(row.RetrieveUnknownReason, "SupportsCooldownReset") {
		t.Errorf("RetrieveUnknownReason = %q, want SupportsCooldownReset detail", row.RetrieveUnknownReason)
	}
}

func TestRunCampaignRejectsScheduleOverBudget(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			Caps:          adapter.Capabilities{SupportsCooldownReset: true},
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench"}, []string{"single"})
	c.Sampling.SamplesPerCellDefault = 2
	c.Budget = map[string]campaign.ServiceBudget{
		"svc-a": {MaxRunsPerHour: 1},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{})
	if err == nil {
		t.Fatal("RunCampaign: expected budget error, got nil")
	}
	if !strings.Contains(err.Error(), "budget for svc-a exceeded") {
		t.Fatalf("RunCampaign error = %v, want svc-a budget detail", err)
	}
	if len(f.Recorder.CampaignRuns) != 0 {
		t.Fatalf("CampaignRuns = %d, want 0 (budget failure should happen before audit row insert)", len(f.Recorder.CampaignRuns))
	}
	if len(f.Recorder.Runs) != 0 {
		t.Fatalf("Runs = %d, want 0 (budget failure should happen before replay execution)", len(f.Recorder.Runs))
	}
}

func TestRunCampaignAllowsUnlimitedBudget(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			Caps:          adapter.Capabilities{SupportsCooldownReset: true},
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench"}, []string{"single"})
	c.Sampling.SamplesPerCellDefault = 2
	c.Budget = map[string]campaign.ServiceBudget{
		"svc-a": {MaxRunsPerHour: 0},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{}); err != nil {
		t.Fatalf("RunCampaign: %v", err)
	}
	if len(f.Recorder.CampaignRuns) != 1 {
		t.Fatalf("CampaignRuns = %d, want 1", len(f.Recorder.CampaignRuns))
	}
	if len(f.Recorder.Runs) != 2 {
		t.Fatalf("Runs = %d, want 2 replay rows", len(f.Recorder.Runs))
	}
}

// TestRunCampaign_NilCampaign — RunCampaign rejects a nil campaign
// before touching the database. No campaign_runs row should be written.
func TestRunCampaign_NilCampaign(t *testing.T) {
	f := runtest.NewFixture(t)

	ctx := context.Background()
	_, err := runner.RunCampaign(ctx, nil, 1, f.Fleet, f.Recorder, nil, f.Services, runner.RunCampaignOptions{})
	if err == nil {
		t.Fatal("RunCampaign(nil): expected error")
	}
	if len(f.Recorder.CampaignRuns) != 0 {
		t.Errorf("CampaignRuns = %d, want 0 (no insert before validation)", len(f.Recorder.CampaignRuns))
	}
}

// TestRunCampaign_ContextCancellation — when the context is cancelled
// before the first slot fires, RunCampaign returns ctx.Err() and closes
// the campaign row with reason="aborted". The InsertCampaignRun row
// must still be written so the abort is visible in the audit trail.
func TestRunCampaign_ContextCancellation(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench"}, []string{"single"})
	// Long enough that the first slot offset is > 0, so the context
	// cancellation path during the wait-loop is the one that fires.
	c.Duration = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{})
	if err == nil {
		t.Fatal("RunCampaign with cancelled ctx: expected error")
	}

	if len(f.Recorder.CampaignRuns) != 1 {
		t.Errorf("CampaignRuns = %d, want 1", len(f.Recorder.CampaignRuns))
	}
	if f.Recorder.CloseCampaignRunReason != "aborted" {
		t.Errorf("CloseCampaignRunReason = %q, want aborted", f.Recorder.CloseCampaignRunReason)
	}
}

// TestRunCampaign_RejectsMultiHostPatterns — campaign generator can produce
// multi-host designs from "two_random" / "all" patterns, but the scenario
// format and runner execution path are single-target today. Reject before
// writing an audit row so operators do not accidentally publish a zero-sample
// campaign.
func TestRunCampaign_RejectsMultiHostPatterns(t *testing.T) {
	f := runtest.NewFixture(t)
	f.Adapters = []adapter.Adapter{
		&runtest.SimpleAdapter{
			ID:            "svc-a",
			RetrieveValue: adapter.RetrieveResult{Status: adapter.RetrieveKnown},
		},
	}

	c := smallCampaign(t, []string{"bench", "bench-other"}, []string{"two_random"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := runner.RunCampaign(ctx, c, c.Seed, f.Fleet, f.Recorder, f.Adapters, f.Services, runner.RunCampaignOptions{})
	if err == nil {
		t.Fatal("RunCampaign: expected multi-host pattern error, got nil")
	}
	if !strings.Contains(err.Error(), "multi-host scenario support") {
		t.Fatalf("RunCampaign error = %v, want multi-host support detail", err)
	}
	if len(f.Recorder.CampaignRuns) != 0 {
		t.Fatalf("CampaignRuns = %d, want 0 (scope failure should happen before audit row insert)", len(f.Recorder.CampaignRuns))
	}
	if len(f.Recorder.Runs) != 0 {
		t.Errorf("Runs = %d, want 0", len(f.Recorder.Runs))
	}
}
