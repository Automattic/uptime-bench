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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapter/adaptertest"
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

func newTestAdapterWithOptions(srvURL, apiKey string, opts ...Option) *Adapter {
	a := New("uptimerobot", srvURL, apiKey, opts...)
	a.client = http.DefaultClient // bypass the 30s timeout for tests
	return a
}

// formOf parses a routedFake-captured form-encoded request body into
// url.Values. Adapter is form-encoded throughout, so test sites that
// need to inspect form fields go through this helper rather than
// re-implementing the parse each time.
func formOf(r adaptertest.RequestRecord) url.Values {
	v, _ := url.ParseQuery(string(r.Body))
	return v
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

func TestCapabilities_PaidMinimumAndHEADMethod(t *testing.T) {
	a := newTestAdapterWithOptions("http://x", "k",
		WithHTTPMethod("HEAD"),
		WithMinCheckFrequency(time.Minute),
	)
	caps := a.Capabilities()
	if caps.MinCheckFrequency != time.Minute {
		t.Fatalf("MinCheckFrequency = %v, want 1m", caps.MinCheckFrequency)
	}
	if caps.SupportsKeyword {
		t.Fatal("HEAD monitor should not claim keyword support")
	}
	if caps.SupportsInvertedKeyword {
		t.Fatal("HEAD monitor should not claim inverted keyword support")
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
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example.com/"},
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
	if c.form.Get("url") != "http://bench-a.example.com/" {
		t.Errorf("url field = %q", c.form.Get("url"))
	}
	if c.form.Get("interval") != strconv.Itoa(int((5 * time.Minute).Seconds())) {
		t.Errorf("interval field = %q, want %d", c.form.Get("interval"), int((5 * time.Minute).Seconds()))
	}
	if c.form.Get("http_method") != "" {
		t.Errorf("http_method field = %q, want empty when auth.http_method is omitted", c.form.Get("http_method"))
	}
	// friendly_name should include the target ID so the operator can find it.
	if !strings.Contains(c.form.Get("friendly_name"), "bench-a") {
		t.Errorf("friendly_name = %q, should contain target id", c.form.Get("friendly_name"))
	}
	if !strings.Contains(c.form.Get("friendly_name"), "uptimerobot") {
		t.Errorf("friendly_name = %q, should contain service id", c.form.Get("friendly_name"))
	}

	if handle.ServiceID != "uptimerobot" {
		t.Errorf("handle.ServiceID = %q", handle.ServiceID)
	}
	if handle.MonitorID != "777888" {
		t.Errorf("handle.MonitorID = %q, want 777888", handle.MonitorID)
	}
	if handle.Fields["url"] != "http://bench-a.example.com/" {
		t.Errorf("handle.Fields[url] = %q", handle.Fields["url"])
	}
	// No keyword config -> no keyword fields set on the form.
	if c.form.Get("keyword_value") != "" || c.form.Get("keyword_type") != "" {
		t.Errorf("status check should not set keyword fields, got value=%q type=%q",
			c.form.Get("keyword_value"), c.form.Get("keyword_type"))
	}
}

func TestProvision_HTTPMethodGET(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":1,"status":1}}`)
	defer srv.Close()

	a := newTestAdapterWithOptions(srv.URL, "u123-XXX", WithHTTPMethod("get"))
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if c.form.Get("http_method") != "2" {
		t.Errorf("http_method = %q, want 2 (GET)", c.form.Get("http_method"))
	}
	if handle.Fields["http_method"] != "GET" {
		t.Errorf("handle.Fields[http_method] = %q, want GET", handle.Fields["http_method"])
	}
}

func TestProvision_HTTPMethodHEAD(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":1,"status":1}}`)
	defer srv.Close()

	a := newTestAdapterWithOptions(srv.URL, "u123-XXX", WithHTTPMethod("HEAD"))
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if c.form.Get("http_method") != "1" {
		t.Errorf("http_method = %q, want 1 (HEAD)", c.form.Get("http_method"))
	}
}

func TestProvision_HTTPMethodHEADRejectsKeyword(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":1,"status":1}}`)
	defer srv.Close()

	a := newTestAdapterWithOptions(srv.URL, "u123-XXX", WithHTTPMethod("HEAD"))
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: time.Minute,
			Keyword:        "uptime-bench-canary",
			KeywordCheck:   adapter.KeywordCheckPresent,
		},
	)
	if err == nil {
		t.Fatal("expected HEAD keyword provisioning to fail")
	}
	if c.form != nil {
		t.Fatal("HEAD keyword provisioning should fail before calling the API")
	}
}

