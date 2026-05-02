// Package jetmonv2 implements the uptime-bench adapter for Jetmon 2's
// internal REST API.
//
// Wire conventions:
//
//   - Bearer token in Authorization header
//   - JSON requests and responses
//   - Endpoints under /api/v1/
//   - Site identity is Jetmon's blog_id; uptime-bench provisions synthetic
//     high-range positive blog IDs so every run starts with fresh event state.
//
// services.toml:
//
//	[[services]]
//	id      = "jetmon-v2"
//	type    = "jetmon-v2"
//	url     = "http://localhost:8081/api/v1"
//	enabled = true
//	auth    = { token = "<jetmon-api-token>", bucket_no = "0" }
package jetmonv2

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

const (
	apiPathSuffix = "/api/v1"

	// Synthetic IDs live below 2^53 so they stay lossless for JSON tooling,
	// but far above any real WordPress.com blog_id range.
	syntheticBlogIDBase  = int64(8_000_000_000_000_000)
	syntheticBlogIDRange = int64(1_000_000_000_000_000)

	maxCreateAttempts = 5
	maxEventPages     = 100

	jetmonErrorTimeout       = 1
	jetmonErrorConnect       = 2
	jetmonErrorSSL           = 3
	jetmonErrorRedirect      = 4
	jetmonErrorKeyword       = 5
	jetmonErrorTLSExpired    = 6
	jetmonErrorTLSDeprecated = 7
)

// classification maps Jetmon v2 event states and metadata-derived reason
// labels to uptime-bench's normalized vocabulary. Raw report labels are lower
// snake case, matching Jetmon v1's existing adapter labels where the concepts
// overlap.
var classification = map[string]string{
	"down":           "http_failure",
	"seems_down":     "http_failure",
	"degraded":       "http_failure",
	"server":         "http_failure",
	"client":         "http_failure",
	"blocked":        "http_failure",
	"connect":        "http_failure",
	"redirect":       "http_failure",
	"timeout":        "timeout",
	"ssl":            "tls_failure",
	"https":          "tls_failure",
	"tls_expired":    "tls_failure",
	"tls_expiry":     "tls_advisory",
	"keyword":        "content_failure",
	"up":             "recovered",
	"resolved":       "recovered",
	"tls_deprecated": "tls_advisory",
	"warning":        "unknown",
	"paused":         "unknown",
	"maintenance":    "unknown",
	"unknown":        "unknown",

	"Down":           "http_failure",
	"Seems Down":     "http_failure",
	"Degraded":       "http_failure",
	"Server":         "http_failure",
	"Client":         "http_failure",
	"Blocked":        "http_failure",
	"Connect":        "http_failure",
	"Redirect":       "http_failure",
	"Timeout":        "timeout",
	"SSL":            "tls_failure",
	"HTTPS":          "tls_failure",
	"TLS Expired":    "tls_failure",
	"TLS Expiry":     "tls_advisory",
	"Keyword":        "content_failure",
	"Up":             "recovered",
	"Resolved":       "recovered",
	"TLS Deprecated": "tls_advisory",
	"Warning":        "unknown",
	"Paused":         "unknown",
	"Maintenance":    "unknown",
	"Unknown":        "unknown",
}

// Adapter implements adapter.Adapter for Jetmon 2.
type Adapter struct {
	id       string
	apiURL   string
	token    string
	bucketNo int
	client   *http.Client
}

// Option customizes a Jetmon v2 adapter.
type Option func(*Adapter)

// WithBucketNo sets the Jetmon bucket assigned to synthetic benchmark sites.
// Operators should choose a bucket owned by the Jetmon v2 host under test.
func WithBucketNo(bucketNo int) Option {
	return func(a *Adapter) {
		a.bucketNo = bucketNo
	}
}

