package providerstate

import (
	"context"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

func TestScopeFromFleet(t *testing.T) {
	scope := ScopeFromFleet(&fleet.Config{Targets: []fleet.Target{
		{
			Sites: []fleet.Site{
				{Host: "bench-a.example", Paths: []string{"/", "health"}},
				{Host: "bench-b.example"},
			},
		},
	}})

	if !scope.MatchesURL("http://bench-a.example/health") {
		t.Fatal("scope should match configured host/path")
	}
	if !scope.MatchesURL("https://bench-b.example/") {
		t.Fatal("scope should include default root path for sites without paths")
	}
	if scope.MatchesURL("http://outside.example/") {
		t.Fatal("scope should not match unconfigured host")
	}
}

func TestRunMarksUnsupportedAdaptersSkipped(t *testing.T) {
	summaries := Run(context.Background(), []adapter.Adapter{unsupportedAdapter{id: "plain"}}, adapter.CleanupOptions{})
	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	if summaries[0].Supported {
		t.Fatal("unsupported adapter should be marked unsupported")
	}
	if summaries[0].Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", summaries[0].Skipped)
	}
	if summaries[0].ReasonCounts["adapter does not implement stale cleanup"] != 1 {
		t.Fatalf("ReasonCounts = %+v, want unsupported reason", summaries[0].ReasonCounts)
	}
}

func TestRunTalliesCleanupReasonsAndKinds(t *testing.T) {
	summaries := Run(context.Background(), []adapter.Adapter{cleanerAdapter{
		unsupportedAdapter: unsupportedAdapter{id: "cleaner"},
		actions: []adapter.CleanupAction{
			{
				Candidate: adapter.CleanupCandidate{
					ResourceID: "1",
					Kind:       "monitor",
					Reason:     "stale benchmark resource",
				},
				Action: adapter.CleanupActionWouldDelete,
			},
			{
				Candidate: adapter.CleanupCandidate{
					ResourceID: "2",
					Kind:       "monitor",
					Reason:     "stale benchmark resource",
				},
				Action: adapter.CleanupActionSkipped,
			},
		},
	}}, adapter.CleanupOptions{})

	if len(summaries) != 1 {
		t.Fatalf("len(summaries) = %d, want 1", len(summaries))
	}
	s := summaries[0]
	if s.Found != 2 || s.WouldDelete != 1 || s.Skipped != 1 {
		t.Fatalf("summary = %+v, want found=2 would_delete=1 skipped=1", s)
	}
	if s.ReasonCounts["stale benchmark resource"] != 2 || s.KindCounts["monitor"] != 2 {
		t.Fatalf("diagnostic counts = reasons %+v kinds %+v", s.ReasonCounts, s.KindCounts)
	}
}

type unsupportedAdapter struct {
	id string
}

func (a unsupportedAdapter) ServiceID() string { return a.id }

func (a unsupportedAdapter) Capabilities() adapter.Capabilities { return adapter.Capabilities{} }

func (a unsupportedAdapter) Provision(context.Context, adapter.Target, adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	return adapter.MonitorHandle{}, nil
}

func (a unsupportedAdapter) Retrieve(context.Context, adapter.MonitorHandle, adapter.RunWindow) (adapter.RetrieveResult, error) {
	return adapter.RetrieveResult{}, nil
}

func (a unsupportedAdapter) Deprovision(context.Context, adapter.MonitorHandle) error { return nil }

func (a unsupportedAdapter) Normalize(string) string { return adapter.UnrecognizedClassification }

type cleanerAdapter struct {
	unsupportedAdapter
	actions []adapter.CleanupAction
}

func (a cleanerAdapter) CleanupStale(context.Context, adapter.CleanupOptions) (adapter.CleanupResult, error) {
	return adapter.CleanupResult{Actions: a.actions}, nil
}
