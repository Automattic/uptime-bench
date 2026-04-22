// Package serviceconfig loads and parses the services configuration file.
// The services config declares which monitoring services to evaluate:
// their adapter type, API URL, credentials, and enabled state.
package serviceconfig

import (
	"fmt"
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

	// Type is the adapter type: "jetmon", "pingdom", "uptimerobot",
	// "datadog-synthetics", or "better-uptime".
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
			ID      string            `toml:"id"`
			Type    string            `toml:"type"`
			URL     string            `toml:"url"`
			Auth    map[string]string `toml:"auth"`
			Enabled bool              `toml:"enabled"`
		} `toml:"services"`
	}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("services: parse: %w", err)
	}
	c := &Config{}
	for i, s := range raw.Services {
		if s.ID == "" {
			return nil, fmt.Errorf("services: entry %d: id is required", i)
		}
		if s.Type == "" {
			return nil, fmt.Errorf("services: %q: type is required", s.ID)
		}
		c.Services = append(c.Services, Service{
			ID:      s.ID,
			Type:    s.Type,
			URL:     s.URL,
			Auth:    s.Auth,
			Enabled: s.Enabled,
		})
	}
	return c, nil
}
