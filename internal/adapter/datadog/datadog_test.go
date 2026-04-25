package datadog

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
)

type captured struct {
	method string
	path   string
	query  string
	apiKey string
	appKey string
	body   []byte
}

func fakeAPI(t *testing.T, c *captured, status int, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.query = r.URL.RawQuery
		c.apiKey = r.Header.Get("DD-API-KEY")
		c.appKey = r.Header.Get("DD-APPLICATION-KEY")
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
}

func newTestAdapter(srvURL, apiKey, appKey string) *Adapter {
	a := New("datadog-synthetics", srvURL, apiKey, appKey)
	a.client = http.DefaultClient
	return a
}

// ─── Conformance ────────────────────────────────────────────────────────────

func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}

func TestServiceID(t *testing.T) {
	a := New("custom", "http://x", "ak", "pk")
	if a.ServiceID() != "custom" {
		t.Fatal(a.ServiceID())
	}
}

func TestCapabilities(t *testing.T) {
	c := newTestAdapter("http://x", "ak", "pk").Capabilities()
	if c.MinCheckFrequency != 30*time.Second {
		t.Errorf("MinCheckFrequency = %v, want 30s", c.MinCheckFrequency)
	}
}

func TestNormalize(t *testing.T) {
	a := newTestAdapter("http://x", "ak", "pk")
	cases := map[string]string{
		"Alert":     "http_failure",
		"Triggered": "http_failure",
		"Recovered": "recovered",
		"No Data":   "unknown",
		"Warn":      "unknown",
		"":          adapter.UnrecognizedClassification,
		"unknown":   adapter.UnrecognizedClassification, // distinct from Datadog's "No Data"
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

// ─── tickEverySeconds ───────────────────────────────────────────────────────

func TestTickEverySeconds(t *testing.T) {
	cases := map[time.Duration]int{
		10 * time.Second: 30, // sub-30s clamps
		30 * time.Second: 30,
		45 * time.Second: 30, // < 60s rounds down to 30
		time.Minute:      60,
		2 * time.Minute:  60, // < 5m rounds down to 60
		5 * time.Minute:  300,
		10 * time.Minute: 300, // < 15m rounds down to 300
		15 * time.Minute: 900,
		30 * time.Minute: 1800,
		time.Hour:        3600,
		8 * time.Hour:    21600, // anything ≥ 6h clamps to 21600
	}
	for d, want := range cases {
		if got := tickEverySeconds(d); got != want {
			t.Errorf("tickEverySeconds(%v) = %d, want %d", d, got, want)
		}
	}
}

// ─── Provision ──────────────────────────────────────────────────────────────

func TestProvision_RequestShape(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"public_id":"abc-def-ghi","name":"x","status":"live"}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "AK", "PK")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.harmonic.party/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/api/v1/synthetics/tests/api" {
		t.Errorf("path = %q", c.path)
	}
	if c.apiKey != "AK" {
		t.Errorf("DD-API-KEY = %q", c.apiKey)
	}
	if c.appKey != "PK" {
		t.Errorf("DD-APPLICATION-KEY = %q", c.appKey)
	}

	var got newTestRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v\nbody=%q", err, c.body)
	}
	if got.Type != "api" || got.Subtype != "http" {
		t.Errorf("type/subtype = %q/%q, want api/http", got.Type, got.Subtype)
	}
	if got.Status != "live" {
		t.Errorf("status = %q, want live", got.Status)
	}
	if got.Config.Request.URL != "http://bench-a.harmonic.party/" {
		t.Errorf("config.request.url = %q", got.Config.Request.URL)
	}
	if got.Options.TickEvery != 60 {
		t.Errorf("options.tick_every = %d, want 60", got.Options.TickEvery)
	}
	if len(got.Config.Assertions) != 1 || got.Config.Assertions[0].Target != 200 {
		t.Errorf("assertions = %+v, want one statusCode=200", got.Config.Assertions)
	}
	if len(got.Locations) == 0 {
		t.Error("locations should not be empty")
	}

	if handle.MonitorID != "abc-def-ghi" {
		t.Errorf("handle.MonitorID = %q", handle.MonitorID)
	}
}

