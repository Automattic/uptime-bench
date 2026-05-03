// Package gatus implements the uptime-bench adapter for a self-hosted Gatus
// instance fronted by the narrow uptime-bench Gatus bridge.
//
// services.toml:
//
//	[[services]]
//	id      = "gatus"
//	type    = "gatus"
//	url     = "http://gatus-host:8081"
//	enabled = true
//	auth    = { token = "<bridge-token>", http_method = "GET" }
package gatus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

const (
	minCheckFrequency = time.Second
)

var classification = map[string]string{
	"content":   "content_failure",
	"timeout":   "timeout",
	"unhealthy": "http_failure",
	"resolved":  "recovered",
}

type Adapter struct {
	id         string
	bridgeURL  string
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

func New(id, bridgeURL, token string, opts ...Option) *Adapter {
	a := &Adapter{
		id:         id,
		bridgeURL:  strings.TrimRight(bridgeURL, "/"),
		token:      token,
		httpMethod: http.MethodGet,
		client:     &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	caps := adapter.Capabilities{
		MinCheckFrequency:             minCheckFrequency,
		MonitorKinds:                  []string{adapter.MonitorKindHTTP},
		SupportsCooldownReset:         true,
		SupportsResponseTimeThreshold: true,
		SupportsRequestHeaders:        true,
		DefaultMaxCallsPerRun:         0,
	}
	if a.httpMethod != http.MethodHead {
		caps.SupportsKeyword = true
		caps.SupportsInvertedKeyword = true
	}
	return caps
}

func (a *Adapter) Normalize(raw string) string {
	if strings.HasPrefix(raw, "http_") {
		return "http_failure"
	}
	if v, ok := classification[raw]; ok {
		return v
	}
	return adapter.UnrecognizedClassification
}

type createRequest struct {
	ID                      string            `json:"id"`
	Name                    string            `json:"name,omitempty"`
	URL                     string            `json:"url"`
	IntervalSeconds         int               `json:"interval_seconds"`
	Method                  string            `json:"method"`
	Keyword                 string            `json:"keyword,omitempty"`
	KeywordCheck            string            `json:"keyword_check,omitempty"`
	ResponseTimeThresholdMS int               `json:"response_time_threshold_ms,omitempty"`
	Headers                 map[string]string `json:"headers,omitempty"`
}

type createResponse struct {
	ID        string `json:"id"`
	MonitorID string `json:"monitor_id"`
}

type statusResponse struct {
	EndpointKey string         `json:"endpoint_key"`
	Results     []statusResult `json:"results"`
}

type statusResult struct {
	Status           int               `json:"status"`
	Success          bool              `json:"success"`
	Timestamp        string            `json:"timestamp"`
	ConditionResults []conditionResult `json:"conditionResults"`
}

type conditionResult struct {
	Condition string `json:"condition"`
	Success   bool   `json:"success"`
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.bridgeURL == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("gatus: url is not configured")
	}
	if a.token == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("gatus: auth.token is required")
	}
	if config.CheckFrequency > 0 && config.CheckFrequency < minCheckFrequency {
		return adapter.MonitorHandle{}, &adapter.FrequencyError{Requested: config.CheckFrequency, MinAchievable: minCheckFrequency}
	}
	if a.httpMethod == http.MethodHead && config.Keyword != "" {
		return adapter.MonitorHandle{}, fmt.Errorf("gatus: keyword monitoring requires a response body; http_method HEAD is not supported")
	}
	id, err := monitorID(target.ID)
	if err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("gatus: monitor id: %w", err)
	}
	req := createRequest{
		ID:              id,
		Name:            "uptime-bench:" + id,
		URL:             target.URL,
		IntervalSeconds: intervalSeconds(config.CheckFrequency),
		Method:          a.httpMethod,
		Keyword:         config.Keyword,
		KeywordCheck:    config.KeywordCheck,
		Headers:         config.RequestHeaders,
	}
	if req.KeywordCheck == "" {
		req.KeywordCheck = adapter.KeywordCheckPresent
	}
	if config.ResponseTimeThreshold > 0 {
		req.ResponseTimeThresholdMS = int(config.ResponseTimeThreshold / time.Millisecond)
	}

	var resp createResponse
	if err := a.do(ctx, http.MethodPost, "/monitors", req, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("gatus: POST /monitors: %w", err)
	}
	if resp.ID == "" {
		resp.ID = id
	}
	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: resp.ID,
		Fields: map[string]string{
			"bridge_monitor_id": resp.MonitorID,
			"http_method":       a.httpMethod,
		},
	}, nil
}

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if handle.MonitorID == "" {
		return adapter.RetrieveResult{Status: adapter.RetrieveUnknown, Reason: "missing monitor id", ReasonCode: adapter.ReasonAdapterError}, nil
	}
	var resp statusResponse
	if err := a.do(ctx, http.MethodGet, "/monitors/"+url.PathEscape(handle.MonitorID)+"/statuses", nil, &resp); err != nil {
		return adapter.RetrieveResult{Status: adapter.RetrieveUnknown, Reason: fmt.Sprintf("gatus: retrieve statuses: %v", err), ReasonCode: adapter.ReasonAdapterError}, nil
	}
	results := resp.Results
	sort.Slice(results, func(i, j int) bool {
		return parseGatusTime(results[i].Timestamp).Before(parseGatusTime(results[j].Timestamp))
	})

	reports := make([]adapter.MonitorReport, 0, 2)
	var fired bool
	for _, r := range results {
		t := parseGatusTime(r.Timestamp)
		if t.IsZero() || t.Before(window.FailureStarted) || t.After(window.GracePeriodEnd) {
			continue
		}
		if !r.Success && !fired {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertFired,
				RawClassification: rawClassification(r),
				ReportedAt:        t,
				RetrievedAt:       time.Now(),
				Metadata: map[string]any{
					"http_code": r.Status,
				},
			})
			fired = true
			continue
		}
		if fired && r.Success && !t.Before(window.FailureEnded) {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertResolved,
				RawClassification: "resolved",
				ReportedAt:        t,
				RetrievedAt:       time.Now(),
				Metadata: map[string]any{
					"http_code": r.Status,
				},
			})
			break
		}
	}
	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
		Metadata: map[string]any{
			"endpoint_key": resp.EndpointKey,
		},
	}, nil
}

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if handle.MonitorID == "" {
		return nil
	}
	if err := a.do(ctx, http.MethodDelete, "/monitors/"+url.PathEscape(handle.MonitorID), nil, nil); err != nil {
		return fmt.Errorf("gatus: DELETE /monitors/%s: %w", handle.MonitorID, err)
	}
	return nil
}

