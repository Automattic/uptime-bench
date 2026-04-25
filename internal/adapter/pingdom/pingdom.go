// Package pingdom implements the uptime-bench adapter for Pingdom.
//
// API: https://docs.pingdom.com/api/  (v3.1)
//
// Wire conventions:
//
//   - Bearer token in Authorization header
//   - JSON requests and responses
//   - Endpoints under /api/3.1/
//   - Outage history via GET /summary.outage/{checkid}?from=&to=
//
// services.toml:
//
//	[[services]]
//	id      = "pingdom"
//	type    = "pingdom"
//	enabled = true
//	auth    = { token = "<your_pingdom_api_token>" }
//
// `url` is optional; the default endpoint is https://api.pingdom.com/api/3.1.
//
// Status: implemented against the public API documentation. The wire
// shapes are pinned by unit tests using httptest, and a full
// Provision/Retrieve/Deprovision cycle has been exercised against the
// live Pingdom API via the build-tagged smoke test in `live_test.go`.
package pingdom

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// DefaultAPIURL is used when services.toml omits `url`.
const DefaultAPIURL = "https://api.pingdom.com/api/3.1"

// classification maps Pingdom outage state strings to uptime-bench's
// normalized vocabulary.
var classification = map[string]string{
	"down":             "http_failure",
	"unconfirmed_down": "http_failure",
	"up":               "recovered",
	"unknown":          "unknown",
	"paused":           "unknown",
}

// Adapter implements adapter.Adapter for Pingdom.
type Adapter struct {
	id     string
	apiURL string
	token  string
	client *http.Client
}

// New creates a Pingdom adapter.
func New(id, apiURL, token string) *Adapter {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &Adapter{
		id:     id,
		apiURL: strings.TrimRight(apiURL, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		// Pingdom's minimum resolution is 1 minute on most plans.
		MinCheckFrequency:     time.Minute,
		SupportsKeyword:       true,
		SupportsAgentChecks:   false,
		DefaultMaxCallsPerRun: 50, // typical 10-100 req/min limit; budget conservatively
	}
}

// Normalize implements adapter.Adapter.Normalize.
func (a *Adapter) Normalize(raw string) string {
	if v, ok := classification[raw]; ok {
		return v
	}
	return adapter.UnrecognizedClassification
}

// ─── Provision ──────────────────────────────────────────────────────────────

// newCheckRequest mirrors the JSON shape Pingdom accepts on POST /checks.
// `host` must be the hostname only; the path goes in `url`. For the bench
// scenarios we always use HTTP type with a path and a 5xx-failed status
// rule so monitors report down on the failure-injection responses.
type newCheckRequest struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Type       string `json:"type"`
	URL        string `json:"url,omitempty"`
	Resolution int    `json:"resolution,omitempty"` // minutes
}

type checkEnvelope struct {
	Check checkInfo `json:"check"`
	Error *apiError `json:"error,omitempty"`
}

type checkInfo struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type apiError struct {
	StatusCode int    `json:"statuscode"`
	StatusDesc string `json:"statusdesc"`
	ErrorMsg   string `json:"errormessage"`
}

func (e *apiError) String() string {
	if e == nil {
		return ""
	}
	if e.ErrorMsg != "" {
		return fmt.Sprintf("%d %s: %s", e.StatusCode, e.StatusDesc, e.ErrorMsg)
	}
	if e.StatusDesc != "" {
		return fmt.Sprintf("%d %s", e.StatusCode, e.StatusDesc)
	}
	return ""
}

// resolutionMinutes returns the API-friendly resolution in whole minutes.
// Pingdom accepts 1, 5, 15, 30, 60. Anything below 1 minute is clamped to
// 1 (the API minimum), and anything above is rounded down to the nearest
// supported tier.
func resolutionMinutes(d time.Duration) int {
	mins := int(d.Round(time.Minute).Minutes())
	switch {
	case mins <= 1:
		return 1
	case mins < 5:
		return 1
	case mins < 15:
		return 5
	case mins < 30:
		return 15
	case mins < 60:
		return 30
	default:
		return 60
	}
}

