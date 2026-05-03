// Package serviceconfig loads and parses the services configuration file.
// The services config declares which monitoring services to evaluate:
// their adapter type, API URL, credentials, and enabled state.
package serviceconfig

import (
	"fmt"
	"net"
	"os"

	"github.com/BurntSushi/toml"
)

// Config is the parsed services configuration.
type Config struct {
	Services []Service
}

// Service is one monitoring service instance declared in services.toml.
type Service struct {
	// ID is the unique identifier for this instance. Scenario TOML monitor
	// lists reference services by this ID.
	ID string

	// Type is the adapter type: "jetmon-v1", "jetmon-v2", "pingdom",
	// "uptimerobot", "datadog-synthetics", "better-uptime", "gatus",
	// or "uptime-kuma".
	Type string

	// URL is the root API URL. If empty, the adapter uses its default
	// public endpoint. Required for services with no public API (e.g. Jetmon).
	URL string

	// Auth holds credential key=value pairs. Keys are adapter-defined;
	// see services.example.toml for each type's required keys.
	Auth map[string]string

	// Enabled controls whether this service participates in runs.
	// Set to false to skip a service without removing its config.
	Enabled bool

	// ProbeRanges maps region names to the CIDR blocks used by this service's
	// probes in that region. Used to expand scenario failure Regions fields into
	// concrete source IP ranges for geographic failure injection.
	// Example: probe_ranges.us-east = ["74.125.0.0/16", "198.51.100.0/24"]
	ProbeRanges map[string][]string

	// Capacity documents practical service limits the scheduler can use when
	// deciding how many benchmark monitors may run in parallel.
	Capacity ServiceCapacity
}

// ServiceCapacity describes the monitor/account capacity available for a
// configured service. Values are advisory today, but are parsed as first-class
// config so campaign generation can use them.
type ServiceCapacity struct {
	// Unlimited means the service has no practical monitor-count cap for this
	// harness. Self-hosted Jetmon instances should normally set this true.
	Unlimited bool `toml:"unlimited"`

	// MaxActiveMonitors is the account/service cap for simultaneously active
	// monitors. Zero means unknown unless Unlimited is true.
	MaxActiveMonitors int `toml:"max_active_monitors"`

	// ReservedMonitors is the count held back for non-benchmark use.
	ReservedMonitors int `toml:"reserved_monitors"`

	// MaxParallelRuns is the benchmarker's chosen limit for simultaneous runs
	// using this service after accounting for reservations and risk.
	MaxParallelRuns int `toml:"max_parallel_runs"`

	// APIRateLimitPerMinute is the known API request limit, if any. Zero means
	// unknown or not relevant.
	APIRateLimitPerMinute int `toml:"api_rate_limit_per_minute"`

	// BillingModel records the operator-facing constraint, for example
	// "monitor", "test_run", or "self_hosted".
	BillingModel string `toml:"billing_model"`

	// Source records how the cap was determined, such as a live API endpoint,
	// account UI, plan docs, or operator policy.
	Source string `toml:"source"`

	// Notes holds short operator context that is useful when planning large
	// matrix runs.
	Notes string `toml:"notes"`
}

// Load reads and parses a services config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("services: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates services config from TOML bytes.
func Parse(data []byte) (*Config, error) {
	var raw struct {
		Services []struct {
			ID          string              `toml:"id"`
			Type        string              `toml:"type"`
			URL         string              `toml:"url"`
			Auth        map[string]string   `toml:"auth"`
			Enabled     bool                `toml:"enabled"`
			ProbeRanges map[string][]string `toml:"probe_ranges"`
			Capacity    ServiceCapacity     `toml:"capacity"`
		} `toml:"services"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("services: parse: %w", err)
	}
	c := &Config{}
	seen := make(map[string]int, len(raw.Services))
	for i, s := range raw.Services {
		if s.ID == "" {
			return nil, fmt.Errorf("services: entry %d: id is required", i)
		}
		if s.Type == "" {
			return nil, fmt.Errorf("services: %q: type is required", s.ID)
		}
		if prev, dup := seen[s.ID]; dup {
			return nil, fmt.Errorf("services: duplicate id %q at entries %d and %d — scenario monitor lookup is keyed by id, so duplicates silently shadow each other", s.ID, prev, i)
		}
		seen[s.ID] = i

		// Probe ranges drive geographic failure injection. A malformed CIDR
		// here means the corresponding region's failure silently doesn't
		// match any source IP at runtime — fail at parse time instead.
		for region, cidrs := range s.ProbeRanges {
			for _, cidr := range cidrs {
				if _, _, err := net.ParseCIDR(cidr); err != nil {
					return nil, fmt.Errorf("services: %q: probe_ranges.%s: invalid CIDR %q: %w", s.ID, region, cidr, err)
				}
			}
		}
		if err := validateCapacity(s.ID, s.Capacity); err != nil {
			return nil, err
		}

		c.Services = append(c.Services, Service{
			ID:          s.ID,
			Type:        s.Type,
			URL:         s.URL,
			Auth:        s.Auth,
			Enabled:     s.Enabled,
			ProbeRanges: s.ProbeRanges,
			Capacity:    s.Capacity,
		})
	}
	return c, nil
}

func validateCapacity(id string, c ServiceCapacity) error {
	if c.MaxActiveMonitors < 0 {
		return fmt.Errorf("services: %q: capacity.max_active_monitors must be non-negative", id)
	}
	if c.ReservedMonitors < 0 {
		return fmt.Errorf("services: %q: capacity.reserved_monitors must be non-negative", id)
	}
	if c.MaxParallelRuns < 0 {
		return fmt.Errorf("services: %q: capacity.max_parallel_runs must be non-negative", id)
	}
	if c.APIRateLimitPerMinute < 0 {
		return fmt.Errorf("services: %q: capacity.api_rate_limit_per_minute must be non-negative", id)
	}
	if !c.Unlimited && c.MaxActiveMonitors > 0 {
		if c.ReservedMonitors > c.MaxActiveMonitors {
			return fmt.Errorf("services: %q: capacity.reserved_monitors exceeds max_active_monitors", id)
		}
		if c.MaxParallelRuns > c.MaxActiveMonitors-c.ReservedMonitors {
			return fmt.Errorf("services: %q: capacity.max_parallel_runs exceeds available monitor capacity", id)
		}
	}
	return nil
}
