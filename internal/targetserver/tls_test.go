package targetserver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/certlibrary"
	"github.com/Automattic/uptime-bench/internal/control"
)

func TestSelfSignedCertificateIncludesSubjectAltNames(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	cert, err := SelfSignedCertificate([]string{
		"bench.local",
		"bench.local.",
		"probe.local:443",
		"127.0.0.1",
	}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("Leaf is nil")
	}
	if !slices.Contains(cert.Leaf.DNSNames, "bench.local") {
		t.Fatalf("DNSNames = %v, want bench.local", cert.Leaf.DNSNames)
	}
	if !slices.Contains(cert.Leaf.DNSNames, "probe.local") {
		t.Fatalf("DNSNames = %v, want probe.local", cert.Leaf.DNSNames)
	}
	if got, want := len(cert.Leaf.DNSNames), 2; got != want {
		t.Fatalf("len(DNSNames) = %d, want %d: %v", got, want, cert.Leaf.DNSNames)
	}
	if len(cert.Leaf.IPAddresses) != 1 || !cert.Leaf.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IPAddresses = %v, want 127.0.0.1", cert.Leaf.IPAddresses)
	}
	if !cert.Leaf.NotBefore.Before(now) {
		t.Fatalf("NotBefore = %s, want before %s", cert.Leaf.NotBefore, now)
	}
	if !cert.Leaf.NotAfter.After(now.Add(364 * 24 * time.Hour)) {
		t.Fatalf("NotAfter = %s, want roughly one year after %s", cert.Leaf.NotAfter, now)
	}
}

func TestSelfSignedCertificateDefaultsToLocalhost(t *testing.T) {
	cert, err := SelfSignedCertificate(nil, time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	if got := cert.Leaf.DNSNames; len(got) != 1 || got[0] != "localhost" {
		t.Fatalf("DNSNames = %v, want [localhost]", got)
	}
}

func TestCertificateSelectorUsesExpiringLibraryCertificate(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	fiveDay := writeLibraryCert(t, dir, "five-day", now.Add(5*day), "*.bench.example.com")
	sixDay := writeLibraryCert(t, dir, "six-day", now.Add(6*day), "*.bench.example.com")
	ninetyDay := writeLibraryCert(t, dir, "ninety-day", now.Add(90*day), "*.bench.example.com")
	library := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{
			ninetyDay,
			sixDay,
			fiveDay,
		},
	}
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_expiring",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"days_remaining": float64(5)},
	}, 1)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	selector := &CertificateSelector{
		Registry: registry,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	selector.SetLibrary(library)

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got.Leaf == nil || !got.Leaf.NotAfter.Equal(fiveDay.NotAfter) {
		t.Fatalf("selected NotAfter = %v, want %v", got.Leaf.NotAfter, fiveDay.NotAfter)
	}
}

