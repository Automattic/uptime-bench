package manifest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileReturnsEmptyVersionedManifest(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "manifest.json"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != Version {
		t.Fatalf("Version = %d, want %d", got.Version, Version)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("Entries = %#v, want empty", got.Entries)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "manifest.json")
	want := Manifest{
		Entries: []Entry{
			{
				ID:                "bench.example.com_shortlived_20260427_slot00_abcdef1234567890",
				Environment:       "production",
				Domain:            "bench.example.com",
				Profile:           "shortlived",
				RequiredProfile:   "shortlived",
				SlotDate:          "20260427",
				Slot:              0,
				Identifiers:       []string{"bench.example.com", "*.bench.example.com"},
				CertName:          "ub-certmint-bench-example-com-shortlived-20260427-00",
				IssuedAt:          time.Date(2026, 4, 27, 1, 2, 3, 0, time.UTC),
				NotBefore:         time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC),
				NotAfter:          time.Date(2026, 5, 3, 16, 0, 0, 0, time.UTC),
				FingerprintSHA256: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
				Paths: Paths{
					Cert:      "/library/cert.pem",
					Chain:     "/library/chain.pem",
					FullChain: "/library/fullchain.pem",
					PrivKey:   "/library/privkey.pem",
				},
			},
		},
	}

	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", gotMode)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Version != Version {
		t.Fatalf("Version = %d, want %d", got.Version, Version)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(got.Entries))
	}
	if got.Entries[0].ID != want.Entries[0].ID || got.Entries[0].FingerprintSHA256 != want.Entries[0].FingerprintSHA256 {
		t.Fatalf("loaded entry = %+v, want %+v", got.Entries[0], want.Entries[0])
	}
	if !got.Entries[0].NotAfter.Equal(want.Entries[0].NotAfter) {
		t.Fatalf("NotAfter = %s, want %s", got.Entries[0].NotAfter, want.Entries[0].NotAfter)
	}
}

func TestHasSlotAndAppend(t *testing.T) {
	m := Manifest{}
	first := Entry{
		ID:       "first",
		Domain:   "bench.example.com",
		Profile:  "shortlived",
		SlotDate: "20260427",
		Slot:     1,
		CertName: "old",
	}
	replacement := first
	replacement.CertName = "new"

	if m.HasSlot(first.Domain, first.Profile, first.SlotDate, first.Slot) {
		t.Fatal("HasSlot() = true before append")
	}
	m.Append(first)
	if !m.HasSlot(first.Domain, first.Profile, first.SlotDate, first.Slot) {
		t.Fatal("HasSlot() = false after append")
	}
	m.Append(replacement)
	if len(m.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want replacement without duplicate", len(m.Entries))
	}
	if m.Entries[0].CertName != "new" {
		t.Fatalf("CertName = %q, want replacement", m.Entries[0].CertName)
	}
}

func TestTrim_KeepsNonExpiredAndRecentlyExpired(t *testing.T) {
	now := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	m := Manifest{Entries: []Entry{
		// fresh cert, not expired
		{ID: "fresh", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(60 * 24 * time.Hour)},
		// expired 5 days ago — within retention
		{ID: "recent-expired", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-5 * 24 * time.Hour)},
		// expired 25 days ago — within retention (just barely)
		{ID: "edge", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-25 * 24 * time.Hour)},
	}}

	out, removed := Trim(m, now, 30*24*time.Hour)
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want empty (nothing past retention)", removed)
	}
	if len(out.Entries) != 3 {
		t.Fatalf("Entries = %d, want 3", len(out.Entries))
	}
}

func TestTrim_DropsVeryOldExceptOldestPerGroup(t *testing.T) {
	now := time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC)
	m := Manifest{Entries: []Entry{
		// classic for ex.com — three very-old entries; oldest must survive.
		{ID: "ex-classic-200d", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-200 * 24 * time.Hour)}, // oldest
		{ID: "ex-classic-100d", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-100 * 24 * time.Hour)},
		{ID: "ex-classic-50d", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-50 * 24 * time.Hour)},
		// shortlived for ex.com — one very-old; survives by virtue of being the only one.
		{ID: "ex-short-90d", Domain: "ex.com", Profile: "shortlived", NotAfter: now.Add(-90 * 24 * time.Hour)},
		// classic for other.com — separate group, also keeps its oldest.
		{ID: "other-classic-365d", Domain: "other.com", Profile: "classic", NotAfter: now.Add(-365 * 24 * time.Hour)},
		{ID: "other-classic-40d", Domain: "other.com", Profile: "classic", NotAfter: now.Add(-40 * 24 * time.Hour)},
		// recently expired classic for ex.com — within retention, kept.
		{ID: "ex-classic-10d", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-10 * 24 * time.Hour)},
	}}

	out, removed := Trim(m, now, 30*24*time.Hour)

	keptIDs := map[string]bool{}
	for _, e := range out.Entries {
		keptIDs[e.ID] = true
	}
	for _, want := range []string{"ex-classic-200d", "ex-short-90d", "other-classic-365d", "ex-classic-10d"} {
		if !keptIDs[want] {
			t.Errorf("expected to keep %q, kept %v", want, keptIDs)
		}
	}
	removedIDs := map[string]bool{}
	for _, e := range removed {
		removedIDs[e.ID] = true
	}
	for _, want := range []string{"ex-classic-100d", "ex-classic-50d", "other-classic-40d"} {
		if !removedIDs[want] {
			t.Errorf("expected to remove %q, removed %v", want, removedIDs)
		}
	}
}

func TestTrim_RetentionZeroIsNoOp(t *testing.T) {
	now := time.Now()
	m := Manifest{Entries: []Entry{
		{ID: "x", Domain: "ex.com", Profile: "classic", NotAfter: now.Add(-5 * 24 * time.Hour)},
	}}
	out, removed := Trim(m, now, 0)
	if len(out.Entries) != 1 || len(removed) != 0 {
		t.Fatalf("retention=0 should be no-op; out=%v removed=%v", out.Entries, removed)
	}
}

func TestTrim_EmptyManifestIsNoOp(t *testing.T) {
	out, removed := Trim(Manifest{}, time.Now(), 30*24*time.Hour)
	if len(out.Entries) != 0 || len(removed) != 0 {
		t.Fatalf("empty input should produce empty output; out=%v removed=%v", out.Entries, removed)
	}
}