// New creates a Jetmon v2 adapter. apiURL may be either the server root or the
// versioned API root; "/api/v1" is appended when it is absent.
func New(id, apiURL, token string, opts ...Option) *Adapter {
	a := &Adapter{
		id:     id,
		apiURL: normalizeAPIURL(apiURL),
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
	return adapter.Capabilities{
		MinCheckFrequency:          time.Minute,
		SupportsKeyword:            true,
		SupportsInvertedKeyword:    true,
		SupportsAgentChecks:        true,
		SupportsMaintenanceWindows: true,
		SupportsCooldownReset:      true,
		SupportsRequestHeaders:     true,
		DefaultMaxCallsPerRun:      0, // self-hosted internal API
	}
}

// Normalize implements adapter.Adapter.Normalize.
func (a *Adapter) Normalize(raw string) string {
	if v, ok := classification[raw]; ok {
		return v
	}
	return adapter.UnrecognizedClassification
}

type createSiteRequest struct {
	BlogID               int64             `json:"blog_id"`
	MonitorURL           string            `json:"monitor_url"`
	MonitorActive        bool              `json:"monitor_active"`
	BucketNo             int               `json:"bucket_no"`
	CheckKeyword         *string           `json:"check_keyword"`
	ForbiddenKeyword     *string           `json:"forbidden_keyword"`
	RedirectPolicy       string            `json:"redirect_policy"`
	TimeoutSeconds       *int              `json:"timeout_seconds"`
	CustomHeaders        map[string]string `json:"custom_headers"`
	AlertCooldownMinutes *int              `json:"alert_cooldown_minutes"`
	CheckInterval        int               `json:"check_interval"`
}

type siteResponse struct {
	ID                   int64   `json:"id"`
	BlogID               int64   `json:"blog_id"`
	MonitorURL           string  `json:"monitor_url"`
	MonitorActive        bool    `json:"monitor_active"`
	BucketNo             int     `json:"bucket_no"`
	CheckInterval        int     `json:"check_interval"`
	CurrentState         string  `json:"current_state"`
	CurrentSeverity      uint8   `json:"current_severity"`
	ActiveEventID        *int64  `json:"active_event_id"`
	LastCheckedAt        *string `json:"last_checked_at"`
	LastStatusChangeAt   *string `json:"last_status_change_at"`
	CheckKeyword         *string `json:"check_keyword"`
	ForbiddenKeyword     *string `json:"forbidden_keyword"`
	RedirectPolicy       string  `json:"redirect_policy"`
	MaintenanceStart     *string `json:"maintenance_start"`
	MaintenanceEnd       *string `json:"maintenance_end"`
	AlertCooldownMinutes *int    `json:"alert_cooldown_minutes"`
}

type updateSiteRequest struct {
	MaintenanceStart *string `json:"maintenance_start,omitempty"`
	MaintenanceEnd   *string `json:"maintenance_end,omitempty"`
}

// Provision creates a synthetic Jetmon site for this benchmark run.
func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiURL == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: url is not configured")
	}
	if a.token == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: token is not configured")
	}
	checkInterval, err := checkIntervalMinutes(config.CheckFrequency)
	if err != nil {
		return adapter.MonitorHandle{}, err
	}
	checkKeyword, forbiddenKeyword := keywordRules(config)
	customHeaders := config.RequestHeaders
	if customHeaders == nil {
		customHeaders = map[string]string{}
	}

	var lastErr error
	for attempt := 0; attempt < maxCreateAttempts; attempt++ {
		blogID, err := randomSyntheticBlogID()
		if err != nil {
			return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: generate synthetic blog_id: %w", err)
		}

		cooldown := 0
		req := createSiteRequest{
			BlogID:               blogID,
			MonitorURL:           target.URL,
			MonitorActive:        true,
			BucketNo:             a.bucketNo,
			CheckKeyword:         checkKeyword,
			ForbiddenKeyword:     forbiddenKeyword,
			RedirectPolicy:       "follow",
			CustomHeaders:        customHeaders,
			AlertCooldownMinutes: &cooldown,
			CheckInterval:        checkInterval,
		}

		var resp siteResponse
		err = a.do(ctx, http.MethodPost, "/sites", req, &resp, map[string]string{
			"Idempotency-Key": fmt.Sprintf("uptime-bench-jetmon-v2-%d", blogID),
		})
		if statusErr, ok := err.(*statusError); ok && statusErr.StatusCode == http.StatusConflict {
			lastErr = err
			continue
		}
		if err != nil {
			return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: POST /sites: %w", err)
		}

		handle := monitorHandle(a.id, resp)
		if handle.MonitorID == "" {
			return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: POST /sites: response missing site id")
		}

		if config.MaintenanceWindow != nil {
			if err := a.applyMaintenance(ctx, handle.MonitorID, config.MaintenanceWindow); err != nil {
				_ = a.Deprovision(context.Background(), handle)
				return adapter.MonitorHandle{}, err
			}
		}
		return handle, nil
	}

	return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v2: could not allocate unused synthetic blog_id after %d attempts: %w",
		maxCreateAttempts, lastErr)
}

