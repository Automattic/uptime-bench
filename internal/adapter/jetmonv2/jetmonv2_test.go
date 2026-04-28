package jetmonv2

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapter/adaptertest"
)

type captured struct {
	method      string
	path        string
	query       string
	auth        string
	contentType string
	idempotency string
	body        []byte
}

func fakeAPI(t *testing.T, c *captured, status int, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.auth = r.Header.Get("Authorization")
		c.contentType = r.Header.Get("Content-Type")
		c.idempotency = r.Header.Get("Idempotency-Key")
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func newTestAdapter(srvURL, token string) *Adapter {
	a := New("jetmon-v2", srvURL, token, WithBucketNo(17))
	a.client = http.DefaultClient
	return a
}

func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}

func TestServiceID(t *testing.T) {
	a := New("custom-id", "http://x", "tok")
	if a.ServiceID() != "custom-id" {
		t.Fatalf("ServiceID = %q, want custom-id", a.ServiceID())
	}
}

func TestCapabilities(t *testing.T) {
	c := newTestAdapter("http://x", "tok").Capabilities()
	if c.MinCheckFrequency != time.Minute {
		t.Errorf("MinCheckFrequency = %v, want 1m", c.MinCheckFrequency)
	}
	if !c.SupportsKeyword {
		t.Error("SupportsKeyword should be true")
	}
	if c.SupportsInvertedKeyword {
		t.Error("SupportsInvertedKeyword should be false")
	}
	if !c.SupportsAgentChecks {
		t.Error("SupportsAgentChecks should be true")
	}
	if !c.SupportsMaintenanceWindows {
		t.Error("SupportsMaintenanceWindows should be true")
	}
	if !c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be true")
	}
}

