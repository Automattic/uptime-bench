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
// Status: implemented against the public API documentation. The wire
// shapes are pinned by unit tests using httptest, and a full
// Provision/Retrieve/Deprovision cycle has been exercised against the
// live UptimeRobot API via the build-tagged smoke test in `live_test.go`.
package uptimerobot

import (
	"context"
	"encoding/json"
	"errors"
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

// Monitor type 1 is HTTP(s); 2 is keyword check. Provision picks one or
// the other based on whether ProvisionConfig.Keyword is set.
const (
	monitorTypeHTTP    = 1
	monitorTypeKeyword = 2
)

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
		MinCheckFrequency:          5 * time.Minute,
		SupportsKeyword:            true,
		SupportsInvertedKeyword:    true, // keyword_type=1 (alert when present)
		SupportsAgentChecks:        false,
		SupportsMaintenanceWindows: true,
		// Cooldown resets naturally because Deprovision deletes the
		// monitor; the next Provision creates a fresh one. UptimeRobot
		// emits notifications per state change with no separate
		// cooldown-reset endpoint, so deletion is the reset path.
		SupportsCooldownReset: true,
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

type apiFailure struct {
	op  string
	err *apiError
}

func (e *apiFailure) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil {
		return e.op + ": API returned stat=fail without an error payload"
	}
	return e.op + ": " + e.err.String()
}

type createMonitorUncertainError struct {
	err error
}

func (e *createMonitorUncertainError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *createMonitorUncertainError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
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

func friendlyName(target adapter.Target) string {
	return "uptime-bench: " + target.ID
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiKey == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: api_key is not configured")
	}

	name := friendlyName(target)
	monitorID, err := a.createMonitor(ctx, target, config, name)
	if err != nil {
		var uncertain *createMonitorUncertainError
		switch {
		case isAlreadyExists(err):
			if cleanupErr := a.deleteMatchingBenchmarkMonitors(ctx, target, name); cleanupErr != nil {
				return adapter.MonitorHandle{}, fmt.Errorf("uptimerobot: newMonitor already_exists cleanup: %w", cleanupErr)
			}
			monitorID, err = a.createMonitor(ctx, target, config, name)
			if err != nil {
				return adapter.MonitorHandle{}, err
			}
		case errors.As(err, &uncertain):
			recoveryCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			recoveredID, recoveryErr := a.adoptSingleMatchingBenchmarkMonitor(recoveryCtx, target, name)
			if recoveryErr != nil {
				return adapter.MonitorHandle{}, err
			}
			monitorID = recoveredID
		default:
			return adapter.MonitorHandle{}, err
		}
	}

	handle := adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: strconv.FormatInt(monitorID, 10),
		Fields: map[string]string{
			"url": target.URL,
		},
	}

	if config.MaintenanceWindow != nil {
		mwID, err := a.createMWindow(ctx, target.ID, config.MaintenanceWindow)
		if err != nil {
			// Roll back the just-created or recovered monitor.
			_ = a.deleteMonitor(context.Background(), monitorID)
			return adapter.MonitorHandle{}, err
		}
		if err := a.attachMWindow(ctx, monitorID, mwID); err != nil {
			// Roll back both.
			_ = a.deleteMWindow(context.Background(), mwID)
			_ = a.deleteMonitor(context.Background(), monitorID)
			return adapter.MonitorHandle{}, err
		}
		handle.Fields["maintenance_id"] = strconv.FormatInt(mwID, 10)
	}

	return handle, nil
}

