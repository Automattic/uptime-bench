package pingdom

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

// ─── boilerplate: a captured-request fake server ───────────────────────────

type captured struct {
	method      string
	path        string
	rawQuery    string
	auth        string
	contentType string
	accept      string
	body        []byte
}

func fakeAPI(t *testing.T, c *captured, status int, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.rawQuery = r.URL.RawQuery
		c.auth = r.Header.Get("Authorization")
		c.contentType = r.Header.Get("Content-Type")
		c.accept = r.Header.Get("Accept")
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func newTestAdapter(srvURL, token string) *Adapter {
	a := New("pingdom", srvURL, token)
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

func TestCapabilities_MaintenanceAndCooldown(t *testing.T) {
	c := newTestAdapter("http://x", "tok").Capabilities()
	if !c.SupportsMaintenanceWindows {
		t.Error("SupportsMaintenanceWindows should be true (Pingdom has POST /maintenance)")
	}
	if !c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be true (delete-recreate cycles state per run)")
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
	if c.SupportsAgentChecks {
		t.Error("SupportsAgentChecks should be false")
	}
}

func TestNormalize(t *testing.T) {
	a := newTestAdapter("http://x", "tok")
	cases := map[string]string{
		"down":             "http_failure",
		"unconfirmed_down": "http_failure",
		"up":               "recovered",
		"unknown":          "unknown",
		"paused":           "unknown",
		"flapping":         adapter.UnrecognizedClassification,
		"":                 adapter.UnrecognizedClassification,
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// ─── New: defaults and url normalization ────────────────────────────────────

func TestNew_DefaultsAPIURL(t *testing.T) {
	if New("p", "", "tok").apiURL != DefaultAPIURL {
		t.Fatal("empty url should default")
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	a := New("p", "https://example/api/3.1/", "tok")
	if a.apiURL != "https://example/api/3.1" {
		t.Fatalf("apiURL = %q, want trimmed", a.apiURL)
	}
}

// ─── splitHostPath ──────────────────────────────────────────────────────────

func TestSplitHostPath(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPath string
	}{
		{"http://bench-a.harmonic.party/", "bench-a.harmonic.party", "/"},
		{"https://bench-a.harmonic.party/health", "bench-a.harmonic.party", "/health"},
		{"http://bench-a.harmonic.party", "bench-a.harmonic.party", "/"},
		{"bench-a.harmonic.party", "bench-a.harmonic.party", "/"},
		{"http://bench-a.harmonic.party/api/v2/health", "bench-a.harmonic.party", "/api/v2/health"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			h, p := splitHostPath(tc.in)
			if h != tc.wantHost || p != tc.wantPath {
				t.Fatalf("splitHostPath(%q) = (%q, %q), want (%q, %q)", tc.in, h, p, tc.wantHost, tc.wantPath)
			}
		})
	}
}

// ─── resolutionMinutes ──────────────────────────────────────────────────────

func TestResolutionMinutes(t *testing.T) {
	cases := map[time.Duration]int{
		30 * time.Second: 1, // sub-1m clamps to 1
		time.Minute:      1,
		3 * time.Minute:  1, // < 5m → 1
		5 * time.Minute:  5,
		10 * time.Minute: 5, // < 15m → 5
		15 * time.Minute: 15,
		20 * time.Minute: 15, // < 30m → 15
		30 * time.Minute: 30,
		45 * time.Minute: 30, // < 60m → 30
		time.Hour:        60,
		2 * time.Hour:    60, // anything ≥ 60m → 60
	}
	for d, want := range cases {
		if got := resolutionMinutes(d); got != want {
			t.Errorf("resolutionMinutes(%v) = %d, want %d", d, got, want)
		}
	}
}

// ─── Provision ──────────────────────────────────────────────────────────────

func TestProvision_RequestShape(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"check":{"id":777,"name":"x","status":"unknown"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok-abc")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.harmonic.party/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/checks" {
		t.Errorf("path = %q, want /checks", c.path)
	}
	if c.auth != "Bearer tok-abc" {
		t.Errorf("auth = %q", c.auth)
	}
	if !strings.HasPrefix(c.contentType, "application/json") {
		t.Errorf("content-type = %q, want application/json", c.contentType)
	}
	if c.accept != "application/json" {
		t.Errorf("accept = %q, want application/json", c.accept)
	}

	var got newCheckRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v\nbody=%q", err, c.body)
	}
	if got.Type != "http" {
		t.Errorf("body.type = %q, want http", got.Type)
	}
	if got.Host != "bench-a.harmonic.party" {
		t.Errorf("body.host = %q", got.Host)
	}
	if got.URL != "/" {
		t.Errorf("body.url = %q, want /", got.URL)
	}
	if got.Resolution != 5 {
		t.Errorf("body.resolution = %d, want 5 (minutes)", got.Resolution)
	}
	if !strings.Contains(got.Name, "bench-a") {
		t.Errorf("body.name = %q, should contain target id", got.Name)
	}

	if handle.MonitorID != "777" {
		t.Errorf("handle.MonitorID = %q", handle.MonitorID)
	}
	if handle.Fields["host"] != "bench-a.harmonic.party" {
		t.Errorf("handle.Fields[host] = %q", handle.Fields["host"])
	}
	if handle.Fields["path"] != "/" {
		t.Errorf("handle.Fields[path] = %q", handle.Fields["path"])
	}
	// No keyword config -> neither shouldcontain nor shouldnotcontain set.
	if got.ShouldContain != "" || got.ShouldNotContain != "" {
		t.Errorf("status-only check should not set keyword fields, got contain=%q notcontain=%q",
			got.ShouldContain, got.ShouldNotContain)
	}
}

// TestProvision_KeywordPresent: present-mode populates shouldcontain.
func TestProvision_KeywordPresent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"check":{"id":1,"status":"unknown"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: time.Minute,
			Keyword:        "uptime-bench-canary",
			KeywordCheck:   adapter.KeywordCheckPresent,
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	var got newCheckRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ShouldContain != "uptime-bench-canary" {
		t.Errorf("ShouldContain = %q, want uptime-bench-canary", got.ShouldContain)
	}
	if got.ShouldNotContain != "" {
		t.Errorf("ShouldNotContain should be empty in present-mode, got %q", got.ShouldNotContain)
	}
	if got.Type != "http" {
		t.Errorf("type = %q, want http (Pingdom keeps http for keyword checks)", got.Type)
	}
}

// TestProvision_KeywordAbsent: absent-mode populates shouldnotcontain.
func TestProvision_KeywordAbsent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"check":{"id":1,"status":"unknown"}}`)
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
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	var got newCheckRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatal(err)
	}
	if got.ShouldNotContain != "HACKED" {
		t.Errorf("ShouldNotContain = %q, want HACKED", got.ShouldNotContain)
	}
	if got.ShouldContain != "" {
		t.Errorf("ShouldContain should be empty in absent-mode, got %q", got.ShouldContain)
	}
}

func TestProvision_RejectsTargetWithoutHost(t *testing.T) {
	a := newTestAdapter("http://nope", "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: ""},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "no host") {
		t.Fatalf("err = %v, want one mentioning no host", err)
	}
}

func TestProvision_NonJSONErrorBubblesUp(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 401, `unauthorized`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "bad")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want one mentioning the 401 status", err)
	}
}

func TestProvision_APIErrorEnvelope(t *testing.T) {
	var c captured
	body := `{"error":{"statuscode":403,"statusdesc":"Forbidden","errormessage":"plan does not allow this"}}`
	srv := fakeAPI(t, &c, 200, body) // some Pingdom endpoints return 200 with error in body
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil {
		t.Fatal("expected error from in-body API error")
	}
	if !strings.Contains(err.Error(), "plan does not allow") {
		t.Fatalf("err = %v, want it to surface the API message", err)
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

func TestRetrieve_HappyPath(t *testing.T) {
	var c captured
	body := `{
		"summary": {
			"states": [
				{"status":"up","timefrom":1714000000,"timeto":1714000100},
				{"status":"down","timefrom":1714000100,"timeto":1714000150},
				{"status":"up","timefrom":1714000150,"timeto":1714000300}
			]
		}
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	window := adapter.RunWindow{
		FailureStarted: time.Unix(1714000100, 0).UTC(),
		FailureEnded:   time.Unix(1714000150, 0).UTC(),
		GracePeriodEnd: time.Unix(1714000300, 0).UTC(),
	}
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "777"}, window)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q, want known", res.Status)
	}

	// Three states → three reports (none are paused/skipped).
	if len(res.Reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(res.Reports))
	}
	if res.Reports[1].EventType != adapter.EventAlertFired {
		t.Errorf("Reports[1] should be alert_fired (down), got %q", res.Reports[1].EventType)
	}
	if res.Reports[1].RawClassification != "down" {
		t.Errorf("Reports[1].RawClassification = %q, want down", res.Reports[1].RawClassification)
	}

	// Wire shape on the way out.
	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET", c.method)
	}
	if !strings.HasPrefix(c.path, "/summary.outage/") {
		t.Errorf("path = %q, want /summary.outage/...", c.path)
	}
	if !strings.Contains(c.path, "777") {
		t.Errorf("path = %q, should contain check id 777", c.path)
	}
	if !strings.Contains(c.rawQuery, "from=1714000100") {
		t.Errorf("query = %q, missing from=", c.rawQuery)
	}
	if !strings.Contains(c.rawQuery, "to=1714000300") {
		t.Errorf("query = %q, missing to=", c.rawQuery)
	}
}

