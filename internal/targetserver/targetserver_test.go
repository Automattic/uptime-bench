package targetserver

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// ─── peekHTTPHost ────────────────────────────────────────────────────────────

func TestPeekHTTPHost(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "GET / HTTP/1.1\r\nHost: example.com\r\n\r\n", "example.com"},
		{"with port", "GET / HTTP/1.1\r\nHost: example.com:8080\r\n\r\n", "example.com"},
		{"no leading space after colon", "GET / HTTP/1.1\r\nHost:example.com\r\n\r\n", "example.com"},
		{"different case", "GET / HTTP/1.1\r\nhost: example.com\r\n\r\n", "example.com"},
		{"after other headers", "GET / HTTP/1.1\r\nUser-Agent: x\r\nHost: example.com\r\n\r\n", "example.com"},
		{"missing host", "GET / HTTP/1.1\r\nUser-Agent: x\r\n\r\n", ""},
		{"truncated", "GET / HTTP/1.1\r\nHost: exa", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			br := bufio.NewReaderSize(strings.NewReader(tc.in), 4096)
			got := peekHTTPHost(br)
			if got != tc.want {
				t.Fatalf("peekHTTPHost = %q, want %q", got, tc.want)
			}
		})
	}
}

// peekHTTPHost must NOT consume the bytes — the proxy needs to replay them
// to the upstream server intact.
func TestPeekHTTPHost_DoesNotConsume(t *testing.T) {
	in := "GET / HTTP/1.1\r\nHost: example.com\r\n\r\nbody"
	br := bufio.NewReaderSize(strings.NewReader(in), 4096)
	_ = peekHTTPHost(br)
	all, err := io.ReadAll(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(all) != in {
		t.Fatalf("after peek: got %q, want full input back", all)
	}
}

func TestPeekHTTPHost_DoesNotWaitForFullBuffer(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	defer server.Close()

	br := bufio.NewReaderSize(server, 4096)
	done := make(chan string, 1)
	go func() {
		done <- peekHTTPHost(br)
	}()

	_, err := client.Write([]byte("GET / HTTP/1.1\r\nHost: example.com\r\n\r\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	select {
	case got := <-done:
		if got != "example.com" {
			t.Fatalf("peekHTTPHost = %q, want example.com", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("peekHTTPHost blocked waiting for the full buffer")
	}
}

// ─── parseRemoteIP ───────────────────────────────────────────────────────────

type fakeAddr string

func (a fakeAddr) Network() string { return "tcp" }
func (a fakeAddr) String() string  { return string(a) }

func TestParseRemoteIP(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4:5678":    "1.2.3.4",
		"[::1]:80":        "::1",
		"[2001:db8::1]:9": "2001:db8::1",
		"":                "",
		"garbage":         "",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got := parseRemoteIP(fakeAddr(in))
			if want == "" {
				if got != nil {
					t.Fatalf("parseRemoteIP(%q) = %v, want nil", in, got)
				}
				return
			}
			if got == nil || !got.Equal(net.ParseIP(want)) {
				t.Fatalf("parseRemoteIP(%q) = %v, want %s", in, got, want)
			}
		})
	}
}

// ─── VirtualHostHandler — each failure type ──────────────────────────────────

func newHandler() (*VirtualHostHandler, *control.FailureRegistry) {
	reg := control.NewRegistry()
	return &VirtualHostHandler{Registry: reg}, reg
}

func get(t *testing.T, h http.Handler, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	return request(t, h, http.MethodGet, host, path)
}

func request(t *testing.T, h http.Handler, method, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestVHH_HealthyByDefault(t *testing.T) {
	h, _ := newHandler()
	w := get(t, h, "site.local", "/")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "uptime-bench-canary") {
		t.Fatalf("healthy body missing canary marker")
	}
}

func TestVHH_HTTPStatus(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_status", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"status_code": float64(503)},
	}, 0)
	w := get(t, h, "site.local", "/")
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

func TestVHH_HTTPStatusFallsBackTo500(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_status", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		// no status_code param
	}, 0)
	w := get(t, h, "site.local", "/")
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500 (default)", w.Code)
	}
}

func TestVHH_HTTPMethodStatus_HeadFailsGetHealthy(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_method_status", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "HEAD", "status_code": float64(405)},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD status = %d, want 405", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	if !strings.Contains(get.Body.String(), "uptime-bench-canary") {
		t.Fatal("GET response should be the healthy page")
	}
}

func TestVHH_HTTPMethodStatus_GetFailsHeadHealthy(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_method_status", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "GET", "status_code": float64(503)},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if get.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET status = %d, want 503", get.Code)
	}
}

