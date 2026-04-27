package library

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/planner"
)

func TestArchiveCopiesLineageAndReturnsManifestEntry(t *testing.T) {
	tmp := t.TempDir()
	notBefore := time.Date(2026, 4, 27, 10, 0, 0, 0, time.UTC)
	notAfter := time.Date(2026, 4, 30, 10, 0, 0, 0, time.UTC)
	certPEM, fingerprint := testCertificate(t, notBefore, notAfter)
	privKeyPEM := []byte("test private key\n")

	cfg := config.Config{
		LibraryDir: filepath.Join(tmp, "library"),
		Certbot: config.CertbotConfig{
			ConfigDir: filepath.Join(tmp, "letsencrypt"),
		},
	}
	order := planner.Order{
		DomainName:       "bench.example.com",
		ProfileName:      "shortlived",
		PreferredProfile: "shortlived",
		SlotDate:         "20260427",
		Slot:             1,
		CertName:         "ub-certmint-bench-example-com-shortlived-20260427-01",
		Identifiers: []string{
			"bench.example.com",
			"*.bench.example.com",
			"cert-20260427-01-shortlived.bench.example.com",
		},
	}
	writeLineage(t, cfg.Certbot.ConfigDir, order.CertName, certPEM, privKeyPEM)

	issuedAt := time.Date(2026, 4, 27, 7, 0, 0, 0, time.FixedZone("CDT", -5*60*60))
	entry, err := Archive(cfg, order, issuedAt)
	if err != nil {
		t.Fatalf("Archive() error = %v", err)
	}
	fpShort := fingerprint[:16]
	wantID := "bench.example.com_shortlived_20260427_slot01_" + fpShort
	if entry.ID != wantID {
		t.Fatalf("entry.ID = %q, want %q", entry.ID, wantID)
	}
	if entry.Domain != order.DomainName || entry.Profile != order.ProfileName || entry.PreferredProfile != order.PreferredProfile {
		t.Fatalf("entry profile fields = %+v", entry)
	}
	if entry.CertName != order.CertName || entry.SlotDate != order.SlotDate || entry.Slot != order.Slot {
		t.Fatalf("entry slot fields = %+v", entry)
	}
	if !entry.IssuedAt.Equal(issuedAt.UTC()) {
		t.Fatalf("entry.IssuedAt = %s, want %s", entry.IssuedAt, issuedAt.UTC())
	}
	if !entry.NotBefore.Equal(notBefore) || !entry.NotAfter.Equal(notAfter) {
		t.Fatalf("entry validity = %s/%s, want %s/%s", entry.NotBefore, entry.NotAfter, notBefore, notAfter)
	}
	if entry.FingerprintSHA256 != fingerprint {
		t.Fatalf("entry.FingerprintSHA256 = %q, want %q", entry.FingerprintSHA256, fingerprint)
	}

	order.Identifiers[0] = "mutated.example.com"
	if entry.Identifiers[0] != "bench.example.com" {
		t.Fatalf("entry identifiers alias order identifiers: %#v", entry.Identifiers)
	}

	wantDirFragment := filepath.Join(
		"bench.example.com",
		"shortlived",
		"20260427",
		"slot-01-"+notAfter.Format("20060102T150405Z")+"-"+fpShort,
	)
	if !strings.Contains(filepath.Dir(entry.Paths.Cert), wantDirFragment) {
		t.Fatalf("entry cert path = %q, want to contain %q", entry.Paths.Cert, wantDirFragment)
	}
	assertFile(t, entry.Paths.Cert, certPEM, 0o600)
	assertFile(t, entry.Paths.Chain, certPEM, 0o600)
	assertFile(t, entry.Paths.FullChain, certPEM, 0o600)
	assertFile(t, entry.Paths.PrivKey, privKeyPEM, 0o600)
	assertDirMode(t, filepath.Dir(entry.Paths.Cert), 0o700)
}

func TestLiveCertExists(t *testing.T) {
	cfg := config.CertbotConfig{ConfigDir: t.TempDir()}
	if LiveCertExists(cfg, "missing") {
		t.Fatal("LiveCertExists() = true for missing cert")
	}

	liveDir := filepath.Join(cfg.ConfigDir, "live", "present")
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "cert.pem"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !LiveCertExists(cfg, "present") {
		t.Fatal("LiveCertExists() = false for present cert")
	}
}

func writeLineage(t *testing.T, configDir, certName string, certPEM, privKeyPEM []byte) {
	t.Helper()
	liveDir := filepath.Join(configDir, "live", certName)
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"cert.pem":      certPEM,
		"chain.pem":     certPEM,
		"fullchain.pem": certPEM,
		"privkey.pem":   privKeyPEM,
	} {
		if err := os.WriteFile(filepath.Join(liveDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func testCertificate(t *testing.T, notBefore, notAfter time.Time) ([]byte, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "bench.example.com",
		},
		DNSNames:              []string{"bench.example.com", "*.bench.example.com"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), hex.EncodeToString(sum[:])
}

func assertFile(t *testing.T, path string, want []byte, mode os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s contents = %q, want %q", path, got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != mode {
		t.Fatalf("%s mode = %o, want %o", path, gotMode, mode)
	}
}

func assertDirMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != mode {
		t.Fatalf("%s mode = %o, want %o", path, gotMode, mode)
	}
}
