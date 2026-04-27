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
	library := &certlibrary.Library{
		Version: certlibrary.ManifestVersion,
		Entries: []certlibrary.Entry{
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
