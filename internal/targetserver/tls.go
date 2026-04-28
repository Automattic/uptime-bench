package targetserver

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Automattic/uptime-bench/internal/certlibrary"
	"github.com/Automattic/uptime-bench/internal/control"
)

const selfSignedCertLifetime = 365 * 24 * time.Hour
const day = 24 * time.Hour

// CertificateSelector chooses the TLS certificate to present for one
// ClientHello. Normal traffic gets the library default or fallback cert;
// active TLS certificate failures override that with the requested variant.
//
// The library pointer is held atomically so a polling goroutine can
// SetLibrary() with a freshly-fetched library while in-flight handshakes
// finish cleanly with whatever they read at GetCertificate-time.
type CertificateSelector struct {
	Registry         *control.FailureRegistry
	Fallback         tls.Certificate
	HostnameMismatch tls.Certificate
	Now              func() time.Time

	library atomic.Pointer[certlibrary.Library]

	mu    sync.Mutex
	cache map[string]*tls.Certificate
}

// SetLibrary atomically replaces the cert library and clears the
// in-memory tls.Certificate cache so a rotated lineage (same entry
// ID, new fingerprint on disk) doesn't shadow the new files. Safe
// to call from a goroutine while handshakes are in flight: a
// handshake that already loaded a *tls.Certificate from the old
// library finishes with that cert; the next handshake sees the
// new library.
func (s *CertificateSelector) SetLibrary(lib *certlibrary.Library) {
	s.library.Store(lib)
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

// Library returns the currently-active cert library, or nil when no
// library is configured.
func (s *CertificateSelector) Library() *certlibrary.Library {
	return s.library.Load()
}

// TLSConfigSelector chooses the per-ClientHello TLS protocol configuration.
// Certificate selection still lives in CertificateSelector; this type owns
// failures that alter the handshake protocol rather than the presented cert.
type TLSConfigSelector struct {
	Registry     *control.FailureRegistry
	Certificates *CertificateSelector
	Base         *tls.Config
}

// GetConfigForClient implements tls.Config.GetConfigForClient.
func (s *TLSConfigSelector) GetConfigForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	if s == nil {
		return nil, fmt.Errorf("target: tls config selector is nil")
	}
	cfg := s.baseConfig()
	host := ""
	if hello != nil {
		host = hello.ServerName
	}
	host = normalizeTLSHost(host)
	if host == "" || s.Registry == nil {
		return cfg, nil
	}
	if spec, ok := s.Registry.Lookup("tls_handshake", host, ""); ok {
		reason, _ := spec.Params["reason"].(string)
		switch reason {
		case "", "version_mismatch", "no_common_cipher":
			if reason == "" {
				reason = "version_mismatch"
			}
			return nil, fmt.Errorf("target: tls_handshake active: %s", reason)
		default:
			return nil, fmt.Errorf("target: unsupported tls_handshake reason %q", reason)
		}
	}
	if spec, ok := s.Registry.Lookup("tls_deprecated", host, ""); ok {
		variant, _ := spec.Params["variant"].(string)
		switch variant {
		case "", "TLS11":
			cfg.MinVersion = tls.VersionTLS10
			cfg.MaxVersion = tls.VersionTLS11
		case "TLS10":
			cfg.MinVersion = tls.VersionTLS10
			cfg.MaxVersion = tls.VersionTLS10
		default:
			return nil, fmt.Errorf("target: unsupported tls_deprecated variant %q", variant)
		}
	}
	return cfg, nil
}

func (s *TLSConfigSelector) baseConfig() *tls.Config {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.Base != nil {
		cfg = s.Base.Clone()
	}
	cfg.GetConfigForClient = nil
	if s.Certificates != nil {
		cfg.GetCertificate = s.Certificates.GetCertificate
	}
	return cfg
}

// GetCertificate implements tls.Config.GetCertificate.
func (s *CertificateSelector) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if s == nil {
		return nil, fmt.Errorf("target: tls selector is nil")
	}
	host := ""
	if hello != nil {
		host = hello.ServerName
	}
	if cert, err := s.failureCertificate(host); err != nil || cert != nil {
		return cert, err
	}
	if cert, err := s.defaultCertificate(host); err != nil || cert != nil {
		return cert, err
	}
	return &s.Fallback, nil
}