func TestTLSConfigSelectorServesLibraryFailureCertificatesInRealHandshake(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		failureType  string
		params       map[string]any
		entries      []certlibrary.Entry
		wantNotAfter time.Time
	}{
		{
			name:        "expired",
			failureType: "tls_expired",
			params:      map[string]any{"days_expired": float64(30)},
			entries: []certlibrary.Entry{
				writeLibraryCert(t, t.TempDir(), "expired-one-day", now.Add(-1*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "expired-thirty-days", now.Add(-30*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "expired-one-year", now.Add(-365*day), "*.bench.example.com"),
			},
			wantNotAfter: now.Add(-30 * day),
		},
		{
			name:        "expiring",
			failureType: "tls_expiring",
			params:      map[string]any{"days_remaining": float64(5)},
			entries: []certlibrary.Entry{
				writeLibraryCert(t, t.TempDir(), "expiring-five-days", now.Add(5*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "expiring-six-days", now.Add(6*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "expiring-ninety-days", now.Add(90*day), "*.bench.example.com"),
			},
			wantNotAfter: now.Add(5 * day),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := control.NewRegistry()
			registry.Set(control.FailureSpec{
				Type:     tc.failureType,
				Host:     "target.bench.example.com",
				Duration: time.Hour,
				Params:   tc.params,
			}, 1)
			fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
			if err != nil {
				t.Fatalf("SelfSignedCertificate: %v", err)
			}
			certSelector := &CertificateSelector{
				Registry: registry,
				Fallback: fallback,
				Now:      func() time.Time { return now },
			}
			certSelector.SetLibrary(&certlibrary.Library{
				Version: certlibrary.ManifestVersion,
				Entries: tc.entries,
			})
			selector := &TLSConfigSelector{
				Registry:     registry,
				Certificates: certSelector,
				Base: &tls.Config{
					MinVersion: tls.VersionTLS12,
					MaxVersion: tls.VersionTLS13,
				},
			}

			result := runTLSHandshake(t, selector, &tls.Config{
				ServerName:         "target.bench.example.com",
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: true,
			})
			if result.clientErr != nil {
				t.Fatalf("client handshake: %v", result.clientErr)
			}
			if result.serverErr != nil {
				t.Fatalf("server handshake: %v", result.serverErr)
			}
			if len(result.clientState.PeerCertificates) == 0 {
				t.Fatal("client handshake did not receive a peer certificate")
			}
			if got := result.clientState.PeerCertificates[0].NotAfter; !got.Equal(tc.wantNotAfter) {
				t.Fatalf("peer certificate NotAfter = %v, want %v", got, tc.wantNotAfter)
			}
		})
	}
}

func TestTLSConfigSelectorOpenSSLServesLibraryFailureCertificates(t *testing.T) {
	openssl := requireOpenSSL(t)
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		failureType  string
		params       map[string]any
		entries      []certlibrary.Entry
		wantNotAfter time.Time
	}{
		{
			name:        "expired",
			failureType: "tls_expired",
			params:      map[string]any{"days_expired": float64(30)},
			entries: []certlibrary.Entry{
				writeLibraryCert(t, t.TempDir(), "openssl-expired-one-day", now.Add(-1*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "openssl-expired-thirty-days", now.Add(-30*day), "*.bench.example.com"),
			},
			wantNotAfter: now.Add(-30 * day),
		},
		{
			name:        "expiring",
			failureType: "tls_expiring",
			params:      map[string]any{"days_remaining": float64(5)},
			entries: []certlibrary.Entry{
				writeLibraryCert(t, t.TempDir(), "openssl-expiring-five-days", now.Add(5*day), "*.bench.example.com"),
				writeLibraryCert(t, t.TempDir(), "openssl-expiring-ninety-days", now.Add(90*day), "*.bench.example.com"),
			},
			wantNotAfter: now.Add(5 * day),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selector := tlsSelectorWithLibraryFailure(t, now, tc.failureType, tc.params, tc.entries)
			output, clientErr, serverErr := runOpenSSLSClient(t, openssl, selector, func(addr string) []string {
				return []string{
					"s_client",
					"-connect", addr,
					"-servername", "target.bench.example.com",
					"-showcerts",
				}
			})
			if clientErr != nil {
				t.Fatalf("openssl s_client failed: %v\n%s", clientErr, output)
			}
			if serverErr != nil {
				t.Fatalf("server handshake: %v\n%s", serverErr, output)
			}
			cert := firstCertificateFromOpenSSL(t, output)
			if !cert.NotAfter.Equal(tc.wantNotAfter) {
				t.Fatalf("OpenSSL peer certificate NotAfter = %v, want %v", cert.NotAfter, tc.wantNotAfter)
			}
		})
	}
}

func TestCertificateSelectorUsesDefaultLibraryCertificateWithoutTLSFailure(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	sixDay := writeLibraryCert(t, dir, "six-day", now.Add(6*day), "*.bench.example.com")
	ninetyDay := writeLibraryCert(t, dir, "ninety-day", now.Add(90*day), "*.bench.example.com")
	library := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{
			sixDay,
			ninetyDay,
		},
	}
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	selector := &CertificateSelector{
		Registry: control.NewRegistry(),
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	selector.SetLibrary(library)

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got.Leaf == nil || !got.Leaf.NotAfter.Equal(ninetyDay.NotAfter) {
		t.Fatalf("selected NotAfter = %v, want %v", got.Leaf.NotAfter, ninetyDay.NotAfter)
	}
}

func TestCertificateSelectorTLSInvalidSelfSignedOverridesLibraryDefault(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	ninetyDay := writeLibraryCert(t, dir, "ninety-day", now.Add(90*day), "*.bench.example.com")
	library := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{
			ninetyDay,
		},
	}
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_invalid",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"variant": "self_signed"},
	}, 1)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	selector := &CertificateSelector{
		Registry: registry,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	selector.SetLibrary(library)

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != &selector.Fallback {
		t.Fatal("GetCertificate returned library cert, want fallback self-signed cert")
	}
}