func TestNormalize(t *testing.T) {
	a := newTestAdapter("http://x", "tok")
	cases := map[string]string{
		"down":        "http_failure",
		"seems_down":  "http_failure",
		"degraded":    "http_failure",
		"Down":        "http_failure",
		"Seems Down":  "http_failure",
		"Degraded":    "http_failure",
		"up":          "recovered",
		"Resolved":    "recovered",
		"Warning":     "unknown",
		"Maintenance": "unknown",
		"":            adapter.UnrecognizedClassification,
		"flapping":    adapter.UnrecognizedClassification,
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNew_AppendsAPIVersion(t *testing.T) {
	if got := New("j", "https://jetmon.example", "tok").apiURL; got != "https://jetmon.example/api/v1" {
		t.Fatalf("apiURL = %q, want /api/v1 appended", got)
	}
}

func TestNew_PreservesVersionedAPIURL(t *testing.T) {
	if got := New("j", "https://jetmon.example/api/v1/", "tok").apiURL; got != "https://jetmon.example/api/v1" {
		t.Fatalf("apiURL = %q, want existing /api/v1 preserved", got)
	}
}

func TestCheckIntervalMinutes(t *testing.T) {
	got, err := checkIntervalMinutes(5 * time.Minute)
	if err != nil {
		t.Fatalf("checkIntervalMinutes: %v", err)
	}
	if got != 5 {
		t.Fatalf("checkIntervalMinutes = %d, want 5", got)
	}

	_, err = checkIntervalMinutes(90 * time.Second)
	var freqErr *adapter.FrequencyError
	if !errors.As(err, &freqErr) {
		t.Fatalf("err = %v, want FrequencyError", err)
	}
	if freqErr.MinAchievable != 2*time.Minute {
		t.Fatalf("MinAchievable = %v, want 2m", freqErr.MinAchievable)
	}
}

func TestProvision_RequestShape(t *testing.T) {
	var c captured
	body := `{"id":8000000000000123,"blog_id":8000000000000123,"monitor_url":"http://bench-a.example/","monitor_active":true,"bucket_no":17,"check_interval":5,"current_state":"Up","current_severity":0,"redirect_policy":"follow"}`
	srv := fakeAPI(t, &c, http.StatusCreated, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok-abc")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/api/v1/sites" {
		t.Errorf("path = %q, want /api/v1/sites", c.path)
	}
	if c.auth != "Bearer tok-abc" {
		t.Errorf("auth = %q", c.auth)
	}
	if c.contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", c.contentType)
	}
	if !strings.HasPrefix(c.idempotency, "uptime-bench-jetmon-v2-") {
		t.Errorf("Idempotency-Key = %q", c.idempotency)
	}

	var got createSiteRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if got.BlogID < syntheticBlogIDBase || got.BlogID >= syntheticBlogIDBase+syntheticBlogIDRange {
		t.Errorf("blog_id = %d, want synthetic high-range id", got.BlogID)
	}
	if got.MonitorURL != "http://bench-a.example/" {
		t.Errorf("monitor_url = %q", got.MonitorURL)
	}
	if !got.MonitorActive {
		t.Error("monitor_active should be true")
	}
	if got.BucketNo != 17 {
		t.Errorf("bucket_no = %d, want 17", got.BucketNo)
	}
	if got.CheckInterval != 5 {
		t.Errorf("check_interval = %d, want 5", got.CheckInterval)
	}
	if got.CheckKeyword != nil {
		t.Errorf("check_keyword = %q, want nil", *got.CheckKeyword)
	}
	if got.AlertCooldownMinutes == nil || *got.AlertCooldownMinutes != 0 {
		t.Errorf("alert_cooldown_minutes = %v, want 0", got.AlertCooldownMinutes)
	}

	if handle.MonitorID != "8000000000000123" {
		t.Errorf("MonitorID = %q, want response site id", handle.MonitorID)
	}
	if handle.Fields["blog_id"] != "8000000000000123" {
		t.Errorf("blog_id field = %q", handle.Fields["blog_id"])
	}
	if handle.Fields["bucket_no"] != "17" {
		t.Errorf("bucket_no field = %q, want 17", handle.Fields["bucket_no"])
	}
	if handle.Fields["check_interval"] != "5" {
		t.Errorf("check_interval field = %q, want 5", handle.Fields["check_interval"])
	}
}

func TestProvision_KeywordPresent(t *testing.T) {
	var c captured
	body := `{"id":8000000000000124,"blog_id":8000000000000124,"monitor_url":"http://bench-a.example/","monitor_active":true}`
	srv := fakeAPI(t, &c, http.StatusCreated, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: 5 * time.Minute,
			Keyword:        "uptime-bench-canary",
			KeywordCheck:   adapter.KeywordCheckPresent,
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	var got createSiteRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.CheckKeyword == nil || *got.CheckKeyword != "uptime-bench-canary" {
		t.Fatalf("check_keyword = %v, want uptime-bench-canary", got.CheckKeyword)
	}
}

func TestProvision_KeywordAbsentRejected(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, http.StatusCreated, `{"id":1,"blog_id":1}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: time.Minute,
			Keyword:        "HACKED",
			KeywordCheck:   adapter.KeywordCheckAbsent,
		},
	)
	if err == nil {
		t.Fatal("expected error for absent-mode")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Fatalf("err = %v, want absent", err)
	}
	if c.method != "" {
		t.Fatalf("expected no request, got %s %s", c.method, c.path)
	}
}

func TestProvision_RetriesSyntheticBlogIDConflict(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if count == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"site_exists","message":"exists"}}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":8000000000000777,"blog_id":8000000000000777,"monitor_url":"http://bench-a.example/","monitor_active":true}`))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if count != 2 {
		t.Fatalf("request count = %d, want 2", count)
	}
	if handle.MonitorID != "8000000000000777" {
		t.Fatalf("MonitorID = %q", handle.MonitorID)
	}
}

func TestProvision_WithMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/api/v1/sites", Status: 201, Body: `{"id":8000000000000999,"blog_id":8000000000000999,"monitor_url":"http://bench-a.example/","monitor_active":true}`},
		{Method: "PATCH", PathPrefix: "/api/v1/sites/8000000000000999", Status: 200, Body: `{"id":8000000000000999,"blog_id":8000000000000999}`},
	})
	defer srv.Close()

	start := time.Date(2026, 4, 28, 14, 30, 0, 0, time.UTC)
	end := start.Add(45 * time.Minute)

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: end},
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 2 {
		t.Fatalf("expected POST + PATCH, got %d requests", len(rf.Requests()))
	}
	if rf.Requests()[1].Method != "PATCH" || rf.Requests()[1].Path != "/api/v1/sites/8000000000000999" {
		t.Fatalf("second request = %s %s", rf.Requests()[1].Method, rf.Requests()[1].Path)
	}

	var got updateSiteRequest
	if err := json.Unmarshal(rf.Requests()[1].Body, &got); err != nil {
		t.Fatalf("PATCH body: %v", err)
	}
	if got.MaintenanceStart == nil || *got.MaintenanceStart != "2026-04-28T14:30:00Z" {
		t.Errorf("maintenance_start = %v", got.MaintenanceStart)
	}
	if got.MaintenanceEnd == nil || *got.MaintenanceEnd != "2026-04-28T15:15:00Z" {
		t.Errorf("maintenance_end = %v", got.MaintenanceEnd)
	}
}

