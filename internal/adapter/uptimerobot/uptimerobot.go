// Package uptimerobot implements the uptime-bench adapter for UptimeRobot.
//
// API: https://uptimerobot.com/api/  (v2)
//
// Wire conventions worth knowing before reading this file:
//
//   - All endpoints are POST, even the GET-shaped ones. UptimeRobot's API
//     went its own way on this and we follow the spec.
//   - Auth is the api_key in the form body, not a header.
//   - Bodies are application/x-www-form-urlencoded; responses are JSON
//     with a top-level `stat` field of "ok" or "fail".
//   - Failure responses include an `error` object with type + message.
//
// services.toml:
//
//	[[services]]
//	id      = "uptimerobot"
//	type    = "uptimerobot"
//	enabled = true
//	auth    = { api_key = "u123-XXXXXXX" }
//
// `url` is optional; the default endpoint is https://api.uptimerobot.com/v2.
//
// Status: implemented against the public API documentation. The wire shapes
// match the docs and are pinned by unit tests using httptest, but this code
// has not been exercised against the live UptimeRobot API. First-run quirks
// should be caught quickly because every call surfaces stat=fail with the
// service's error message.
package uptimerobot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// DefaultAPIURL is used when services.toml omits `url`.
const DefaultAPIURL = "https://api.uptimerobot.com/v2"

// Monitor type 1 is HTTP(s); 2 is keyword check; we always use 1 here and
// rely on adapter-level keyword scenarios where the harness's failure-
// injection target serves the (mis-)content directly.
const monitorTypeHTTP = 1

// UptimeRobot status codes returned by getMonitors.status.
const (
	statusPaused     = 0
	statusNotChecked = 1
	statusUp         = 2
	statusSeemsDown  = 8
	statusDown       = 9
)

// classification maps UptimeRobot's log event types and string statuses to
// uptime-bench's normalized vocabulary. Lives in this package so the core
// stays adapter-agnostic.
var classification = map[string]string{
	"down":       "http_failure",
	"seems_down": "http_failure",
	"up":         "recovered",
	"paused":     "unknown",
}

// Adapter implements adapter.Adapter for UptimeRobot.
type Adapter struct {
	id     string
	apiURL string
	apiKey string
	client *http.Client
}

// New creates an UptimeRobot adapter.
func New(id, apiURL, apiKey string) *Adapter {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &Adapter{
		id:     id,
		apiURL: strings.TrimRight(apiURL, "/"),
		apiKey: apiKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		// Free tier is 5 min; paid plans go down to 30s. 5 min is the safe
		// default so a free-tier scenario doesn't fail capability check.
		MinCheckFrequency:     5 * time.Minute,
		SupportsKeyword:       true,
		SupportsAgentChecks:   false,
		DefaultMaxCallsPerRun: 50, // typical 10 req/min on free; budget for ~5 minutes of polling
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

// newMonitorResponse mirrors the JSON shape of a successful newMonitor reply.
type newMonitorResponse struct {
	Stat    string `json:"stat"`
	Monitor struct {
		ID     int64 `json:"id"`
		Status int   `json:"status"`
	} `json:"monitor"`
	Error *apiError `json:"error,omitempty"`
}

type apiError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e *apiError) String() string {
	if e == nil {
		return ""
	}
	if e.Type == "" {
		return e.Message
	}
	return e.Type + ": " + e.Message
}

// intervalSeconds returns the API-friendly interval in whole seconds. The
// API expects an integer; sub-second precision is meaningless here. Values
// below 30 seconds are clamped to 60 because UptimeRobot rejects anything
// shorter (paid plans support 30s, free plans 5 minutes — 60s is the
// safest default that doesn't exceed paid-tier limits).
func intervalSeconds(d time.Duration) int {
	s := int(d.Round(time.Second).Seconds())
	if s < 30 {
		s = 60
	}
	return s
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiKey == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: api_key is not configured")
	}

	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("friendly_name", "uptime-bench: "+target.ID)
	form.Set("url", target.URL)
	form.Set("type", strconv.Itoa(monitorTypeHTTP))
	form.Set("interval", strconv.Itoa(intervalSeconds(config.CheckFrequency)))

	var resp newMonitorResponse
	if err := a.postJSON(ctx, "/newMonitor", form, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: newMonitor: %w", err)
	}
	if resp.Stat != "ok" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: newMonitor: %s", resp.Error)
	}
	if resp.Monitor.ID == 0 {
		return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: newMonitor: response missing monitor id")
	}
	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: strconv.FormatInt(resp.Monitor.ID, 10),
		Fields: map[string]string{
			"url": target.URL,
		},
	}, nil
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

type getMonitorsResponse struct {
	Stat     string    `json:"stat"`
	Monitors []monitor `json:"monitors"`
	Error    *apiError `json:"error,omitempty"`
}