func TestCertificateSelectorTLSInvalidHostnameMismatch(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_invalid",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"variant": "hostname_mismatch"},
	}, 1)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	mismatch, err := SelfSignedCertificate([]string{"wrong.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate mismatch: %v", err)
	}
	selector := &CertificateSelector{
		Registry:         registry,
		Fallback:         fallback,
		HostnameMismatch: mismatch,
		Now:              func() time.Time { return now },
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != &selector.HostnameMismatch {
		t.Fatal("GetCertificate returned fallback cert, want hostname mismatch cert")
	}
	if err := got.Leaf.VerifyHostname("target.bench.example.com"); err == nil {
		t.Fatal("hostname mismatch cert unexpectedly verifies for requested host")
	}
}

func TestTLSConfigSelectorUsesBaseConfigWithoutProtocolFailure(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	certSelector := &CertificateSelector{
		Registry: control.NewRegistry(),
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	selector := &TLSConfigSelector{
		Registry:     control.NewRegistry(),
		Certificates: certSelector,
		Base: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
		},
	}

	cfg, err := selector.GetConfigForClient(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS12 || cfg.MaxVersion != tls.VersionTLS13 {
		t.Fatalf("versions = min %x max %x, want TLS12/TLS13", cfg.MinVersion, cfg.MaxVersion)
	}
	if cfg.GetConfigForClient != nil {
		t.Fatal("returned TLS config should not recursively carry GetConfigForClient")
	}
	if cfg.GetCertificate == nil {
		t.Fatal("GetCertificate is nil")
	}
	got, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != &certSelector.Fallback {
		t.Fatal("GetCertificate returned non-fallback cert")
	}
}

func TestTLSConfigSelectorTLSDeprecatedVariants(t *testing.T) {
	cases := []struct {
		name    string
		variant string
		wantMin uint16
		wantMax uint16
	}{
		{name: "default TLS11", variant: "", wantMin: tls.VersionTLS10, wantMax: tls.VersionTLS11},
		{name: "TLS11", variant: "TLS11", wantMin: tls.VersionTLS10, wantMax: tls.VersionTLS11},
		{name: "TLS10", variant: "TLS10", wantMin: tls.VersionTLS10, wantMax: tls.VersionTLS10},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := control.NewRegistry()
			registry.Set(control.FailureSpec{
				Type:     "tls_deprecated",
				Host:     "target.bench.example.com",
				Duration: time.Hour,
				Params:   map[string]any{"variant": tc.variant},
			}, 1)
			selector := &TLSConfigSelector{
				Registry: registry,
				Base: &tls.Config{
					MinVersion: tls.VersionTLS12,
					MaxVersion: tls.VersionTLS13,
				},
			}

			cfg, err := selector.GetConfigForClient(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
			if err != nil {
				t.Fatalf("GetConfigForClient: %v", err)
			}
			if cfg.MinVersion != tc.wantMin || cfg.MaxVersion != tc.wantMax {
				t.Fatalf("versions = min %x max %x, want min %x max %x", cfg.MinVersion, cfg.MaxVersion, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestTLSConfigSelectorTLSHandshakeRejectsBeforeConfigSelection(t *testing.T) {
	cases := []struct {
		name       string
		reason     string
		wantReason string
	}{
		{name: "default", reason: "", wantReason: "version_mismatch"},
		{name: "version mismatch", reason: "version_mismatch", wantReason: "version_mismatch"},
		{name: "no common cipher", reason: "no_common_cipher", wantReason: "no_common_cipher"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := control.NewRegistry()
			registry.Set(control.FailureSpec{
				Type:     "tls_handshake",
				Host:     "target.bench.example.com",
				Duration: time.Hour,
				Params:   map[string]any{"reason": tc.reason},
			}, 1)
			selector := &TLSConfigSelector{
				Registry: registry,
				Base: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			}

			cfg, err := selector.GetConfigForClient(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
			if err == nil {
				t.Fatal("GetConfigForClient returned nil error for active tls_handshake")
			}
			if cfg != nil {
				t.Fatalf("GetConfigForClient config = %+v, want nil", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("GetConfigForClient error = %v, want one mentioning %q", err, tc.wantReason)
			}
		})
	}
}

func TestTLSConfigSelectorTLSHandshakeFailsRealHandshake(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_handshake",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"reason": "version_mismatch"},
	}, 1)
	selector := &TLSConfigSelector{Registry: registry}

	result := runTLSHandshake(t, selector, &tls.Config{
		ServerName:         "target.bench.example.com",
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true,
	})
	if result.clientErr == nil {
		t.Fatal("client handshake succeeded, want tls_handshake failure")
	}
	if result.serverErr == nil {
		t.Fatal("server handshake succeeded, want tls_handshake failure")
	}
	if !strings.Contains(result.serverErr.Error(), "tls_handshake active") {
		t.Fatalf("server handshake error = %v, want tls_handshake active", result.serverErr)
	}
}

func TestTLSConfigSelectorTLSDeprecatedNegotiatesRealDeprecatedHandshake(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_deprecated",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"variant": "TLS11"},
	}, 1)
	certSelector := &CertificateSelector{
		Registry: registry,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	selector := &TLSConfigSelector{
		Registry:     registry,
		Certificates: certSelector,
		Base: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
		},
	}

	result := runTLSHandshake(t, selector, &tls.Config{
		ServerName:         "target.bench.example.com",
		MinVersion:         tls.VersionTLS10,
		MaxVersion:         tls.VersionTLS11,
		InsecureSkipVerify: true,
	})
	if result.clientErr != nil {
		t.Fatalf("client handshake: %v", result.clientErr)
	}
	if result.serverErr != nil {
		t.Fatalf("server handshake: %v", result.serverErr)
	}
	if result.clientState.Version != tls.VersionTLS11 {
		t.Fatalf("client TLS version = %x, want TLS 1.1", result.clientState.Version)
	}
	if result.serverState.Version != tls.VersionTLS11 {
		t.Fatalf("server TLS version = %x, want TLS 1.1", result.serverState.Version)
	}
}

func TestTLSConfigSelectorOpenSSLProtocolFailures(t *testing.T) {
	openssl := requireOpenSSL(t)
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)

	t.Run("handshake abort", func(t *testing.T) {
		registry := control.NewRegistry()
		registry.Set(control.FailureSpec{
			Type:     "tls_handshake",
			Host:     "target.bench.example.com",
			Duration: time.Hour,
			Params:   map[string]any{"reason": "version_mismatch"},
		}, 1)
		selector := &TLSConfigSelector{Registry: registry}

		output, clientErr, serverErr := runOpenSSLSClient(t, openssl, selector, func(addr string) []string {
			return []string{
				"s_client",
				"-connect", addr,
				"-servername", "target.bench.example.com",
				"-tls1_3",
				"-brief",
			}
		})
		if clientErr == nil {
			t.Fatalf("openssl s_client succeeded, want tls_handshake failure\n%s", output)
		}
		if serverErr == nil || !strings.Contains(serverErr.Error(), "tls_handshake active") {
			t.Fatalf("serverErr = %v, want tls_handshake active", serverErr)
		}
	})

	t.Run("deprecated TLS 1.1", func(t *testing.T) {
		fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
		if err != nil {
			t.Fatalf("SelfSignedCertificate: %v", err)
		}
		registry := control.NewRegistry()
		registry.Set(control.FailureSpec{
			Type:     "tls_deprecated",
			Host:     "target.bench.example.com",
			Duration: time.Hour,
			Params:   map[string]any{"variant": "TLS11"},
		}, 1)
		certSelector := &CertificateSelector{
			Registry: registry,
			Fallback: fallback,
			Now:      func() time.Time { return now },
		}
		selector := &TLSConfigSelector{
			Registry:     registry,
			Certificates: certSelector,
			Base: &tls.Config{
				MinVersion: tls.VersionTLS12,
				MaxVersion: tls.VersionTLS13,
			},
		}

		output, clientErr, serverErr := runOpenSSLSClient(t, openssl, selector, func(addr string) []string {
			return []string{
				"s_client",
				"-connect", addr,
				"-servername", "target.bench.example.com",
				"-tls1_1",
				"-cipher", "DEFAULT:@SECLEVEL=0",
				"-brief",
			}
		})
		if clientErr != nil {
			t.Fatalf("openssl s_client failed: %v\n%s", clientErr, output)
		}
		if serverErr != nil {
			t.Fatalf("server handshake: %v\n%s", serverErr, output)
		}
		if !strings.Contains(output, "TLSv1.1") {
			t.Fatalf("openssl output does not report TLSv1.1\n%s", output)
		}
	})
}

func TestTLSConfigSelectorRejectsUnsupportedTLSHandshakeReason(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_handshake",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"reason": "cert_required"},
	}, 1)
	selector := &TLSConfigSelector{Registry: registry}

	if _, err := selector.GetConfigForClient(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"}); err == nil {
		t.Fatal("GetConfigForClient returned nil error for unsupported tls_handshake reason")
	}
}

