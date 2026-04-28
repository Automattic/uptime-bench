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
	LibraryDir   string   `json:"library_dir"`
	StateDir     string   `json:"state_dir"`
	LockPath     string   `json:"lock_path"`
	PollInterval Duration `json:"poll_interval"`
	// InterOrderQuiet is the minimum time to wait between two orders
	// that target the same domain. Wildcard identifiers in different
	// orders share the same _acme-challenge.<domain> TXT name, so a
	// new order issued immediately after the previous one's cleanup
	// hook can race Let's Encrypt's recursive resolver caching the
	// old TXT value (TTL 30s on uptime-bench-dns). Default is 60s
	// (2× the canonical TTL); set to 0 to disable.
	InterOrderQuiet Duration `json:"inter_order_quiet,omitempty"`

	// ExpiredRetention is how long to keep an expired cert in the
	// library before garbage-collecting it. The trim rule is per
	// (domain, profile): expired certs younger than this are kept;
	// older ones are dropped, EXCEPT for the oldest in each group,
	// which is preserved so SelectExpired always has at least one
	// "very old" cert per domain/profile to match against scenarios
	// asking for large days_expired values. Default 720h (30 days).
	// Set to 0 to disable trimming and let the library grow forever.
	ExpiredRetention Duration `json:"expired_retention,omitempty"`

	Certbot CertbotConfig  `json:"certbot"`
	Domains []DomainConfig `json:"domains"`
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
	IssuanceTimeout   Duration `json:"issuance_timeout,omitempty"`
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
	Name             string   `json:"name"`
	PreferredProfile string   `json:"preferred_profile,omitempty"`
	RequiredProfile  string   `json:"required_profile,omitempty"`
	MaxLifetime      Duration `json:"max_lifetime,omitempty"`
	PerDay           int      `json:"per_day"`
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

// ACMEEnvironment returns the library/lineage environment implied by config.
func (c Config) ACMEEnvironment() string {
	if c.Certbot.Staging {
		return "staging"
	}
	return "production"
}

// ApplyDefaults fills optional fields with production-oriented paths.
func (c *Config) ApplyDefaults() {
	if c.LibraryDir == "" {
		c.LibraryDir = "/var/lib/uptime-bench/certs"
	}
	if c.StateDir == "" {
		c.StateDir = "/var/lib/uptime-bench-certmint"
	}
	if c.LockPath == "" {
		c.LockPath = filepath.Join(c.StateDir, "certmint.lock")
	}
	if c.PollInterval.Duration == 0 {
		c.PollInterval.Duration = 15 * time.Minute
	}
	if c.InterOrderQuiet.Duration == 0 {
		c.InterOrderQuiet.Duration = 60 * time.Second
	}
	if c.ExpiredRetention.Duration == 0 {
		c.ExpiredRetention.Duration = 30 * 24 * time.Hour
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
	if c.Certbot.IssuanceTimeout.Duration == 0 {
		c.Certbot.IssuanceTimeout.Duration = 10 * time.Minute
	}
	for i := range c.Domains {
		if c.Domains[i].UniqueSANTemplate == "" {
			c.Domains[i].UniqueSANTemplate = "cert-{date}-{slot}-{profile}.unique.{domain}"
		}
	}
}

// Validate checks for missing or conflicting configuration.
func (c Config) Validate() error {
	var errs []error
	if c.LibraryDir == "" {
		errs = append(errs, errors.New("config: library_dir is required"))
	}
	if c.LockPath == "" {
		errs = append(errs, errors.New("config: lock_path is required"))
	}
	if c.PollInterval.Duration <= 0 {
		errs = append(errs, errors.New("config: poll_interval must be positive"))
	}
	if c.InterOrderQuiet.Duration < 0 {
		errs = append(errs, errors.New("config: inter_order_quiet must be non-negative"))
	}
	if c.ExpiredRetention.Duration < 0 {
		errs = append(errs, errors.New("config: expired_retention must be non-negative"))
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
	if c.Certbot.IssuanceTimeout.Duration <= 0 {
		errs = append(errs, errors.New("config: certbot.issuance_timeout must be positive"))
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
		if err := validateUniqueSANTemplate(domain); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", prefix, err))
		}
		for j, profile := range domain.Profiles {
			pprefix := fmt.Sprintf("%s.profiles[%d]", prefix, j)
			if profile.Name == "" {
				errs = append(errs, fmt.Errorf("%s.name is required", pprefix))
			}
			if profile.PreferredProfile != "" && profile.RequiredProfile != "" {
				errs = append(errs, fmt.Errorf("%s.preferred_profile and required_profile are mutually exclusive", pprefix))
			}
			if profile.MaxLifetime.Duration < 0 {
				errs = append(errs, fmt.Errorf("%s.max_lifetime must be positive", pprefix))
			}
			if profile.PerDay <= 0 {
				errs = append(errs, fmt.Errorf("%s.per_day must be positive", pprefix))
			}
		}
	}
	return errors.Join(errs...)
}

// validateUniqueSANTemplate renders the template with placeholder values and
// rejects it if the result is covered by a wildcard identifier in the same
// order. Let's Encrypt rejects orders that pair a wildcard with an identifier
// the wildcard already covers.
func validateUniqueSANTemplate(domain DomainConfig) error {
	if domain.UniqueSANTemplate == "" {
		return nil
	}
	rendered := strings.ToLower(strings.TrimSpace(renderUniqueSANForValidation(domain)))
	if rendered == "" {
		return errors.New("unique_san_template renders to an empty string")
	}
	for _, identifier := range domain.Identifiers {
		id := strings.ToLower(strings.TrimSpace(identifier))
		if !strings.HasPrefix(id, "*.") {
			continue
		}
		base := strings.TrimPrefix(id, "*.")
		if !strings.HasSuffix(rendered, "."+base) {
			continue
		}
		prefix := strings.TrimSuffix(rendered, "."+base)
		if prefix != "" && !strings.Contains(prefix, ".") {
			return fmt.Errorf("unique_san_template renders to %q, which is covered by wildcard %q; use a deeper name such as <label>.unique.%s", rendered, identifier, base)
		}
	}
	return nil
}

func renderUniqueSANForValidation(domain DomainConfig) string {
	r := strings.NewReplacer(
		"{domain}", domain.Name,
		"{profile}", "validate",
		"{date}", "00000000",
		"{slot}", "00",
		"{unix}", "0",
	)
	return r.Replace(domain.UniqueSANTemplate)
}
