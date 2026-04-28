package targetserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Automattic/uptime-bench/internal/certlibrary"
)

// CertLibraryConfigRequest is the body of PUT /config/cert-library on
// the target. The harness calls this at startup, forwarding the URL
// from fleet.toml's [certmint] section so the target doesn't need its
// own copy of fleet topology.
type CertLibraryConfigRequest struct {
	URL string `json:"url"`
	// PollInterval is parseable as time.ParseDuration. Empty string
	// means use the controller's default (30 minutes).
	PollInterval string `json:"poll_interval,omitempty"`
}

// CertLibraryController runs (and re-runs) the polling loop the
// target uses to consume certmint's cert library. The target's
// CONTROL_TOKEN authenticates the polling requests to certmint —
// fleet members share one token, so the harness doesn't need to
// forward credentials over the wire.
type CertLibraryController struct {
	Selector *CertificateSelector
	Token    string
	CacheDir string

	// DefaultPollInterval is used when the request leaves
	// PollInterval empty. Defaults to 30 minutes when zero.
	DefaultPollInterval time.Duration

	// StatePath, when non-empty, is the file the controller
	// writes the most-recently-applied config to so that a target
	// restart can resume polling without needing the harness to
	// re-push first. The state file holds only the URL + parsed
	// interval — the bearer token is sourced from the target's
	// own CONTROL_TOKEN at runtime, never persisted to disk.
	// Empty StatePath disables persistence (used by tests).
	StatePath string

	mu             sync.Mutex
	current        CertLibraryConfigRequest
	cancel         context.CancelFunc
	parsedInterval time.Duration
}

// RegisterCertLibraryConfigHandler wires PUT /config/cert-library onto
// mux. The caller wraps mux in control.AuthMiddleware so the harness
// is the only thing that can change a target's cert-library config.
func RegisterCertLibraryConfigHandler(mux *http.ServeMux, c *CertLibraryController) {
	mux.HandleFunc("PUT /config/cert-library", func(w http.ResponseWriter, r *http.Request) {
		var req CertLibraryConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.URL == "" {
			http.Error(w, "url is required", http.StatusBadRequest)
			return
		}
		if err := c.Apply(req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Apply restarts the polling goroutine with the new config, or no-ops
// when the new config is identical to what's already running. The
// no-op path matters because the harness pushes config on every
// invocation; without dedupe, every scenario run would flap the
// target's poller.
func (c *CertLibraryController) Apply(req CertLibraryConfigRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	interval := c.DefaultPollInterval
	if interval == 0 {
		interval = 30 * time.Minute
	}
	if req.PollInterval != "" {
		parsed, err := time.ParseDuration(req.PollInterval)
		if err != nil {
			return fmt.Errorf("invalid poll_interval %q: %w", req.PollInterval, err)
		}
		if parsed <= 0 {
			return fmt.Errorf("poll_interval must be positive (got %s)", parsed)
		}
		interval = parsed
	}

	if c.cancel != nil && c.current == req && c.parsedInterval == interval {
		// Same config as last time — leave the running goroutine alone.
		return nil
	}

	if c.cancel != nil {
		c.cancel()
	}
	c.current = req
	c.parsedInterval = interval

	poller := &certlibrary.Poller{
		BaseURL:  req.URL,
		Token:    c.Token,
		CacheDir: c.CacheDir,
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.runPoller(ctx, poller, interval)
	log.Printf("target: cert-library config applied source=%s interval=%s", req.URL, interval)

	if c.StatePath != "" {
		if err := writeStateFile(c.StatePath, req); err != nil {
			// Persistence is a safety net, not a hard requirement
			// — the running poll loop is healthy regardless. Log
			// loudly so an operator notices the failure mode (next
			// target restart will fall back to the no-config
			// state until the harness re-pushes).
			log.Printf("target: cert-library state persist: %v", err)
		}
	}
	return nil
}

// RestoreState reads the persisted cert-library config (if any) and
// applies it. Called at target startup, before the data-plane
// listener accepts traffic, so a restart resumes serving certs from
// the library without a "wait for the next harness push" window
// where the fallback self-signed cert is served.
//
// Missing state file is not an error — the target simply boots in
// the no-config state and waits for a harness push. Corrupt state is
// also non-fatal, just logged: the broken file is left in place for
// debugging and the controller proceeds as if no state existed.
func (c *CertLibraryController) RestoreState() error {
	if c.StatePath == "" {
		return nil
	}
	data, err := os.ReadFile(c.StatePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read state: %w", err)
	}
	var req CertLibraryConfigRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}
	if req.URL == "" {
		return fmt.Errorf("state missing url")
	}
	log.Printf("target: cert-library restoring config from %s (source=%s)", c.StatePath, req.URL)
	return c.Apply(req)
}

func writeStateFile(path string, req CertLibraryConfigRequest) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Stop cancels any running poller. Used by the binary's shutdown path
// so the goroutine doesn't outlive the process under test scenarios.
func (c *CertLibraryController) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (c *CertLibraryController) runPoller(ctx context.Context, p *certlibrary.Poller, interval time.Duration) {
	poll := func() {
		lib, err := p.Poll(ctx)
		if err != nil {
			log.Printf("target: cert-library poll: %v", err)
			return
		}
		c.Selector.SetLibrary(lib)
		log.Printf("target: cert-library refreshed from %s entries=%d", p.BaseURL, len(lib.Entries))
		// Prune AFTER SetLibrary so the in-memory cert cache is
		// already cleared and the selector's view is the new
		// library — no handshake path can still try to load a
		// cached cert from a directory we're about to delete.
		if removed, err := p.PruneCache(lib); err != nil {
			log.Printf("target: cert-library prune: %v", err)
		} else if len(removed) > 0 {
			log.Printf("target: cert-library pruned %d stale cache entries: %v", len(removed), removed)
		}
	}
	poll()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