func TestTLSConfigSelectorRejectsUnsupportedTLSDeprecatedVariant(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "tls_deprecated",
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   map[string]any{"variant": "SSL30"},
	}, 1)
	selector := &TLSConfigSelector{Registry: registry}

	if _, err := selector.GetConfigForClient(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"}); err == nil {
		t.Fatal("GetConfigForClient returned nil error for unsupported tls_deprecated variant")
	}
}

// TestCertificateSelector_SetLibrarySwapsAndClearsCache — the
// atomic-swap contract Phase B-3 relies on. After SetLibrary, the
// next handshake must see the new library, and a previously-cached
// *tls.Certificate from the old library must not shadow a freshly
// rotated entry with the same ID.
func TestCertificateSelector_SetLibrarySwapsAndClearsCache(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	// Two physically-distinct cert directories with the SAME entry
	// ID — simulates a lineage rotation where the manifest re-uses
	// the ID but the on-disk PEMs (and their fingerprints) change.
	v1 := writeLibraryCert(t, t.TempDir(), "rotating-id", now.Add(90*day), "*.bench.example.com")
	v2 := writeLibraryCert(t, t.TempDir(), "rotating-id", now.Add(180*day), "*.bench.example.com")
	libV1 := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{v1},
	}
	libV2 := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{v2},
	}

	selector := &CertificateSelector{
		Registry: control.NewRegistry(),
		Now:      func() time.Time { return now },
	}
	selector.SetLibrary(libV1)

	// Prime the cert cache by serving v1 once.
	first, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate v1: %v", err)
	}
	if !first.Leaf.NotAfter.Equal(v1.NotAfter) {
		t.Fatalf("v1 NotAfter = %v, want %v", first.Leaf.NotAfter, v1.NotAfter)
	}

	// Swap. The new entry has the same ID but a different file on
	// disk and a 180-day NotAfter. The cache eviction is what
	// prevents the v1 in-memory cert from masking v2.
	selector.SetLibrary(libV2)

	second, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate v2: %v", err)
	}
	if !second.Leaf.NotAfter.Equal(v2.NotAfter) {
		t.Fatalf("post-swap NotAfter = %v, want %v (cache may not have been cleared)", second.Leaf.NotAfter, v2.NotAfter)
	}
}

