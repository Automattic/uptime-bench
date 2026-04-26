// Package datadog implements the uptime-bench adapter for Datadog Synthetics.
//
// API: https://docs.datadoghq.com/api/latest/synthetics/  (v1)
//
// Wire conventions (different from every other adapter so far):
//
//   - Auth uses two headers, not Bearer: DD-API-KEY and DD-APPLICATION-KEY
//   - Test creation: POST /api/v1/synthetics/tests/api
//   - Result retrieval: GET /api/v1/synthetics/tests/{public_id}/results
//   - Test deletion: POST /api/v1/synthetics/tests/delete (yes, POST with
//     a body; there is no DELETE endpoint for synthetic tests)
//   - public_id is the test's stable identifier, e.g. "abc-def-ghi"
//
// services.toml:
//
//	[[services]]
//	id      = "datadog-synthetics"
//	type    = "datadog-synthetics"
//	enabled = true
//	auth    = { api_key = "<DD-API-KEY>", app_key = "<DD-APPLICATION-KEY>" }
//
// `url` is optional; the default endpoint is https://api.datadoghq.com.
//
// Status: implemented against the public API documentation. The wire
// shapes are pinned by unit tests using httptest, and a full
// Provision/Retrieve/Deprovision cycle has been exercised against the
// live Datadog Synthetics API via the build-tagged smoke test in
// `live_test.go`.
package datadog

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

// DefaultAPIURL is used when services.toml omits `url`. Datadog has
// region-specific endpoints (datadoghq.com for US, datadoghq.eu for EU,
// us3/us5/ap1.datadoghq.com for other regions); operators on non-US
// accounts must override `url` accordingly.
const DefaultAPIURL = "https://api.datadoghq.com"

// classification maps the strings Datadog returns in synthetic check events
// to uptime-bench's normalized vocabulary.
var classification = map[string]string{
	"Alert":     "http_failure",
	"Triggered": "http_failure",
	"Recovered": "recovered",
	"No Data":   "unknown",
	"Warn":      "unknown",
}

// Adapter implements adapter.Adapter for Datadog Synthetics.
type Adapter struct {
	id     string
	apiURL string
	apiKey string
	appKey string
	client *http.Client
}