func (a *Adapter) createMonitor(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig, name string) (int64, error) {
	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("friendly_name", name)
	form.Set("url", target.URL)
	form.Set("interval", strconv.Itoa(intervalSeconds(config.CheckFrequency)))
	if config.Keyword != "" {
		// Keyword monitors are a distinct type. keyword_type encodes
		// presence (1 = exists; alert when found) vs. absence (2 = not
		// exists; alert when missing). We invert the project-level
		// vocabulary because UptimeRobot's flag describes the *alert
		// trigger condition*, not the healthy expectation.
		form.Set("type", strconv.Itoa(monitorTypeKeyword))
		form.Set("keyword_value", config.Keyword)
		switch config.KeywordCheck {
		case adapter.KeywordCheckPresent, "":
			// Keyword expected present; alert when not present.
			form.Set("keyword_type", "2")
		case adapter.KeywordCheckAbsent:
			// Keyword expected absent; alert when present.
			form.Set("keyword_type", "1")
		default:
			return 0, fmt.Errorf("uptimerobot: unsupported KeywordCheck %q", config.KeywordCheck)
		}
	} else {
		form.Set("type", strconv.Itoa(monitorTypeHTTP))
	}

	var resp newMonitorResponse
	if err := a.postJSON(ctx, "/newMonitor", form, &resp); err != nil {
		return 0, &createMonitorUncertainError{err: fmt.Errorf("uptimerobot: newMonitor: %w", err)}
	}
	if resp.Stat != "ok" {
		return 0, &apiFailure{op: "uptimerobot: newMonitor", err: resp.Error}
	}
	if resp.Monitor.ID == 0 {
		return 0, fmt.Errorf("uptimerobot: newMonitor: response missing monitor id")
	}
	return resp.Monitor.ID, nil
}

func isAlreadyExists(err error) bool {
	var failure *apiFailure
	if !errors.As(err, &failure) || failure.err == nil {
		return false
	}
	text := strings.ToLower(failure.err.Type + " " + failure.err.Message)
	return strings.Contains(text, "already_exists") || strings.Contains(text, "already exists")
}

// newMWindowResponse mirrors POST /v2/newMWindow.
type newMWindowResponse struct {
	Stat    string `json:"stat"`
	MWindow struct {
		ID     int64 `json:"id"`
		Status int   `json:"status"`
	} `json:"mwindow"`
	Error *apiError `json:"error,omitempty"`
}

// editMonitorResponse mirrors POST /v2/editMonitor — used to attach a
// maintenance window to a monitor via the `mwindows` field.
type editMonitorResponse struct {
	Stat    string `json:"stat"`
	Monitor struct {
		ID int64 `json:"id"`
	} `json:"monitor"`
	Error *apiError `json:"error,omitempty"`
}

// createMWindow posts a one-shot maintenance window covering the absolute
// [Start, End] interval. UptimeRobot's `start_time` for type=1 (Once) is
// a Unix timestamp; `duration` is in minutes. Cross-midnight UTC windows
// are rejected because the type=1 semantics around midnight aren't
// reliably documented and we'd rather fail loudly than silently
// misconfigure (see docs/inter-run-state-design.md).
func (a *Adapter) createMWindow(ctx context.Context, targetID string, window *adapter.MaintenanceWindow) (int64, error) {
	startUTC := window.Start.UTC()
	endUTC := window.End.UTC()
	if startUTC.Year() != endUTC.Year() ||
		startUTC.Month() != endUTC.Month() ||
		startUTC.Day() != endUTC.Day() {
		return 0, fmt.Errorf("uptimerobot: maintenance window %s → %s crosses midnight UTC; not supported (type=1 cross-midnight semantics are unverified)",
			startUTC.Format(time.RFC3339), endUTC.Format(time.RFC3339))
	}
	durationMinutes := int(endUTC.Sub(startUTC).Round(time.Minute).Minutes())
	if durationMinutes < 1 {
		return 0, fmt.Errorf("uptimerobot: maintenance window duration %v rounds to less than 1 minute (UptimeRobot's smallest unit)", endUTC.Sub(startUTC))
	}

	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("friendly_name", "uptime-bench: "+targetID)
	form.Set("type", "1") // 1 = Once
	form.Set("start_time", strconv.FormatInt(startUTC.Unix(), 10))
	form.Set("duration", strconv.Itoa(durationMinutes))

	var resp newMWindowResponse
	if err := a.postJSON(ctx, "/newMWindow", form, &resp); err != nil {
		return 0, fmt.Errorf("uptimerobot: newMWindow: %w", err)
	}
	if resp.Stat != "ok" {
		return 0, fmt.Errorf("uptimerobot: newMWindow: %s", resp.Error)
	}
	if resp.MWindow.ID == 0 {
		return 0, fmt.Errorf("uptimerobot: newMWindow: response missing mwindow id")
	}
	return resp.MWindow.ID, nil
}

