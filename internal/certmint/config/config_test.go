package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	stateDir := filepath.Join(t.TempDir(), "state")
	writeJSON(t, path, map[string]any{
		"state_dir": stateDir,
		"certbot": map[string]any{
			"email":              "ops@example.com",
			"agree_tos":          true,
			"authenticator_args": []string{"--dns-test"},
		},
		"domains": []map[string]any{
			{
				"name":        "bench.example.com",
				"identifiers": []string{"bench.example.com", "*.bench.example.com"},
				"profiles": []map[string]any{
					{"name": "classic", "per_day": 1},
				},
			},
		},
	})

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LibraryDir != "/var/lib/uptime-bench/certs" {
		t.Fatalf("LibraryDir = %q", cfg.LibraryDir)
	}
	if cfg.PollInterval.Duration != 15*time.Minute {
		t.Fatalf("PollInterval = %s", cfg.PollInterval.Duration)
	}
	if cfg.LockPath != filepath.Join(stateDir, "certmint.lock") {
		t.Fatalf("LockPath = %q", cfg.LockPath)
	}
	if cfg.Certbot.Binary != "certbot" {
		t.Fatalf("Certbot.Binary = %q", cfg.Certbot.Binary)
	}
	if cfg.Certbot.ConfigDir != filepath.Join(stateDir, "letsencrypt") {
		t.Fatalf("Certbot.ConfigDir = %q", cfg.Certbot.ConfigDir)
	}
	if cfg.Certbot.WorkDir != filepath.Join(stateDir, "work") {
		t.Fatalf("Certbot.WorkDir = %q", cfg.Certbot.WorkDir)
	}
	if cfg.Certbot.LogsDir != filepath.Join(stateDir, "logs") {
		t.Fatalf("Certbot.LogsDir = %q", cfg.Certbot.LogsDir)
	}
	if cfg.Domains[0].UniqueSANTemplate != "cert-{date}-{slot}-{profile}.unique.{domain}" {
		t.Fatalf("UniqueSANTemplate = %q", cfg.Domains[0].UniqueSANTemplate)
	}
	if cfg.Certbot.IssuanceTimeout.Duration != 10*time.Minute {
		t.Fatalf("IssuanceTimeout = %s, want 10m", cfg.Certbot.IssuanceTimeout.Duration)
	}
}

func TestValidateRejectsTemplateCoveredByWildcard(t *testing.T) {
	cfg := Config{
		LibraryDir:   "/tmp/library",
		LockPath:     "/tmp/certmint.lock",
		PollInterval: Duration{Duration: time.Minute},
		Certbot: CertbotConfig{
			Binary:            "certbot",
			Email:             "ops@example.com",
			AgreeTOS:          true,
			IssuanceTimeout:   Duration{Duration: 10 * time.Minute},
			AuthenticatorArgs: []string{"--manual"},
		},
		Domains: []DomainConfig{
			{
				Name:              "bench.example.com",
				Identifiers:       []string{"bench.example.com", "*.bench.example.com"},
				UniqueSANTemplate: "cert-{date}-{slot}.bench.example.com",
				Profiles: []ProfileConfig{
					{Name: "classic", PerDay: 1},
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want wildcard collision")
	}
	if !strings.Contains(err.Error(), "covered by wildcard") {
		t.Fatalf("Validate() error = %v, want 'covered by wildcard'", err)
	}
}

func TestValidateAcceptsTemplateOutsideWildcard(t *testing.T) {
	cfg := Config{
		LibraryDir:   "/tmp/library",
		LockPath:     "/tmp/certmint.lock",
		PollInterval: Duration{Duration: time.Minute},
		Certbot: CertbotConfig{
			Binary:            "certbot",
			Email:             "ops@example.com",
			AgreeTOS:          true,
			IssuanceTimeout:   Duration{Duration: 10 * time.Minute},
			AuthenticatorArgs: []string{"--manual"},
		},
		Domains: []DomainConfig{
			{
				Name:              "bench.example.com",
				Identifiers:       []string{"bench.example.com", "*.bench.example.com"},
				UniqueSANTemplate: "cert-{date}-{slot}.unique.bench.example.com",
				Profiles: []ProfileConfig{
					{Name: "classic", PerDay: 1},
				},
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadParsesExplicitDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	writeJSON(t, path, map[string]any{
		"library_dir":   "/tmp/library",
		"poll_interval": "45m",
		"certbot": map[string]any{
			"email":              "ops@example.com",
			"agree_tos":          true,
			"authenticator_args": []string{"--dns-test"},
		},
		"domains": []map[string]any{
			{
				"name":        "bench.example.com",
				"identifiers": []string{"bench.example.com"},
				"profiles": []map[string]any{
					{"name": "shortlived", "required_profile": "shortlived", "max_lifetime": "168h", "per_day": 1},
				},
			},
		},
	})

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.PollInterval.Duration != 45*time.Minute {
		t.Fatalf("PollInterval = %s", cfg.PollInterval.Duration)
	}
	profile := cfg.Domains[0].Profiles[0]
	if profile.RequiredProfile != "shortlived" {
		t.Fatalf("RequiredProfile = %q", profile.RequiredProfile)
	}
	if profile.MaxLifetime.Duration != 168*time.Hour {
		t.Fatalf("MaxLifetime = %s", profile.MaxLifetime.Duration)
	}
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"poll_interval":"soon"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want malformed duration error")
	}
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Fatalf("Load() error = %v, want invalid duration", err)
	}
}

func TestValidateReportsConfigurationErrors(t *testing.T) {
	cfg := Config{
		LibraryDir: "/tmp/library",
		LockPath:   "/tmp/certmint.lock",
		PollInterval: Duration{
			Duration: time.Minute,
		},
		Certbot: CertbotConfig{
			Binary:            "certbot",
			Server:            "https://acme.example.test/directory",
			Staging:           true,
			Email:             "ops@example.com",
			AgreeTOS:          true,
			AuthenticatorArgs: []string{"--dns-test"},
		},
		Domains: []DomainConfig{
			{
				Name:        "bench.example.com",
				Identifiers: []string{"bench.example.com"},
				Profiles: []ProfileConfig{
					{
						Name:             "classic",
						PreferredProfile: "classic",
						RequiredProfile:  "shortlived",
						MaxLifetime: Duration{
							Duration: -time.Hour,
						},
					},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil")
	}
	for _, want := range []string{
		"certbot.server and certbot.staging are mutually exclusive",
		"certbot.issuance_timeout must be positive",
		"domains[0].profiles[0].preferred_profile and required_profile are mutually exclusive",
		"domains[0].profiles[0].max_lifetime must be positive",
		"domains[0].profiles[0].per_day must be positive",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want substring %q", err, want)
		}
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