func TestRetrieve_UnconfirmedDownIsAlert(t *testing.T) {
	var c captured
	body := `{"summary":{"states":[{"status":"unconfirmed_down","timefrom":100,"timeto":200}]}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("got %d reports", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("unconfirmed_down should produce alert_fired, got %q", res.Reports[0].EventType)
	}
}

func TestRetrieve_PausedSkipped(t *testing.T) {
	var c captured
	body := `{"summary":{"states":[{"status":"paused","timefrom":100,"timeto":200}]}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 0 {
		t.Fatalf("paused state should be skipped, got %d reports", len(res.Reports))
	}
}

func TestRetrieve_HTTPErrorReturnsUnknown(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 502, "Bad Gateway")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("Retrieve should not return Go error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
}

func TestRetrieve_APIErrorEnvelopeReturnsUnknown(t *testing.T) {
	var c captured
	body := `{"error":{"statuscode":429,"statusdesc":"Too Many Requests","errormessage":"rate limit exceeded"}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
	if !strings.Contains(res.Reason, "rate limit exceeded") {
		t.Fatalf("Reason = %q, want it to surface API message", res.Reason)
	}
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func TestDeprovision_Success(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"message":"Deletion of check was successful!"}`)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777"}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if c.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", c.method)
	}
	if c.path != "/checks/777" {
		t.Errorf("path = %q, want /checks/777", c.path)
	}
}

