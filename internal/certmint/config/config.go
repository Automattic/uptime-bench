// Package config loads and validates certmint runtime configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Duration wraps time.Duration with JSON string parsing.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		d.Duration = 0
		return nil
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// Config is the top-level certmint configuration.
type Config struct {
	LibraryDir   string         `json:"library_dir"`
	StateDir     string         `json:"state_dir"`
	PollInterval Duration       `json:"poll_interval"`
	Certbot      CertbotConfig  `json:"certbot"`
	Domains      []DomainConfig `json:"domains"`
}

// CertbotConfig contains certbot executable, state, and authenticator settings.
type CertbotConfig struct {
	Binary            string   `json:"binary"`
	Server            string   `json:"server,omitempty"`
	Staging           bool     `json:"staging,omitempty"`
	Email             string   `json:"email"`
	AgreeTOS          bool     `json:"agree_tos"`
	ConfigDir         string   `json:"config_dir"`
	WorkDir           string   `json:"work_dir"`
	LogsDir           string   `json:"logs_dir"`
	AuthenticatorArgs []string `json:"authenticator_args"`
	ExtraArgs         []string `json:"extra_args,omitempty"`
}

// DomainConfig describes one domain family to mint certificates for.
type DomainConfig struct {
	Name              string          `json:"name"`
	Identifiers       []string        `json:"identifiers"`
	UniqueSANTemplate string          `json:"unique_san_template"`
	Profiles          []ProfileConfig `json:"profiles"`
}

// ProfileConfig describes one issuance cadence for a CA profile.
type ProfileConfig struct {
	Name             string `json:"name"`
	PreferredProfile string `json:"preferred_profile,omitempty"`
	PerDay           int    `json:"per_day"`
}

// Load reads, defaults, and validates a config file.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ApplyDefaults fills optional fields with production-oriented paths.
func (c *Config) ApplyDefaults() {
	if c.LibraryDir == "" {
		c.LibraryDir = "/var/lib/uptime-bench/certs"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/uptime-bench-certmint"
	}
	if c.PollInterval.Duration == 0 {
		c.PollInterval.Duration = 15 * time.Minute
	}
	if c.Certbot.Binary == "" {
		c.Certbot.Binary = "certbot"
	}
	if c.Certbot.ConfigDir == "" {
		c.Certbot.ConfigDir = filepath.Join(c.StateDir, "letsencrypt")
	}
	if c.Certbot.WorkDir == "" {
		c.Certbot.WorkDir = filepath.Join(c.StateDir, "work")
	}
	if c.Certbot.LogsDir == "" {
		c.Certbot.LogsDir = filepath.Join(c.StateDir, "logs")
	}
	for i := range c.Domains {
		if c.Domains[i].UniqueSANTemplate == "" {
			c.Domains[i].UniqueSANTemplate = "cert-{date}-{slot}-{profile}.{domain}"
		}
	}
}

// Validate checks for missing or conflicting configuration.
func (c Config) Validate() error {
	var errs []error
	if c.LibraryDir == "" {
		errs = append(errs, errors.New("config: library_dir is required"))
	}
	if c.PollInterval.Duration <= 0 {
		errs = append(errs, errors.New("config: poll_interval must be positive"))
	}
	if c.Certbot.Binary == "" {
		errs = append(errs, errors.New("config: certbot.binary is required"))
	}
	if c.Certbot.Server != "" && c.Certbot.Staging {
		errs = append(errs, errors.New("config: certbot.server and certbot.staging are mutually exclusive"))
	}
	if c.Certbot.Email == "" {
		errs = append(errs, errors.New("config: certbot.email is required"))
	}
	if !c.Certbot.AgreeTOS {
		errs = append(errs, errors.New("config: certbot.agree_tos must be true for non-interactive issuance"))
	}
	if len(c.Certbot.AuthenticatorArgs) == 0 {
		errs = append(errs, errors.New("config: certbot.authenticator_args is required"))
	}
	if len(c.Domains) == 0 {
		errs = append(errs, errors.New("config: at least one domain is required"))
	}
	for i, domain := range c.Domains {
		prefix := fmt.Sprintf("config: domains[%d]", i)
		if domain.Name == "" {
			errs = append(errs, fmt.Errorf("%s.name is required", prefix))
		}
		if len(domain.Identifiers) == 0 {
			errs = append(errs, fmt.Errorf("%s.identifiers is required", prefix))
		}
		if len(domain.Profiles) == 0 {
			errs = append(errs, fmt.Errorf("%s.profiles is required", prefix))
		}
		for j, profile := range domain.Profiles {
			pprefix := fmt.Sprintf("%s.profiles[%d]", prefix, j)
			if profile.Name == "" {
				errs = append(errs, fmt.Errorf("%s.name is required", pprefix))
			}
			if profile.PerDay <= 0 {
				errs = append(errs, fmt.Errorf("%s.per_day must be positive", pprefix))
			}
		}
	}
	return errors.Join(errs...)
}
