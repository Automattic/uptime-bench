package jetmoncapacity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

func TestBuildCapacityReplayPlanMapsExplicitHostStartToService(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{HostPattern: "site-%07d.example.test"},
		CapacityReplay: CapacityReplayConfig{
			Enabled: true,
			Seed:    100,
			Events: []CapacityReplayEvent{{
				ID:          "v2-explicit-range",
				Offset:      "1m",
				Duration:    "2m",
				Type:        "http_status",
				StatusCode:  503,
				HostStart:   500001,
				HostCount:   5,
				SampleCount: 5,
			}},
		},
	}
	services := []ServiceLifecycle{
		{ID: "jetmon-v1", Config: Config{URLNumberStart: 1, BlogIDStart: 8000000000000000, Count: 5}},
		{ID: "jetmon-v2", Config: Config{URLNumberStart: 500001, BlogIDStart: 8000001000000000, Count: 5}},
	}

	plan, err := buildCapacityReplayPlan(cfg, services, 5, 10*time.Minute, "run-1")
	if err != nil {
		t.Fatalf("buildCapacityReplayPlan: %v", err)
	}
	if len(plan.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(plan.Events))
	}
	event := plan.Events[0]
	wantNumbers := []int64{500001, 500002, 500003, 500004, 500005}
	if !reflect.DeepEqual(event.HostNumbers, wantNumbers) {
		t.Fatalf("HostNumbers = %#v, want %#v", event.HostNumbers, wantNumbers)
	}
	if len(event.Services) != 1 || event.Services[0].Service != "jetmon-v2" {
		t.Fatalf("Services = %#v, want only jetmon-v2 mapping", event.Services)
	}
	if !reflect.DeepEqual(event.Services[0].HostNumbers, wantNumbers) {
		t.Fatalf("service HostNumbers = %#v, want %#v", event.Services[0].HostNumbers, wantNumbers)
	}
}

func TestBuildCapacityReplayPlanSupportsMethodScopedServiceEvents(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{HostPattern: "site-%07d.example.test"},
		CapacityReplay: CapacityReplayConfig{
			Enabled: true,
			Seed:    300,
			Events: []CapacityReplayEvent{{
				ID:          "v1-head-503",
				Offset:      "1m",
				Duration:    "2m",
				Type:        "http_method_status",
				Method:      "HEAD",
				StatusCode:  503,
				HostStart:   1,
				HostCount:   5,
				SampleCount: 5,
			}, {
				ID:          "v2-get-503",
				Offset:      "1m",
				Duration:    "2m",
				Type:        "http_method_status",
				Method:      "GET",
				StatusCode:  503,
				HostStart:   500001,
				HostCount:   5,
				SampleCount: 5,
			}},
		},
	}
	services := []ServiceLifecycle{
		{ID: "jetmon-v1", Config: Config{URLNumberStart: 1, BlogIDStart: 8000000000000000, Count: 5}},
		{ID: "jetmon-v2", Config: Config{URLNumberStart: 500001, BlogIDStart: 8000001000000000, Count: 5}},
	}

	plan, err := buildCapacityReplayPlan(cfg, services, 5, 10*time.Minute, "run-1")
	if err != nil {
		t.Fatalf("buildCapacityReplayPlan: %v", err)
	}
	if len(plan.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(plan.Events))
	}

	v1 := plan.Events[0]
	if v1.Type != "http_method_status" || v1.Method != "HEAD" || v1.StatusCode != 503 {
		t.Fatalf("v1 event = %#v, want HEAD-scoped 503", v1)
	}
	if len(v1.Services) != 1 || v1.Services[0].Service != "jetmon-v1" {
		t.Fatalf("v1 Services = %#v, want only jetmon-v1", v1.Services)
	}
	if !reflect.DeepEqual(v1.HostNumbers, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("v1 HostNumbers = %#v, want v1 range", v1.HostNumbers)
	}
	if params := replayFailureParams(v1); params["method"] != "HEAD" || params["status_code"] != 503 {
		t.Fatalf("v1 replay params = %#v, want method HEAD and status 503", params)
	}

	v2 := plan.Events[1]
	if v2.Type != "http_method_status" || v2.Method != "GET" || v2.StatusCode != 503 {
		t.Fatalf("v2 event = %#v, want GET-scoped 503", v2)
	}
	if len(v2.Services) != 1 || v2.Services[0].Service != "jetmon-v2" {
		t.Fatalf("v2 Services = %#v, want only jetmon-v2", v2.Services)
	}
	if !reflect.DeepEqual(v2.HostNumbers, []int64{500001, 500002, 500003, 500004, 500005}) {
		t.Fatalf("v2 HostNumbers = %#v, want v2 range", v2.HostNumbers)
	}
	if params := replayFailureParams(v2); params["method"] != "GET" || params["status_code"] != 503 {
		t.Fatalf("v2 replay params = %#v, want method GET and status 503", params)
	}
}

