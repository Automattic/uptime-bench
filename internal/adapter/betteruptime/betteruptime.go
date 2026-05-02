// Package betteruptime implements the uptime-bench adapter for Better Uptime
// (now branded as Better Stack Uptime).
//
// API: https://betterstack.com/docs/uptime/api/  (v2)
//
// Wire conventions:
//
//   - Bearer token in Authorization header
//   - JSON requests and responses, all under /api/v2/
//   - List endpoints wrap results in {"data": [...]} with each item having
//     id, type, attributes — JSON:API style
//   - Outage history via GET /incidents?monitor_id={id} (the flat endpoint;
//     there is no nested /monitors/{id}/incidents route)
//
// services.toml:
//
//	[[services]]
//	id      = "better-uptime"
//	type    = "better-uptime"
//	enabled = true
//	auth    = { token = "<your_better_uptime_api_token>", http_method = "GET" }
//
// Optional auth keys:
//
//	http_method — explicit HTTP method for status monitors: "GET" or "HEAD".
//	              Keyword monitors always use GET because HEAD has no body.
//
// `url` is optional; the default endpoint is https://uptime.betterstack.com/api/v2.
//
// Status: implemented against the public API documentation. The wire
// shapes are pinned by unit tests using httptest, and a full
// Provision/Retrieve/Deprovision cycle has been exercised against the
// live Better Uptime API via the build-tagged smoke test in `live_test.go`.
package betteruptime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// DefaultAPIURL is used when services.toml omits `url`. Better Stack
// rebranded the service from betteruptime.com to betterstack.com — both
// host the same API, but the betterstack.com URL is the current canonical.
const DefaultAPIURL = "https://uptime.betterstack.com/api/v2"

// classification maps Better Uptime incident/monitor states to
// uptime-bench's normalized vocabulary.
var classification = map[string]string{
	"down":       "http_failure",
	"validating": "http_failure", // probe seeing failures, awaiting confirmation
	"up":         "recovered",
	"paused":     "unknown",
	"pending":    "unknown",
}

// Adapter implements adapter.Adapter for Better Uptime.
type Adapter struct {
	id         string
	apiURL     string
	token      string
	httpMethod string
	client     *http.Client
}

type Option func(*Adapter)

func WithHTTPMethod(method string) Option {
	return func(a *Adapter) {
		a.httpMethod = normalizeHTTPMethod(method)
	}
}

