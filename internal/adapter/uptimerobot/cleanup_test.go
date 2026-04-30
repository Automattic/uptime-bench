package uptimerobot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func TestCleanupStaleDryRunScopesBenchmarkMonitors(t *testing.T) {
	var deleteCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		switch r.URL.Path {
		case "/getMonitors":
			if form.Get("search") != "uptime-bench:" {
				t.Errorf("search = %q, want uptime-bench:", form.Get("search"))
			}
			_, _ = w.Write([]byte(`{"stat":"ok","monitors":[` +
				`{"id":1,"friendly_name":"uptime-bench: ur: bench-a","url":"http://bench-a.example/path"},` +
				`{"id":2,"friendly_name":"uptime-bench: ur: outside","url":"http://outside.example/path"},` +
				`{"id":3,"friendly_name":"customer monitor","url":"http://bench-a.example/path"}` +
				`]}`))
		case "/deleteMonitor":
			deleteCalls++
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":1}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL, "key")
	result, err := a.CleanupStale(context.Background(), adapter.CleanupOptions{
		DryRun: true,
		Scope:  adapter.CleanupScope{TargetURLs: []string{"http://bench-a.example/path"}},
	})
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("dry run made %d delete calls", deleteCalls)
	}
	if len(result.Actions) != 2 {
		t.Fatalf("actions = %+v, want 2 benchmark-owned candidates", result.Actions)
	}
	if result.Actions[0].Action != adapter.CleanupActionWouldDelete || result.Actions[0].Candidate.ResourceID != "1" {
		t.Fatalf("first action = %+v, want would_delete id=1", result.Actions[0])
	}
	if result.Actions[1].Action != adapter.CleanupActionSkipped || result.Actions[1].Candidate.ResourceID != "2" {
		t.Fatalf("second action = %+v, want skipped id=2", result.Actions[1])
	}
}

func TestCleanupStaleDeletesMatchingMonitor(t *testing.T) {
	var deletedID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		switch r.URL.Path {
		case "/getMonitors":
			_, _ = w.Write([]byte(`{"stat":"ok","monitors":[{"id":9,"friendly_name":"uptime-bench: ur: bench-a","url":"http://bench-a.example/path"}]}`))
		case "/deleteMonitor":
			deletedID = form.Get("id")
			_, _ = w.Write([]byte(`{"stat":"ok","monitor":{"id":9}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	a := newTestAdapter(srv.URL, "key")
	result, err := a.CleanupStale(context.Background(), adapter.CleanupOptions{
		Scope: adapter.CleanupScope{TargetHosts: []string{"bench-a.example"}},
	})
	if err != nil {
		t.Fatalf("CleanupStale: %v", err)
	}
	if deletedID != "9" {
		t.Fatalf("deleted id = %q, want 9", deletedID)
	}
	if len(result.Actions) != 1 || result.Actions[0].Action != adapter.CleanupActionDeleted {
		t.Fatalf("actions = %+v, want one deleted action", result.Actions)
	}
}
