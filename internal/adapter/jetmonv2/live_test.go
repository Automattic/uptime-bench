//go:build live

// Package jetmonv2 live test. Run with:
//
//	JETMON_V2_API_URL=http://jetmon-host:8081/api/v1 \
//	JETMON_V2_TOKEN=jm_... \
//	JETMON_V2_TARGET_URL=http://your-target.example/ \
//	JETMON_V2_BUCKET_NO=0 \
//	go test -tags live -run Live ./internal/adapter/jetmonv2/ -v
//
// Without the `live` build tag this file is skipped entirely. The test creates
// a synthetic Jetmon site, queries events over a recent window, and soft-deletes
// the site. Cleanup is deferred so a failure between Provision and Deprovision
// still removes the synthetic site.
package jetmonv2

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

type liveConfig struct {
	apiURL    string
	token     string
	targetURL string
	bucketNo  int
}

func loadLiveConfig(t *testing.T) liveConfig {
	t.Helper()
	apiURL := os.Getenv("JETMON_V2_API_URL")
	token := os.Getenv("JETMON_V2_TOKEN")
	targetURL := os.Getenv("JETMON_V2_TARGET_URL")
	if apiURL == "" || token == "" || targetURL == "" {
		t.Skip("set JETMON_V2_API_URL, JETMON_V2_TOKEN, and JETMON_V2_TARGET_URL to run live test")
	}
	bucketNo := 0
	if raw := os.Getenv("JETMON_V2_BUCKET_NO"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("JETMON_V2_BUCKET_NO must be an integer: %v", err)
		}
		bucketNo = parsed
	}
	return liveConfig{
		apiURL:    normalizeAPIURL(apiURL),
		token:     token,
		targetURL: targetURL,
		bucketNo:  bucketNo,
	}
}

func liveAdapter(t *testing.T) (*Adapter, string) {
	t.Helper()
	cfg := loadLiveConfig(t)
	return New("jetmon-v2-live", cfg.apiURL, cfg.token, WithBucketNo(cfg.bucketNo)), cfg.targetURL
}

func TestLiveProvisionRetrieveDeprovision(t *testing.T) {
	a, targetURL := liveAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	target := adapter.Target{ID: "smoketest-bench-a", URL: targetURL}
	cfg := adapter.ProvisionConfig{CheckFrequency: time.Minute}

	t.Logf("Provision: creating Jetmon v2 site for %s", targetURL)
	handle, err := a.Provision(ctx, target, cfg)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision: handle = %+v", handle)

	defer func() {
		t.Logf("Deprovision: soft-deleting site %s", handle.MonitorID)
		if err := a.Deprovision(context.Background(), handle); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	if handle.MonitorID == "" {
		t.Fatal("Provision returned empty MonitorID")
	}
	if handle.ServiceID != "jetmon-v2-live" {
		t.Errorf("ServiceID = %q, want jetmon-v2-live", handle.ServiceID)
	}

	window := adapter.RunWindow{
		FailureStarted: time.Now().Add(-24 * time.Hour),
		FailureEnded:   time.Now(),
		GracePeriodEnd: time.Now(),
	}
	res, err := a.Retrieve(ctx, handle, window)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	t.Logf("Retrieve: status=%s reports=%d reason=%q", res.Status, len(res.Reports), res.Reason)
	if res.Status == adapter.RetrieveUnknown {
		t.Errorf("Retrieve returned Unknown: %s", res.Reason)
	}
	for i, r := range res.Reports {
		t.Logf("  report[%d]: %s raw=%q at=%s", i, r.EventType, r.RawClassification, r.ReportedAt.Format(time.RFC3339))
	}
}

func TestLiveAPIContract(t *testing.T) {
	cfg := loadLiveConfig(t)
	a := New("jetmon-v2-live-contract", cfg.apiURL, cfg.token, WithBucketNo(cfg.bucketNo))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	assertLiveGETStatus(t, ctx, cfg, "/health", "", http.StatusOK)

	status, openAPI := liveGET(t, ctx, cfg, "/openapi.json", cfg.token)
	if status != http.StatusOK {
		t.Fatalf("GET /openapi.json = %d, want 200 (body=%s)", status, truncate(openAPI, 200))
	}
	for _, field := range []string{`"bucket_no"`, `"check_interval"`, `"/api/v1/sites/{id}"`, `"/api/v1/sites/{id}/events"`} {
		if !strings.Contains(openAPI, field) {
			t.Fatalf("OpenAPI document missing %s", field)
		}
	}

	assertLiveGETStatus(t, ctx, cfg, "/openapi.json", "", http.StatusUnauthorized)
	assertLiveGETStatus(t, ctx, cfg, "/openapi.json", "invalid-token", http.StatusUnauthorized)

	status, me := liveGET(t, ctx, cfg, "/me", cfg.token)
	if status != http.StatusOK {
		t.Fatalf("GET /me = %d, want 200 (body=%s)", status, truncate(me, 200))
	}
	for _, field := range []string{`"consumer_name"`, `"scope"`, `"rate_limit_per_minute"`} {
		if !strings.Contains(me, field) {
			t.Fatalf("/me response missing %s", field)
		}
	}

	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	handle, err := a.Provision(ctx,
		adapter.Target{ID: "contract-bench-a", URL: cfg.targetURL},
		adapter.ProvisionConfig{
			CheckFrequency: 3 * time.Minute,
			Keyword:        "uptime-bench-contract-canary",
			KeywordCheck:   adapter.KeywordCheckPresent,
			MaintenanceWindow: &adapter.MaintenanceWindow{
				Start: start,
				End:   end,
			},
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			if err := a.Deprovision(context.Background(), handle); err != nil {
				t.Errorf("cleanup Deprovision: %v", err)
			}
		}
	}()

	if handle.Fields["bucket_no"] != strconv.Itoa(cfg.bucketNo) {
		t.Fatalf("handle bucket_no = %q, want %d", handle.Fields["bucket_no"], cfg.bucketNo)
	}
	if handle.Fields["check_interval"] != "3" {
		t.Fatalf("handle check_interval = %q, want 3", handle.Fields["check_interval"])
	}

	site := liveSite(t, ctx, cfg, handle.MonitorID)
	assertLiveSite(t, site, cfg, true, 3, start, end)

	if err := a.Deprovision(ctx, handle); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	cleaned = true

	site = liveSite(t, ctx, cfg, handle.MonitorID)
	if site.MonitorActive {
		t.Fatalf("site monitor_active after delete = true, want false")
	}
	if err := a.Deprovision(ctx, handle); err != nil {
		t.Fatalf("second Deprovision should be idempotent: %v", err)
	}
}

func assertLiveGETStatus(t *testing.T, ctx context.Context, cfg liveConfig, path, token string, want int) {
	t.Helper()
	got, body := liveGET(t, ctx, cfg, path, token)
	if got != want {
		t.Fatalf("GET %s = %d, want %d (body=%s)", path, got, want, truncate(body, 200))
	}
}

func liveGET(t *testing.T, ctx context.Context, cfg liveConfig, path, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.apiURL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read GET %s response: %v", path, err)
	}
	return resp.StatusCode, string(body)
}

