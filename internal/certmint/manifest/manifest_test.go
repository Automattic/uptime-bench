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
