package planner

import (
	"testing"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/manifest"
)

func TestDueCatchesUpSlotsAndSkipsManifestEntries(t *testing.T) {
	cfg := config.Config{
		Domains: []config.DomainConfig{
			{
				Name:              "bench.example.com",
				Identifiers:       []string{"bench.example.com", "*.bench.example.com"},
				UniqueSANTemplate: "cert-{date}-{slot}-{profile}.bench.example.com",
				Profiles: []config.ProfileConfig{
					{Name: "classic", PerDay: 1},
					{Name: "shortlived", PreferredProfile: "shortlived", PerDay: 2},
				},
			},
		},
	}
	current := manifest.Manifest{
		Entries: []manifest.Entry{
			{Domain: "bench.example.com", Profile: "shortlived", SlotDate: "20260427", Slot: 0},
		},
	}
	now := time.Date(2026, 4, 27, 13, 0, 0, 0, time.UTC)

	got := Due(cfg, current, now)
	if len(got) != 2 {
		t.Fatalf("got %d orders, want 2: %+v", len(got), got)
	}
	if got[0].ProfileName != "classic" || got[0].Slot != 0 {
		t.Fatalf("first order = %+v, want classic slot 0", got[0])
	}
	if got[1].ProfileName != "shortlived" || got[1].Slot != 1 {
		t.Fatalf("second order = %+v, want shortlived slot 1", got[1])
	}
	if got[1].Identifiers[2] != "cert-20260427-01-shortlived.bench.example.com" {
		t.Fatalf("unique SAN = %q", got[1].Identifiers[2])
	}
}
