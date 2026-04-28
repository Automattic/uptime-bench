package targetserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	return nil
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