func TestProvision_ErrorMessageInBody(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"errors":["invalid url"]}`)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "invalid url") {
		t.Fatalf("err = %v, want one mentioning the API error", err)
	}
}

func TestProvision_HTTPError(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 403, `{"errors":["forbidden"]}`)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
}

func TestProvision_MissingKeys(t *testing.T) {
	cases := []struct {
		ak, pk string
	}{{"", "pk"}, {"ak", ""}, {"", ""}}
	for _, c := range cases {
		a := New("dd", "", c.ak, c.pk)
		_, err := a.Provision(context.Background(),
			adapter.Target{URL: "http://x/"},
			adapter.ProvisionConfig{},
		)
		if err == nil || !strings.Contains(err.Error(), "required") {
			t.Errorf("ak=%q pk=%q: err = %v, want one mentioning required", c.ak, c.pk, err)
		}
	}
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

func TestRetrieve_CoalescesPassFailTransitions(t *testing.T) {
	// Three results: pass, fail, pass. Coalesce produces alert_fired
	// (on the first fail) and alert_resolved (on the first pass after).
	var c captured
	body := `{
		"results": [
			{"result_id":"r1","check_time":1714000000000,"status":0,"result":{"eventType":"Recovered"}},
			{"result_id":"r2","check_time":1714000060000,"status":1,"result":{"eventType":"Alert"}},
			{"result_id":"r3","check_time":1714000120000,"status":0,"result":{"eventType":"Recovered"}}
		]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	res, err := a.Retrieve(context.Background(),
		adapter.MonitorHandle{MonitorID: "abc-def-ghi"},
		adapter.RunWindow{
			FailureStarted: time.UnixMilli(1714000000000),
			GracePeriodEnd: time.UnixMilli(1714000180000),
		},
	)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q, want known", res.Status)
	}
	if len(res.Reports) != 2 {
		t.Fatalf("got %d reports, want 2 (one fired, one resolved)", len(res.Reports))
	}
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("Reports[0] = %q, want alert_fired", res.Reports[0].EventType)
	}
	if res.Reports[1].EventType != adapter.EventAlertResolved {
		t.Errorf("Reports[1] = %q, want alert_resolved", res.Reports[1].EventType)
	}
	// Path includes from_ts/to_ts in milliseconds.
	if !strings.Contains(c.path, "abc-def-ghi") {
		t.Errorf("path = %q, should contain public_id", c.path)
	}
	if !strings.Contains(c.query, "from_ts=1714000000000") {
		t.Errorf("query = %q, want from_ts in millis", c.query)
	}
}

func TestRetrieve_AllPassesProducesNoReports(t *testing.T) {
	var c captured
	body := `{"results":[
		{"result_id":"r1","check_time":1714000000000,"status":0},
		{"result_id":"r2","check_time":1714000060000,"status":0}
	]}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "x"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 0 {
		t.Fatalf("got %d reports for all-passes, want 0", len(res.Reports))
	}
}

func TestRetrieve_ConsecutiveFailsCollapse(t *testing.T) {
	// Multiple consecutive fails should produce only one alert_fired,
	// not one per result. Same for consecutive passes after.
	var c captured
	body := `{"results":[
		{"result_id":"r1","check_time":1714000000000,"status":0},
		{"result_id":"r2","check_time":1714000060000,"status":1},
		{"result_id":"r3","check_time":1714000120000,"status":1},
		{"result_id":"r4","check_time":1714000180000,"status":1},
		{"result_id":"r5","check_time":1714000240000,"status":0}
	]}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "x"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 2 {
		t.Fatalf("got %d reports, want 2 (single fired + single resolved)", len(res.Reports))
	}
}

func TestRetrieve_HTTPErrorReturnsUnknown(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 429, "rate limit")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "x"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("should not return Go error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func TestDeprovision_PostsBulkDelete(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "abc-def-ghi"}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/api/v1/synthetics/tests/delete" {
		t.Errorf("path = %q", c.path)
	}
	var got deleteTestsRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if len(got.PublicIDs) != 1 || got.PublicIDs[0] != "abc-def-ghi" {
		t.Errorf("body.public_ids = %v", got.PublicIDs)
	}
}

func TestDeprovision_404IsIdempotent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 404, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "abc"}); err != nil {
		t.Fatalf("404 should be idempotent: %v", err)
	}
}

func TestDeprovision_OtherErrorPropagates(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 401, "")
	defer srv.Close()
	a := newTestAdapter(srv.URL, "AK", "PK")
	err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "abc"})
	if err == nil {
		t.Fatal("401 should propagate")
	}
}

func TestDeprovision_EmptyHandleIsNoop(t *testing.T) {
	a := newTestAdapter("http://nope", "AK", "PK")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{}); err != nil {
		t.Fatalf("empty handle: %v", err)
	}
}