func TestVHH_HTTPRedirect_MethodScoped(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_redirect", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "GET", "variant": "loop"},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if get.Code != http.StatusFound {
		t.Fatalf("GET status = %d, want 302", get.Code)
	}
	if loc := get.Header().Get("Location"); loc != "/" {
		t.Fatalf("GET Location = %q, want /", loc)
	}
}

func TestVHH_HTTPPartial_MethodScoped(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_partial", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "GET", "truncate_after_bytes": 9},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	if got := get.Body.String(); got != "<!DOCTYPE" {
		t.Fatalf("GET body = %q, want first 9 healthy bytes", got)
	}
}

func TestVHH_HTTPBody_MethodScoped(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "GET", "content": "defacement"},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if !strings.Contains(get.Body.String(), "H4CK3D") {
		t.Fatalf("GET body missing defacement marker: %q", get.Body.String())
	}
}

func TestVHH_HTTPTimeout_MethodScoped(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_timeout", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"method": "GET", "delay": "1ms"},
	}, 0)

	head := request(t, h, http.MethodHead, "site.local", "/")
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", head.Code)
	}
	get := request(t, h, http.MethodGet, "site.local", "/")
	if get.Code != http.StatusGatewayTimeout {
		t.Fatalf("GET status = %d, want 504", get.Code)
	}
}

// TestVHH_HTTPTimeout_RespectsRequestCancel — the http_timeout path
// sleeps until the configured delay or the request context is cancelled.
// A monitor that gives up before the delay elapses must not leave the
// handler stuck.
func TestVHH_HTTPTimeout_RespectsRequestCancel(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_timeout", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"delay": "10s"},
	}, 0)

	ctx, cancel := context.WithCancel(context.Background())
	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	r.Host = "site.local"
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		h.ServeHTTP(w, r)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Handler returned promptly on ctx cancel.
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after ctx cancel; http_timeout path may not honor request context")
	}
}

func TestVHH_HTTPBody_Empty(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"content": "empty"},
	}, 0)
	w := get(t, h, "site.local", "/")
	if w.Body.String() != "<html></html>" {
		t.Fatalf("body = %q, want <html></html>", w.Body.String())
	}
}

func TestVHH_HTTPBody_Ransomware(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"content": "ransomware"},
	}, 0)
	w := get(t, h, "site.local", "/")
	body := w.Body.String()
	// Status is 200 — that's the whole point of the scenario: monitors
	// that only check status codes won't notice this.
	if w.Code != 200 {
		t.Fatalf("ransomware status = %d, want 200 (silent failure)", w.Code)
	}
	if !strings.Contains(body, "DARKLOCK RANSOMWARE") {
		t.Fatal("ransomware body missing identifying string")
	}
	if strings.Contains(body, "uptime-bench-canary") {
		t.Fatal("ransomware page should not contain canary marker")
	}
}

func TestVHH_HTTPBody_ContentVariants(t *testing.T) {
	cases := []struct {
		content string
		marker  string
	}{
		{content: "error_page", marker: "Error establishing a database connection"},
		{content: "defacement", marker: "H4CK3D"},
		{content: "malicious_script", marker: "metrics.evil-cdn.example/collect.js"},
		{content: "spam_links", marker: "buy cheap viagra online no prescription"},
	}

	for _, tc := range cases {
		t.Run(tc.content, func(t *testing.T) {
			h, reg := newHandler()
			reg.Set(control.FailureSpec{
				Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
				Params: map[string]any{"content": tc.content},
			}, 0)
			w := get(t, h, "site.local", "/")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			if !strings.Contains(w.Body.String(), tc.marker) {
				t.Fatalf("body missing marker %q: %s", tc.marker, w.Body.String())
			}
		})
	}
}

func TestVHH_HTTPBody_KeywordInjected(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"content": "keyword_injected", "keyword": "betting site!"},
	}, 0)
	w := get(t, h, "site.local", "/")
	if !strings.Contains(w.Body.String(), "betting site!") {
		t.Fatal("keyword_injected body missing the injected keyword")
	}
}

func TestVHH_HTTPBody_KeywordMissing(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		// keyword "Operational" is in the healthy page; a keyword_missing
		// response strips it out.
		Params: map[string]any{"content": "keyword_missing", "keyword": "uptime-bench-canary"},
	}, 0)
	w := get(t, h, "site.local", "/")
	if strings.Contains(w.Body.String(), "uptime-bench-canary") {
		t.Fatal("keyword_missing body still contains the canary marker")
	}
}

func TestVHH_HTTPBody_UnknownContentIs500(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_body", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"content": "made_up"},
	}, 0)
	w := get(t, h, "site.local", "/")
	if w.Code != 500 {
		t.Fatalf("unknown content variant: status = %d, want 500", w.Code)
	}
}

