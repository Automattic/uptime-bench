package adapterfactory

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapter/betteruptime"
	"github.com/Automattic/uptime-bench/internal/adapter/datadog"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv1"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv2"
	"github.com/Automattic/uptime-bench/internal/adapter/pingdom"
	"github.com/Automattic/uptime-bench/internal/adapter/uptimerobot"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// Factory builds an adapter from a service config entry.
type Factory func(id, url string, auth map[string]string) (adapter.Adapter, error)

// Registry maps service type names to their factory functions.
// To add a new service: implement its adapter package and add an entry here.
var Registry = map[string]Factory{
	"jetmon-v1": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		if apiURL == "" {
			return nil, fmt.Errorf("url is required (jetmon-v1 has no public API endpoint; point at jetmon-bridge)")
		}
		writeMode := auth["write_mode"] == "true"
		return jetmonv1.New(id, apiURL, auth["token"], writeMode), nil
	},
	"jetmon-v2": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		if apiURL == "" {
			return nil, fmt.Errorf("url is required (jetmon-v2 needs the Jetmon 2 /api/v1 endpoint)")
		}
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("jetmon-v2: auth.token is required")
		}
		var opts []jetmonv2.Option
		if raw := auth["bucket_no"]; raw != "" {
			bucketNo, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("jetmon-v2: auth.bucket_no must be an integer: %w", err)
			}
			if bucketNo < 0 {
				return nil, fmt.Errorf("jetmon-v2: auth.bucket_no must be non-negative")
			}
			opts = append(opts, jetmonv2.WithBucketNo(bucketNo))
		}
		return jetmonv2.New(id, apiURL, token, opts...), nil
	},
	"uptimerobot": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		key := auth["api_key"]
		if key == "" {
			return nil, fmt.Errorf("uptimerobot: auth.api_key is required")
		}
		var opts []uptimerobot.Option
		if method := strings.TrimSpace(auth["http_method"]); method != "" {
			if _, err := uptimerobot.HTTPMethodCode(method); err != nil {
				return nil, err
			}
			opts = append(opts, uptimerobot.WithHTTPMethod(method))
		}
		if raw := strings.TrimSpace(auth["min_check_frequency"]); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("uptimerobot: auth.min_check_frequency must be a duration: %w", err)
			}
			opts = append(opts, uptimerobot.WithMinCheckFrequency(d))
		}
		return uptimerobot.New(id, apiURL, key, opts...), nil
	},
	"pingdom": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("pingdom: auth.token is required")
		}
		return pingdom.New(id, apiURL, token), nil
	},
	"better-uptime": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("better-uptime: auth.token is required")
		}
		return betteruptime.New(id, apiURL, token), nil
	},
	"datadog-synthetics": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		apiKey := auth["api_key"]
		appKey := auth["app_key"]
		if apiKey == "" || appKey == "" {
			return nil, fmt.Errorf("datadog-synthetics: auth.api_key and auth.app_key are both required")
		}
		return datadog.New(id, apiURL, apiKey, appKey), nil
	},
}

func ForService(svc serviceconfig.Service) (adapter.Adapter, error) {
	factory, ok := Registry[svc.Type]
	if !ok {
		return nil, fmt.Errorf("service %q: unknown type %q", svc.ID, svc.Type)
	}
	a, err := factory(svc.ID, svc.URL, svc.Auth)
	if err != nil {
		return nil, fmt.Errorf("service %q: %w", svc.ID, err)
	}
	return a, nil
}

func ForScenario(svcCfg *serviceconfig.Config, monitorIDs []string) ([]adapter.Adapter, error) {
	wantedIDs := make(map[string]bool, len(monitorIDs))
	for _, id := range monitorIDs {
		wantedIDs[id] = true
	}

	allAdapters := make(map[string]adapter.Adapter, len(monitorIDs))
	for _, svc := range svcCfg.Services {
		if !svc.Enabled || !wantedIDs[svc.ID] {
			continue
		}
		a, err := ForService(svc)
		if err != nil {
			return nil, err
		}
		allAdapters[svc.ID] = a
	}

	adapters := make([]adapter.Adapter, 0, len(monitorIDs))
	for _, id := range monitorIDs {
		a, ok := allAdapters[id]
		if !ok {
			return nil, fmt.Errorf("scenario monitor %q not found in services config (check id and enabled)", id)
		}
		adapters = append(adapters, a)
	}
	return adapters, nil
}

func Enabled(svcCfg *serviceconfig.Config) ([]adapter.Adapter, error) {
	var adapters []adapter.Adapter
	for _, svc := range svcCfg.Services {
		if !svc.Enabled {
			continue
		}
		a, err := ForService(svc)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, a)
	}
	if len(adapters) == 0 {
		return nil, fmt.Errorf("no enabled services found in services config")
	}
	return adapters, nil
}
