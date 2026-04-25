package uptimerobot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// ─── boilerplate: a captured-form fake server ───────────────────────────────

type captured struct {
	method      string
	path        string
	contentType string
	form        url.Values
}

// fakeAPI returns an httptest.Server that captures every incoming request's
// form data into c and responds with the given JSON body and status.
func fakeAPI(t *testing.T, c *captured, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.contentType = r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		v, err := url.ParseQuery(string(raw))
		if err != nil {
			t.Errorf("server: bad form body: %v", err)
		}
		c.form = v
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func newTestAdapter(srvURL, apiKey string) *Adapter {
	a := New("uptimerobot", srvURL, apiKey)
	a.client = http.DefaultClient // bypass the 30s timeout for tests
	return a
}

// ─── Adapter interface conformance ──────────────────────────────────────────

func TestImplementsAdapterInterface(t *testing.T) {
	var _ adapter.Adapter = (*Adapter)(nil)
}

// ─── Capabilities + Normalize + ServiceID ───────────────────────────────────

func TestCapabilities(t *testing.T) {
	a := newTestAdapter("http://x", "k")
	caps := a.Capabilities()
	if caps.MinCheckFrequency != 5*time.Minute {
		t.Fatalf("MinCheckFrequency = %v, want 5m", caps.MinCheckFrequency)
	}
	if !caps.SupportsKeyword {
		t.Fatal("SupportsKeyword should be true")
	}
	if caps.SupportsAgentChecks {
		t.Fatal("SupportsAgentChecks should be false (probe-based service)")
	}
}

func TestNormalize(t *testing.T) {
	a := newTestAdapter("http://x", "k")
	cases := map[string]string{
		"down":       "http_failure",
		"seems_down": "http_failure",
		"up":         "recovered",
		"paused":     "unknown",
		"":           adapter.UnrecognizedClassification,
		"flapping":   adapter.UnrecognizedClassification,
	}
	for raw, want := range cases {
		if got := a.Normalize(raw); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestServiceID(t *testing.T) {
	a := New("my-uptimerobot-instance", "http://x", "k")
	if a.ServiceID() != "my-uptimerobot-instance" {
		t.Fatalf("ServiceID = %q", a.ServiceID())
	}
}

// ─── New: defaults and url normalization ────────────────────────────────────

func TestNew_DefaultsAPIURL(t *testing.T) {
	a := New("u", "", "key")
	if a.apiURL != DefaultAPIURL {
		t.Fatalf("apiURL = %q, want %q", a.apiURL, DefaultAPIURL)
	}
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	a := New("u", "https://api.example.com/v2/", "key")
	if a.apiURL != "https://api.example.com/v2" {
		t.Fatalf("apiURL = %q, want trimmed", a.apiURL)
	}
}

// ─── Provision ──────────────────────────────────────────────────────────────

func TestProvision_RequestShape(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":777888,"status":1}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "u123-XXX")
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
	if c.path != "/newMonitor" {
		t.Errorf("path = %q, want /newMonitor", c.path)
	}
	if !strings.HasPrefix(c.contentType, "application/x-www-form-urlencoded") {
		t.Errorf("content-type = %q, want form-urlencoded", c.contentType)
	}
	if c.form.Get("api_key") != "u123-XXX" {
		t.Errorf("api_key field = %q", c.form.Get("api_key"))
	}
	if c.form.Get("format") != "json" {
		t.Errorf("format field = %q, want json", c.form.Get("format"))
	}
	if c.form.Get("type") != "1" {
		t.Errorf("type field = %q, want 1 (HTTP)", c.form.Get("type"))
	}
	if c.form.Get("url") != "http://bench-a.harmonic.party/" {
		t.Errorf("url field = %q", c.form.Get("url"))
	}
	if c.form.Get("interval") != strconv.Itoa(int((5 * time.Minute).Seconds())) {
		t.Errorf("interval field = %q, want %d", c.form.Get("interval"), int((5 * time.Minute).Seconds()))
	}
	// friendly_name should include the target ID so the operator can find it.
	if !strings.Contains(c.form.Get("friendly_name"), "bench-a") {
		t.Errorf("friendly_name = %q, should contain target id", c.form.Get("friendly_name"))
	}

	if handle.ServiceID != "uptimerobot" {
		t.Errorf("handle.ServiceID = %q", handle.ServiceID)
	}
	if handle.MonitorID != "777888" {
		t.Errorf("handle.MonitorID = %q, want 777888", handle.MonitorID)
	}
	if handle.Fields["url"] != "http://bench-a.harmonic.party/" {
		t.Errorf("handle.Fields[url] = %q", handle.Fields["url"])
	}
}

func TestProvision_APIErrorReturnsError(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"fail","error":{"type":"invalid_parameter","message":"url already exists"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err == nil {
		t.Fatal("expected error from stat=fail response")
	}
	if !strings.Contains(err.Error(), "url already exists") {
		t.Fatalf("error = %v, want it to surface the API message", err)
	}
}

func TestProvision_MissingMonitorID(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":0,"status":1}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "missing monitor id") {
		t.Fatalf("err = %v, want one mentioning missing id", err)
	}
}

func TestProvision_MissingAPIKey(t *testing.T) {
	a := New("u", "", "") // no key
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("err = %v, want api_key error without making any HTTP calls", err)
	}
}

// ─── Retrieve ───────────────────────────────────────────────────────────────

func TestRetrieve_HappyPath_DownThenUp(t *testing.T) {
	var c captured
	body := `{
		"stat": "ok",
		"monitors": [{
			"id": 777888,
			"status": 2,
			"logs": [
				{"type": 1, "datetime": 1714000000, "duration": 0, "reason": {"code": "200", "detail": "Connection refused"}},
				{"type": 2, "datetime": 1714000060, "duration": 60, "reason": ""}
			]
		}]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	window := adapter.RunWindow{
		FailureStarted: time.Unix(1714000000, 0).UTC(),
		FailureEnded:   time.Unix(1714000060, 0).UTC(),
		GracePeriodEnd: time.Unix(1714000300, 0).UTC(),
	}
	handle := adapter.MonitorHandle{MonitorID: "777888"}

	res, err := a.Retrieve(context.Background(), handle, window)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown {
		t.Fatalf("Status = %q, want known", res.Status)
	}
	if len(res.Reports) != 2 {
		t.Fatalf("got %d reports, want 2", len(res.Reports))
	}

	// First report: alert_fired with raw=down.
	if res.Reports[0].EventType != adapter.EventAlertFired {
		t.Errorf("Reports[0].EventType = %q, want alert_fired", res.Reports[0].EventType)
	}
	if res.Reports[0].RawClassification != "down" {
		t.Errorf("Reports[0].RawClassification = %q, want down", res.Reports[0].RawClassification)
	}
	if !res.Reports[0].ReportedAt.Equal(time.Unix(1714000000, 0).UTC()) {
		t.Errorf("Reports[0].ReportedAt = %v", res.Reports[0].ReportedAt)
	}

	// Second report: alert_resolved with raw=up.
	if res.Reports[1].EventType != adapter.EventAlertResolved {
		t.Errorf("Reports[1].EventType = %q, want alert_resolved", res.Reports[1].EventType)
	}
	if res.Reports[1].RawClassification != "up" {
		t.Errorf("Reports[1].RawClassification = %q, want up", res.Reports[1].RawClassification)
	}

	// Wire shape on the way out.
	if c.path != "/getMonitors" {
		t.Errorf("path = %q, want /getMonitors", c.path)
	}
	if c.form.Get("monitors") != "777888" {
		t.Errorf("monitors field = %q", c.form.Get("monitors"))
	}
	if c.form.Get("logs") != "1" {
		t.Errorf("logs field = %q, want 1", c.form.Get("logs"))
	}
	if c.form.Get("logs_start_date") != "1714000000" {
		t.Errorf("logs_start_date = %q", c.form.Get("logs_start_date"))
	}
	if c.form.Get("logs_end_date") != "1714000300" {
		t.Errorf("logs_end_date = %q", c.form.Get("logs_end_date"))
	}
}

func TestRetrieve_SeemsDownDistinguishedFromDown(t *testing.T) {
	var c captured
	body := `{
		"stat": "ok",
		"monitors": [{
			"id": 1, "status": 8,
			"logs": [{"type": 1, "datetime": 100, "duration": 0, "reason": {"code": "seems_down", "detail": "n/a"}}]
		}]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("got %d reports", len(res.Reports))
	}
	if res.Reports[0].RawClassification != "seems_down" {
		t.Fatalf("RawClassification = %q, want seems_down (reason.code)", res.Reports[0].RawClassification)
	}
}

func TestRetrieve_SkipsStartedAndPausedLogs(t *testing.T) {
	var c captured
	body := `{
		"stat": "ok",
		"monitors": [{
			"id": 1, "status": 2,
			"logs": [
				{"type": 98, "datetime": 100, "reason": ""},
				{"type": 99, "datetime": 200, "reason": ""}
			]
		}]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 0 {
		t.Fatalf("got %d reports for started/paused-only logs, want 0", len(res.Reports))
	}
}

func TestRetrieve_HandlesStringReason(t *testing.T) {
	// Some responses send reason as a plain string instead of an object.
	// Reason.UnmarshalJSON tolerates both shapes.
	var c captured
	body := `{
		"stat": "ok",
		"monitors": [{
			"id": 1, "status": 2,
			"logs": [{"type": 1, "datetime": 100, "duration": 0, "reason": "Timeout (10s)"}]
		}]
	}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Reports) != 1 {
		t.Fatalf("got %d reports", len(res.Reports))
	}
	if res.Reports[0].Metadata["reason_text"] != "Timeout (10s)" {
		t.Fatalf("reason_text = %v, want %q", res.Reports[0].Metadata["reason_text"], "Timeout (10s)")
	}
}

func TestRetrieve_APIFailReturnsUnknown(t *testing.T) {
	var c captured
	body := `{"stat":"fail","error":{"type":"rate_limit","message":"slow down"}}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("Retrieve should return Unknown via result, not error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
	if !strings.Contains(res.Reason, "slow down") {
		t.Fatalf("Reason = %q, want it to surface the API error", res.Reason)
	}
}

func TestRetrieve_HTTPErrorReturnsUnknown(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 502, "Bad Gateway")
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "1"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("Retrieve should not return Go error: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown", res.Status)
	}
}

func TestRetrieve_NoMonitorReturnsUnknown(t *testing.T) {
	var c captured
	body := `{"stat":"ok","monitors":[]}`
	srv := fakeAPI(t, &c, 200, body)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{MonitorID: "999"}, adapter.RunWindow{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != adapter.RetrieveUnknown {
		t.Fatalf("Status = %q, want unknown when monitor not in response", res.Status)
	}
}

// ─── Deprovision ────────────────────────────────────────────────────────────

func TestDeprovision_Success(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":777888}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777888"}); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if c.path != "/deleteMonitor" {
		t.Errorf("path = %q, want /deleteMonitor", c.path)
	}
	if c.form.Get("id") != "777888" {
		t.Errorf("id field = %q", c.form.Get("id"))
	}
}

func TestDeprovision_NotFoundIsIdempotent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"fail","error":{"type":"invalid_parameter","message":"monitor not found"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777888"}); err != nil {
		t.Fatalf("Deprovision should be idempotent on not-found: %v", err)
	}
}

func TestDeprovision_OtherErrorPropagates(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"fail","error":{"type":"auth","message":"bad api key"}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	err := a.Deprovision(context.Background(), adapter.MonitorHandle{MonitorID: "777888"})
	if err == nil || !strings.Contains(err.Error(), "bad api key") {
		t.Fatalf("err = %v, want one mentioning the API error", err)
	}
}

func TestDeprovision_EmptyHandleIsNoop(t *testing.T) {
	a := newTestAdapter("http://nope", "k")
	if err := a.Deprovision(context.Background(), adapter.MonitorHandle{}); err != nil {
		t.Fatalf("empty handle: %v", err)
	}
}

// ─── reason.UnmarshalJSON edge cases ────────────────────────────────────────

func TestReasonUnmarshal(t *testing.T) {
	cases := []struct {
		in    string
		want  reason
		errOK bool
	}{
		{`""`, reason{}, false},
		{`null`, reason{}, false},
		{`"some plain text"`, reason{Detail: "some plain text"}, false},
		{`{"code":"503","detail":"Service unavailable"}`, reason{Code: "503", Detail: "Service unavailable"}, false},
		{`{"code":"","detail":""}`, reason{}, false},
		{`123`, reason{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			var r reason
			err := json.Unmarshal([]byte(tc.in), &r)
			if tc.errOK {
				if err == nil {
					t.Fatal("expected unmarshal error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if r != tc.want {
				t.Fatalf("got %+v, want %+v", r, tc.want)
			}
		})
	}
}

// ─── intervalSeconds ────────────────────────────────────────────────────────

func TestIntervalSeconds(t *testing.T) {
	cases := map[time.Duration]int{
		30 * time.Second:                       30, // paid-tier minimum
		60 * time.Second:                       60,
		5 * time.Minute:                        300,
		300*time.Second + 400*time.Millisecond: 300, // rounds to nearest second
		0:                                      60,  // zero clamps to 60
		500 * time.Millisecond:                 60,  // sub-tier intervals clamp
		20 * time.Second:                       60,  // below paid-tier minimum clamps
	}
	for d, want := range cases {
		if got := intervalSeconds(d); got != want {
			t.Errorf("intervalSeconds(%v) = %d, want %d", d, got, want)
		}
	}
}
