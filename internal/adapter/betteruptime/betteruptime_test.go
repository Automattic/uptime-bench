package betteruptime

import (
	"context"
	"encoding/json"
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
	method string
	path   string
	query  string
	auth   string
	body   []byte
}

func fakeAPI(t *testing.T, c *captured, status int, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.auth = r.Header.Get("Authorization")
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func newTestAdapter(srvURL, token string) *Adapter {
	a := New("better-uptime", srvURL, token)
	a.client = http.DefaultClient
	return a
}

// ─── Conformance ────────────────────────────────────────────────────────────

func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}

func TestServiceID(t *testing.T) {
	a := New("custom-id", "http://x", "tok")
	if a.ServiceID() != "custom-id" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

func TestCapabilities(t *testing.T) {
	c := newTestAdapter("http://x", "tok").Capabilities()
	if c.MinCheckFrequency != 3*time.Minute {
		t.Errorf("MinCheckFrequency = %v, want 3m", c.MinCheckFrequency)
	}
	if !c.SupportsKeyword {
		t.Error("SupportsKeyword should be true")
	}
}

func TestNormalize(t *testing.T) {
	a := newTestAdapter("http://x", "tok")
	cases := map[string]string{
		"down":       "http_failure",
		"validating": "http_failure",
		"up":         "recovered",
		"paused":     "unknown",
		"pending":    "unknown",
		"":           adapter.UnrecognizedClassification,
		"flapping":   adapter.UnrecognizedClassification,
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNew_DefaultsAPIURL(t *testing.T) {
	if New("p", "", "tok").apiURL != DefaultAPIURL {
		t.Fatal("empty url should default")
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	a := New("p", "https://example/api/v2/", "tok")
	if a.apiURL != "https://example/api/v2" {
		t.Fatalf("apiURL = %q, want trimmed", a.apiURL)
	}
}

// ─── checkFrequencySeconds ──────────────────────────────────────────────────

func TestCheckFrequencySeconds(t *testing.T) {
	cases := map[time.Duration]int{
		30 * time.Second: 30, // paid-tier minimum
		60 * time.Second: 60,
		3 * time.Minute:  180,
		5 * time.Minute:  300,
		10 * time.Second: 180, // sub-30s clamps to 180
		0:                180, // zero clamps
	}
	for d, want := range cases {
		if got := checkFrequencySeconds(d); got != want {
			t.Errorf("checkFrequencySeconds(%v) = %d, want %d", d, got, want)
		}
	}
}

// ─── Provision ──────────────────────────────────────────────────────────────

func TestProvision_RequestShape(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 201, `{"data":{"id":"42","type":"monitor","attributes":{"url":"http://x/"}}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok-abc")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.harmonic.party/"},
		adapter.ProvisionConfig{CheckFrequency: 3 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/monitors" {
		t.Errorf("path = %q, want /monitors", c.path)
	}
	if c.auth != "Bearer tok-abc" {
		t.Errorf("auth = %q", c.auth)
	}

	var got newMonitorRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if got.URL != "http://bench-a.harmonic.party/" {
		t.Errorf("body.url = %q", got.URL)
	}
	if got.MonitorType != "status" {
		t.Errorf("body.monitor_type = %q, want status", got.MonitorType)
	}
	if got.CheckFrequency != 180 {
		t.Errorf("body.check_frequency = %d, want 180 (3m)", got.CheckFrequency)
	}
	if !strings.Contains(got.PronounceableName, "bench-a") {
		t.Errorf("body.pronounceable_name = %q, should contain target id", got.PronounceableName)
	}

	if handle.MonitorID != "42" {
		t.Errorf("handle.MonitorID = %q, want 42", handle.MonitorID)
	}
	if got.MonitorType != "status" {
		t.Errorf("monitor_type = %q, want status (no keyword config)", got.MonitorType)
	}
	if got.RequiredKeyword != "" {
		t.Errorf("required_keyword should be empty for status check, got %q", got.RequiredKeyword)
	}
}

// TestProvision_KeywordPresent: present-mode flips monitor_type to
// "keyword" and sets required_keyword.
func TestProvision_KeywordPresent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 201, `{"data":{"id":"1","type":"monitor","attributes":{}}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: 3 * time.Minute,
			Keyword:        "uptime-bench-canary",
			KeywordCheck:   adapter.KeywordCheckPresent,
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	var got newMonitorRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.MonitorType != "keyword" {
		t.Errorf("monitor_type = %q, want keyword", got.MonitorType)
	}
	if got.RequiredKeyword != "uptime-bench-canary" {
		t.Errorf("required_keyword = %q", got.RequiredKeyword)
	}
}

// TestProvision_KeywordAbsentRejected: absent-mode is unsupported for
// Better Uptime; the adapter must fail loudly instead of silently
// provisioning a present-mode monitor.
func TestProvision_KeywordAbsentRejected(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 201, `{"data":{"id":"1"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: 3 * time.Minute,
			Keyword:        "HACKED",
			KeywordCheck:   adapter.KeywordCheckAbsent,
		},
	)
	if err == nil {
		t.Fatal("expected error for absent-mode (SupportsInvertedKeyword = false)")
	}
	if !strings.Contains(err.Error(), "absent") {
		t.Errorf("err = %v, want one mentioning absent", err)
	}
}

func TestProvision_CapabilitiesMarkInvertedUnsupported(t *testing.T) {
	c := newTestAdapter("http://x", "tok").Capabilities()
	if c.SupportsInvertedKeyword {
		t.Error("SupportsInvertedKeyword should be false (Better Uptime keyword type only supports the canary direction)")
	}
}

func TestProvision_APIErrorEnvelope(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 422, `{"errors":[{"detail":"URL is invalid","title":"Validation failed","status":"422"}]}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "URL is invalid") {
		t.Fatalf("err = %v, want one mentioning the API error", err)
	}
}

func TestProvision_MissingToken(t *testing.T) {
	a := New("p", "", "")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want one mentioning token", err)
	}
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

func TestRetrieve_HappyPath_ResolvedIncident(t *testing.T) {
	var c captured
	body := `{
		"data": [
			{
				"id": "1001",
				"attributes": {
					"name": "uptime-bench: bench-a",
					"url": "http://bench-a.harmonic.party/",
					"cause": "HTTP 503",
					"started_at": "2026-04-25T08:00:00Z",
					"resolved_at": "2026-04-25T08:05:00Z",
					"status": "Resolved"
				}
			}
		]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "42"},
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
		t.Fatalf("got %d reports, want 2 (started + resolved)", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("Reports[0].EventType = %q, want alert_fired", res.Reports[0].EventType)
	}
	if res.Reports[1].EventType != adapter.EventAlertResolved {
		t.Errorf("Reports[1].EventType = %q, want alert_resolved", res.Reports[1].EventType)
	}
	wantStart := time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC)
	if !res.Reports[0].ReportedAt.Equal(wantStart) {
		t.Errorf("Reports[0].ReportedAt = %v, want %v", res.Reports[0].ReportedAt, wantStart)
	}

	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET", c.method)
	}
	if c.path != "/incidents" {
		t.Errorf("path = %q, want /incidents", c.path)
	}
	if !strings.Contains(c.query, "monitor_id=42") {
		t.Errorf("query = %q, want monitor_id=42", c.query)
	}
	if !strings.Contains(c.query, "from=") || !strings.Contains(c.query, "to=") {
		t.Errorf("query = %q, want from= and to=", c.query)
	}
}

func TestRetrieve_HappyPath_OngoingIncident(t *testing.T) {
	// Incident with no resolved_at — only the started side is reported.
	var c captured
	body := `{
		"data": [
			{
				"id": "1002",
				"attributes": {
					"started_at": "2026-04-25T08:00:00Z",
					"resolved_at": "",
					"status": "Started"
				}
			}
		]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "42"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("got %d reports, want 1 (started only)", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("EventType = %q, want alert_fired", res.Reports[0].EventType)
	}
}

func TestRetrieve_NoIncidents(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"data":[]}`)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "42"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q, want known", res.Status)
	}
	if len(res.Reports) != 0 {
		t.Fatalf("got %d reports, want 0", len(res.Reports))
	}
}

func TestRetrieve_HTTPErrorReturnsUnknown(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 503, "Service Unavailable")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "42"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("should not return Go error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func TestDeprovision_204Success(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 204, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "42"}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if c.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", c.method)
	}
	if c.path != "/monitors/42" {
		t.Errorf("path = %q, want /monitors/42", c.path)
	}
}

func TestDeprovision_404IsIdempotent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 404, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "42"}); err != nil {
		t.Fatalf("404 should be idempotent: %v", err)
	}
}

func TestDeprovision_OtherErrorPropagates(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 401, `{"errors":[{"detail":"unauthenticated"}]}`)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "42"})
	if err == nil {
		t.Fatal("expected error from 401")
	}
}

func TestDeprovision_EmptyHandleIsNoop(t *testing.T) {
	a := newTestAdapter("http://nope", "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{}); err != nil {
		t.Fatalf("empty handle: %v", err)
	}
}

// ─── Maintenance windows ────────────────────────────────────────────────────

// TestCapabilities_MaintenanceAndCooldown confirms both flags flipped to
// true in Phase B. SupportsCooldownReset is true because Deprovision
// already deletes the monitor — no extra reset call needed.
func TestCapabilities_MaintenanceAndCooldown(t *testing.T) {
	c := newTestAdapter("http://x", "tok").Capabilities()
	if !c.SupportsMaintenanceWindows {
		t.Error("SupportsMaintenanceWindows should be true")
	}
	if !c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be true (delete-recreate cycles state)")
	}
}

// TestProvision_NoMaintenanceWindow: nil window means a single POST
// /monitors call and no PATCH.
func TestProvision_NoMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/monitors", Status: 201, Body: `{"data":{"id":"42","type":"monitor","attributes":{}}}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 3 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 1 {
		t.Errorf("expected 1 request without maintenance, got %d", len(rf.Requests()))
	}
}

// TestProvision_WithMaintenanceWindow: in-day window produces a PATCH
// with HH:MM:SS UTC, today's day name, and timezone=UTC.
func TestProvision_WithMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/monitors", Status: 201, Body: `{"data":{"id":"42","type":"monitor","attributes":{}}}`},
		{Method: "PATCH", PathPrefix: "/monitors/42", Status: 200, Body: `{"data":{"id":"42","type":"monitor","attributes":{}}}`},
	})
	defer srv.Close()

	// Pick a Tuesday in UTC, well inside a single calendar day.
	start := time.Date(2026, 4, 28, 14, 30, 0, 0, time.UTC)
	end := start.Add(45 * time.Minute) // 15:15:00 UTC same Tuesday

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    3 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: end},
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 2 {
		t.Fatalf("expected 2 requests (POST /monitors + PATCH /monitors/42), got %d", len(rf.Requests()))
	}
	if rf.Requests()[0].Method != "POST" || rf.Requests()[0].Path != "/monitors" {
		t.Errorf("first request = %s %s, want POST /monitors", rf.Requests()[0].Method, rf.Requests()[0].Path)
	}
	if rf.Requests()[1].Method != "PATCH" || rf.Requests()[1].Path != "/monitors/42" {
		t.Errorf("second request = %s %s, want PATCH /monitors/42", rf.Requests()[1].Method, rf.Requests()[1].Path)
	}

	var got updateMonitorRequest
	if err := json.Unmarshal(rf.Requests()[1].Body, &got); err != nil {
		t.Fatalf("PATCH body unmarshal: %v", err)
	}
	if got.MaintenanceFrom != "14:30:00" {
		t.Errorf("maintenance_from = %q, want 14:30:00", got.MaintenanceFrom)
	}
	if got.MaintenanceTo != "15:15:00" {
		t.Errorf("maintenance_to = %q, want 15:15:00", got.MaintenanceTo)
	}
	if got.MaintenanceTimezone != "UTC" {
		t.Errorf("maintenance_timezone = %q, want UTC", got.MaintenanceTimezone)
	}
	if len(got.MaintenanceDays) != 1 || got.MaintenanceDays[0] != "tue" {
		t.Errorf("maintenance_days = %v, want [tue] (2026-04-28 is a Tuesday)", got.MaintenanceDays)
	}
}

