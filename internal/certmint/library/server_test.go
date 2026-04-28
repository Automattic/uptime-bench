package library

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/certmint/manifest"
)

// writeManifest stages a manifest.json plus matching PEM files for an
// entry on disk, returning (manifestPath, libraryDir).
func writeManifest(t *testing.T, entries []manifest.Entry) (string, string) {
	t.Helper()
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.json")
	for _, e := range entries {
		for _, p := range []string{e.Paths.Cert, e.Paths.Chain, e.Paths.FullChain, e.Paths.PrivKey} {
			if p == "" {
				continue
			}
			if err := os.WriteFile(p, []byte("FAKE_PEM:"+p), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := manifest.Save(manifestPath, manifest.Manifest{Entries: entries}); err != nil {
		t.Fatal(err)
	}
	return manifestPath, dir
}

func newServer(t *testing.T, manifestPath string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterServerHandlers(mux, manifestPath)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestServer_ManifestRoundTripsThroughHTTP(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	full := filepath.Join(dir, "fullchain.pem")
	priv := filepath.Join(dir, "privkey.pem")
	entries := []manifest.Entry{{
		ID:                "abc123",
		Domain:            "harmonic.party",
		Profile:           "classic",
		Identifiers:       []string{"harmonic.party", "*.harmonic.party"},
		NotAfter:          time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC),
		FingerprintSHA256: "deadbeef",
		Paths:             manifest.Paths{Cert: cert, FullChain: full, PrivKey: priv},
	}}
	manifestPath, _ := writeManifest(t, entries)
	srv := newServer(t, manifestPath)

	resp, err := http.Get(srv.URL + "/library/manifest.json")
	if err != nil {
		t.Fatalf("GET manifest.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got manifest.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].ID != "abc123" {
		t.Fatalf("entries = %+v", got.Entries)
	}
}

func TestServer_EmptyManifestWhenFileMissing(t *testing.T) {
	// File doesn't exist — server returns the empty-manifest shape
	// rather than 404 so a target's first poll has a clean envelope.
	srv := newServer(t, filepath.Join(t.TempDir(), "missing.json"))
	resp, err := http.Get(srv.URL + "/library/manifest.json")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got manifest.Manifest
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %v, want empty", got.Entries)
	}
	if got.Version != manifest.Version {
		t.Fatalf("version = %d, want %d", got.Version, manifest.Version)
	}
}

func TestServer_FetchEntryPEMs(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	full := filepath.Join(dir, "fullchain.pem")
	chain := filepath.Join(dir, "chain.pem")
	priv := filepath.Join(dir, "privkey.pem")
	manifestPath, _ := writeManifest(t, []manifest.Entry{{
		ID:    "abc123",
		Paths: manifest.Paths{Cert: cert, Chain: chain, FullChain: full, PrivKey: priv},
	}})
	srv := newServer(t, manifestPath)

	for _, file := range []string{"cert.pem", "chain.pem", "fullchain.pem", "privkey.pem"} {
		resp, err := http.Get(srv.URL + "/library/abc123/" + file)
		if err != nil {
			t.Fatalf("GET %s: %v", file, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s status = %d", file, resp.StatusCode)
		}
		want := "FAKE_PEM:" + filepath.Join(dir, file)
		if string(body) != want {
			t.Fatalf("%s body = %q, want %q", file, string(body), want)
		}
	}
}

func TestServer_RejectsUnknownEntryAndUnknownFile(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	manifestPath, _ := writeManifest(t, []manifest.Entry{{
		ID:    "abc123",
		Paths: manifest.Paths{Cert: cert, FullChain: cert, PrivKey: cert},
	}})
	srv := newServer(t, manifestPath)

	cases := []struct {
		name string
		url  string
	}{
		{"unknown entry", "/library/never-existed/cert.pem"},
		{"unknown file extension", "/library/abc123/secret.txt"},
		{"path traversal in entry id", "/library/..%2Fsecret/cert.pem"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.url)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", resp.StatusCode)
			}
		})
	}
}

// TestServer_EmptyPathReturns404 — chain.pem is sometimes absent from
// an entry's Paths field (depends on certbot config). When that
// happens the request must 404, not stat a zero-length string.
func TestServer_EmptyPathReturns404(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "cert.pem")
	manifestPath, _ := writeManifest(t, []manifest.Entry{{
		ID: "abc123",
		Paths: manifest.Paths{
			Cert:      cert,
			Chain:     "", // intentionally empty
			FullChain: cert,
			PrivKey:   cert,
		},
	}})
	srv := newServer(t, manifestPath)
	resp, err := http.Get(srv.URL + "/library/abc123/chain.pem")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty Chain should 404, got %d", resp.StatusCode)
	}
}