func TestDeprovision_404IsIdempotent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 404, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777"}); err != nil {
		t.Fatalf("404 should be idempotent: %v", err)
	}
}

func TestDeprovision_InBodyNotFoundIdempotent(t *testing.T) {
	var c captured
	body := `{"error":{"statuscode":400,"statusdesc":"Bad Request","errormessage":"check not found"}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777"}); err != nil {
		t.Fatalf("in-body not-found should be idempotent: %v", err)
	}
}

func TestDeprovision_OtherErrorPropagates(t *testing.T) {
	var c captured
	body := `{"error":{"statuscode":403,"statusdesc":"Forbidden","errormessage":"insufficient permissions"}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "tok")
	err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777"})
	if err == nil || !strings.Contains(err.Error(), "insufficient permissions") {
		t.Fatalf("err = %v, want one mentioning the API error", err)
	}
}

func TestDeprovision_EmptyHandleIsNoop(t *testing.T) {
	a := newTestAdapter("http://nope", "tok")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{}); err != nil {
		t.Fatalf("empty handle: %v", err)
	}
}

// ─── Maintenance windows ────────────────────────────────────────────────────

// TestProvision_NoMaintenanceWindow: when ProvisionConfig.MaintenanceWindow
// is nil, only the /checks call is made; no maintenance_id field is set
// on the handle.
func TestProvision_NoMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/checks", Status: 200, Body: `{"check":{"id":555,"status":"unknown"}}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 1 {
		t.Errorf("expected 1 request when no maintenance window, got %d", len(rf.Requests()))
	}
	if handle.Fields["maintenance_id"] != "" {
		t.Errorf("maintenance_id should be empty, got %q", handle.Fields["maintenance_id"])
	}
}

