package uptimekuma

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
	caps := New("uptime-kuma-head", "http://bridge", "tok", WithHTTPMethod("HEAD")).Capabilities()
	if caps.SupportsKeyword || caps.SupportsInvertedKeyword {
		t.Fatalf("HEAD capabilities should not expose keyword support: %+v", caps)
	}
}

func TestProvisionRetrieveDeprovision(t *testing.T) {
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
			_ = json.NewEncoder(w).Encode(createResponse{ID: req.ID, MonitorID: "123"})
		case r.Method == http.MethodGet && r.URL.Path == "/monitors/123/beats":
			_ = json.NewEncoder(w).Encode(beatsResponse{
				MonitorID: "123",
				Beats: []beat{
					{
						Status: 0,
						Msg:    "503 - Service Unavailable",
						Time:   failureStart.Add(10 * time.Second).Format("2006-01-02 15:04:05.000"),
						Ping:   0,
					},
					{
						Status: 1,
						Msg:    "200 - OK",
						Time:   failureEnd.Add(10 * time.Second).Format("2006-01-02 15:04:05.000"),
						Ping:   95,
					},
				},
			})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/123"):
			deleted = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	a := New("uptime-kuma", srv.URL, "tok")
	handle, err := a.Provision(context.Background(), adapter.Target{ID: "bench-a", URL: "http://bench.example/"}, adapter.ProvisionConfig{
		CheckFrequency: time.Minute,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if handle.MonitorID != "123" {
		t.Fatalf("monitor id = %q", handle.MonitorID)
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
	if res.Reports[0].RawClassification != "http" {
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