// TestProvision_KeywordPresent: present-mode switches type to 2 (keyword)
// and sets keyword_type=2 (alert when keyword not present).
func TestProvision_KeywordPresent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":1,"status":1}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "u123-XXX")
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
	if c.form.Get("type") != "2" {
		t.Errorf("type = %q, want 2 (keyword)", c.form.Get("type"))
	}
	if c.form.Get("keyword_value") != "uptime-bench-canary" {
		t.Errorf("keyword_value = %q", c.form.Get("keyword_value"))
	}
	if c.form.Get("keyword_type") != "2" {
		t.Errorf("keyword_type = %q, want 2 (alert when not exists; the 'present' canary case)",
			c.form.Get("keyword_type"))
	}
}

// TestProvision_KeywordAbsent: absent-mode keeps type=2 but flips
// keyword_type to 1 (alert when keyword exists).
func TestProvision_KeywordAbsent(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"ok","monitor":{"id":1,"status":1}}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "u123-XXX")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency: 5 * time.Minute,
			Keyword:        "HACKED",
			KeywordCheck:   adapter.KeywordCheckAbsent,
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if c.form.Get("keyword_value") != "HACKED" {
		t.Errorf("keyword_value = %q", c.form.Get("keyword_value"))
	}
	if c.form.Get("keyword_type") != "1" {
		t.Errorf("keyword_type = %q, want 1 (alert when exists; the 'absent' injected case)",
			c.form.Get("keyword_type"))
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

func TestProvision_APIErrorWithoutPayloadReturnsError(t *testing.T) {
	var c captured
	srv := fakeAPI(t, &c, 200, `{"stat":"fail"}`)
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err == nil {
		t.Fatal("expected error from malformed stat=fail response")
	}
	if !strings.Contains(err.Error(), "without an error payload") {
		t.Fatalf("error = %v, want missing payload context", err)
	}
}

func TestProvision_LocalValidationErrorDoesNotAttemptRecovery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected API request for local validation error: %s", r.URL.Path)
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL, "k")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "x", URL: "http://x/"},
		adapter.ProvisionConfig{
			CheckFrequency: 5 * time.Minute,
			Keyword:        "uptime-bench-canary",
			KeywordCheck:   "unsupported",
		},
	)
	if err == nil {
		t.Fatal("expected unsupported KeywordCheck error")
	}
	if !strings.Contains(err.Error(), "unsupported KeywordCheck") {
		t.Fatalf("error = %v, want unsupported KeywordCheck context", err)
	}
}