func TestBuildCapacityReplayPlanSamplesEachSelectedServiceRange(t *testing.T) {
	cfg := RunConfig{
		Targets: TargetConfig{HostPattern: "site-%07d.example.test"},
		CapacityReplay: CapacityReplayConfig{
			Enabled: true,
			Seed:    200,
			Events: []CapacityReplayEvent{{
				ID:          "all-services",
				Offset:      "1m",
				Duration:    "2m",
				Type:        "http_status",
				StatusCode:  503,
				HostCount:   10,
				SampleCount: 3,
			}},
		},
	}
	services := []ServiceLifecycle{
		{ID: "jetmon-v1", Config: Config{URLNumberStart: 1, BlogIDStart: 8000000000000000, Count: 10}},
		{ID: "jetmon-v2", Config: Config{URLNumberStart: 500001, BlogIDStart: 8000001000000000, Count: 10}},
	}

	plan, err := buildCapacityReplayPlan(cfg, services, 10, 10*time.Minute, "run-1")
	if err != nil {
		t.Fatalf("buildCapacityReplayPlan: %v", err)
	}
	event := plan.Events[0]
	if len(event.Services) != 2 {
		t.Fatalf("Services = %#v, want two service mappings", event.Services)
	}
	for _, service := range event.Services {
		if len(service.HostNumbers) != 3 || len(service.Hosts) != 3 {
			t.Fatalf("%s service hosts = %#v, want three sampled hosts", service.Service, service)
		}
	}
	if len(event.Hosts) != 6 {
		t.Fatalf("union host count = %d, want 6", len(event.Hosts))
	}
}

func TestExecuteCapacityReplayRunsSameOffsetEventsConcurrently(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	seenBoth := make(chan struct{})
	var seenBothOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/activate":
			var req control.ActivateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode activate: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			if active == 2 {
				seenBothOnce.Do(func() { close(seenBoth) })
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/deactivate":
			var req control.DeactivateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode deactivate: %v", err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			mu.Lock()
			active--
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	release := make(chan struct{})
	runner := Runner{Sleeper: replayBarrierSleeper{release: release}}
	plan := CapacityReplayPlan{
		RunID:            "run-1",
		TargetControlURL: server.URL,
		Events: []CapacityReplayEventPlan{{
			ID:       "v1",
			Offset:   "0s",
			Duration: "1h",
			Type:     "http_method_status",
			Method:   "HEAD",
			Hosts:    []string{"site-0000001.example.test"},
		}, {
			ID:       "v2",
			Offset:   "0s",
			Duration: "1h",
			Type:     "http_method_status",
			Method:   "GET",
			Hosts:    []string{"site-0500001.example.test"},
		}},
	}

	done := make(chan CapacityReplayRun, 1)
	go func() {
		done <- runner.executeCapacityReplay(context.Background(), plan, "token", time.Second)
	}()

	select {
	case <-seenBoth:
	case <-time.After(2 * time.Second):
		t.Fatal("same-offset replay events did not overlap before deactivation")
	}
	close(release)

	var run CapacityReplayRun
	select {
	case run = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("executeCapacityReplay did not finish after releasing sleepers")
	}
	if run.Status != "pass" || run.Error != "" {
		t.Fatalf("run status/error = %q/%q, want pass", run.Status, run.Error)
	}
	if len(run.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(run.Events))
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 2 {
		t.Fatalf("max active failures = %d, want overlapping 2", maxActive)
	}
	if active != 0 {
		t.Fatalf("active failures after run = %d, want 0", active)
	}
}

type replayBarrierSleeper struct {
	release <-chan struct{}
}

func (s replayBarrierSleeper) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