// TestProvision_WithMaintenanceWindow: when MaintenanceWindow is set,
// Provision posts /checks first, then /maintenance with the right shape;
// the handle carries maintenance_id.
func TestProvision_WithMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/checks", Status: 200, Body: `{"check":{"id":555,"status":"unknown"}}`},
		{Method: "POST", PathPrefix: "/maintenance", Status: 200, Body: `{"maintenance":{"id":9000}}`},
	})
	defer srv.Close()

	start := time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)

	a := newTestAdapter(srv.URL, "tok")
	handle, err := a.Provision(context.Background(),
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
		t.Fatalf("expected 2 requests (checks + maintenance), got %d", len(rf.Requests()))
	}
	if rf.Requests()[0].Path != "/checks" {
		t.Errorf("first request path = %q, want /checks", rf.Requests()[0].Path)
	}
	if rf.Requests()[1].Path != "/maintenance" {
		t.Errorf("second request path = %q, want /maintenance", rf.Requests()[1].Path)
	}

	var mw newMaintenanceRequest
	if err := json.Unmarshal(rf.Requests()[1].Body, &mw); err != nil {
		t.Fatalf("maintenance body unmarshal: %v", err)
	}
	if mw.From != start.Unix() {
		t.Errorf("from = %d, want %d", mw.From, start.Unix())
	}
	if mw.To != end.Unix() {
		t.Errorf("to = %d, want %d", mw.To, end.Unix())
	}
	if len(mw.Checks) != 1 || mw.Checks[0] != 555 {
		t.Errorf("checks = %v, want [555]", mw.Checks)
	}
	if !strings.Contains(mw.Description, "bench-a") {
		t.Errorf("description = %q, should contain target id", mw.Description)
	}

	if handle.Fields["maintenance_id"] != "9000" {
		t.Errorf("handle.Fields[maintenance_id] = %q, want 9000", handle.Fields["maintenance_id"])
	}
}

// TestProvision_MaintenanceFailureRollsBackCheck: if the /maintenance
// call fails after /checks succeeded, the adapter must delete the
// just-created check so the run doesn't leak monitors. Returns a Go
// error so the runner records adapter_error.
func TestProvision_MaintenanceFailureRollsBackCheck(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/checks", Status: 200, Body: `{"check":{"id":555,"status":"unknown"}}`},
		{Method: "POST", PathPrefix: "/maintenance", Status: 400, Body: `{"error":{"statuscode":400,"errormessage":"invalid range"}}`},
		{Method: "DELETE", PathPrefix: "/checks/555", Status: 200, Body: `{}`},
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
		t.Fatal("expected error from maintenance failure")
	}
	// Verify rollback: should see DELETE /checks/555 in the request log.
	sawDelete := false
	for _, r := range rf.Requests() {
		if r.Method == "DELETE" && r.Path == "/checks/555" {
			sawDelete = true
			break
		}
	}
	if !sawDelete {
		t.Errorf("expected DELETE /checks/555 rollback after maintenance failure; requests = %+v", rf.Requests())
	}
}

// TestDeprovision_DeletesMaintenanceFirst: when handle has a
// maintenance_id, Deprovision sends DELETE /maintenance/{id} before
// DELETE /checks/{id}.
func TestDeprovision_DeletesMaintenanceFirst(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "DELETE", PathPrefix: "/maintenance/", Status: 200, Body: `{}`},
		{Method: "DELETE", PathPrefix: "/checks/", Status: 200, Body: `{"message":"deleted"}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	handle := adapter.MonitorHandle{
		MonitorID: "555",
		Fields:    map[string]string{"maintenance_id": "9000"},
	}
	if err := a.Deprovision(context.Background(), handle); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(rf.Requests()) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(rf.Requests()))
	}
	if rf.Requests()[0].Path != "/maintenance/9000" {
		t.Errorf("first request = %q, want /maintenance/9000 (must come first)", rf.Requests()[0].Path)
	}
	if rf.Requests()[1].Path != "/checks/555" {
		t.Errorf("second request = %q, want /checks/555", rf.Requests()[1].Path)
	}
}

// TestDeprovision_MaintenanceDeleteFailureDoesNotBlockCheckDelete: if
// the maintenance delete fails (e.g. transient 5xx), Deprovision still
// proceeds with check deletion. Maintenance windows self-expire at
// `to` so a leaked window is a dashboard nuisance, not corruption.
func TestDeprovision_MaintenanceDeleteFailureDoesNotBlockCheckDelete(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "DELETE", PathPrefix: "/maintenance/", Status: 503, Body: `{"error":{"statuscode":503}}`},
		{Method: "DELETE", PathPrefix: "/checks/", Status: 200, Body: `{"message":"deleted"}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "tok")
	handle := adapter.MonitorHandle{
		MonitorID: "555",
		Fields:    map[string]string{"maintenance_id": "9000"},
	}
	if err := a.Deprovision(context.Background(), handle); err != nil {
		t.Fatalf("Deprovision should not fail when maintenance delete errors: %v", err)
	}
	if len(rf.Requests()) != 2 {
		t.Errorf("expected 2 requests (try maintenance, then check), got %d", len(rf.Requests()))
	}
}