func TestCertificateSelectorUsesFallbackWithoutTLSFailure(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	selector := &CertificateSelector{
		Registry: control.NewRegistry(),
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got != &selector.Fallback {
		t.Fatal("GetCertificate returned library cert, want fallback")
	}
}

type tlsHandshakeResult struct {
	clientState tls.ConnectionState
	clientErr   error
	serverState tls.ConnectionState
	serverErr   error
}

func runTLSHandshake(t *testing.T, selector *TLSConfigSelector, clientConfig *tls.Config) tlsHandshakeResult {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverCh := make(chan tlsHandshakeResult, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- tlsHandshakeResult{serverErr: err}
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			serverCh <- tlsHandshakeResult{serverErr: err}
			return
		}
		server := tls.Server(conn, &tls.Config{
			GetConfigForClient: selector.GetConfigForClient,
		})
		err = server.Handshake()
		serverCh <- tlsHandshakeResult{
			serverState: server.ConnectionState(),
			serverErr:   err,
		}
	}()

	cfg := clientConfig.Clone()
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	client, clientErr := tls.DialWithDialer(dialer, "tcp", ln.Addr().String(), cfg)
	var clientState tls.ConnectionState
	if clientErr == nil {
		clientState = client.ConnectionState()
		_ = client.Close()
	}

	serverResult := <-serverCh
	return tlsHandshakeResult{
		clientState: clientState,
		clientErr:   clientErr,
		serverState: serverResult.serverState,
		serverErr:   serverResult.serverErr,
	}
}

