// Package adaptertest contains shared helpers for adapter unit tests.
//
// This package is intentionally importable from regular adapter test
// files (not _test) — it lets tests in different packages share the
// same fake-server scaffolding without copy-pasting it.
package adaptertest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// RequestRecord captures one inbound HTTP request. Body is the raw
// request body — tests parse it themselves (json.Unmarshal,
// url.ParseQuery, etc.) since adapter packages use different wire
// encodings.
type RequestRecord struct {
	Method string
	Path   string
	Body   []byte
}

// RoutedResponse describes one canned response a [RoutedFake] should
// return when an inbound request matches Method + PathPrefix.
type RoutedResponse struct {
	Method     string
	PathPrefix string
	Status     int
	Body       string
}

// RoutedFake routes inbound requests to canned responses by
// (method, path-prefix) and records every request in arrival order.
//
// Multiple responses may share a path prefix: the first matching entry
// wins, so list narrower prefixes before broader ones.
//
// Adapter unit tests use this to exercise multi-call provision flows
// (e.g. POST /monitors followed by PATCH /monitors/{id} for
// maintenance windows) and assert on the request order and shape.
type RoutedFake struct {
	t        *testing.T
	requests []RequestRecord
}

// NewRoutedFake returns a running httptest.Server plus the [RoutedFake]
// that backs it. Caller must Close the server (typically with defer).
//
// Unmatched requests fail the test with t.Errorf and respond 404 so
// the adapter sees an error.
func NewRoutedFake(t *testing.T, responses []RoutedResponse) (*httptest.Server, *RoutedFake) {
	t.Helper()
	rf := &RoutedFake{t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rf.requests = append(rf.requests, RequestRecord{
			Method: r.Method,
			Path:   r.URL.Path,
			Body:   body,
		})
		for _, resp := range responses {
			if resp.Method == r.Method && strings.HasPrefix(r.URL.Path, resp.PathPrefix) {
				w.WriteHeader(resp.Status)
				_, _ = w.Write([]byte(resp.Body))
				return
			}
		}
		t.Errorf("RoutedFake: no response matched %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	return srv, rf
}

// Requests returns the captured requests in arrival order.
func (rf *RoutedFake) Requests() []RequestRecord {
	return rf.requests
}
