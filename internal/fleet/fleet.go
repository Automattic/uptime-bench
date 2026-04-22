// Package fleet loads and provides access to the fleet configuration.
package fleet

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the parsed fleet configuration.
type Config struct {
	Control     ControlConfig
	Adapters    map[string]AdapterConfig
	Nameservers []Nameserver
	Targets     []Target
	Domains     []Domain
}

type ControlConfig struct {
	Timeout       time.Duration
	AuthTokenFile string
}

// AdapterConfig holds per-adapter operational limits.
type AdapterConfig struct {
	MaxCallsPerRun int
}

// Nameserver is one authoritative DNS VM in the fleet.
type Nameserver struct {
	ID          string
	Address     string
	ControlPort int
	DNSPort     int
	Domains     []string
}

// Target is one target VM in the fleet.
type Target struct {
	ID          string
	Address     string
	ControlPort int
	Sites       []Site
}

// Site is one virtual host served by a target VM.
type Site struct {
	ID    string
	Host  string
	Paths []string
}

// Domain holds domain-level configuration.
type Domain struct {
	Name        string
	Registrar   string
	Nameservers []string
	TTL         int
}

// Load reads and parses a fleet config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fleet: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates fleet config from TOML bytes.
func Parse(data []byte) (*Config, error) {
	var r rawConfig
	if err := toml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("fleet: parse: %w", err)
	}
	return convert(r)
}

func convert(r rawConfig) (*Config, error) {
	timeout := 10 * time.Second
	if r.Control.Timeout != "" {
		d, err := time.ParseDuration(r.Control.Timeout)
		if err != nil {
			return nil, fmt.Errorf("fleet: control.timeout: %w", err)
		}
		timeout = d
	}

	c := &Config{
		Control: ControlConfig{
			Timeout:       timeout,
			AuthTokenFile: r.Control.AuthTokenFile,
		},
		Adapters: make(map[string]AdapterConfig, len(r.Adapters)),
	}

	for id, a := range r.Adapters {
		c.Adapters[id] = AdapterConfig{MaxCallsPerRun: a.MaxCallsPerRun}
	}

	for _, ns := range r.Nameservers {
		if ns.ID == "" {
			return nil, fmt.Errorf("fleet: nameserver missing id")
		}
		port := ns.DNSPort
		if port == 0 {
			port = 53
		}
		ctrlPort := ns.ControlPort
		if ctrlPort == 0 {
			return nil, fmt.Errorf("fleet: nameserver %q: control_port is required", ns.ID)
		}
		c.Nameservers = append(c.Nameservers, Nameserver{
			ID:          ns.ID,
			Address:     ns.Address,
			ControlPort: ctrlPort,
			DNSPort:     port,
			Domains:     ns.Domains,
		})
	}

	for _, t := range r.Targets {
		if t.ID == "" {
			return nil, fmt.Errorf("fleet: target missing id")
		}
		ctrlPort := t.ControlPort
		if ctrlPort == 0 {
			return nil, fmt.Errorf("fleet: target %q: control_port is required", t.ID)
		}
		tgt := Target{
			ID:          t.ID,
			Address:     t.Address,
			ControlPort: ctrlPort,
		}
		for _, s := range t.Sites {
			tgt.Sites = append(tgt.Sites, Site{
				ID:    s.ID,
				Host:  s.Host,
				Paths: s.Paths,
			})
		}
		c.Targets = append(c.Targets, tgt)
	}

	for _, d := range r.Domains {
		c.Domains = append(c.Domains, Domain{
			Name:        d.Name,
			Registrar:   d.Registrar,
			Nameservers: d.Nameservers,
			TTL:         d.TTL,
		})
	}

	return c, nil
}

// raw types mirror the TOML structure for unmarshalling.

type rawConfig struct {
	Control     rawControl            `toml:"control"`
	Adapters    map[string]rawAdapter `toml:"adapters"`
	Nameservers []rawNameserver       `toml:"nameservers"`
	Targets     []rawTarget           `toml:"targets"`
	Domains     []rawDomain           `toml:"domains"`
}

type rawControl struct {
	Timeout       string `toml:"timeout"`
	AuthTokenFile string `toml:"auth_token_file"`
}

type rawAdapter struct {
	MaxCallsPerRun int `toml:"max_calls_per_run"`
}

type rawNameserver struct {
	ID          string   `toml:"id"`
	Address     string   `toml:"address"`
	ControlPort int      `toml:"control_port"`
	DNSPort     int      `toml:"dns_port"`
	Domains     []string `toml:"domains"`
}

type rawTarget struct {
	ID          string    `toml:"id"`
	Address     string    `toml:"address"`
	ControlPort int       `toml:"control_port"`
	Sites       []rawSite `toml:"sites"`
}

type rawSite struct {
	ID    string   `toml:"id"`
	Host  string   `toml:"host"`
	Paths []string `toml:"paths"`
}

type rawDomain struct {
	Name        string   `toml:"name"`
	Registrar   string   `toml:"registrar"`
	Nameservers []string `toml:"nameservers"`
	TTL         int      `toml:"ttl"`
}
