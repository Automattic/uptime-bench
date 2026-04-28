package certbot

import (
	"slices"
	"testing"

	"github.com/Automattic/uptime-bench/internal/certmint/config"
	"github.com/Automattic/uptime-bench/internal/certmint/planner"
)

func TestArgsBuildsShortlivedStagingCommand(t *testing.T) {
	cfg := config.CertbotConfig{
		Binary:    "certbot",
		ConfigDir: "/var/lib/certmint/letsencrypt",
		WorkDir:   "/var/lib/certmint/work",
		LogsDir:   "/var/log/certmint",
		Email:     "ops@example.com",
		Staging:   true,
		AuthenticatorArgs: []string{
			"--dns-rfc2136",
			"--dns-rfc2136-credentials", "/etc/certmint/rfc2136.ini",
		},
		ExtraArgs: []string{"--keep-until-expiring"},
	}
	order := planner.Order{
		CertName:        "ub-certmint-staging-bench-shortlived-20260427-00",
		RequiredProfile: "shortlived",
		Identifiers: []string{
			"bench.example.com",
			"*.bench.example.com",
			"cert-20260427-00-shortlived.unique.bench.example.com",
		},
	}

	want := []string{
		"certonly",
		"--non-interactive",
		"--cert-name", "ub-certmint-staging-bench-shortlived-20260427-00",
		"--config-dir", "/var/lib/certmint/letsencrypt",
		"--work-dir", "/var/lib/certmint/work",
		"--logs-dir", "/var/log/certmint",
		"--preferred-challenges", "dns",
		"--email", "ops@example.com",
		"--agree-tos",
		"--staging",
		"--required-profile", "shortlived",
		"--dns-rfc2136",
		"--dns-rfc2136-credentials", "/etc/certmint/rfc2136.ini",
		"--keep-until-expiring",
		"-d", "bench.example.com",
		"-d", "*.bench.example.com",
		"-d", "cert-20260427-00-shortlived.unique.bench.example.com",
	}
	if got := Args(cfg, order); !slices.Equal(got, want) {
		t.Fatalf("Args() = %#v\nwant %#v", got, want)
	}
}

func TestArgsUsesCustomServerWithoutStagingOrPreferredProfile(t *testing.T) {
	cfg := config.CertbotConfig{
		ConfigDir: "/var/lib/certmint/letsencrypt",
		WorkDir:   "/var/lib/certmint/work",
		LogsDir:   "/var/log/certmint",
		Email:     "ops@example.com",
		Server:    "https://acme.example.test/directory",
		AuthenticatorArgs: []string{
			"--manual",
		},
	}
	order := planner.Order{
		CertName:    "ub-certmint-bench-classic-20260427-00",
		Identifiers: []string{"bench.example.com"},
	}

	got := Args(cfg, order)
	assertFlagValue(t, got, "--server", "https://acme.example.test/directory")
	if slices.Contains(got, "--staging") {
		t.Fatalf("Args() unexpectedly included --staging: %#v", got)
	}
	if slices.Contains(got, "--preferred-profile") {
		t.Fatalf("Args() unexpectedly included --preferred-profile: %#v", got)
	}
	if slices.Contains(got, "--required-profile") {
		t.Fatalf("Args() unexpectedly included --required-profile: %#v", got)
	}
}

func TestCommandLineQuotesBinaryAndArguments(t *testing.T) {
	cfg := config.CertbotConfig{
		Binary:            "certbot",
		ConfigDir:         "/var/lib/certmint/letsencrypt",
		WorkDir:           "/var/lib/certmint/work",
		LogsDir:           "/var/log/certmint",
		Email:             "ops@example.com",
		AuthenticatorArgs: []string{"--manual-auth-hook", "/usr/local/bin/dns hook"},
	}
	order := planner.Order{
		CertName:    "ub-certmint-bench-classic-20260427-00",
		Identifiers: []string{"bench.example.com"},
	}

	want := `"certbot" "certonly" "--non-interactive" "--cert-name" "ub-certmint-bench-classic-20260427-00" "--config-dir" "/var/lib/certmint/letsencrypt" "--work-dir" "/var/lib/certmint/work" "--logs-dir" "/var/log/certmint" "--preferred-challenges" "dns" "--email" "ops@example.com" "--agree-tos" "--manual-auth-hook" "/usr/local/bin/dns hook" "-d" "bench.example.com"`
	if got := CommandLine(cfg, order); got != want {
		t.Fatalf("CommandLine() = %q\nwant %q", got, want)
	}
}

func assertFlagValue(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return
		}
	}
	t.Fatalf("Args() missing %s %q in %#v", flag, value, args)
}