type monitor struct {
	ID     int64    `json:"id"`
	Status int      `json:"status"`
	Logs   []logRow `json:"logs"`
}

// logRow is one entry from monitor.logs. The `type` values that matter to
// uptime-bench are 1 (down), 2 (up), 98 (started), 99 (paused). `datetime`
// is a Unix timestamp in seconds.
type logRow struct {
	Type     int    `json:"type"`
	Datetime int64  `json:"datetime"`
	Duration int    `json:"duration"`
	Reason   reason `json:"reason"`
}

// reason is annoyingly polymorphic in the v2 API: sometimes it's an empty
// string, sometimes an object with `code` and `detail`. Decode either.
type reason struct {
	Code   string
	Detail string
}

func (r *reason) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" || string(b) == `""` {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		r.Detail = s
		return nil
	}
	var raw struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	r.Code = raw.Code
	r.Detail = raw.Detail
	return nil
}

const (
	logTypeDown    = 1
	logTypeUp      = 2
	logTypeStarted = 98
	logTypePaused  = 99
)

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.apiKey == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "uptimerobot: api_key is not configured",
		}, nil
	}

	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("monitors", handle.MonitorID)
	form.Set("logs", "1")
	form.Set("logs_start_date", strconv.FormatInt(window.FailureStarted.Unix(), 10))
	form.Set("logs_end_date", strconv.FormatInt(window.GracePeriodEnd.Unix(), 10))

	var resp getMonitorsResponse
	if err := a.postJSON(ctx, "/getMonitors", form, &resp); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("uptimerobot: getMonitors: %v", err),
		}, nil
	}
	if resp.Stat != "ok" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("uptimerobot: getMonitors: %s", resp.Error),
		}, nil
	}
	if len(resp.Monitors) == 0 {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("uptimerobot: getMonitors: no monitor with id %s", handle.MonitorID),
		}, nil
	}

	now := time.Now()
	mon := resp.Monitors[0]
	reports := make([]adapter.MonitorReport, 0, len(mon.Logs))
	for _, lg := range mon.Logs {
		ev, raw := classifyLog(lg)
		if ev == "" {
			continue // log type we don't translate (e.g. paused/started markers)
		}
		reports = append(reports, adapter.MonitorReport{
			EventType:         ev,
			RawClassification: raw,
			ReportedAt:        time.Unix(lg.Datetime, 0).UTC(),
			RetrievedAt:       now,
			Metadata: map[string]any{
				"duration_s":   lg.Duration,
				"reason_code":  lg.Reason.Code,
				"reason_text":  lg.Reason.Detail,
				"monitor_id":   mon.ID,
				"final_status": mon.Status,
			},
		})
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

// classifyLog turns a log row into the (EventType, raw classification) pair
// the harness records. Paused/started markers return "", "" — the caller
// skips them; they aren't detection events.
func classifyLog(lg logRow) (adapter.ReportEventType, string) {
	switch lg.Type {
	case logTypeDown:
		// UptimeRobot uses "seems down" when the local probe sees a failure
		// but a confirmation probe disagrees, vs. fully "down" when both
		// agree. The reason.code field carries the distinction in some
		// responses; default to "down" when we can't tell.
		raw := "down"
		if strings.EqualFold(lg.Reason.Code, "seems_down") {
			raw = "seems_down"
		}
		return adapter.EventAlertFired, raw
	case logTypeUp:
		return adapter.EventAlertResolved, "up"
	case logTypeStarted, logTypePaused:
		return "", ""
	}
	return "", ""
}

// ─── Deprovision ────────────────────────────────────────────────────────────

type deleteMonitorResponse struct {
	Stat    string `json:"stat"`
	Monitor struct {
		ID int64 `json:"id"`
	} `json:"monitor"`
	Error *apiError `json:"error,omitempty"`
}

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if a.apiKey == "" {
		return fmt.Errorf("uptimerobot: api_key is not configured")
	}
	if handle.MonitorID == "" {
		return nil // nothing to delete; Provision didn't succeed
	}

	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("id", handle.MonitorID)

	var resp deleteMonitorResponse
	if err := a.postJSON(ctx, "/deleteMonitor", form, &resp); err != nil {
		return fmt.Errorf("uptimerobot: deleteMonitor: %w", err)
	}
	if resp.Stat != "ok" {
		// Treat "not found" as success — Deprovision is idempotent.
		if resp.Error != nil && strings.Contains(strings.ToLower(resp.Error.Message), "not found") {
			return nil
		}
		return fmt.Errorf("uptimerobot: deleteMonitor: %s", resp.Error)
	}
	return nil
}

// ─── HTTP plumbing ──────────────────────────────────────────────────────────

// postJSON sends a form-encoded POST and decodes the JSON response into out.
// The body is read fully even on non-2xx so the error message can include
// the server's response.
func (a *Adapter) postJSON(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cache-Control", "no-cache")

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, truncate(string(body), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