type listResponse struct {
	Monitors []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"monitors"`
}

func (a *Adapter) CleanupStale(ctx context.Context, opts adapter.CleanupOptions) (adapter.CleanupResult, error) {
	var resp listResponse
	if err := a.do(ctx, http.MethodGet, "/monitors", nil, &resp); err != nil {
		return adapter.CleanupResult{}, fmt.Errorf("gatus: GET /monitors: %w", err)
	}
	result := adapter.CleanupResult{}
	for _, m := range resp.Monitors {
		if !strings.HasPrefix(m.Name, "uptime-bench_") || m.Name == "uptime-bench-bootstrap" {
			continue
		}
		id := strings.TrimPrefix(m.Name, "uptime-bench_")
		candidate := adapter.CleanupCandidate{
			ServiceID:  a.id,
			ResourceID: id,
			Kind:       "endpoint",
			Name:       m.Name,
			URL:        m.URL,
			Reason:     "benchmark-owned Gatus endpoint",
		}
		if !opts.Scope.MatchesURL(m.URL) {
			candidate.Reason = "outside cleanup scope"
			result.Actions = append(result.Actions, adapter.CleanupAction{Candidate: candidate, Action: adapter.CleanupActionSkipped})
			continue
		}
		if opts.DryRun {
			result.Actions = append(result.Actions, adapter.CleanupAction{Candidate: candidate, Action: adapter.CleanupActionWouldDelete})
			continue
		}
		if err := a.Deprovision(ctx, adapter.MonitorHandle{ServiceID: a.id, MonitorID: id}); err != nil {
			result.Actions = append(result.Actions, adapter.CleanupAction{Candidate: candidate, Action: adapter.CleanupActionError, Error: err.Error()})
			continue
		}
		result.Actions = append(result.Actions, adapter.CleanupAction{Candidate: candidate, Action: adapter.CleanupActionDeleted})
	}
	return result, nil
}

func (a *Adapter) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.bridgeURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func normalizeHTTPMethod(method string) string {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "", http.MethodGet:
		return http.MethodGet
	case http.MethodHead:
		return http.MethodHead
	default:
		return strings.ToUpper(strings.TrimSpace(method))
	}
}

func intervalSeconds(d time.Duration) int {
	if d <= 0 {
		return int(time.Minute / time.Second)
	}
	seconds := int(d / time.Second)
	if d%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func monitorID(targetID string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return '-'
	}, targetID)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		clean = "target"
	}
	return clean + "-" + hex.EncodeToString(b[:]), nil
}

func parseGatusTime(raw string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}

func rawClassification(r statusResult) string {
	for _, cr := range r.ConditionResults {
		if cr.Success {
			continue
		}
		switch {
		case strings.Contains(cr.Condition, "[BODY]"):
			return "content"
		case strings.Contains(cr.Condition, "[RESPONSE_TIME]"):
			return "timeout"
		case strings.Contains(cr.Condition, "[STATUS]"):
			if r.Status > 0 {
				return fmt.Sprintf("http_%d", r.Status)
			}
			return "unhealthy"
		}
	}
	if r.Status > 0 {
		return fmt.Sprintf("http_%d", r.Status)
	}
	return "unhealthy"
}
