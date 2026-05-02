package runplan

import (
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

func TestScenarioRuntimeUsesLatestFailureEndPlusGrace(t *testing.T) {
	sc := &scenario.Scenario{
		Duration:    5 * time.Minute,
		GracePeriod: 2 * time.Minute,
		Failures: []scenario.Failure{
			{Type: "http_status"},
			{Type: "http_timeout", Offset: 4 * time.Minute, Duration: 3 * time.Minute},
		},
	}

	got := ScenarioRuntime(sc)
	want := 9 * time.Minute
	if got != want {
		t.Fatalf("ScenarioRuntime = %v, want %v", got, want)
	}
}

func TestEstimateCampaignReportsSerialAndScheduledRuntime(t *testing.T) {
	c := &campaign.Campaign{
		Duration:    time.Hour,
		GracePeriod: time.Minute,
	}
	plan := &campaign.Plan{
		Designs: []campaign.Design{
			{ID: "d1", Duration: 10 * time.Minute, Replays: 2},
			{ID: "d2", Duration: 20 * time.Minute, Replays: 1},
		},
		Schedule: []campaign.ReplaySlot{
			{DesignID: "d1", Offset: 0},
			{DesignID: "d2", Offset: 5 * time.Minute},
			{DesignID: "d1", Offset: 30 * time.Minute},
		},
	}

	got := EstimateCampaign(c, plan)
	if got.Replays != 3 || got.Designs != 2 {
		t.Fatalf("counts = designs %d replays %d, want 2/3", got.Designs, got.Replays)
	}
	if got.ScheduledSpan != 41*time.Minute {
		t.Fatalf("ScheduledSpan = %v, want 41m", got.ScheduledSpan)
	}
	if got.SerialRuntime != 43*time.Minute {
		t.Fatalf("SerialRuntime = %v, want 43m", got.SerialRuntime)
	}
	if got.MaxConcurrentSamples != 2 {
		t.Fatalf("MaxConcurrentSamples = %d, want 2", got.MaxConcurrentSamples)
	}
}