func liveSite(t *testing.T, ctx context.Context, cfg liveConfig, siteID string) siteResponse {
	t.Helper()
	path := "/sites/" + url.PathEscape(siteID)
	status, body := liveGET(t, ctx, cfg, path, cfg.token)
	if status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body=%s)", path, status, truncate(body, 200))
	}
	for _, field := range []string{`"bucket_no"`, `"check_interval"`} {
		if !strings.Contains(body, field) {
			t.Fatalf("GET %s response missing %s", path, field)
		}
	}
	var site siteResponse
	if err := json.Unmarshal([]byte(body), &site); err != nil {
		t.Fatalf("decode GET %s response: %v", path, err)
	}
	return site
}

func assertLiveSite(t *testing.T, site siteResponse, cfg liveConfig, active bool, checkInterval int, start, end time.Time) {
	t.Helper()
	if site.MonitorURL != cfg.targetURL {
		t.Fatalf("monitor_url = %q, want %q", site.MonitorURL, cfg.targetURL)
	}
	if site.MonitorActive != active {
		t.Fatalf("monitor_active = %v, want %v", site.MonitorActive, active)
	}
	if site.BucketNo != cfg.bucketNo {
		t.Fatalf("bucket_no = %d, want %d", site.BucketNo, cfg.bucketNo)
	}
	if site.CheckInterval != checkInterval {
		t.Fatalf("check_interval = %d, want %d", site.CheckInterval, checkInterval)
	}
	if site.CheckKeyword == nil || *site.CheckKeyword != "uptime-bench-contract-canary" {
		t.Fatalf("check_keyword = %v, want uptime-bench-contract-canary", site.CheckKeyword)
	}
	if site.RedirectPolicy != "follow" {
		t.Fatalf("redirect_policy = %q, want follow", site.RedirectPolicy)
	}
	if site.AlertCooldownMinutes == nil || *site.AlertCooldownMinutes != 0 {
		t.Fatalf("alert_cooldown_minutes = %v, want 0", site.AlertCooldownMinutes)
	}
	if site.MaintenanceStart == nil || *site.MaintenanceStart != start.Format(time.RFC3339) {
		t.Fatalf("maintenance_start = %v, want %s", site.MaintenanceStart, start.Format(time.RFC3339))
	}
	if site.MaintenanceEnd == nil || *site.MaintenanceEnd != end.Format(time.RFC3339) {
		t.Fatalf("maintenance_end = %v, want %s", site.MaintenanceEnd, end.Format(time.RFC3339))
	}
}