func TestProvision_AlreadyExistsDeletesMatchingMonitorAndRetries(t *testing.T) {
	var newMonitorCalls int
	var requests []adaptertest.RequestRecord
	var requestsMu sync.Mutex
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, adaptertest.RequestRecord{Method: r.Method, Path: r.URL.Path, Body: body})
		requestsMu.Unlock()
		switch r.URL.Path {
		case "/newMonitor":
			newMonitorCalls++
			if newMonitorCalls == 1 {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"stat":"fail","error":{"type":"already_exists","message":"monitor already exists."}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":777,"status":1}}`))
		case "/getMonitors":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitors":[{"id":555,"friendly_name":"uptime-bench: bench-a","url":"http://bench-a.example/","status":2}]}`))
		case "/deleteMonitor":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":555}}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv2.Close()

	a := newTestAdapter(srv2.URL, "k")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if handle.MonitorID != "777" {
		t.Fatalf("MonitorID = %q, want retried monitor 777", handle.MonitorID)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	gotOrder := make([]string, 0, len(requests))
	for _, r := range requests {
		gotOrder = append(gotOrder, r.Path)
	}
	wantOrder := []string{"/newMonitor", "/getMonitors", "/deleteMonitor", "/newMonitor"}
	if strings.Join(gotOrder, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("request order = %v, want %v", gotOrder, wantOrder)
	}
	if formOf(requests[2]).Get("id") != "555" {
		t.Fatalf("deleteMonitor id = %q, want stale monitor 555", formOf(requests[2]).Get("id"))
	}
}

func TestProvision_TimeoutAfterCreateAdoptsMatchingMonitor(t *testing.T) {
	var created atomic.Bool
	var requests []adaptertest.RequestRecord
	var requestsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, adaptertest.RequestRecord{Method: r.Method, Path: r.URL.Path, Body: body})
		requestsMu.Unlock()
		switch r.URL.Path {
		case "/newMonitor":
			created.Store(true)
			time.Sleep(80 * time.Millisecond)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":888,"status":1}}`))
		case "/getMonitors":
			if !created.Load() {
				t.Error("getMonitors called before simulated server-side create")
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitors":[{"id":888,"friendly_name":"uptime-bench: bench-a","url":"http://bench-a.example/","status":1}]}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := New("uptimerobot", srv.URL, "k")
	a.client = &http.Client{Timeout: 20 * time.Millisecond}
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if handle.MonitorID != "888" {
		t.Fatalf("MonitorID = %q, want adopted monitor 888", handle.MonitorID)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	gotOrder := make([]string, 0, len(requests))
	for _, r := range requests {
		gotOrder = append(gotOrder, r.Path)
	}
	if strings.Join(gotOrder, ",") != "/newMonitor,/getMonitors" {
		t.Fatalf("request order = %v, want newMonitor then getMonitors", gotOrder)
	}
}

func TestProvision_TimeoutBeforeCreateRetries(t *testing.T) {
	var newMonitorCalls int
	var requests []adaptertest.RequestRecord
	var requestsMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestsMu.Lock()
		requests = append(requests, adaptertest.RequestRecord{Method: r.Method, Path: r.URL.Path, Body: body})
		requestsMu.Unlock()
		switch r.URL.Path {
		case "/newMonitor":
			newMonitorCalls++
			if newMonitorCalls == 1 {
				time.Sleep(80 * time.Millisecond)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":999,"status":1}}`))
		case "/getMonitors":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"stat":"ok","monitors":[]}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := New("uptimerobot", srv.URL, "k")
	a.client = &http.Client{Timeout: 20 * time.Millisecond}
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if handle.MonitorID != "999" {
		t.Fatalf("MonitorID = %q, want retried monitor 999", handle.MonitorID)
	}
	requestsMu.Lock()
	defer requestsMu.Unlock()
	gotOrder := make([]string, 0, len(requests))
	for _, r := range requests {
		gotOrder = append(gotOrder, r.Path)
	}
	if strings.Join(gotOrder, ",") != "/newMonitor,/getMonitors,/newMonitor" {
		t.Fatalf("request order = %v, want newMonitor, getMonitors, newMonitor", gotOrder)
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

// ─── Maintenance windows ────────────────────────────────────────────────────

// TestCapabilities_MaintenanceAndCooldown — both Phase B flags true.
func TestCapabilities_MaintenanceAndCooldown(t *testing.T) {
	c := newTestAdapter("http://x", "u123-XXX").Capabilities()
	if !c.SupportsMaintenanceWindows {
		t.Error("SupportsMaintenanceWindows should be true (newMWindow + editMonitor flow)")
	}
	if !c.SupportsCooldownReset {
		t.Error("SupportsCooldownReset should be true (delete-recreate cycles state)")
	}
}

// TestProvision_NoMaintenanceWindow — nil window means a single
// /newMonitor call.
func TestProvision_NoMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/newMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777,"status":1}}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "u123-XXX")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{CheckFrequency: 5 * time.Minute},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 1 {
		t.Errorf("expected 1 request without maintenance, got %d", len(rf.Requests()))
	}
}

// TestProvision_WithMaintenanceWindow — three-call sequence with the
// right shapes: /newMonitor → /newMWindow → /editMonitor (mwindows=ID).
func TestProvision_WithMaintenanceWindow(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/newMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777,"status":1}}`},
		{Method: "POST", PathPrefix: "/newMWindow", Status: 200, Body: `{"stat":"ok","mwindow":{"id":9000,"status":1}}`},
		{Method: "POST", PathPrefix: "/editMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777}}`},
	})
	defer srv.Close()

	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	end := start.Add(45 * time.Minute) // 45 minutes, well within the day

	a := newTestAdapter(srv.URL, "u123-XXX")
	handle, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    5 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: end},
		},
	)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(rf.Requests()) != 3 {
		t.Fatalf("expected 3 requests (newMonitor + newMWindow + editMonitor), got %d", len(rf.Requests()))
	}
	if rf.Requests()[0].Path != "/newMonitor" {
		t.Errorf("requests[0].path = %q, want /newMonitor", rf.Requests()[0].Path)
	}
	if rf.Requests()[1].Path != "/newMWindow" {
		t.Errorf("requests[1].path = %q, want /newMWindow", rf.Requests()[1].Path)
	}
	if rf.Requests()[2].Path != "/editMonitor" {
		t.Errorf("requests[2].path = %q, want /editMonitor", rf.Requests()[2].Path)
	}

	mw := formOf(rf.Requests()[1])
	if mw.Get("type") != "1" {
		t.Errorf("newMWindow type = %q, want 1 (Once)", mw.Get("type"))
	}
	if mw.Get("start_time") != strconv.FormatInt(start.Unix(), 10) {
		t.Errorf("start_time = %q, want %d", mw.Get("start_time"), start.Unix())
	}
	if mw.Get("duration") != "45" {
		t.Errorf("duration = %q, want 45 (minutes)", mw.Get("duration"))
	}
	if !strings.Contains(mw.Get("friendly_name"), "bench-a") {
		t.Errorf("friendly_name = %q, should contain target id", mw.Get("friendly_name"))
	}

	em := formOf(rf.Requests()[2])
	if em.Get("id") != "777" {
		t.Errorf("editMonitor id = %q, want 777 (the monitor id)", em.Get("id"))
	}
	if em.Get("mwindows") != "9000" {
		t.Errorf("editMonitor mwindows = %q, want 9000 (the new window id)", em.Get("mwindows"))
	}

	if handle.Fields["maintenance_id"] != "9000" {
		t.Errorf("handle.Fields[maintenance_id] = %q, want 9000", handle.Fields["maintenance_id"])
	}
}