func (a *Adapter) applyMaintenance(ctx context.Context, siteID string, window *adapter.MaintenanceWindow) error {
	start := window.Start.UTC().Format(time.RFC3339)
	end := window.End.UTC().Format(time.RFC3339)
	req := updateSiteRequest{
		MaintenanceStart: &start,
		MaintenanceEnd:   &end,
	}
	path := "/sites/" + url.PathEscape(siteID)
	if err := a.do(ctx, http.MethodPatch, path, req, nil, nil); err != nil {
		return fmt.Errorf("jetmon-v2: PATCH %s (maintenance): %w", path, err)
	}
	return nil
}

type eventsResponse struct {
	Data []eventResponse `json:"data"`
	Page pageResponse    `json:"page"`
}

type pageResponse struct {
	Next  *string `json:"next"`
	Limit int     `json:"limit"`
}

type eventResponse struct {
	ID               int64           `json:"id"`
	SiteID           int64           `json:"site_id"`
	EndpointID       *int64          `json:"endpoint_id"`
	CheckType        string          `json:"check_type"`
	Discriminator    *string         `json:"discriminator"`
	Severity         uint8           `json:"severity"`
	State            string          `json:"state"`
	StartedAt        string          `json:"started_at"`
	EndedAt          *string         `json:"ended_at"`
	ResolutionReason *string         `json:"resolution_reason"`
	CauseEventID     *int64          `json:"cause_event_id"`
	Metadata         json.RawMessage `json:"metadata"`
	DurationMs       int64           `json:"duration_ms"`
	TransitionCount  int             `json:"transition_count"`
}

// Retrieve fetches Jetmon v2 HTTP events for the run window.
func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.apiURL == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon-v2: url is not configured",
		}, nil
	}
	if a.token == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon-v2: token is not configured",
		}, nil
	}
	siteID := handle.MonitorID
	if siteID == "" {
		siteID = handle.Fields["blog_id"]
	}
	if siteID == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon-v2: handle missing site id",
		}, nil
	}

	events, err := a.fetchEvents(ctx, siteID, window)
	if err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon-v2: GET /sites/%s/events: %v", siteID, err),
		}, nil
	}

	now := time.Now()
	reports := make([]adapter.MonitorReport, 0, len(events)*2)
	for _, ev := range events {
		startedAt, ok := parseAPITime(ev.StartedAt)
		if ok && isReportableEvent(ev) {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertFired,
				RawClassification: rawClassification(ev),
				ReportedAt:        startedAt,
				RetrievedAt:       now,
				Metadata:          eventMetadata(ev),
			})
		}
		if ev.EndedAt != nil {
			endedAt, ok := parseAPITime(*ev.EndedAt)
			if ok && inRetrieveWindow(endedAt, window) {
				reports = append(reports, adapter.MonitorReport{
					EventType:         adapter.EventAlertResolved,
					RawClassification: "up",
					ReportedAt:        endedAt,
					RetrievedAt:       now,
					Metadata:          eventMetadata(ev),
				})
			}
		}
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