func TestVHH_HTTPRedirect_Loop(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_redirect", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"variant": "loop"},
	}, 0)
	w := get(t, h, "site.local", "/")
	if w.Code != 302 {
		t.Fatalf("redirect status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if loc != "/" {
		t.Fatalf("redirect Location = %q, want / (loop)", loc)
	}
}

func TestVHH_HTTPRedirect_Chain(t *testing.T) {
	h, reg := newHandler()
	reg.Set(control.FailureSpec{
		Type: "http_redirect", Host: "site.local", Duration: time.Minute, Rate: 1.0,
		Params: map[string]any{"variant": "chain"},
	}, 0)
	w := get(t, h, "site.local", "/foo")
	loc := w.Header().Get("Location")
	if loc != "/foo/redir" {
		t.Fatalf("redirect Location = %q, want /foo/redir (chain)", loc)
	}
}

// ─── applyGeoFailure ─────────────────────────────────────────────────────────

// pipeConns returns two connected net.Conns; bytes written to one are
// readable from the other.
func pipeConns() (net.Conn, net.Conn) {
	return net.Pipe()
}

func TestApplyGeoFailure_TCPRefused_WritesNothing(t *testing.T) {
	server, client := pipeConns()
	defer client.Close()

	go func() {
		applyGeoFailure(server, control.FailureSpec{Type: "tcp_refused"})
		server.Close()
	}()

	// Reader should see EOF immediately, no bytes.
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, err := client.Read(buf)
	if n != 0 {
		t.Fatalf("tcp_refused wrote %d bytes; should write none", n)
	}
	if err == nil {
		t.Fatal("tcp_refused: expected EOF or close, got nil error")
	}
}

func TestApplyGeoFailure_HTTPStatus_WritesResponse(t *testing.T) {
	server, client := pipeConns()
	defer client.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		applyGeoFailure(server, control.FailureSpec{
			Type: "http_status",
			Params: map[string]any{
				"status_code": float64(503),
			},
		})
		server.Close()
	}()

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	body, err := io.ReadAll(client)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	wg.Wait()

	got := string(body)
	if !strings.HasPrefix(got, "HTTP/1.1 503 Service Unavailable\r\n") {
		t.Fatalf("response status line wrong:\n%s", got)
	}
	if !strings.Contains(got, "Connection: close") {
		t.Fatalf("response missing Connection: close header:\n%s", got)
	}
	if !strings.Contains(got, "\r\n\r\nService Unavailable\n") {
		t.Fatalf("response body wrong:\n%s", got)
	}
}

// ─── HandleTCP ───────────────────────────────────────────────────────────────

// TestHandleTCP_RefusedClosesImmediately verifies the global tcp_refused
// path: HandleTCP returns and closes the client connection without ever
// reading the request bytes.
func TestHandleTCP_RefusedClosesImmediately(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type: "tcp_refused", Duration: time.Minute, Rate: 1.0,
	}, 0)

	server, client := net.Pipe()

	done := make(chan struct{})
	go func() {
		HandleTCP(server, registry, "127.0.0.1:1") // unreachable; should never be dialed
		close(done)
	}()

	// Try to write a request. The handler should close before reading it.
	_ = client.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = client.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 64)
	n, _ := client.Read(buf)
	if n != 0 {
		t.Fatalf("tcp_refused wrote %d bytes; should write none", n)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTCP did not return after tcp_refused")
	}
	client.Close()
}

func TestHandleTCP_ForwardsHealthyRequest(t *testing.T) {
	h, _ := newHandler()
	upstream := httptest.NewServer(h)
	defer upstream.Close()

	upstreamAddr := strings.TrimPrefix(upstream.URL, "http://")
	server, client := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		HandleTCP(server, control.NewRegistry(), upstreamAddr)
		close(done)
	}()

	_, err := client.Write([]byte("GET / HTTP/1.1\r\nHost: site.local\r\nConnection: close\r\n\r\n"))
	if err != nil {
		t.Fatalf("write request: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleTCP did not return after client close")
	}
}

// ─── benchmarks (for the canary regression check) ───────────────────────────

func BenchmarkPeekHTTPHost(b *testing.B) {
	in := "GET /api/health HTTP/1.1\r\nUser-Agent: monitor/1.0\r\nHost: bench-a.example.com\r\nAccept: */*\r\n\r\n"
	for i := 0; i < b.N; i++ {
		br := bufio.NewReaderSize(bytes.NewReader([]byte(in)), 4096)
		_ = peekHTTPHost(br)
	}
}
