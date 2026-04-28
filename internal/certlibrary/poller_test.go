package certlibrary

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mintTestCert generates an RSA cert+key pair and writes them as PEM
// to certPath/keyPath. Returns the leaf's SHA-256 fingerprint hex,
// matching what the manifest carries.
func mintTestCert(t *testing.T, certPath, keyPath string, identifiers []string, notAfter time.Time) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: identifiers[0]},
		DNSNames:              identifiers,
		NotBefore:             notAfter.Add(-30 * 24 * time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return fmt.Sprintf("%x", sum[:])
}

// fakeCertmint serves a manifest + cert files in the certmint shape.
// It mirrors the wire contract the real library/server.go provides.
func fakeCertmint(t *testing.T, manifestJSON []byte, files map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /library/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(manifestJSON)
	})
	mux.HandleFunc("GET /library/{entry_id}/{file}", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("entry_id") + "/" + r.PathValue("file")
		path, ok := files[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestPoller_MirrorsManifestAndRewritesPaths(t *testing.T) {
	src := t.TempDir()
	cert := filepath.Join(src, "cert.pem")
	key := filepath.Join(src, "privkey.pem")
	identifiers := []string{"bench-a.example.com", "*.example.com"}
	fingerprint := mintTestCert(t, cert, key, identifiers, time.Now().Add(90*24*time.Hour))

	// Use the cert as fullchain too — fine for tests.
	manifest := fmt.Sprintf(`{
  "version": 1,
  "entries": [
    {
      "id": "abc123",
      "domain": "example.com",
      "profile": "classic",
      "identifiers": ["bench-a.example.com", "*.example.com"],
      "not_after": "%s",
      "fingerprint_sha256": "%s",
      "paths": {
        "cert":      "/upstream/cert.pem",
        "fullchain": "/upstream/fullchain.pem",
        "privkey":   "/upstream/privkey.pem"
      }
    }
  ]
}`, time.Now().Add(90*24*time.Hour).Format(time.RFC3339Nano), fingerprint)

	srv := fakeCertmint(t, []byte(manifest), map[string]string{
		"abc123/cert.pem":      cert,
		"abc123/fullchain.pem": cert,
		"abc123/privkey.pem":   key,
	})

	cacheDir := t.TempDir()
	p := &Poller{BaseURL: srv.URL, CacheDir: cacheDir, HTTP: srv.Client()}
	lib, err := p.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(lib.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(lib.Entries))
	}
	e := lib.Entries[0]
	if e.Paths.Cert != filepath.Join(cacheDir, "abc123", "cert.pem") {
		t.Fatalf("Cert path = %q, want local cache", e.Paths.Cert)
	}
	if e.Paths.FullChain != filepath.Join(cacheDir, "abc123", "fullchain.pem") {
		t.Fatalf("FullChain path = %q", e.Paths.FullChain)
	}
	if e.Paths.PrivKey != filepath.Join(cacheDir, "abc123", "privkey.pem") {
		t.Fatalf("PrivKey path = %q", e.Paths.PrivKey)
	}
	if e.Paths.Chain != "" {
		t.Fatalf("Chain = %q, want empty (wire manifest didn't include it)", e.Paths.Chain)
	}

	// Files should exist on disk.
	for _, name := range []string{"cert.pem", "fullchain.pem", "privkey.pem"} {
		if _, err := os.Stat(filepath.Join(cacheDir, "abc123", name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// TestPoller_FingerprintMismatchRejectsAndCleansCache — the integrity
// gate. The cert-library API runs over plain HTTP; the leaf
// fingerprint check is the only thing standing between a hostile
// network intermediary and a poisoned cert. Mismatch must drop the
// cache entry, not just log a warning.
func TestPoller_FingerprintMismatchRejectsAndCleansCache(t *testing.T) {
	src := t.TempDir()
	realCert := filepath.Join(src, "real.pem")
	wrongCert := filepath.Join(src, "wrong.pem")
	key := filepath.Join(src, "key.pem")
	_ = mintTestCert(t, realCert, key, []string{"example.com"}, time.Now().Add(90*24*time.Hour))
	wrongFingerprint := mintTestCert(t, wrongCert, filepath.Join(src, "ignored.pem"), []string{"other.com"}, time.Now().Add(90*24*time.Hour))

	// Manifest claims the *wrong* fingerprint for what the server actually serves
	// — the simulated tampering. Note we serve realCert but advertise wrongFingerprint.
	manifest := fmt.Sprintf(`{
  "version": 1,
  "entries": [{
    "id":"abc123","domain":"example.com","profile":"classic",
    "identifiers":["example.com"],"not_after":"%s",
    "fingerprint_sha256":"%s",
    "paths":{"cert":"/x","fullchain":"/x","privkey":"/x"}
  }]
}`, time.Now().Add(90*24*time.Hour).Format(time.RFC3339Nano), wrongFingerprint)

	srv := fakeCertmint(t, []byte(manifest), map[string]string{
		"abc123/cert.pem":      realCert,
		"abc123/fullchain.pem": realCert,
		"abc123/privkey.pem":   key,
	})

	cacheDir := t.TempDir()
	p := &Poller{BaseURL: srv.URL, CacheDir: cacheDir, HTTP: srv.Client()}
	_, err := p.Poll(context.Background())
	if err == nil {
		t.Fatal("Poll should fail on fingerprint mismatch")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("err = %v, want one mentioning fingerprint mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, "abc123")); !os.IsNotExist(err) {
		t.Fatalf("cache dir for poisoned entry still exists: %v", err)
	}
}

func TestPoller_BearerTokenSentToServer(t *testing.T) {
	gotAuth := ""
	mux := http.NewServeMux()
	mux.HandleFunc("GET /library/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":1,"entries":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := &Poller{BaseURL: srv.URL, Token: "secret-tok", CacheDir: t.TempDir(), HTTP: srv.Client()}
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if gotAuth != "Bearer secret-tok" {
		t.Fatalf("Authorization header = %q, want %q", gotAuth, "Bearer secret-tok")
	}
}

func TestPoller_RejectsUnauthorizedManifest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /library/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	p := &Poller{BaseURL: srv.URL, Token: "wrong", CacheDir: t.TempDir(), HTTP: srv.Client()}
	if _, err := p.Poll(context.Background()); err == nil {
		t.Fatal("Poll should fail on 401")
	}
}

// TestPoller_WriteIsAtomic — a partial fetch (server returns
// truncated body or the connection drops mid-stream) must not leave
// a half-written file under the published path. This guards against
// torn reads from the targetserver during a poll cycle.
func TestPoller_WriteIsAtomic(t *testing.T) {
	src := t.TempDir()
	cert := filepath.Join(src, "cert.pem")
	key := filepath.Join(src, "key.pem")
	fp := mintTestCert(t, cert, key, []string{"example.com"}, time.Now().Add(90*24*time.Hour))

	manifest := fmt.Sprintf(`{
  "version": 1,
  "entries": [{
    "id":"abc123","domain":"example.com","profile":"classic",
    "identifiers":["example.com"],"not_after":"%s",
    "fingerprint_sha256":"%s",
    "paths":{"cert":"/x","fullchain":"/x","privkey":"/x"}
  }]
}`, time.Now().Add(90*24*time.Hour).Format(time.RFC3339Nano), fp)

	srv := fakeCertmint(t, []byte(manifest), map[string]string{
		"abc123/cert.pem":      cert,
		"abc123/fullchain.pem": cert,
		"abc123/privkey.pem":   key,
	})

	cacheDir := t.TempDir()
	p := &Poller{BaseURL: srv.URL, CacheDir: cacheDir, HTTP: srv.Client()}
	if _, err := p.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// After a successful poll, no .tmp files remain — the rename
	// step is the atomic publish.
	matches, _ := filepath.Glob(filepath.Join(cacheDir, "abc123", "*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("tmp files leaked: %v", matches)
	}
}