// TestProvision_MaintenanceCrossingMidnightRejected: Better Uptime's
// recurring-day model can't express a one-shot cross-midnight window
// without also affecting the next day's same range. Adapter must reject
// rather than silently mis-configuring.
func TestProvision_MaintenanceCrossingMidnightRejected(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/monitors", Status: 201, Body: `{"data":{"id":"42","type":"monitor","attributes":{}}}`},
		// Rollback delete after the failed PATCH attempt.
		{Method: "DELETE", PathPrefix: "/monitors/42", Status: 204, Body: ``},
	})
	defer srv.Close()

	// 23:30 UTC + 1 hour spans into the next UTC day.
	start := time.Date(2026, 4, 28, 23, 30, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour)

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    3 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: end},
		},
	)
	if err == nil {
		t.Fatal("expected error for cross-midnight window")
	}
	if !strings.Contains(err.Error(), "crosses midnight") {
		t.Errorf("err = %v, want one mentioning cross-midnight", err)
	}
	// Verify rollback: monitor created → maintenance failed → monitor deleted.
	sawDelete := false
	for _, r := range rf.Requests() {
		if r.Method == "DELETE" && r.Path == "/monitors/42" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("expected DELETE /monitors/42 rollback after rejection; requests = %+v", rf.Requests())
	}
}
