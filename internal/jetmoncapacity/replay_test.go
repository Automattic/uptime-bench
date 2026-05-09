package jetmoncapacity

import (
	"reflect"
	"testing"
	"time"
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