func TestProvision_MaintenanceFailureRollsBack(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/api/v1/sites", Status: 201, Body: `{"id":8000000000000999,"blog_id":8000000000000999,"monitor_url":"http://bench-a.example/","monitor_active":true}`},
		{Method: "PATCH", PathPrefix: "/api/v1/sites/8000000000000999", Status: 500, Body: `boom`},
		{Method: "DELETE", PathPrefix: "/api/v1/sites/8000000000000999", Status: 204, Body: ``},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: time.Now(), End: time.Now().Add(time.Minute)},
		},
	)
	if err == nil {
		t.Fatal("expected maintenance error")
	}
	sawDelete := false
	for _, r := range rf.Requests() {
		if r.Method == "DELETE" && r.Path == "/api/v1/sites/8000000000000999" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Fatalf("expected rollback DELETE, requests = %+v", rf.Requests())
	}
}

func TestRetrieve_HappyPathResolvedEvent(t *testing.T) {
	var c captured
	body := `{
		"data": [
			{
				"id": 487291,
				"site_id": 8000000000000123,
				"endpoint_id": null,
				"check_type": "http",
				"discriminator": null,
				"severity": 4,
				"state": "Down",
				"started_at": "2026-04-25T08:00:00Z",
				"ended_at": "2026-04-25T08:05:00Z",
				"resolution_reason": "verifier_cleared",
				"cause_event_id": null,
				"metadata": {"http_code": 503, "url": "http://bench-a.example/"},
				"duration_ms": 300000,
				"transition_count": 3
			}
		],
		"page": {"next": null, "limit": 200}
	}`
	srv := fakeAPI(t, &c, http.StatusOK, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "8000000000000123"},
		adapter.RunWindow{
			FailureStarted: time.Date(2026, 4, 25, 7, 55, 0, 0, time.UTC),
			GracePeriodEnd: time.Date(2026, 4, 25, 8, 10, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q, want known", res.Status)
	}
	if len(res.Reports) != 2 {
		t.Fatalf("reports = %d, want fired + resolved", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("Reports[0].EventType = %q", res.Reports[0].EventType)
	}
	if res.Reports[0].RawClassification != "down" {
		t.Errorf("Reports[0].RawClassification = %q", res.Reports[0].RawClassification)
	}
	if res.Reports[1].EventType != adapter.EventAlertResolved {
		t.Errorf("Reports[1].EventType = %q", res.Reports[1].EventType)
	}
	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET", c.method)
	}
	if c.path != "/api/v1/sites/8000000000000123/events" {
		t.Errorf("path = %q", c.path)
	}
	if !strings.Contains(c.query, "check_type=http") {
		t.Errorf("query = %q, want check_type=http", c.query)
	}
	if !strings.Contains(c.query, "started_at__gte=") || !strings.Contains(c.query, "started_at__lt=") {
		t.Errorf("query = %q, want started_at range", c.query)
	}
}

func TestRetrieve_SeemsDownCountsAsFired(t *testing.T) {
	var c captured
	body := `{
		"data": [
			{
				"id": 1,
				"site_id": 8000000000000123,
				"check_type": "http",
				"severity": 3,
				"state": "Seems Down",
				"started_at": "2026-04-25T08:00:00Z",
				"ended_at": null,
				"metadata": null,
				"duration_ms": 1000,
				"transition_count": 1
			}
		],
		"page": {"next": null, "limit": 200}
	}`
	srv := fakeAPI(t, &c, http.StatusOK, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "8000000000000123"},
		adapter.RunWindow{
			FailureStarted: time.Date(2026, 4, 25, 7, 55, 0, 0, time.UTC),
			GracePeriodEnd: time.Date(2026, 4, 25, 8, 10, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(res.Reports))
	}
	if res.Reports[0].RawClassification != "seems_down" {
		t.Fatalf("RawClassification = %q, want seems_down", res.Reports[0].RawClassification)
	}
}

func TestRetrieve_LowercaseFailureStatesCountAsFired(t *testing.T) {
	var c captured
	body := `{
		"data": [
			{
				"id": 1,
				"site_id": 8000000000000123,
				"check_type": "http",
				"severity": 4,
				"state": "down",
				"started_at": "2026-04-25T08:00:00Z",
				"ended_at": null,
				"metadata": null,
				"duration_ms": 1000,
				"transition_count": 1
			},
			{
				"id": 2,
				"site_id": 8000000000000123,
				"check_type": "http",
				"severity": 3,
				"state": "seems_down",
				"started_at": "2026-04-25T08:01:00Z",
				"ended_at": null,
				"metadata": null,
				"duration_ms": 1000,
				"transition_count": 1
			},
			{
				"id": 3,
				"site_id": 8000000000000123,
				"check_type": "http",
				"severity": 2,
				"state": "degraded",
				"started_at": "2026-04-25T08:02:00Z",
				"ended_at": null,
				"metadata": null,
				"duration_ms": 1000,
				"transition_count": 1
			}
		],
		"page": {"next": null, "limit": 200}
	}`
	srv := fakeAPI(t, &c, http.StatusOK, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "8000000000000123"},
		adapter.RunWindow{
			FailureStarted: time.Date(2026, 4, 25, 7, 55, 0, 0, time.UTC),
			GracePeriodEnd: time.Date(2026, 4, 25, 8, 10, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 3 {
		t.Fatalf("reports = %d, want 3", len(res.Reports))
	}
	want := []string{"down", "seems_down", "degraded"}
	for i, raw := range want {
		if res.Reports[i].EventType != adapter.EventAlertFired {
			t.Fatalf("Reports[%d].EventType = %q, want fired", i, res.Reports[i].EventType)
		}
		if res.Reports[i].RawClassification != raw {
			t.Fatalf("Reports[%d].RawClassification = %q, want %q", i, res.Reports[i].RawClassification, raw)
		}
	}
}

func TestRetrieve_Paginates(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		w.WriteHeader(http.StatusOK)
		if r.URL.Query().Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"data":[],"page":{"next":"cursor-1","limit":200}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[],"page":{"next":null,"limit":200}}`))
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "8000000000000123"},
		adapter.RunWindow{
			FailureStarted: time.Date(2026, 4, 25, 7, 55, 0, 0, time.UTC),
			GracePeriodEnd: time.Date(2026, 4, 25, 8, 10, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q", res.Status)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if !strings.Contains(requests[1], "cursor=cursor-1") {
		t.Fatalf("second query = %q, want cursor", requests[1])
	}
}

func TestRetrieve_HTTPErrorReturnsUnknown(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, http.StatusServiceUnavailable, "Service Unavailable")
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "8000000000000123"},
		adapter.RunWindow{
			FailureStarted: time.Date(2026, 4, 25, 7, 55, 0, 0, time.UTC),
			GracePeriodEnd: time.Date(2026, 4, 25, 8, 10, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("should not return Go error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
}

func TestRetrieve_MissingHandleReturnsUnknown(t *testing.T) {
	a := newTestAdapter("http://nope", "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
	if !strings.Contains(res.Reason, "missing site id") {
		t.Fatalf("Reason = %q", res.Reason)
	}
}

func TestDeprovision_204Success(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, http.StatusNoContent, "")
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "8000000000000123"}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if c.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", c.method)
	}
	if c.path != "/api/v1/sites/8000000000000123" {
		t.Errorf("path = %q", c.path)
	}
}

func TestDeprovision_404IsIdempotent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, http.StatusNotFound, `{"error":{"code":"site_not_found"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "8000000000000123"}); err != nil {
		t.Fatalf("404 should be idempotent: %v", err)
	}
}

func TestDeprovision_EmptyHandleIsNoop(t *testing.T) {
	a := newTestAdapter("http://nope", "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{}); err != nil {
		t.Fatalf("empty handle: %v", err)
	}
}