func (a *Adapter) fetchEvents(ctx context.Context, siteID string, window adapter.RunWindow) ([]eventResponse, error) {
	var (
		cursor string
		out    []eventResponse
	)
	for page := 0; page < maxEventPages; page++ {
		q := url.Values{}
		q.Set("limit", "200")
		q.Set("check_type__in", "http,tls_expiry,tls_deprecated")
		q.Set("started_at__gte", window.FailureStarted.UTC().Format(time.RFC3339))
		q.Set("started_at__lt", window.GracePeriodEnd.UTC().Format(time.RFC3339))
		if cursor != "" {
			q.Set("cursor", cursor)
		}

		path := "/sites/" + url.PathEscape(siteID) + "/events?" + q.Encode()
		var resp eventsResponse
		if err := a.do(ctx, http.MethodGet, path, nil, &resp, nil); err != nil {
			return nil, err
		}
		out = append(out, resp.Data...)
		if resp.Page.Next == nil || *resp.Page.Next == "" {
			return out, nil
		}
		cursor = *resp.Page.Next
	}
	return nil, fmt.Errorf("event pagination exceeded %d pages", maxEventPages)
}

// Deprovision soft-deletes the synthetic Jetmon site and closes any active
// events. A missing site is treated as success so cleanup is idempotent.
func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	siteID := handle.MonitorID
	if siteID == "" {
		siteID = handle.Fields["blog_id"]
	}
	if siteID == "" {
		return nil
	}
	if a.token == "" {
		return fmt.Errorf("jetmon-v2: token is not configured")
	}
	if a.apiURL == "" {
		return fmt.Errorf("jetmon-v2: url is not configured")
	}

	path := "/sites/" + url.PathEscape(siteID)
	if err := a.do(ctx, http.MethodDelete, path, nil, nil, nil); err != nil {
		if statusErr, ok := err.(*statusError); ok && statusErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return fmt.Errorf("jetmon-v2: DELETE %s: %w", path, err)
	}
	return nil
}

type statusError struct {
	StatusCode int
	Body       string
}

func (e *statusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("status %d", e.StatusCode)
	}
	return fmt.Sprintf("status %d: %s", e.StatusCode, e.Body)
}

func (a *Adapter) do(ctx context.Context, method, path string, body, out any, headers map[string]string) error {
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
	for k, v := range headers {
		req.Header.Set(k, v)
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
		return &statusError{StatusCode: resp.StatusCode, Body: truncate(string(respBody), 200)}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode: %w (body=%s)", err, truncate(string(respBody), 200))
	}
	return nil
}

func monitorHandle(serviceID string, site siteResponse) adapter.MonitorHandle {
	siteID := site.ID
	if siteID == 0 {
		siteID = site.BlogID
	}
	if siteID == 0 {
		return adapter.MonitorHandle{ServiceID: serviceID}
	}
	id := strconv.FormatInt(siteID, 10)
	fields := map[string]string{
		"blog_id": id,
	}
	if site.MonitorURL != "" {
		fields["monitor_url"] = site.MonitorURL
	}
	fields["bucket_no"] = strconv.Itoa(site.BucketNo)
	fields["check_interval"] = strconv.Itoa(site.CheckInterval)
	return adapter.MonitorHandle{
		ServiceID: serviceID,
		MonitorID: id,
		Fields:    fields,
	}
}

func checkIntervalMinutes(d time.Duration) (int, error) {
	if d < time.Minute {
		return 0, &adapter.FrequencyError{Requested: d, MinAchievable: time.Minute}
	}
	if d%time.Minute != 0 {
		min := ((d / time.Minute) + 1) * time.Minute
		return 0, &adapter.FrequencyError{Requested: d, MinAchievable: min}
	}
	return int(d / time.Minute), nil
}

func keywordPtr(keyword string) *string {
	if keyword == "" {
		return nil
	}
	return &keyword
}

