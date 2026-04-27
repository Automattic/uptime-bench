// Package certutil reads certificate metadata from PEM files.
package certutil

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
)

// Metadata is the subset of x509 metadata the library manifest needs.
type Metadata struct {
	NotBefore         string
	NotAfter          string
	FingerprintSHA256 string
}

// Certificate wraps a parsed leaf certificate and derived metadata.
type Certificate struct {
	Leaf              *x509.Certificate
	FingerprintSHA256 string
}

// LoadLeaf parses the first certificate block from a PEM file.
func LoadLeaf(path string) (Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Certificate{}, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return Certificate{}, fmt.Errorf("certutil: %s does not contain a certificate PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Certificate{}, err
	}
	sum := sha256.Sum256(cert.Raw)
	return Certificate{
		Leaf:              cert,
		FingerprintSHA256: hex.EncodeToString(sum[:]),
	}, nil
}