func requireOpenSSL(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not found in PATH")
	}
	return path
}

func runOpenSSLSClient(t *testing.T, openssl string, selector *TLSConfigSelector, argsForAddr func(string) []string) (string, error, error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	serverCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverCh <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
			serverCh <- err
			return
		}
		server := tls.Server(conn, &tls.Config{
			GetConfigForClient: selector.GetConfigForClient,
		})
		err = server.Handshake()
		if err == nil {
			_ = server.Close()
		}
		serverCh <- err
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, openssl, argsForAddr(ln.Addr().String())...)
	output, clientErr := cmd.CombinedOutput()
	_ = ln.Close()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("openssl s_client timed out\n%s", string(output))
	}

	var serverErr error
	select {
	case serverErr = <-serverCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server handshake timed out")
	}
	return string(output), clientErr, serverErr
}

func firstCertificateFromOpenSSL(t *testing.T, output string) *x509.Certificate {
	t.Helper()
	rest := []byte(output)
	for {
		block, next := pem.Decode(rest)
		if block == nil {
			t.Fatalf("OpenSSL output did not contain a certificate PEM\n%s", output)
		}
		if block.Type == "CERTIFICATE" {
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatalf("parse OpenSSL peer certificate: %v", err)
			}
			return cert
		}
		rest = next
	}
}

func tlsSelectorWithLibraryFailure(t *testing.T, now time.Time, failureType string, params map[string]any, entries []certlibrary.Entry) *TLSConfigSelector {
	t.Helper()
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     failureType,
		Host:     "target.bench.example.com",
		Duration: time.Hour,
		Params:   params,
	}, 1)
	fallback, err := SelfSignedCertificate([]string{"target.bench.example.com"}, now)
	if err != nil {
		t.Fatalf("SelfSignedCertificate: %v", err)
	}
	certSelector := &CertificateSelector{
		Registry: registry,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}
	certSelector.SetLibrary(&certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: entries,
	})
	return &TLSConfigSelector{
		Registry:     registry,
		Certificates: certSelector,
		Base: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
		},
	}
}

func writeLibraryCert(t *testing.T, dir, id string, notAfter time.Time, identifiers ...string) certlibrary.Entry {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: id,
		},
		NotBefore:             notAfter.Add(-30 * day),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addSubjectAltNames(cert, identifiers)
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	certDir := filepath.Join(dir, id)
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(certDir, "cert.pem")
	fullchainPath := filepath.Join(certDir, "fullchain.pem")
	privkeyPath := filepath.Join(certDir, "privkey.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullchainPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privkeyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return certlibrary.Entry{
		ID:                id,
		Domain:            "bench.example.com",
		Profile:           "shortlived",
		Identifiers:       identifiers,
		NotAfter:          notAfter,
		FingerprintSHA256: hex.EncodeToString(sum[:]),
		Paths: certlibrary.Paths{
			Cert:      certPath,
			FullChain: fullchainPath,
			PrivKey:   privkeyPath,
		},
	}
}