func (s *CertificateSelector) failureCertificate(host string) (*tls.Certificate, error) {
	host = normalizeTLSHost(host)
	if host == "" || s.Registry == nil {
		return nil, nil
	}
	if spec, ok := s.Registry.Lookup("tls_invalid", host, ""); ok {
		variant, _ := spec.Params["variant"].(string)
		switch variant {
		case "", "self_signed":
			return &s.Fallback, nil
		case "hostname_mismatch":
			if len(s.HostnameMismatch.Certificate) == 0 {
				return nil, fmt.Errorf("target: tls_invalid hostname_mismatch certificate is not configured")
			}
			return &s.HostnameMismatch, nil
		default:
			return nil, fmt.Errorf("target: unsupported tls_invalid variant %q", variant)
		}
	}
	lib := s.Library()
	if lib == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if spec, ok := s.Registry.Lookup("tls_expired", host, ""); ok {
		daysExpired := paramInt(spec.Params["days_expired"], 1)
		entry, err := lib.SelectExpired(host, now, time.Duration(daysExpired)*day)
		if err != nil {
			return nil, err
		}
		return s.cachedCertificate(entry)
	}
	if spec, ok := s.Registry.Lookup("tls_expiring", host, ""); ok {
		daysRemaining := paramInt(spec.Params["days_remaining"], 1)
		entry, err := lib.SelectExpiring(host, now, time.Duration(daysRemaining)*day)
		if err != nil {
			return nil, err
		}
		return s.cachedCertificate(entry)
	}
	return nil, nil
}

func (s *CertificateSelector) defaultCertificate(host string) (*tls.Certificate, error) {
	host = normalizeTLSHost(host)
	lib := s.Library()
	if host == "" || lib == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	entry, err := lib.SelectDefault(host, now)
	if errors.Is(err, certlibrary.ErrNoCertificate) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.cachedCertificate(entry)
}

func (s *CertificateSelector) cachedCertificate(entry certlibrary.Entry) (*tls.Certificate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = make(map[string]*tls.Certificate)
	}
	if cert := s.cache[entry.ID]; cert != nil {
		return cert, nil
	}
	cert, err := loadLibraryCertificate(entry)
	if err != nil {
		return nil, err
	}
	s.cache[entry.ID] = cert
	return cert, nil
}

func loadLibraryCertificate(entry certlibrary.Entry) (*tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(entry.Paths.FullChain, entry.Paths.PrivKey)
	if err != nil {
		return nil, fmt.Errorf("target: load tls library cert %s: %w", entry.ID, err)
	}
	certPEM, err := os.ReadFile(entry.Paths.Cert)
	if err != nil {
		return nil, fmt.Errorf("target: read tls library leaf %s: %w", entry.ID, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("target: tls library leaf %s is not a certificate PEM", entry.ID)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("target: parse tls library leaf %s: %w", entry.ID, err)
	}
	pair.Leaf = leaf
	if entry.FingerprintSHA256 != "" {
		sum := sha256.Sum256(leaf.Raw)
		if got := fmt.Sprintf("%x", sum[:]); !strings.EqualFold(got, entry.FingerprintSHA256) {
			return nil, fmt.Errorf("target: tls library cert %s fingerprint mismatch", entry.ID)
		}
	}
	return &pair, nil
}

// SelfSignedCertificate returns a default TLS server certificate for the
// target's HTTPS listener. Library-backed TLS failures override it when a
// matching manifest entry is configured for the requested SNI host.
func SelfSignedCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("target: generate tls key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("target: generate tls serial: %w", err)
	}

	cert := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: "uptime-bench target",
		},
		NotBefore:             now.UTC().Add(-time.Hour),
		NotAfter:              now.UTC().Add(selfSignedCertLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	addSubjectAltNames(cert, hosts)
	if len(cert.DNSNames) == 0 && len(cert.IPAddresses) == 0 {
		cert.DNSNames = []string{"localhost"}
	}

	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("target: create tls cert: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("target: parse tls cert pair: %w", err)
	}
	pair.Leaf = cert
	return pair, nil
}

func addSubjectAltNames(cert *x509.Certificate, hosts []string) {
	seenDNS := make(map[string]struct{}, len(hosts))
	seenIP := make(map[string]struct{}, len(hosts))
	for _, host := range hosts {
		host = normalizeTLSHost(host)
		if host == "" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			key := ip.String()
			if _, ok := seenIP[key]; ok {
				continue
			}
			seenIP[key] = struct{}{}
			cert.IPAddresses = append(cert.IPAddresses, ip)
			continue
		}
		key := strings.ToLower(host)
		if _, ok := seenDNS[key]; ok {
			continue
		}
		seenDNS[key] = struct{}{}
		cert.DNSNames = append(cert.DNSNames, host)
	}
}

func normalizeTLSHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimSuffix(host, ".")
}