// New creates a Datadog Synthetics adapter.
func New(id, apiURL, apiKey, appKey string) *Adapter {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	return &Adapter{
		id:     id,
		apiURL: strings.TrimRight(apiURL, "/"),
		apiKey: apiKey,
		appKey: appKey,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		// Datadog Synthetics minimum is 30 seconds.
		MinCheckFrequency:       30 * time.Second,
		SupportsKeyword:         true,
		SupportsInvertedKeyword: true, // body assertion with operator=doesNotContain
		SupportsAgentChecks:     false,
		DefaultMaxCallsPerRun:   100, // generous; Datadog rate limits per endpoint
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

// newTestRequest mirrors POST /api/v1/synthetics/tests/api. Datadog's
// synthetic-test schema is rich; for our purposes we create the simplest
// possible API-type test: a single GET to target.URL, with assertions
// that the response status code is 200.
type newTestRequest struct {
	Type      string      `json:"type"`    // "api"
	Subtype   string      `json:"subtype"` // "http"
	Name      string      `json:"name"`
	Message   string      `json:"message"` // required by API; alert notification body
	Status    string      `json:"status"`  // "live" to enable on creation
	Locations []string    `json:"locations"`
	Config    testConfig  `json:"config"`
	Options   testOptions `json:"options"`
	Tags      []string    `json:"tags,omitempty"`
}

type testConfig struct {
	Request    testRequest     `json:"request"`
	Assertions []testAssertion `json:"assertions"`
}

type testRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

// testAssertion describes one validation Datadog applies to the response.
// We use it for two cases:
//
//   - Status-code check: Type=statusCode, Operator=is, Target=200 (int).
//   - Body keyword check: Type=body, Operator=contains|doesNotContain,
//     Target=<keyword> (string).
//
// Target is `any` because Datadog uses different concrete types per
// assertion (int for statusCode, string for body); the JSON encoder
// preserves both correctly.
type testAssertion struct {
	Type     string `json:"type"`
	Operator string `json:"operator"`
	Target   any    `json:"target"`
}

type testOptions struct {
	TickEvery int `json:"tick_every"` // seconds
}

type newTestResponse struct {
	PublicID string   `json:"public_id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Errors   []string `json:"errors,omitempty"` // sometimes returned as a flat list
	ErrorMsg string   `json:"error,omitempty"`  // sometimes a single string
}

// tickEverySeconds rounds the requested frequency down to a value Datadog
// accepts. Allowed: 30, 60, 300, 900, 1800, 3600, 21600. Anything below
// 30 clamps to 30; anything above 21600 (6 hours) clamps to 21600.
func tickEverySeconds(d time.Duration) int {
	s := int(d.Round(time.Second).Seconds())
	tiers := []int{30, 60, 300, 900, 1800, 3600, 21600}
	if s <= tiers[0] {
		return tiers[0]
	}
	for i := len(tiers) - 1; i >= 0; i-- {
		if s >= tiers[i] {
			return tiers[i]
		}
	}
	return tiers[0]
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiKey == "" || a.appKey == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("datadog: api_key and app_key are both required")
	}

	assertions := []testAssertion{
		{Type: "statusCode", Operator: "is", Target: 200},
	}
	if config.Keyword != "" {
		switch config.KeywordCheck {
		case adapter.KeywordCheckPresent, "":
			assertions = append(assertions, testAssertion{
				Type: "body", Operator: "contains", Target: config.Keyword,
			})
		case adapter.KeywordCheckAbsent:
			assertions = append(assertions, testAssertion{
				Type: "body", Operator: "doesNotContain", Target: config.Keyword,
			})
		default:
			return adapter.MonitorHandle{}, fmt.Errorf("datadog: unsupported KeywordCheck %q", config.KeywordCheck)
		}
	}

	req := newTestRequest{
		Type:      "api",
		Subtype:   "http",
		Name:      "uptime-bench: " + target.ID,
		Message:   "uptime-bench synthetic test for " + target.ID + " failed.",
		Status:    "live",
		Locations: []string{"aws:us-east-1"}, // most accounts have this; operators on EU/etc may need to override via tags
		Config: testConfig{
			Request: testRequest{
				Method: "GET",
				URL:    target.URL,
			},
			Assertions: assertions,
		},
		Options: testOptions{
			TickEvery: tickEverySeconds(config.CheckFrequency),
		},
		Tags: []string{"uptime-bench", "uptime-bench:" + target.ID},
	}

	var resp newTestResponse
	if err := a.do(ctx, http.MethodPost, "/api/v1/synthetics/tests/api", req, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("datadog: POST /synthetics/tests/api: %w", err)
	}
	if resp.PublicID == "" {
		msg := resp.ErrorMsg
		if msg == "" && len(resp.Errors) > 0 {
			msg = strings.Join(resp.Errors, "; ")
		}
		if msg == "" {
			msg = "response missing public_id"
		}
		return adapter.MonitorHandle{}, fmt.Errorf("datadog: POST /synthetics/tests/api: %s", msg)
	}
	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: resp.PublicID,
		Fields: map[string]string{
			"url": target.URL,
		},
	}, nil
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

// resultsResponse mirrors GET /api/v1/synthetics/tests/{public_id}/results.
// Each entry represents one execution of the test from one location.
type resultsResponse struct {
	Results []resultEntry `json:"results"`
	Errors  []string      `json:"errors,omitempty"`
}

type resultEntry struct {
	ResultID  string `json:"result_id"`
	CheckTime int64  `json:"check_time"` // milliseconds since epoch
	Status    int    `json:"status"`     // 0 = pass, 1 = fail
	Result    struct {
		EventType string `json:"eventType"`
	} `json:"result"`
}

// from-millis is the inclusive query parameter; to-millis is the upper
// bound. Datadog uses milliseconds since epoch for both.
func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.apiKey == "" || a.appKey == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "datadog: api_key and app_key are both required",
		}, nil
	}

	path := fmt.Sprintf("/api/v1/synthetics/tests/%s/results?from_ts=%d&to_ts=%d",
		handle.MonitorID,
		window.FailureStarted.UnixMilli(),
		window.GracePeriodEnd.UnixMilli(),
	)

	var resp resultsResponse
	if err := a.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("datadog: GET %s: %v", path, err),
		}, nil
	}
	if len(resp.Errors) > 0 {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("datadog: GET %s: %s", path, strings.Join(resp.Errors, "; ")),
		}, nil
	}

	// Synthetic results aren't pre-grouped into incidents; they're per-check
	// pass/fail samples. Coalesce the sequence into transitions: emit
	// alert_fired on the first fail after pass(es), alert_resolved on the
	// first pass after fail(s).
	//
	// prevStatus starts at 0 (pass) so an initial fail is correctly the
	// first alert_fired and an initial pass is a no-op.
	now := time.Now()
	reports := make([]adapter.MonitorReport, 0, len(resp.Results))
	prevStatus := 0
	for _, r := range resp.Results {
		if r.Status != 0 && r.Status != 1 {
			continue
		}
		if prevStatus == r.Status {
			continue
		}
		if r.Status == 1 {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertFired,
				RawClassification: "Alert",
				ReportedAt:        time.UnixMilli(r.CheckTime).UTC(),
				RetrievedAt:       now,
				Metadata: map[string]any{
					"result_id":  r.ResultID,
					"event_type": r.Result.EventType,
				},
			})
		} else {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertResolved,
				RawClassification: "Recovered",
				ReportedAt:        time.UnixMilli(r.CheckTime).UTC(),
				RetrievedAt:       now,
				Metadata: map[string]any{
					"result_id":  r.ResultID,
					"event_type": r.Result.EventType,
				},
			})
		}
		prevStatus = r.Status
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

// ─── Deprovision ────────────────────────────────────────────────────────────

// deleteTestsRequest is the body for POST /api/v1/synthetics/tests/delete.
// Datadog uses a POST with a JSON body for bulk deletion (the only API
// for removing synthetic tests).
type deleteTestsRequest struct {
	PublicIDs []string `json:"public_ids"`
}

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if a.apiKey == "" || a.appKey == "" {
		return fmt.Errorf("datadog: api_key and app_key are both required")
	}
	if handle.MonitorID == "" {
		return nil
	}

	req := deleteTestsRequest{PublicIDs: []string{handle.MonitorID}}
	if err := a.do(ctx, http.MethodPost, "/api/v1/synthetics/tests/delete", req, nil); err != nil {
		// 404 means the test is already gone — Deprovision is idempotent.
		if strings.Contains(err.Error(), "status 404") {
			return nil
		}
		return fmt.Errorf("datadog: POST /synthetics/tests/delete: %w", err)
	}
	return nil
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
	req.Header.Set("DD-API-KEY", a.apiKey)
	req.Header.Set("DD-APPLICATION-KEY", a.appKey)
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
	if out == nil || len(respBody) == 0 {
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
