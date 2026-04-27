package certlibrary

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEntryCoversExactAndWildcardIdentifiers(t *testing.T) {
	entry := Entry{
		Identifiers: []string{
			"bench.example.com",
			"*.bench.example.com",
		},
	}

	for _, host := range []string{
		"bench.example.com",
		"BENCH.EXAMPLE.COM.",
		"site-a.bench.example.com",
		"site-a.bench.example.com:443",
	} {
		if !entry.Covers(host) {
			t.Fatalf("Covers(%q) = false, want true", host)
		}
	}

	for _, host := range []string{
		"deep.site-a.bench.example.com",
		"other.example.com",
		"",
	} {
		if entry.Covers(host) {
			t.Fatalf("Covers(%q) = true, want false", host)
		}
	}
}

func TestSelectExpiringChoosesClosestFutureCertificate(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	lib := Library{
		Version: ManifestVersion,
		Entries: []Entry{
			entry("six-day", now.Add(6*24*time.Hour), "*.bench.example.com"),
			entry("five-day", now.Add(5*24*time.Hour), "*.bench.example.com"),
			entry("expired", now.Add(-24*time.Hour), "*.bench.example.com"),
			entry("other-host", now.Add(5*24*time.Hour), "*.other.example.com"),
		},
	}

	got, err := lib.SelectExpiring("target.bench.example.com", now, 5*24*time.Hour+3*time.Hour)
	if err != nil {
		t.Fatalf("SelectExpiring: %v", err)
	}
	if got.ID != "five-day" {
		t.Fatalf("got %q, want five-day", got.ID)
	}
}

func TestSelectExpiredChoosesClosestPastCertificate(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	lib := Library{
		Version: ManifestVersion,
		Entries: []Entry{
			entry("fresh", now.Add(6*24*time.Hour), "*.bench.example.com"),
			entry("one-day-expired", now.Add(-24*time.Hour), "*.bench.example.com"),
			entry("three-day-expired", now.Add(-72*time.Hour), "*.bench.example.com"),
		},
	}

	got, err := lib.SelectExpired("target.bench.example.com", now, 26*time.Hour)
	if err != nil {
		t.Fatalf("SelectExpired: %v", err)
	}
	if got.ID != "one-day-expired" {
		t.Fatalf("got %q, want one-day-expired", got.ID)
	}
}

func TestSelectDefaultChoosesLongestCurrentlyValidCertificate(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	lib := Library{
		Version: ManifestVersion,
		Entries: []Entry{
			entryWithWindow("short-valid", now.Add(-24*time.Hour), now.Add(6*24*time.Hour), "*.bench.example.com"),
			entryWithWindow("long-valid", now.Add(-24*time.Hour), now.Add(90*24*time.Hour), "*.bench.example.com"),
			entryWithWindow("future", now.Add(24*time.Hour), now.Add(100*24*time.Hour), "*.bench.example.com"),
			entryWithWindow("expired", now.Add(-10*24*time.Hour), now.Add(-24*time.Hour), "*.bench.example.com"),
			entryWithWindow("other-host", now.Add(-24*time.Hour), now.Add(120*24*time.Hour), "*.other.example.com"),
		},
	}

	got, err := lib.SelectDefault("target.bench.example.com", now)
	if err != nil {
		t.Fatalf("SelectDefault: %v", err)
	}
	if got.ID != "long-valid" {
		t.Fatalf("got %q, want long-valid", got.ID)
	}
}

func TestSelectDefaultReturnsSentinelForNoMatchingCert(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	lib := Library{
		Version: ManifestVersion,
		Entries: []Entry{
			entryWithWindow("expired", now.Add(-10*24*time.Hour), now.Add(-24*time.Hour), "*.bench.example.com"),
		},
	}

	_, err := lib.SelectDefault("target.bench.example.com", now)
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("SelectDefault err = %v, want ErrNoCertificate", err)
	}
}

func TestLoadValidatesManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	data, err := json.Marshal(Library{
		Version: ManifestVersion,
		Entries: []Entry{
			entry("valid", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), "*.bench.example.com"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].ID != "valid" {
		t.Fatalf("Load = %+v", got)
	}
}

func TestValidateRejectsUnsupportedVersionAndMissingPaths(t *testing.T) {
	err := Library{
		Version: 99,
		Entries: []Entry{
			{ID: "bad", Domain: "bench.example.com", Profile: "shortlived", Identifiers: []string{"*.bench.example.com"}},
		},
	}.Validate()
	if err == nil {
		t.Fatal("Validate: expected error")
	}
	msg := err.Error()
	for _, want := range []string{
		"unsupported manifest version",
		"not_after is required",
		"paths.cert is required",
		"paths.fullchain is required",
		"paths.privkey is required",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Validate error %q missing %q", msg, want)
		}
	}
}

func entry(id string, notAfter time.Time, identifiers ...string) Entry {
	return entryWithWindow(id, time.Time{}, notAfter, identifiers...)
}

func entryWithWindow(id string, notBefore, notAfter time.Time, identifiers ...string) Entry {
	return Entry{
		ID:          id,
		Domain:      "bench.example.com",
		Profile:     "shortlived",
		Identifiers: identifiers,
		NotBefore:   notBefore,
		NotAfter:    notAfter,
		Paths: Paths{
			Cert:      "/certs/" + id + "/cert.pem",
			FullChain: "/certs/" + id + "/fullchain.pem",
			PrivKey:   "/certs/" + id + "/privkey.pem",
		},
	}
}