// New creates a Better Uptime adapter.
func New(id, apiURL, token string, opts ...Option) *Adapter {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	a := &Adapter{
		id:     id,
		apiURL: strings.TrimRight(apiURL, "/"),
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	supportsKeyword := !strings.EqualFold(a.httpMethod, http.MethodHead)
	return adapter.Capabilities{
		// Better Uptime supports 30-second checks on paid plans; 3-minute on free.
		// Use 3 minutes as a defensive default that doesn't exclude free-tier users.
		MinCheckFrequency: 3 * time.Minute,
		SupportsKeyword:   supportsKeyword,
		// Better Stack supports monitor_type = "keyword" for required
		// content and "keyword_absence" for forbidden content. HEAD-lane
		// checks disable keyword support because there is no response body.
		SupportsInvertedKeyword:    supportsKeyword,
		SupportsAgentChecks:        false,
		SupportsMaintenanceWindows: true,
		// Cooldown resets naturally because Deprovision deletes the
		// monitor; the next Provision creates a fresh one with no
		// inherited incident state. No vendor-side reset call needed.
		SupportsCooldownReset: true,
		DefaultMaxCallsPerRun: 60, // 60 req/min documented limit
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

// newMonitorRequest mirrors POST /monitors. We use "status" for HTTP
// status checks (failure injection scenarios produce 5xx responses, which
// is what this monitor alerts on) and "keyword" when keyword monitoring
// is requested.
type newMonitorRequest struct {
	URL               string `json:"url"`
	MonitorType       string `json:"monitor_type"` // "status", "keyword", or "keyword_absence"
	PronounceableName string `json:"pronounceable_name,omitempty"`
	HTTPMethod        string `json:"http_method,omitempty"`
	CheckFrequency    int    `json:"check_frequency,omitempty"` // seconds; min 30 on paid
	RequiredKeyword   string `json:"required_keyword,omitempty"`
}

// monitorResource is the JSON:API-style envelope Better Uptime returns.
type monitorResource struct {
	Data struct {
		ID         string         `json:"id"`
		Type       string         `json:"type"`
		Attributes map[string]any `json:"attributes"`
	} `json:"data"`
	Errors []apiError `json:"errors,omitempty"`
}

type monitorsResponse struct {
	Data   []monitorListResource `json:"data"`
	Errors []apiError            `json:"errors,omitempty"`
}

type monitorListResource struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Attributes monitorAttributes `json:"attributes"`
}

type monitorAttributes struct {
	URL               string `json:"url"`
	Name              string `json:"name"`
	PronounceableName string `json:"pronounceable_name"`
}

type apiError struct {
	Detail string `json:"detail"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func (e apiError) String() string {
	if e.Detail != "" {
		return e.Detail
	}
	if e.Title != "" {
		return e.Title
	}
	return e.Status
}

func errorsString(errs []apiError) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		parts = append(parts, e.String())
	}
	return strings.Join(parts, "; ")
}

// checkFrequencySeconds clamps to Better Uptime's accepted range. Free-tier
// minimum is 180 seconds; paid plans go down to 30. Anything below 30 is
// clamped to 180 to avoid rejection on free-tier accounts.
func checkFrequencySeconds(d time.Duration) int {
	s := int(d.Round(time.Second).Seconds())
	if s < 30 {
		s = 180
	}
	return s
}

func normalizeHTTPMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodHead:
		return http.MethodHead
	default:
		return http.MethodGet
	}
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.token == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: token is not configured")
	}

	req := newMonitorRequest{
		URL:               target.URL,
		MonitorType:       "status",
		PronounceableName: "uptime-bench: " + target.ID,
		HTTPMethod:        normalizeHTTPMethod(a.httpMethod),
		CheckFrequency:    checkFrequencySeconds(config.CheckFrequency),
	}
	if config.Keyword != "" {
		if strings.EqualFold(a.httpMethod, http.MethodHead) {
			return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: keyword monitoring is not supported for HEAD checks")
		}
		switch config.KeywordCheck {
		case adapter.KeywordCheckPresent, "":
			req.MonitorType = "keyword"
		case adapter.KeywordCheckAbsent:
			req.MonitorType = "keyword_absence"
		default:
			return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: unsupported KeywordCheck %q", config.KeywordCheck)
		}
		req.HTTPMethod = http.MethodGet
		req.RequiredKeyword = config.Keyword
	}

	var resp monitorResource
	if err := a.do(ctx, http.MethodPost, "/monitors", req, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: POST /monitors: %w", err)
	}
	if len(resp.Errors) > 0 {
		return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: POST /monitors: %s", errorsString(resp.Errors))
	}
	if resp.Data.ID == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("better-uptime: POST /monitors: response missing monitor id")
	}
	handle := adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: resp.Data.ID,
		Fields: map[string]string{
			"url": target.URL,
		},
	}

	if config.MaintenanceWindow != nil {
		if err := a.applyMaintenance(ctx, resp.Data.ID, config.MaintenanceWindow); err != nil {
			// Roll back the just-created monitor so the run doesn't leak.
			// Use context.Background() so a cancelled outer ctx doesn't
			// skip cleanup.
			_ = a.Deprovision(context.Background(), handle)
			return adapter.MonitorHandle{}, err
		}
	}

	return handle, nil
}

// updateMonitorRequest is the body for PATCH /api/v2/monitors/{id} when
// configuring a maintenance window. Better Uptime's maintenance primitive
// is a recurring daily window in HH:MM:SS form, not a one-shot absolute
// range; the adapter converts the scenario's absolute window to today's
// HH:MM:SS plus today's day-name and sets the timezone to UTC. Empty
// fields elsewhere on the monitor are omitted (omitempty) so this PATCH
// only modifies maintenance-related attributes.
type updateMonitorRequest struct {
	MaintenanceFrom     string   `json:"maintenance_from,omitempty"`
	MaintenanceTo       string   `json:"maintenance_to,omitempty"`
	MaintenanceDays     []string `json:"maintenance_days,omitempty"`
	MaintenanceTimezone string   `json:"maintenance_timezone,omitempty"`
}

// dayAbbreviations maps Go's time.Weekday to the lowercase 3-letter form
// Better Uptime accepts in maintenance_days.
var dayAbbreviations = map[time.Weekday]string{
	time.Sunday:    "sun",
	time.Monday:    "mon",
	time.Tuesday:   "tue",
	time.Wednesday: "wed",
	time.Thursday:  "thu",
	time.Friday:    "fri",
	time.Saturday:  "sat",
}

// applyMaintenance configures the monitor's maintenance window. Returns
// an error for cross-midnight UTC windows since Better Uptime's recurring
// model can't express a one-shot range that straddles a day boundary
// without committing to "every day at this UTC time," which would also
// suppress alerts on subsequent days. Caller rolls back the monitor on
// error.
func (a *Adapter) applyMaintenance(ctx context.Context, monitorID string, window *adapter.MaintenanceWindow) error {
	startUTC := window.Start.UTC()
	endUTC := window.End.UTC()
	if startUTC.Year() != endUTC.Year() ||
		startUTC.Month() != endUTC.Month() ||
		startUTC.Day() != endUTC.Day() {
		return fmt.Errorf("better-uptime: maintenance window %s → %s crosses midnight UTC; not supported (Better Uptime's maintenance primitive is recurring-daily, not one-shot)",
			startUTC.Format(time.RFC3339), endUTC.Format(time.RFC3339))
	}

	req := updateMonitorRequest{
		MaintenanceFrom:     startUTC.Format("15:04:05"),
		MaintenanceTo:       endUTC.Format("15:04:05"),
		MaintenanceDays:     []string{dayAbbreviations[startUTC.Weekday()]},
		MaintenanceTimezone: "UTC",
	}
	path := "/monitors/" + monitorID
	if err := a.do(ctx, http.MethodPatch, path, req, nil); err != nil {
		return fmt.Errorf("better-uptime: PATCH %s (maintenance): %w", path, err)
	}
	return nil
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

// incidentsResponse mirrors GET /incidents?monitor_id={id}.
type incidentsResponse struct {
	Data   []incidentResource `json:"data"`
	Errors []apiError         `json:"errors,omitempty"`
}

type incidentResource struct {
	ID         string             `json:"id"`
	Attributes incidentAttributes `json:"attributes"`
}

type incidentAttributes struct {
	Name           string `json:"name"`
	URL            string `json:"url"`
	Cause          string `json:"cause"`
	StartedAt      string `json:"started_at"`      // RFC3339
	AcknowledgedAt string `json:"acknowledged_at"` // RFC3339, may be empty
	ResolvedAt     string `json:"resolved_at"`     // RFC3339, may be empty
	Status         string `json:"status"`          // "Started" | "Acknowledged" | "Resolved"
}

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.token == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "better-uptime: token is not configured",
		}, nil
	}

	path := fmt.Sprintf("/incidents?monitor_id=%s&from=%s&to=%s",
		handle.MonitorID,
		window.FailureStarted.UTC().Format(time.RFC3339),
		window.GracePeriodEnd.UTC().Format(time.RFC3339),
	)

	var resp incidentsResponse
	if err := a.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("better-uptime: GET %s: %v", path, err),
		}, nil
	}
	if len(resp.Errors) > 0 {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("better-uptime: GET %s: %s", path, errorsString(resp.Errors)),
		}, nil
	}

	now := time.Now()
	reports := make([]adapter.MonitorReport, 0, len(resp.Data)*2)
	for _, inc := range resp.Data {
		// Each incident contributes an alert_fired at started_at. If the
		// incident has resolved within the window, it also contributes an
		// alert_resolved at resolved_at.
		if started, ok := parseRFC3339(inc.Attributes.StartedAt); ok {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertFired,
				RawClassification: "down",
				ReportedAt:        started,
				RetrievedAt:       now,
				Metadata: map[string]any{
					"incident_id":          inc.ID,
					"name":                 inc.Attributes.Name,
					"cause":                inc.Attributes.Cause,
					"better_uptime_status": inc.Attributes.Status,
				},
			})
		}
		if resolved, ok := parseRFC3339(inc.Attributes.ResolvedAt); ok {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertResolved,
				RawClassification: "up",
				ReportedAt:        resolved,
				RetrievedAt:       now,
				Metadata: map[string]any{
					"incident_id": inc.ID,
				},
			})
		}
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

// parseRFC3339 returns the parsed time and whether the input was non-empty
// and well-formed. Empty strings (common for unresolved incidents) return
// (zero, false).
func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if a.token == "" {
		return fmt.Errorf("better-uptime: token is not configured")
	}
	if handle.MonitorID == "" {
		return nil
	}

	path := "/monitors/" + handle.MonitorID
	// Better Uptime returns 204 No Content on success. We don't need a body.
	if err := a.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		// 404 means the monitor is already gone — Deprovision is idempotent.
		if strings.Contains(err.Error(), "status 404") {
			return nil
		}
		return fmt.Errorf("better-uptime: DELETE %s: %w", path, err)
	}
	return nil
}

func (a *Adapter) CleanupStale(ctx context.Context, opts adapter.CleanupOptions) (adapter.CleanupResult, error) {
	if a.token == "" {
		return adapter.CleanupResult{}, fmt.Errorf("better-uptime: token is not configured")
	}

	var resp monitorsResponse
	if err := a.do(ctx, http.MethodGet, "/monitors", nil, &resp); err != nil {
		return adapter.CleanupResult{}, fmt.Errorf("better-uptime: GET /monitors: %w", err)
	}
	if len(resp.Errors) > 0 {
		return adapter.CleanupResult{}, fmt.Errorf("better-uptime: GET /monitors: %s", errorsString(resp.Errors))
	}

	result := adapter.CleanupResult{}
	for _, mon := range resp.Data {
		name := betterMonitorName(mon.Attributes)
		if !strings.HasPrefix(name, "uptime-bench:") {
			continue
		}
		candidate := adapter.CleanupCandidate{
			ServiceID:  a.id,
			ResourceID: mon.ID,
			Kind:       "monitor",
			Name:       name,
			URL:        mon.Attributes.URL,
			Reason:     "benchmark-owned Better Uptime monitor",
		}
		if !opts.Scope.MatchesURL(mon.Attributes.URL) {
			candidate.Ambiguous = true
			candidate.Reason = "benchmark-owned monitor outside configured fleet scope"
			result.Actions = append(result.Actions, adapter.CleanupAction{
				Candidate: candidate,
				Action:    adapter.CleanupActionSkipped,
			})
			continue
		}
		if opts.DryRun {
			result.Actions = append(result.Actions, adapter.CleanupAction{
				Candidate: candidate,
				Action:    adapter.CleanupActionWouldDelete,
			})
			continue
		}
		if err := a.Deprovision(ctx, adapter.MonitorHandle{ServiceID: a.id, MonitorID: candidate.ResourceID}); err != nil {
			result.Actions = append(result.Actions, adapter.CleanupAction{
				Candidate: candidate,
				Action:    adapter.CleanupActionError,
				Error:     err.Error(),
			})
			continue
		}
		result.Actions = append(result.Actions, adapter.CleanupAction{
			Candidate: candidate,
			Action:    adapter.CleanupActionDeleted,
		})
	}
	return result, nil
}

func betterMonitorName(attrs monitorAttributes) string {
	if attrs.PronounceableName != "" {
		return attrs.PronounceableName
	}
	return attrs.Name
}

// ─── HTTP plumbing ──────────────────────────────────────────────────────────

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
	if len(respBody) == 0 {
		return nil // 204 No Content
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
