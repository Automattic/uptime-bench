package gatus

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func TestCapabilities_HEADDisablesBodyChecks(t *testing.T) {
	caps := New("gatus-head", "http://bridge", "tok", WithHTTPMethod("HEAD")).Capabilities()
	if caps.SupportsKeyword || caps.SupportsInvertedKeyword {
		t.Fatalf("HEAD capabilities should not expose keyword support: %+v", caps)
	}
}

func TestProvisionRejectsHEADKeyword(t *testing.T) {
	a := New("gatus-head", "http://bridge", "tok", WithHTTPMethod("HEAD"))
	_, err := a.Provision(context.Background(), adapter.Target{ID: "bench-a", URL: "http://bench.example/"}, adapter.ProvisionConfig{
		CheckFrequency: time.Minute,
		Keyword:        "canary",
		KeywordCheck:   adapter.KeywordCheckPresent,
	})
	if err == nil || !strings.Contains(err.Error(), "keyword monitoring requires a response body") {
		t.Fatalf("Provision error = %v, want HEAD keyword rejection", err)
	}
}

func TestProvisionRetrieveDeprovision(t *testing.T) {
	var monitorID string
	var deleted bool
	failureStart := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	failureEnd := failureStart.Add(2 * time.Minute)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/monitors":
			var req createRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			if req.URL != "http://bench.example/" {
				t.Fatalf("url = %q", req.URL)
			}
			if req.Method != http.MethodGet {
				t.Fatalf("method = %q", req.Method)
			}
			if req.Keyword != "canary" || req.KeywordCheck != adapter.KeywordCheckPresent {
				t.Fatalf("keyword fields = %q %q", req.Keyword, req.KeywordCheck)
			}
			monitorID = req.ID
			_ = json.NewEncoder(w).Encode(createResponse{ID: req.ID, MonitorID: "uptime-bench_" + req.ID})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/statuses"):
			if !strings.Contains(r.URL.Path, monitorID) {
				t.Fatalf("status path = %q, want monitor id %q", r.URL.Path, monitorID)
			}
			_ = json.NewEncoder(w).Encode(statusResponse{
				EndpointKey: "uptime-bench_uptime-bench-" + monitorID,
				Results: []statusResult{
					{
						Status:    503,
						Success:   false,
						Timestamp: failureStart.Add(10 * time.Second).Format(time.RFC3339Nano),
						ConditionResults: []conditionResult{{
							Condition: "[STATUS] == 200",
							Success:   false,
						}},
					},
					{
						Status:    200,
						Success:   true,
						Timestamp: failureEnd.Add(10 * time.Second).Format(time.RFC3339Nano),
						ConditionResults: []conditionResult{{
							Condition: "[STATUS] == 200",
							Success:   true,
						}},
					},
				},
			})
		case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, monitorID):
			deleted = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := New("gatus", srv.URL, "tok")
	handle, err := a.Provision(context.Background(), adapter.Target{ID: "bench-a", URL: "http://bench.example/"}, adapter.ProvisionConfig{
		CheckFrequency: time.Minute,
		Keyword:        "canary",
		KeywordCheck:   adapter.KeywordCheckPresent,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if handle.MonitorID == "" {
		t.Fatal("monitor id is empty")
	}
	res, err := a.Retrieve(context.Background(), handle, adapter.RunWindow{
		FailureStarted: failureStart,
		FailureEnded:   failureEnd,
		GracePeriodEnd: failureEnd.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveKnown || len(res.Reports) != 2 {
		t.Fatalf("retrieve = %+v", res)
	}
	if res.Reports[0].RawClassification != "http_503" {
		t.Fatalf("raw classification = %q", res.Reports[0].RawClassification)
	}
	if got := a.Normalize(res.Reports[0].RawClassification); got != "http_failure" {
		t.Fatalf("Normalize = %q", got)
	}
	if err := a.Deprovision(context.Background(), handle); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	if !deleted {
		t.Fatal("delete was not called")
	}
}

func TestRetrieveTruncatesBridgeErrors(t *testing.T) {
	longBody := strings.Repeat("x", 260)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/monitors/m/statuses" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		http.Error(w, longBody, http.StatusBadGateway)
	}))
	defer srv.Close()

	a := New("gatus", srv.URL, "tok")
	res, err := a.Retrieve(context.Background(), adapter.MonitorHandle{ServiceID: "gatus", MonitorID: "m"}, adapter.RunWindow{})
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if res.Status != adapter.RetrieveUnknown || res.ReasonCode != adapter.ReasonAdapterError {
		t.Fatalf("retrieve = %+v, want adapter_error unknown", res)
	}
	if strings.Contains(res.Reason, strings.Repeat("x", 220)) || !strings.Contains(res.Reason, "...") {
		t.Fatalf("reason was not truncated: %q", res.Reason)
	}
}

func TestCleanupStaleScopesDryRunAndDelete(t *testing.T) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/monitors":
			_ = json.NewEncoder(w).Encode(map[string]any{"monitors": []map[string]string{
				{"name": "uptime-bench-bootstrap", "url": "http://127.0.0.1:8080/health"},
				{"name": "uptime-bench_alpha", "url": "https://bench-a.example/check"},
				{"name": "uptime-bench_beta", "url": "https://outside.example/check"},
				{"name": "manual", "url": "https://bench-a.example/check"},
			}})
		case r.Method == http.MethodDelete:
			deleted = append(deleted, strings.TrimPrefix(r.URL.Path, "/monitors/"))
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := New("gatus", srv.URL, "tok")
	scope := adapter.CleanupScope{TargetHosts: []string{"bench-a.example"}}
	dry, err := a.CleanupStale(context.Background(), adapter.CleanupOptions{DryRun: true, Scope: scope})
	if err != nil {
		t.Fatalf("CleanupStale dry run: %v", err)
	}
	if len(dry.Actions) != 2 {
		t.Fatalf("dry run actions = %+v, want scoped candidate and skipped out-of-scope candidate", dry.Actions)
	}
	if dry.Actions[0].Action != adapter.CleanupActionWouldDelete || dry.Actions[0].Candidate.ResourceID != "alpha" {
		t.Fatalf("first dry run action = %+v", dry.Actions[0])
	}
	if dry.Actions[1].Action != adapter.CleanupActionSkipped || dry.Actions[1].Candidate.ResourceID != "beta" {
		t.Fatalf("second dry run action = %+v", dry.Actions[1])
	}
	if len(deleted) != 0 {
		t.Fatalf("dry run deleted resources: %v", deleted)
	}

	live, err := a.CleanupStale(context.Background(), adapter.CleanupOptions{Scope: scope})
	if err != nil {
		t.Fatalf("CleanupStale live: %v", err)
	}
	if len(live.Actions) != 2 || live.Actions[0].Action != adapter.CleanupActionDeleted || live.Actions[1].Action != adapter.CleanupActionSkipped {
		t.Fatalf("live actions = %+v", live.Actions)
	}
	if len(deleted) != 1 || deleted[0] != "alpha" {
		t.Fatalf("deleted = %v, want [alpha]", deleted)
	}
}