func keywordRules(config adapter.ProvisionConfig) (*string, *string) {
	if config.Keyword == "" {
		return nil, nil
	}
	if config.KeywordCheck == adapter.KeywordCheckAbsent {
		return nil, keywordPtr(config.Keyword)
	}
	return keywordPtr(config.Keyword), nil
}

func randomSyntheticBlogID() (int64, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(syntheticBlogIDRange))
	if err != nil {
		return 0, err
	}
	return syntheticBlogIDBase + n.Int64(), nil
}

func normalizeAPIURL(raw string) string {
	raw = strings.TrimRight(raw, "/")
	if raw == "" || strings.HasSuffix(raw, apiPathSuffix) {
		return raw
	}
	return raw + apiPathSuffix
}

func parseAPITime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func isFailureState(state string) bool {
	switch rawState(state) {
	case "seems_down", "down", "degraded":
		return true
	default:
		return false
	}
}

func isReportableEvent(ev eventResponse) bool {
	if isFailureState(ev.State) {
		return true
	}
	switch rawClassification(ev) {
	case "tls_deprecated", "tls_expiry":
		return true
	default:
		return false
	}
}

func rawClassification(ev eventResponse) string {
	if strings.EqualFold(ev.CheckType, "tls_expiry") {
		return "tls_expiry"
	}
	if strings.EqualFold(ev.CheckType, "tls_deprecated") {
		return "tls_deprecated"
	}
	if code, ok := metadataInt(ev.Metadata, "error_code"); ok {
		switch code {
		case jetmonErrorTimeout:
			return "timeout"
		case jetmonErrorConnect:
			return "connect"
		case jetmonErrorSSL:
			return "ssl"
		case jetmonErrorRedirect:
			return "redirect"
		case jetmonErrorKeyword:
			return "keyword"
		case jetmonErrorTLSExpired:
			return "tls_expired"
		case jetmonErrorTLSDeprecated:
			return "tls_deprecated"
		}
	}
	if httpCode, ok := metadataInt(ev.Metadata, "http_code"); ok {
		switch {
		case httpCode == http.StatusForbidden:
			return "blocked"
		case httpCode >= 500:
			return "server"
		case httpCode >= 400:
			return "client"
		}
	}
	return rawState(ev.State)
}

func rawState(state string) string {
	s := strings.ToLower(strings.TrimSpace(state))
	s = strings.ReplaceAll(s, " ", "_")
	if s == "" {
		return "unknown"
	}
	return s
}

func inRetrieveWindow(t time.Time, window adapter.RunWindow) bool {
	if window.GracePeriodEnd.IsZero() {
		return true
	}
	return !t.After(window.GracePeriodEnd.UTC())
}

func metadataInt(raw json.RawMessage, key string) (int, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, false
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return 0, false
	}
	v, ok := meta[key]
	if !ok {
		return 0, false
	}
	switch typed := v.(type) {
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func eventMetadata(ev eventResponse) map[string]any {
	meta := map[string]any{
		"event_id":         ev.ID,
		"site_id":          ev.SiteID,
		"check_type":       ev.CheckType,
		"severity":         ev.Severity,
		"state":            ev.State,
		"duration_ms":      ev.DurationMs,
		"transition_count": ev.TransitionCount,
	}
	if ev.EndpointID != nil {
		meta["endpoint_id"] = *ev.EndpointID
	}
	if ev.Discriminator != nil {
		meta["discriminator"] = *ev.Discriminator
	}
	if ev.ResolutionReason != nil {
		meta["resolution_reason"] = *ev.ResolutionReason
	}
	if ev.CauseEventID != nil {
		meta["cause_event_id"] = *ev.CauseEventID
	}
	if len(ev.Metadata) > 0 && string(ev.Metadata) != "null" {
		var decoded any
		if err := json.Unmarshal(ev.Metadata, &decoded); err == nil {
			meta["jetmon_metadata"] = decoded
		}
	}
	return meta
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