func TestMaintenanceStartUnixCeilsFractionalStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 12, 0, 0, int(500*time.Millisecond), time.UTC)
	now := start.Add(-time.Minute)
	got := maintenanceStartUnix(start, now)
	want := start.Truncate(time.Second).Add(time.Second).Unix()
	if got != want {
		t.Fatalf("maintenanceStartUnix = %d, want %d", got, want)
	}
}

func TestMaintenanceStartUnixClampsPastStart(t *testing.T) {
	start := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	now := start.Add(1500 * time.Millisecond)
	got := maintenanceStartUnix(start, now)
	want := now.Truncate(time.Second).Add(time.Second).Unix()
	if got != want {
		t.Fatalf("maintenanceStartUnix = %d, want %d", got, want)
	}
}

// TestProvision_MaintenanceCrossingMidnightRejected — type=1 (Once)
// cross-midnight semantics aren't reliably documented; reject and roll
// back the just-created monitor.
func TestProvision_MaintenanceCrossingMidnightRejected(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/newMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777,"status":1}}`},
		{Method: "POST", PathPrefix: "/deleteMonitor", Status: 200, Body: `{"stat":"ok"}`},
	})
	defer srv.Close()

	start := time.Date(2026, 4, 28, 23, 30, 0, 0, time.UTC)
	end := start.Add(1 * time.Hour) // crosses midnight UTC

	a := newTestAdapter(srv.URL, "u123-XXX")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    5 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: end},
		},
	)
	if err == nil {
		t.Fatal("expected error for cross-midnight window")
	}
	if !strings.Contains(err.Error(), "crosses midnight") {
		t.Errorf("err = %v, want one mentioning cross-midnight", err)
	}
	sawDelete := false
	for _, r := range rf.Requests() {
		if r.Path == "/deleteMonitor" && formOf(r).Get("id") == "777" {
			sawDelete = true
		}
	}
	if !sawDelete {
		t.Errorf("expected /deleteMonitor rollback after rejection; got requests: %+v", rf.Requests())
	}
}

// TestProvision_MWindowCreateFailureRollsBackMonitor — if /newMWindow
// fails after /newMonitor succeeded, the adapter must delete the just-
// created monitor.
func TestProvision_MWindowCreateFailureRollsBackMonitor(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/newMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777,"status":1}}`},
		{Method: "POST", PathPrefix: "/newMWindow", Status: 200, Body: `{"stat":"fail","error":{"type":"invalid_parameter","message":"bad start_time"}}`},
		{Method: "POST", PathPrefix: "/deleteMonitor", Status: 200, Body: `{"stat":"ok"}`},
	})
	defer srv.Close()

	start := time.Date(2026, 4, 28, 14, 30, 0, 0, time.UTC)
	a := newTestAdapter(srv.URL, "u123-XXX")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    5 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: start.Add(30 * time.Minute)},
		},
	)
	if err == nil {
		t.Fatal("expected error from newMWindow failure")
	}
	sawMonitorDelete := false
	for _, r := range rf.Requests() {
		if r.Path == "/deleteMonitor" && formOf(r).Get("id") == "777" {
			sawMonitorDelete = true
		}
	}
	if !sawMonitorDelete {
		t.Errorf("expected /deleteMonitor rollback; got %+v", rf.Requests())
	}
}

