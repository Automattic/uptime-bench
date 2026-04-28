package certlibrary

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Poller fetches a cert library from a certmint cert-library HTTP API
// and mirrors it into a local cache directory. After Poll returns,
// the returned *Library's entry Paths point at the local cache files
// — ready for use by targetserver.CertificateSelector — not at the
// certmint-side absolute paths the wire manifest carries.
type Poller struct {
	// BaseURL is the certmint cert-library endpoint without a path
	// suffix, e.g. "http://certmint-01.bench:9200".
	BaseURL string

	// Token is the bearer token shared with certmint's library server.
	// Same CONTROL_TOKEN the rest of the fleet uses.
	Token string

	// CacheDir is the writable directory where this target mirrors
	// the library, e.g. "/var/cache/uptime-bench-target/cert-library".
	// Created with mode 0700 if absent.
	CacheDir string

	// HTTP, when nil, falls back to http.DefaultClient. Tests
	// override with httptest.Server.Client().
	HTTP *http.Client
}

// Poll fetches the manifest, downloads any PEM files referenced by it
// into CacheDir, and returns a Library whose entry Paths are rewritten
// to point at the local cache. Each entry's leaf certificate is
// fingerprint-checked against the manifest's FingerprintSHA256 before
// the entry is accepted; mismatch fails the whole poll so a
// half-corrupt library never reaches the selector.
func (p *Poller) Poll(ctx context.Context) (*Library, error) {
	if p.BaseURL == "" {
		return nil, fmt.Errorf("certlibrary: poller BaseURL is required")
	}
	if p.CacheDir == "" {
		return nil, fmt.Errorf("certlibrary: poller CacheDir is required")
	}
	if err := os.MkdirAll(p.CacheDir, 0o700); err != nil {
		return nil, fmt.Errorf("certlibrary: cache dir: %w", err)
	}

	var lib Library
	if err := p.fetchJSON(ctx, "/library/manifest.json", &lib); err != nil {
		return nil, err
	}
	if err := lib.Validate(); err != nil {
		return nil, fmt.Errorf("certlibrary: validate wire manifest: %w", err)
	}

	for i := range lib.Entries {
		if err := p.mirrorEntry(ctx, &lib.Entries[i]); err != nil {
			return nil, fmt.Errorf("certlibrary: entry %s: %w", lib.Entries[i].ID, err)
		}
	}

	if err := lib.Validate(); err != nil {
		return nil, fmt.Errorf("certlibrary: validate after mirror: %w", err)
	}
	return &lib, nil
}

func (p *Poller) httpClient() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func (p *Poller) fetchJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("certlibrary: build request %s: %w", path, err)
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("certlibrary: GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("certlibrary: GET %s: unexpected %d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("certlibrary: decode %s: %w", path, err)
	}
	return nil
}

// mirrorEntry downloads the four PEM files referenced by the entry's
// (wire-side) Paths into CacheDir/<entry-id>/, then rewrites the
// entry's Paths to point at the local copies. An empty wire-side path
// (e.g. Chain is sometimes absent) skips that file and leaves the
// local path empty. Leaf fingerprint is verified after cert.pem
// lands; a mismatch removes the entry's cache directory and returns
// an error.
func (p *Poller) mirrorEntry(ctx context.Context, entry *Entry) error {
	entryDir := filepath.Join(p.CacheDir, sanitizeForFilesystem(entry.ID))
	if err := os.MkdirAll(entryDir, 0o700); err != nil {
		return err
	}

	files := []struct {
		name     string
		localPtr *string
	}{
		{"cert.pem", &entry.Paths.Cert},
		{"chain.pem", &entry.Paths.Chain},
		{"fullchain.pem", &entry.Paths.FullChain},
		{"privkey.pem", &entry.Paths.PrivKey},
	}
	for _, f := range files {
		if *f.localPtr == "" {
			continue // wire manifest didn't include this file
		}
		localPath := filepath.Join(entryDir, f.name)
		if err := p.fetchFile(ctx, "/library/"+entry.ID+"/"+f.name, localPath); err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		*f.localPtr = localPath
	}

	if entry.FingerprintSHA256 != "" && entry.Paths.Cert != "" {
		if err := verifyLeafFingerprint(entry.Paths.Cert, entry.FingerprintSHA256); err != nil {
			os.RemoveAll(entryDir) // don't leave a poisoned entry on disk
			return err
		}
	}
	return nil
}

// fetchFile downloads url into localPath via a tmp+rename so a target
// reading mid-poll never sees a torn file.
func (p *Poller) fetchFile(ctx context.Context, urlPath, localPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.BaseURL+urlPath, nil)
	if err != nil {
		return err
	}
	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", urlPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected %d", urlPath, resp.StatusCode)
	}

	tmp := localPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, localPath)
}

// verifyLeafFingerprint reads the leaf PEM at certPath, hashes the DER,
// and compares to the expected hex string. Mismatch is the integrity
// gate the cert-library API relies on (we serve over plain HTTP).
func verifyLeafFingerprint(certPath, expected string) error {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return fmt.Errorf("certlibrary: %s is not a CERTIFICATE PEM", certPath)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("certlibrary: parse %s: %w", certPath, err)
	}
	sum := sha256.Sum256(leaf.Raw)
	got := fmt.Sprintf("%x", sum[:])
	if !strings.EqualFold(got, expected) {
		return fmt.Errorf("certlibrary: %s fingerprint mismatch (got %s, want %s)", certPath, got, expected)
	}
	return nil
}

// sanitizeForFilesystem strips path separators and parent-dir traversal
// from an entry ID before joining it into a filesystem path. The
// server-side handler already validates entry IDs; this is defense in
// depth in case a hostile manifest reaches us through some other path.
func sanitizeForFilesystem(id string) string {
	id = strings.ReplaceAll(id, "/", "_")
	id = strings.ReplaceAll(id, "\\", "_")
	id = strings.ReplaceAll(id, "..", "_")
	return id
}
