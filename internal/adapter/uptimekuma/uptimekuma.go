// Package uptimekuma implements the uptime-bench adapter for a self-hosted
// Uptime Kuma instance fronted by the narrow uptime-bench Uptime Kuma bridge.
//
// services.toml:
//
//	[[services]]
//	id      = "uptime-kuma"
//	type    = "uptime-kuma"
//	url     = "http://uptime-kuma-host:3002"
//	enabled = true
//	auth    = { token = "<bridge-token>", http_method = "GET" }
package uptimekuma

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

const minCheckFrequency = 20 * time.Second

var classification = map[string]string{
	"http":     "http_failure",
	"keyword":  "content_failure",
	"timeout":  "timeout",
	"tls":      "tls_failure",
	"resolved": "recovered",
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
		MinCheckFrequency:      minCheckFrequency,
		MonitorKinds:           []string{adapter.MonitorKindHTTP},
		SupportsCooldownReset:  true,
		SupportsRequestHeaders: true,
		DefaultMaxCallsPerRun:  0,
	}
	if a.httpMethod != http.MethodHead {
		caps.SupportsKeyword = true
	}
	return caps
}

func (a *Adapter) Normalize(raw string) string {
	if v, ok := classification[raw]; ok {
		return v
	}
	return adapter.UnrecognizedClassification
}

type createRequest struct {
	ID              string            `json:"id"`
	Name            string            `json:"name,omitempty"`
	URL             string            `json:"url"`
	IntervalSeconds int               `json:"interval_seconds"`
	Method          string            `json:"method"`
	Keyword         string            `json:"keyword,omitempty"`
	KeywordCheck    string            `json:"keyword_check,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
}

type createResponse struct {
	ID        string `json:"id"`
	MonitorID string `json:"monitor_id"`
}

type beatsResponse struct {
	MonitorID string `json:"monitor_id"`
	Beats     []beat `json:"beats"`
}

type beat struct {
	Status  int    `json:"status"`
	Msg     string `json:"msg"`
	Time    string `json:"time"`
	EndTime string `json:"end_time"`
	Ping    int    `json:"ping"`
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.bridgeURL == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: url is not configured")
	}
	if a.token == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: auth.token is required")
	}
	if config.CheckFrequency > 0 && config.CheckFrequency < minCheckFrequency {
		return adapter.MonitorHandle{}, &adapter.FrequencyError{Requested: config.CheckFrequency, MinAchievable: minCheckFrequency}
	}
	if a.httpMethod == http.MethodHead && config.Keyword != "" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: keyword monitoring requires a response body; http_method HEAD is not supported")
	}
	if config.Keyword != "" && config.KeywordCheck == adapter.KeywordCheckAbsent {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: inverted keyword checks are not supported")
	}
	id := monitorID(target.ID)
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
	var resp createResponse
	if err := a.do(ctx, http.MethodPost, "/monitors", req, &resp); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: POST /monitors: %w", err)
	}
	if resp.MonitorID == "" || resp.MonitorID == "None" {
		return adapter.MonitorHandle{}, fmt.Errorf("uptime-kuma: POST /monitors response missing monitor_id")
	}
	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: resp.MonitorID,
		Fields: map[string]string{
			"target_id":   id,
			"http_method": a.httpMethod,
		},
	}, nil
}

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if handle.MonitorID == "" {
		return adapter.RetrieveResult{Status: adapter.RetrieveUnknown, Reason: "missing monitor id", ReasonCode: adapter.ReasonAdapterError}, nil
	}
	var resp beatsResponse
	if err := a.do(ctx, http.MethodGet, "/monitors/"+url.PathEscape(handle.MonitorID)+"/beats", nil, &resp); err != nil {
		return adapter.RetrieveResult{Status: adapter.RetrieveUnknown, Reason: fmt.Sprintf("uptime-kuma: retrieve beats: %v", err), ReasonCode: adapter.ReasonAdapterError}, nil
	}
	beats := resp.Beats
	sort.Slice(beats, func(i, j int) bool {
		return parseKumaTime(beats[i].Time).Before(parseKumaTime(beats[j].Time))
	})

	reports := make([]adapter.MonitorReport, 0, 2)
	var fired bool
	for _, b := range beats {
		t := parseKumaTime(b.Time)
		if t.IsZero() || t.Before(window.FailureStarted) || t.After(window.GracePeriodEnd) {
			continue
		}
		if b.Status == 0 && !fired {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertFired,
				RawClassification: rawClassification(b),
				ReportedAt:        t,
				RetrievedAt:       time.Now(),
				Metadata: map[string]any{
					"message": b.Msg,
					"ping_ms": b.Ping,
				},
			})
			fired = true
			continue
		}
		if fired && b.Status == 1 && !t.Before(window.FailureEnded) {
			reports = append(reports, adapter.MonitorReport{
				EventType:         adapter.EventAlertResolved,
				RawClassification: "resolved",
				ReportedAt:        t,
				RetrievedAt:       time.Now(),
				Metadata: map[string]any{
					"message": b.Msg,
					"ping_ms": b.Ping,
				},
			})
			break
		}
	}
	return adapter.RetrieveResult{Status: adapter.RetrieveKnown, Reports: reports}, nil
}

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if handle.MonitorID == "" {
		return nil
	}
	if err := a.do(ctx, http.MethodDelete, "/monitors/"+url.PathEscape(handle.MonitorID), nil, nil); err != nil {
		return fmt.Errorf("uptime-kuma: DELETE /monitors/%s: %w", handle.MonitorID, err)
	}
	return nil
}

type listResponse struct {
	Monitors []map[string]any `json:"monitors"`
}

func (a *Adapter) CleanupStale(ctx context.Context, opts adapter.CleanupOptions) (adapter.CleanupResult, error) {
	var resp listResponse
	if err := a.do(ctx, http.MethodGet, "/monitors", nil, &resp); err != nil {
		return adapter.CleanupResult{}, fmt.Errorf("uptime-kuma: GET /monitors: %w", err)
	}
	result := adapter.CleanupResult{}
	for _, m := range resp.Monitors {
		name := stringValue(m["name"])
		if !strings.HasPrefix(name, "uptime-bench:") {
			continue
		}
		id := stringValue(m["id"])
		if id == "" {
			id = stringValue(m["monitorID"])
		}
		if id == "" {
			continue
		}
		rawURL := stringValue(m["url"])
		candidate := adapter.CleanupCandidate{
			ServiceID:  a.id,
			ResourceID: id,
			Kind:       "monitor",
			Name:       name,
			URL:        rawURL,
			Reason:     "benchmark-owned Uptime Kuma monitor",
		}
		if !opts.Scope.MatchesURL(rawURL) {
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
		return fmt.Errorf("status %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(data)), 200))
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
	if seconds < int(minCheckFrequency/time.Second) {
		seconds = int(minCheckFrequency / time.Second)
	}
	return seconds
}

func monitorID(targetID string) string {
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
	return clean + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func parseKumaTime(raw string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.000",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
	} {
		t, err := time.ParseInLocation(layout, raw, time.UTC)
		if err == nil {
			return t
		}
	}
	return time.Time{}
}

func rawClassification(b beat) string {
	msg := strings.ToLower(b.Msg)
	switch {
	case strings.Contains(msg, "keyword"):
		return "keyword"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "tls"), strings.Contains(msg, "ssl"), strings.Contains(msg, "certificate"):
		return "tls"
	default:
		return "http"
	}
}

func stringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case int:
		return strconv.Itoa(t)
	default:
		return ""
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
