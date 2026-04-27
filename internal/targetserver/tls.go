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
	"time"

	"github.com/Automattic/uptime-bench/internal/certlibrary"
	"github.com/Automattic/uptime-bench/internal/control"
)

const selfSignedCertLifetime = 365 * 24 * time.Hour
const day = 24 * time.Hour

// CertificateSelector chooses the TLS certificate to present for one
// ClientHello. Normal traffic gets the fallback cert; active tls_expired and
// tls_expiring failures select the closest matching cert-library entry.
type CertificateSelector struct {
	Registry *control.FailureRegistry
	Library  *certlibrary.Library
	Fallback tls.Certificate
	Now      func() time.Time

	mu    sync.Mutex
	cache map[string]*tls.Certificate
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
	if host == "" || s.Registry == nil || s.Library == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if spec, ok := s.Registry.Lookup("tls_expired", host, ""); ok {
		daysExpired := paramInt(spec.Params["days_expired"], 1)
		entry, err := s.Library.SelectExpired(host, now, time.Duration(daysExpired)*day)
		if err != nil {
			return nil, err
		}
		return s.cachedCertificate(entry)
	}
	if spec, ok := s.Registry.Lookup("tls_expiring", host, ""); ok {
		daysRemaining := paramInt(spec.Params["days_remaining"], 1)
		entry, err := s.Library.SelectExpiring(host, now, time.Duration(daysRemaining)*day)
		if err != nil {
			return nil, err
		}
		return s.cachedCertificate(entry)
	}
	return nil, nil
}

func (s *CertificateSelector) defaultCertificate(host string) (*tls.Certificate, error) {
	host = normalizeTLSHost(host)
	if host == "" || s.Library == nil {
		return nil, nil
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	entry, err := s.Library.SelectDefault(host, now)
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
// target's HTTPS listener. It is intentionally only the Phase 1 fallback:
// tls_expired/tls_expiring scenarios will later select real library certs.
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
