package targetserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCapacityObserverRecordsGeneratedHosts(t *testing.T) {
	observer := NewCapacityObserver()
	start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	if _, err := observer.Reset(CapacityObserveResetRequest{
		RunID:                "run-1",
		ActiveCount:          3,
		CheckIntervalSeconds: 60,
		Services: []CapacityObserveService{{
			ID:          "jetmon-v1",
			HostPattern: "site-%07d.load.example.test",
			URLStart:    1,
		}},
	}, start); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	observer.RecordResponse("site-0000001.load.example.test", http.MethodGet, http.StatusOK, start.Add(10*time.Second))
	observer.RecordResponse("site-0000002.load.example.test:80", http.MethodHead, http.StatusServiceUnavailable, start.Add(20*time.Second))
	observer.Record("site-0000004.load.example.test", http.MethodGet, start.Add(30*time.Second))
	observer.Record("other.example.test", http.MethodGet, start.Add(40*time.Second))

	summary := observer.Summary(start.Add(2 * time.Minute))
	if !summary.Active || summary.RunID != "run-1" {
		t.Fatalf("summary active/run = %t/%q", summary.Active, summary.RunID)
	}
	if len(summary.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(summary.Services))
	}
	service := summary.Services[0]
	if service.ExpectedSites != 3 {
		t.Fatalf("ExpectedSites = %d, want 3", service.ExpectedSites)
	}
	if service.ObservedSites != 2 || service.NeverSeenSites != 1 {
		t.Fatalf("observed/never = %d/%d, want 2/1", service.ObservedSites, service.NeverSeenSites)
	}
	if service.TotalRequests != 2 {
		t.Fatalf("TotalRequests = %d, want 2", service.TotalRequests)
	}
	if service.MethodCounts["GET"] != 1 || service.MethodCounts["HEAD"] != 1 {
		t.Fatalf("MethodCounts = %#v, want one GET and one HEAD", service.MethodCounts)
	}
	if service.StatusCounts["200"] != 1 || service.StatusCounts["503"] != 1 {
		t.Fatalf("StatusCounts = %#v, want one 200 and one 503", service.StatusCounts)
	}
	if service.StatusHostCounts["503"] != 1 {
		t.Fatalf("StatusHostCounts = %#v, want one distinct 503 host", service.StatusHostCounts)
	}
	if service.ExpectedMinChecksPerSite != 2 || service.ExpectedMinRequests != 6 {
		t.Fatalf("expected checks/requests = %d/%d, want 2/6", service.ExpectedMinChecksPerSite, service.ExpectedMinRequests)
	}
}

func TestCapacityObserverHandlers(t *testing.T) {
	observer := NewCapacityObserver()
	mux := http.NewServeMux()
	RegisterCapacityObserverHandlers(mux, observer)

	body, err := json.Marshal(CapacityObserveResetRequest{
		ActiveCount: 1,
		Services: []CapacityObserveService{{
			ID:          "jetmon-v2",
			HostPattern: "site-%07d.load.example.test",
			URLStart:    10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/capacity/observe/reset", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("reset status = %d body=%s", w.Code, w.Body.String())
	}

	observer.Record("site-0000010.load.example.test", http.MethodGet, time.Now().UTC())
	req = httptest.NewRequest(http.MethodGet, "/capacity/observe/summary", nil)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("summary status = %d body=%s", w.Code, w.Body.String())
	}
	var summary CapacityObserveSummary
	if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if len(summary.Services) != 1 || summary.Services[0].ObservedSites != 1 {
		t.Fatalf("summary = %#v, want one observed site", summary)
	}
}
