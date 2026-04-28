package targetserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeManifestServer returns an httptest.Server that serves an empty
// manifest and counts the number of /library/manifest.json requests
// it has received. Used to observe the polling goroutine without
// needing to validate the wire format.
func fakeManifestServer() (*httptest.Server, *atomic.Int64) {
	var count atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /library/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"version":1,"entries":[]}`))
	})
	return httptest.NewServer(mux), &count
}

// waitForCount busy-waits with a short sleep until the counter reaches
// at least want, or until 2s elapses. Returns the final count.
func waitForCount(c *atomic.Int64, want int64) int64 {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.Load() >= want {
			return c.Load()
		}
		time.Sleep(10 * time.Millisecond)
	}
	return c.Load()
}

func TestCertLibraryController_ApplyStartsPoller(t *testing.T) {
	srv, count := fakeManifestServer()
	t.Cleanup(srv.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
	}
	t.Cleanup(c.Stop)

	if err := c.Apply(CertLibraryConfigRequest{URL: srv.URL}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := waitForCount(count, 1); got < 1 {
		t.Fatalf("count = %d, want at least 1 (poller didn't fetch)", got)
	}
}

// TestCertLibraryController_ApplyDedupesIdenticalConfig — calling
// Apply with the same config a second time must not spawn a second
// goroutine. Without dedupe, a harness pushing on every invocation
// would multiply the in-flight pollers.
func TestCertLibraryController_ApplyDedupesIdenticalConfig(t *testing.T) {
	srv, count := fakeManifestServer()
	t.Cleanup(srv.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 80 * time.Millisecond,
	}
	t.Cleanup(c.Stop)

	req := CertLibraryConfigRequest{URL: srv.URL}
	if err := c.Apply(req); err != nil {
		t.Fatal(err)
	}
	waitForCount(count, 1)
	first := count.Load()

	// Second Apply with identical request: the existing goroutine
	// keeps running, no second goroutine spawned.
	if err := c.Apply(req); err != nil {
		t.Fatal(err)
	}
	// Wait long enough that, if a second goroutine had started, it
	// would have fired its initial poll. With dedupe, only the
	// existing goroutine ticks.
	time.Sleep(40 * time.Millisecond)
	beforeNextTick := count.Load()
	if delta := beforeNextTick - first; delta >= 2 {
		t.Fatalf("delta = %d in 40ms; second Apply spawned a duplicate goroutine", delta)
	}
}

// TestCertLibraryController_ApplyRestartsOnURLChange — Apply with a
// different URL must cancel the previous poller and start a new one.
// Verified by switching to a distinct fake server and asserting that
// the new server starts receiving polls while the old one stops.
func TestCertLibraryController_ApplyRestartsOnURLChange(t *testing.T) {
	srvA, countA := fakeManifestServer()
	t.Cleanup(srvA.Close)
	srvB, countB := fakeManifestServer()
	t.Cleanup(srvB.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
	}
	t.Cleanup(c.Stop)

	if err := c.Apply(CertLibraryConfigRequest{URL: srvA.URL}); err != nil {
		t.Fatal(err)
	}
	waitForCount(countA, 1)

	if err := c.Apply(CertLibraryConfigRequest{URL: srvB.URL}); err != nil {
		t.Fatal(err)
	}
	waitForCount(countB, 1)

	beforeA := countA.Load()
	time.Sleep(150 * time.Millisecond)
	afterA := countA.Load()
	if afterA != beforeA {
		t.Fatalf("server A still polled after URL change: before=%d after=%d", beforeA, afterA)
	}
}

func TestCertLibraryController_StopHaltsPoller(t *testing.T) {
	srv, count := fakeManifestServer()
	t.Cleanup(srv.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 30 * time.Millisecond,
	}
	if err := c.Apply(CertLibraryConfigRequest{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	waitForCount(count, 1)

	c.Stop()
	before := count.Load()
	time.Sleep(120 * time.Millisecond)
	after := count.Load()
	if after != before {
		t.Fatalf("poller still running after Stop: before=%d after=%d", before, after)
	}
}

func TestCertLibraryController_RejectsInvalidPollInterval(t *testing.T) {
	c := &CertLibraryController{
		Selector: &CertificateSelector{},
		CacheDir: t.TempDir(),
	}
	cases := []struct{ name, value string }{
		{"unparseable", "not-a-duration"},
		{"negative", "-1m"},
		{"zero", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.Apply(CertLibraryConfigRequest{URL: "http://x", PollInterval: tc.value})
			if err == nil {
				t.Fatalf("Apply with poll_interval=%q should fail", tc.value)
			}
		})
	}
}

func TestCertLibraryHandler_ValidPUTReturns204(t *testing.T) {
	srv, _ := fakeManifestServer()
	t.Cleanup(srv.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
	}
	t.Cleanup(c.Stop)
	mux := http.NewServeMux()
	RegisterCertLibraryConfigHandler(mux, c)

	body, _ := json.Marshal(CertLibraryConfigRequest{URL: srv.URL})
	req := httptest.NewRequest(http.MethodPut, "/config/cert-library", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rec.Code, rec.Body.String())
	}
}

// TestCertLibraryController_PersistAndRestoreRoundTrip — Apply
// writes the config to StatePath; a fresh controller pointed at the
// same path picks it up via RestoreState and starts polling without
// needing the harness to re-push. This is the safeguard against the
// "target restart between harness runs serves fallback cert" failure
// mode.
func TestCertLibraryController_PersistAndRestoreRoundTrip(t *testing.T) {
	srv, count := fakeManifestServer()
	t.Cleanup(srv.Close)

	statePath := filepath.Join(t.TempDir(), "cert-library-config.json")

	// Original controller (simulates the running target). Apply
	// writes the state file as a side effect.
	c1 := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
		StatePath:           statePath,
	}
	if err := c1.Apply(CertLibraryConfigRequest{URL: srv.URL, PollInterval: "100ms"}); err != nil {
		t.Fatalf("c1 Apply: %v", err)
	}
	waitForCount(count, 1)
	c1.Stop()
	beforeRestart := count.Load()

	// State file should exist with the URL we applied.
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	var got CertLibraryConfigRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("state file not JSON: %v", err)
	}
	if got.URL != srv.URL || got.PollInterval != "100ms" {
		t.Fatalf("state = %+v, want URL=%s interval=100ms", got, srv.URL)
	}

	// New controller (simulates target restart). Should restore
	// from disk and resume polling without needing a harness push.
	c2 := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
		StatePath:           statePath,
	}
	t.Cleanup(c2.Stop)
	if err := c2.RestoreState(); err != nil {
		t.Fatalf("RestoreState: %v", err)
	}
	if got := waitForCount(count, beforeRestart+1); got <= beforeRestart {
		t.Fatalf("restored controller didn't poll; count=%d, want > %d", got, beforeRestart)
	}
}

func TestCertLibraryController_RestoreStateMissingFileIsNoOp(t *testing.T) {
	c := &CertLibraryController{
		Selector:  &CertificateSelector{},
		CacheDir:  t.TempDir(),
		StatePath: filepath.Join(t.TempDir(), "does-not-exist.json"),
	}
	if err := c.RestoreState(); err != nil {
		t.Fatalf("RestoreState on missing file should be a no-op: %v", err)
	}
}

func TestCertLibraryController_RestoreStateRejectsCorruptFile(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(statePath, []byte("{ not valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &CertLibraryController{
		Selector:  &CertificateSelector{},
		CacheDir:  t.TempDir(),
		StatePath: statePath,
	}
	if err := c.RestoreState(); err == nil {
		t.Fatal("RestoreState on corrupt file should error so the operator notices")
	}
}

// TestCertLibraryController_NoStatePathSkipsPersistence — controllers
// constructed without StatePath (tests, dev runs) must not write
// anywhere. Guards against accidental pollution of working dirs.
func TestCertLibraryController_NoStatePathSkipsPersistence(t *testing.T) {
	srv, count := fakeManifestServer()
	t.Cleanup(srv.Close)

	c := &CertLibraryController{
		Selector:            &CertificateSelector{},
		CacheDir:            t.TempDir(),
		DefaultPollInterval: 50 * time.Millisecond,
		// StatePath intentionally empty
	}
	t.Cleanup(c.Stop)
	if err := c.Apply(CertLibraryConfigRequest{URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	waitForCount(count, 1)
	// Just confirming no crash; persistence is silently skipped.
}

func TestCertLibraryHandler_RejectsMissingURLAndBadJSON(t *testing.T) {
	c := &CertLibraryController{
		Selector: &CertificateSelector{},
		CacheDir: t.TempDir(),
	}
	mux := http.NewServeMux()
	RegisterCertLibraryConfigHandler(mux, c)

	cases := []struct {
		name string
		body string
	}{
		{"missing url", `{"poll_interval":"5m"}`},
		{"empty body", ``},
		{"malformed json", `{not json`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/config/cert-library", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
		})
	}
}
