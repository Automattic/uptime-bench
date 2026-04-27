package targetserver

import (
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
		Library:  library,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}

	got, err := selector.GetCertificate(&tls.ClientHelloInfo{ServerName: "target.bench.example.com"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if got.Leaf == nil || !got.Leaf.NotAfter.Equal(fiveDay.NotAfter) {
		t.Fatalf("selected NotAfter = %v, want %v", got.Leaf.NotAfter, fiveDay.NotAfter)
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
		Library:  library,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}

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
		Library:  library,
		Fallback: fallback,
		Now:      func() time.Time { return now },
	}

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
