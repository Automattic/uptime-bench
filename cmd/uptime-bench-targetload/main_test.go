package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDoRequestConnectAddressPreservesHostHeader(t *testing.T) {
	gotHost := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost <- r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	res := doRequest(context.Background(), srv.Client(), nil, options{
		URLPattern:     "http://site-%07d.example.com/",
		Start:          1,
		Hosts:          1,
		Requests:       1,
		Concurrency:    1,
		Timeout:        time.Second,
		ConnectAddress: u.Host,
		Method:         http.MethodGet,
	}, 1)
	if res.Error != "" || res.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v", res)
	}
	if got := <-gotHost; got != "site-0000001.example.com" {
		t.Fatalf("Host = %q, want generated host", got)
	}
}

func TestSummarize(t *testing.T) {
	results := []result{
		{Latency: 10 * time.Millisecond, DNSLatency: time.Millisecond, StatusCode: 200},
		{Latency: 20 * time.Millisecond, DNSLatency: 2 * time.Millisecond, StatusCode: 200},
		{Latency: 30 * time.Millisecond, DNSLatency: 3 * time.Millisecond, StatusCode: 503},
		{Latency: 40 * time.Millisecond, Error: "boom"},
	}

	rep := summarize("http://site-%07d.example.com/", 1, 10, 4, 2, "127.0.0.1:53", "", time.Second, results)
	if rep.Success != 2 || rep.Failure != 2 {
		t.Fatalf("success/failure = %d/%d, want 2/2", rep.Success, rep.Failure)
	}
	if rep.StatusCodes["200"] != 2 || rep.StatusCodes["503"] != 1 || rep.StatusCodes["error"] != 1 {
		t.Fatalf("StatusCodes = %+v", rep.StatusCodes)
	}
	if rep.Errors["request_error"] != 1 {
		t.Fatalf("Errors = %+v", rep.Errors)
	}
	if rep.LatencyMS.Avg != 25 || rep.LatencyMS.P50 != 25 || rep.LatencyMS.P95 != 38.5 {
		t.Fatalf("LatencyMS = %+v", rep.LatencyMS)
	}
	if rep.DNSLatencyMS.Avg != 2 || rep.DNSLatencyMS.P95 != 2.9 {
		t.Fatalf("DNSLatencyMS = %+v", rep.DNSLatencyMS)
	}
}

func TestSummarizeValuesEmpty(t *testing.T) {
	if got := summarizeValues(nil); got != (stats{}) {
		t.Fatalf("summarizeValues(nil) = %+v", got)
	}
}

func TestFormatStatusCodes(t *testing.T) {
	got := formatStatusCodes(map[string]int{"error": 1, "200": 2})
	if got != "200=2 error=1" {
		t.Fatalf("formatStatusCodes = %q", got)
	}
}

func TestWriteMarkdown(t *testing.T) {
	rep := summarize("http://site-%07d.example.com/", 1, 10, 3, 2, "127.0.0.1:53", "", time.Second, []result{
		{Latency: 10 * time.Millisecond, DNSLatency: time.Millisecond, StatusCode: 200},
		{Latency: 20 * time.Millisecond, DNSLatency: 2 * time.Millisecond, StatusCode: 200},
		{Latency: 30 * time.Millisecond, DNSLatency: 3 * time.Millisecond, Error: "dns: lookup site.example.com: i/o timeout"},
	})
	var buf bytes.Buffer
	writeMarkdown(&buf, rep)
	out := buf.String()
	for _, want := range []string{"# Target Load Report", "## Analysis", "Success rate", "dns_timeout=1", "## Latency", "| DNS |"} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown missing %q:\n%s", want, out)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := map[string]string{
		"dns: lookup site-1.example.com: i/o timeout":                                      "dns_timeout",
		"dns: lookup site-1.example.com: no such host":                                     "dns_no_such_host",
		"dns: read udp 127.0.0.1:12345->127.0.0.1:53: read: connection refused":            "dns_connection_refused",
		"Get \"http://127.0.0.1/\": context deadline exceeded":                             "request_timeout",
		"Get \"http://127.0.0.1/\": dial tcp 127.0.0.1:18080: connect: connection refused": "connect_refused",
		"Get \"http://127.0.0.1/\": read: connection reset by peer":                        "connection_reset",
		"EOF": "eof",
	}
	for input, want := range tests {
		if got := classifyError(input); got != want {
			t.Fatalf("classifyError(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestValidateURLPattern(t *testing.T) {
	for _, pattern := range []string{
		"http://site-%07d.example.com/",
		"https://site-%d.example.com/health",
	} {
		if err := validateURLPattern(pattern, 1); err != nil {
			t.Fatalf("validateURLPattern(%q): %v", pattern, err)
		}
	}

	for _, pattern := range []string{
		"http://site.example.com/",
		"ftp://site-%07d.example.com/",
		"http:///site-%07d.example.com/",
	} {
		if err := validateURLPattern(pattern, 1); err == nil {
			t.Fatalf("validateURLPattern(%q) returned nil error", pattern)
		}
	}
}