// TestProvision_AttachFailureRollsBackBoth — if /editMonitor fails after
// the window was already created, both window and monitor get cleaned up.
func TestProvision_AttachFailureRollsBackBoth(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/newMonitor", Status: 200, Body: `{"stat":"ok","monitor":{"id":777,"status":1}}`},
		{Method: "POST", PathPrefix: "/newMWindow", Status: 200, Body: `{"stat":"ok","mwindow":{"id":9000,"status":1}}`},
		{Method: "POST", PathPrefix: "/editMonitor", Status: 200, Body: `{"stat":"fail","error":{"type":"invalid_parameter","message":"unknown mwindow"}}`},
		{Method: "POST", PathPrefix: "/deleteMWindow", Status: 200, Body: `{"stat":"ok"}`},
		{Method: "POST", PathPrefix: "/deleteMonitor", Status: 200, Body: `{"stat":"ok"}`},
	})
	defer srv.Close()

	start := time.Date(2026, 4, 28, 14, 30, 0, 0, time.UTC)
	a := newTestAdapter(srv.URL, "u123-XXX")
	_, err := a.Provision(context.Background(),
		adapter.Target{ID: "bench-a", URL: "http://bench-a.example/"},
		adapter.ProvisionConfig{
			CheckFrequency:    5 * time.Minute,
			MaintenanceWindow: &adapter.MaintenanceWindow{Start: start, End: start.Add(30 * time.Minute)},
		},
	)
	if err == nil {
		t.Fatal("expected error from editMonitor failure")
	}
	sawMWindowDelete := false
	sawMonitorDelete := false
	for _, r := range rf.Requests() {
		if r.Path == "/deleteMWindow" && formOf(r).Get("id") == "9000" {
			sawMWindowDelete = true
		}
		if r.Path == "/deleteMonitor" && formOf(r).Get("id") == "777" {
			sawMonitorDelete = true
		}
	}
	if !sawMWindowDelete {
		t.Errorf("expected /deleteMWindow rollback for window 9000; got %+v", rf.Requests())
	}
	if !sawMonitorDelete {
		t.Errorf("expected /deleteMonitor rollback for monitor 777; got %+v", rf.Requests())
	}
}

// TestDeprovision_DeletesMaintenanceFirst — when handle has
// maintenance_id, Deprovision sends /deleteMWindow before /deleteMonitor.
func TestDeprovision_DeletesMaintenanceFirst(t *testing.T) {
	srv, rf := adaptertest.NewRoutedFake(t, []adaptertest.RoutedResponse{
		{Method: "POST", PathPrefix: "/deleteMWindow", Status: 200, Body: `{"stat":"ok"}`},
		{Method: "POST", PathPrefix: "/deleteMonitor", Status: 200, Body: `{"stat":"ok"}`},
	})
	defer srv.Close()

	a := newTestAdapter(srv.URL, "u123-XXX")
	handle := adapter.MonitorHandle{
		MonitorID: "777",
		Fields:    map[string]string{"maintenance_id": "9000"},
	}
	if err := a.Deprovision(context.Background(), handle); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if len(rf.Requests()) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(rf.Requests()))
	}
	if rf.Requests()[0].Path != "/deleteMWindow" {
		t.Errorf("first request = %q, want /deleteMWindow (must come first)", rf.Requests()[0].Path)
	}
	if rf.Requests()[1].Path != "/deleteMonitor" {
		t.Errorf("second request = %q, want /deleteMonitor", rf.Requests()[1].Path)
	}
}
