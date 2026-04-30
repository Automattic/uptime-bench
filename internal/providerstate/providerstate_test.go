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