// attachMWindow associates a maintenance window with a monitor via
// editMonitor's `mwindows` field. UptimeRobot expects the field as a
// dash-separated list of window IDs (e.g. "345-2986-71"); we only ever
// attach one per monitor so it's a single ID.
func (a *Adapter) attachMWindow(ctx context.Context, monitorID, mwindowID int64) error {
	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("id", strconv.FormatInt(monitorID, 10))
	form.Set("mwindows", strconv.FormatInt(mwindowID, 10))

	var resp editMonitorResponse
	if err := a.postJSON(ctx, "/editMonitor", form, &resp); err != nil {
		return fmt.Errorf("uptimerobot: editMonitor (attach mwindow): %w", err)
	}
	if resp.Stat != "ok" {
		return fmt.Errorf("uptimerobot: editMonitor (attach mwindow): %s", resp.Error)
	}
	return nil
}

// deleteMonitor is a rollback helper. Errors are logged but not returned
// because the caller's primary error is what matters.
func (a *Adapter) deleteMonitor(ctx context.Context, monitorID int64) error {
	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("id", strconv.FormatInt(monitorID, 10))
	var resp struct {
		Stat  string    `json:"stat"`
		Error *apiError `json:"error,omitempty"`
	}
	return a.postJSON(ctx, "/deleteMonitor", form, &resp)
}

// deleteMWindow is the symmetric rollback / Deprovision helper for
// maintenance windows.
func (a *Adapter) deleteMWindow(ctx context.Context, mwindowID int64) error {
	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("id", strconv.FormatInt(mwindowID, 10))
	var resp struct {
		Stat  string    `json:"stat"`
		Error *apiError `json:"error,omitempty"`
	}
	return a.postJSON(ctx, "/deleteMWindow", form, &resp)
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

type getMonitorsResponse struct {
	Stat     string    `json:"stat"`
	Monitors []monitor `json:"monitors"`
	Error    *apiError `json:"error,omitempty"`
}

type monitor struct {
	ID           int64    `json:"id"`
	FriendlyName string   `json:"friendly_name"`
	URL          string   `json:"url"`
	Status       int      `json:"status"`
	Logs         []logRow `json:"logs"`
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

func (a *Adapter) adoptSingleMatchingBenchmarkMonitor(ctx context.Context, target adapter.Target, name string) (int64, error) {
	matches, err := a.matchingBenchmarkMonitors(ctx, target, name)
	if err != nil {
		return 0, err
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return 0, fmt.Errorf("uptimerobot: no matching monitor found after newMonitor uncertainty")
	default:
		return 0, fmt.Errorf("uptimerobot: %d matching monitors found after newMonitor uncertainty", len(matches))
	}
}

func (a *Adapter) deleteMatchingBenchmarkMonitors(ctx context.Context, target adapter.Target, name string) error {
	matches, err := a.matchingBenchmarkMonitors(ctx, target, name)
	if err != nil {
		return err
	}
	for _, mon := range matches {
		if err := a.Deprovision(ctx, adapter.MonitorHandle{MonitorID: strconv.FormatInt(mon.ID, 10)}); err != nil {
			return fmt.Errorf("delete stale monitor %d: %w", mon.ID, err)
		}
	}
	return nil
}

func (a *Adapter) matchingBenchmarkMonitors(ctx context.Context, target adapter.Target, name string) ([]monitor, error) {
	form := url.Values{}
	form.Set("api_key", a.apiKey)
	form.Set("format", "json")
	form.Set("logs", "0")
	form.Set("response_times", "0")
	form.Set("search", name)

	var resp getMonitorsResponse
	if err := a.postJSON(ctx, "/getMonitors", form, &resp); err != nil {
		return nil, fmt.Errorf("uptimerobot: find matching monitors: %w", err)
	}
	if resp.Stat != "ok" {
		return nil, fmt.Errorf("uptimerobot: find matching monitors: %s", resp.Error)
	}
	var matches []monitor
	for _, mon := range resp.Monitors {
		if mon.FriendlyName == name && sameURL(mon.URL, target.URL) {
			matches = append(matches, mon)
		}
	}
	return matches, nil
}

func sameURL(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
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

	// Delete the maintenance window first if one was attached. Failures
	// here are tolerated — a one-shot type=1 window expires at its
	// start_time + duration anyway, so a leak is a dashboard nuisance,
	// not corruption. Skip the empty-string check by reading directly.
	if mwID := handle.Fields["maintenance_id"]; mwID != "" {
		mwIDInt, parseErr := strconv.ParseInt(mwID, 10, 64)
		if parseErr == nil {
			_ = a.deleteMWindow(ctx, mwIDInt)
		}
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
