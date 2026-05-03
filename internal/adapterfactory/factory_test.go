package adapterfactory

import (
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

func TestForService_BetterUptimeHEADDisablesKeywordCapabilities(t *testing.T) {
	a, err := ForService(serviceconfig.Service{
		ID:   "better-uptime-head",
		Type: "better-uptime",
		Auth: map[string]string{"token": "tok", "http_method": "HEAD"},
	})
	if err != nil {
		t.Fatalf("ForService: %v", err)
	}
	caps := a.Capabilities()
	if caps.SupportsKeyword || caps.SupportsInvertedKeyword {
		t.Fatalf("HEAD caps = %+v, want keyword capabilities disabled", caps)
	}
}

func TestForService_DatadogHEADDisablesKeywordCapabilities(t *testing.T) {
	a, err := ForService(serviceconfig.Service{
		ID:   "datadog-head",
		Type: "datadog-synthetics",
		Auth: map[string]string{"api_key": "ak", "app_key": "pk", "http_method": "HEAD"},
	})
	if err != nil {
		t.Fatalf("ForService: %v", err)
	}
	caps := a.Capabilities()
	if caps.SupportsKeyword || caps.SupportsInvertedKeyword {
		t.Fatalf("HEAD caps = %+v, want keyword capabilities disabled", caps)
	}
}

func TestForService_SelfHostedHEADDisablesKeywordCapabilities(t *testing.T) {
	for _, svc := range []serviceconfig.Service{
		{ID: "gatus-head", Type: "gatus", URL: "http://bridge", Auth: map[string]string{"token": "tok", "http_method": "HEAD"}},
		{ID: "uptime-kuma-head", Type: "uptime-kuma", URL: "http://bridge", Auth: map[string]string{"token": "tok", "http_method": "HEAD"}},
	} {
		t.Run(svc.Type, func(t *testing.T) {
			a, err := ForService(svc)
			if err != nil {
				t.Fatalf("ForService: %v", err)
			}
			caps := a.Capabilities()
			if caps.SupportsKeyword || caps.SupportsInvertedKeyword {
				t.Fatalf("HEAD caps = %+v, want keyword capabilities disabled", caps)
			}
		})
	}
}

func TestForService_InvalidHTTPMethod(t *testing.T) {
	for _, svc := range []serviceconfig.Service{
		{ID: "better", Type: "better-uptime", Auth: map[string]string{"token": "tok", "http_method": "TRACE"}},
		{ID: "datadog", Type: "datadog-synthetics", Auth: map[string]string{"api_key": "ak", "app_key": "pk", "http_method": "TRACE"}},
		{ID: "gatus", Type: "gatus", URL: "http://bridge", Auth: map[string]string{"token": "tok", "http_method": "TRACE"}},
		{ID: "uptime-kuma", Type: "uptime-kuma", URL: "http://bridge", Auth: map[string]string{"token": "tok", "http_method": "TRACE"}},
	} {
		t.Run(svc.Type, func(t *testing.T) {
			_, err := ForService(svc)
			if err == nil {
				t.Fatal("ForService succeeded, want error")
			}
			if !strings.Contains(err.Error(), "http_method") {
				t.Fatalf("err = %v, want http_method detail", err)
			}
		})
	}
}

func TestForService_BetterUptimeSupportsInvertedKeywordByDefault(t *testing.T) {
	a, err := ForService(serviceconfig.Service{
		ID:   "better-uptime",
		Type: "better-uptime",
		Auth: map[string]string{"token": "tok"},
	})
	if err != nil {
		t.Fatalf("ForService: %v", err)
	}
	if !a.Capabilities().SupportsInvertedKeyword {
		t.Fatal("Better Uptime GET lane should support inverted keyword checks")
	}
	var _ adapter.Adapter = a
}