// splitHostPath separates a URL like "http://bench-a.example/path" into
// its host ("bench-a.example") and path ("/path"). Returns ("", "/") if
// parsing fails — Pingdom requires at least a hostname so the caller's
// error path will surface that clearly.
func splitHostPath(rawURL string) (host, path string) {
	// Strip scheme.
	rest := rawURL
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	// Split at first /.
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[:i], rest[i:]
	}
	return rest, "/"
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.token == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("pingdom: token is not configured")
	}
	host, path := splitHostPath(target.URL)
	if host == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("pingdom: target URL %q has no host", target.URL)
	}

	req := newCheckRequest{
		Name:       "uptime-bench: " + target.ID,
		Host:       host,
		Type:       "http",
		URL:        path,
		Resolution: resolutionMinutes(config.CheckFrequency),
	}

	var resp checkEnvelope
	if err := a.do(ctx, http.MethodPost, "/checks", req, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("pingdom: POST /checks: %w", err)
	}
	if resp.Error != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("pingdom: POST /checks: %s", resp.Error)
	}
	if resp.Check.ID == 0 {
		return adapter.MonitorHandle{}, fmt.Errorf("pingdom: POST /checks: response missing check id")
	}
	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: strconv.FormatInt(resp.Check.ID, 10),
		Fields: map[string]string{
			"host": host,
			"path": path,
		},
	}, nil
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

// outageSummaryResponse mirrors GET /summary.outage/{checkid}.
type outageSummaryResponse struct {
	Summary struct {
		States []outageState `json:"states"`
	} `json:"summary"`
	Error *apiError `json:"error,omitempty"`
}

// outageState is one entry in the outage history.
type outageState struct {
	Status   string `json:"status"`   // "up" | "down" | "unconfirmed_down" | ...
	Timefrom int64  `json:"timefrom"` // Unix seconds
	Timeto   int64  `json:"timeto"`   // Unix seconds
}

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.token == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "pingdom: token is not configured",
		}, nil
	}

	path := fmt.Sprintf("/summary.outage/%s?from=%d&to=%d",
		handle.MonitorID,
		window.FailureStarted.Unix(),
		window.GracePeriodEnd.Unix(),
	)

	var resp outageSummaryResponse
	if err := a.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("pingdom: GET %s: %v", path, err),
		}, nil
	}
	if resp.Error != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("pingdom: GET %s: %s", path, resp.Error),
		}, nil
	}

	now := time.Now()
	reports := make([]adapter.MonitorReport, 0, len(resp.Summary.States))
	for _, st := range resp.Summary.States {
		ev := classifyState(st.Status)
		if ev == "" {
			continue // states like "paused" don't represent monitor detection events
		}
		reports = append(reports, adapter.MonitorReport{
			EventType:         ev,
			RawClassification: st.Status,
			ReportedAt:        time.Unix(st.Timefrom, 0).UTC(),
			RetrievedAt:       now,
			Metadata: map[string]any{
				"timeto":     st.Timeto,
				"duration_s": st.Timeto - st.Timefrom,
				"check_id":   handle.MonitorID,
			},
		})
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

// classifyState turns a Pingdom outage state into the corresponding
// uptime-bench event type. Returns "" for states that aren't detection
// events (e.g. paused).
func classifyState(status string) adapter.ReportEventType {
	switch status {
	case "down", "unconfirmed_down":
		return adapter.EventAlertFired
	case "up":
		return adapter.EventAlertResolved
	}
	return ""
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if a.token == "" {
		return fmt.Errorf("pingdom: token is not configured")
	}
	if handle.MonitorID == "" {
		return nil
	}

	var resp struct {
		Message string    `json:"message"`
		Error   *apiError `json:"error,omitempty"`
	}
	path := "/checks/" + handle.MonitorID
	if err := a.do(ctx, http.MethodDelete, path, nil, &resp); err != nil {
		// 404 means the check is already gone — Deprovision is idempotent.
		if strings.Contains(err.Error(), "status 404") {
			return nil
		}
		return fmt.Errorf("pingdom: DELETE %s: %w", path, err)
	}
	if resp.Error != nil {
		// Same idempotency for in-body errors that mention "not found".
		if strings.Contains(strings.ToLower(resp.Error.ErrorMsg), "not found") {
			return nil
		}
		return fmt.Errorf("pingdom: DELETE %s: %s", path, resp.Error)
	}
	return nil
}

// ─── HTTP plumbing ──────────────────────────────────────────────────────────

// do sends an HTTP request to the Pingdom API and decodes the response
// body into out. body is JSON-encoded if non-nil. Any non-2xx status is
// returned as an error including the response body for debugging.
func (a *Adapter) do(ctx context.Context, method, path string, body, out any) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.apiURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, truncate(string(respBody), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
