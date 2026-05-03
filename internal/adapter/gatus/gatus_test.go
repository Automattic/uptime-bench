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
