// Package library archives certbot lineages into immutable cert snapshots.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/certutil"
	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/manifest"
	"github.com/Automattic/uptime-bench-certmint/internal/planner"
)

// ManifestPath returns the library manifest path.
func ManifestPath(libraryDir string) string {
	return filepath.Join(libraryDir, "manifest.json")
}

// ActiveDir returns the library directory for the current ACME environment.
func ActiveDir(cfg config.Config) string {
	if cfg.Certbot.Staging {
		return filepath.Join(cfg.LibraryDir, "staging")
	}
	return cfg.LibraryDir
}

// ManifestPathForConfig returns the manifest path for the current ACME environment.
func ManifestPathForConfig(cfg config.Config) string {
	return ManifestPath(ActiveDir(cfg))
}

// LiveCertExists reports whether certbot has a live cert for certName.
func LiveCertExists(cfg config.CertbotConfig, certName string) bool {
	_, err := os.Stat(filepath.Join(cfg.ConfigDir, "live", certName, "cert.pem"))
	return err == nil
}

// Archive copies certbot's live PEM files into the immutable library and
// returns the manifest entry.
func Archive(cfg config.Config, order planner.Order, issuedAt time.Time) (entry manifest.Entry, err error) {
	srcDir := filepath.Join(cfg.Certbot.ConfigDir, "live", order.CertName)
	leaf, err := certutil.LoadLeaf(filepath.Join(srcDir, "cert.pem"))
	if err != nil {
		return manifest.Entry{}, err
	}
	if order.MaxLifetime > 0 {
		lifetime := leaf.Leaf.NotAfter.Sub(leaf.Leaf.NotBefore)
		if lifetime > order.MaxLifetime {
			return manifest.Entry{}, fmt.Errorf("certificate lifetime %s exceeds max_lifetime %s for %s/%s", lifetime, order.MaxLifetime, order.DomainName, order.ProfileName)
		}
	}

	fpShort := leaf.FingerprintSHA256
	if len(fpShort) > 16 {
		fpShort = fpShort[:16]
	}
	notAfter := leaf.Leaf.NotAfter.UTC()
	dirName := fmt.Sprintf("slot-%02d-%s-%s", order.Slot, notAfter.Format("20060102T150405Z"), fpShort)
	destDir := filepath.Join(
		ActiveDir(cfg),
		cleanPathPart(order.DomainName),
		cleanPathPart(order.ProfileName),
		order.SlotDate,
		dirName,
	)
	if err = os.MkdirAll(destDir, 0o700); err != nil {
		return manifest.Entry{}, err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(destDir)
		}
	}()

	paths := manifest.Paths{
		Cert:      filepath.Join(destDir, "cert.pem"),
		Chain:     filepath.Join(destDir, "chain.pem"),
		FullChain: filepath.Join(destDir, "fullchain.pem"),
		PrivKey:   filepath.Join(destDir, "privkey.pem"),
	}
	for src, dst := range map[string]string{
		filepath.Join(srcDir, "cert.pem"):      paths.Cert,
		filepath.Join(srcDir, "chain.pem"):     paths.Chain,
		filepath.Join(srcDir, "fullchain.pem"): paths.FullChain,
		filepath.Join(srcDir, "privkey.pem"):   paths.PrivKey,
	} {
		if err = copyFile(src, dst); err != nil {
			return manifest.Entry{}, err
		}
	}

	id := strings.Join([]string{
		cleanPathPart(order.DomainName),
		cleanPathPart(order.ProfileName),
		order.SlotDate,
		fmt.Sprintf("slot%02d", order.Slot),
		fpShort,
	}, "_")

	return manifest.Entry{
		ID:                id,
		Environment:       order.Environment,
		Domain:            order.DomainName,
		Profile:           order.ProfileName,
		PreferredProfile:  order.PreferredProfile,
		RequiredProfile:   order.RequiredProfile,
		SlotDate:          order.SlotDate,
		Slot:              order.Slot,
		Identifiers:       append([]string(nil), order.Identifiers...),
		CertName:          order.CertName,
		IssuedAt:          issuedAt.UTC(),
		NotBefore:         leaf.Leaf.NotBefore.UTC(),
		NotAfter:          notAfter,
		FingerprintSHA256: leaf.FingerprintSHA256,
		Paths:             paths,
	}, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

var pathCleaner = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func cleanPathPart(s string) string {
	s = strings.ToLower(s)
	s = pathCleaner.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "unknown"
	}
	return s
}
