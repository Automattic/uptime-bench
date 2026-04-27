// Package certlibrary loads and selects certificate snapshots produced by
// uptime-bench-certmint.
package certlibrary

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// ManifestVersion is the supported cert-library manifest schema version.
const ManifestVersion = 1

// Library is a parsed certificate library manifest.
type Library struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Entry describes one immutable certificate snapshot.
type Entry struct {
	ID                string    `json:"id"`
	Domain            string    `json:"domain"`
	Profile           string    `json:"profile"`
	PreferredProfile  string    `json:"preferred_profile,omitempty"`
	SlotDate          string    `json:"slot_date"`
	Slot              int       `json:"slot"`
	Identifiers       []string  `json:"identifiers"`
	CertName          string    `json:"cert_name"`
	IssuedAt          time.Time `json:"issued_at"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"`
	Paths             Paths     `json:"paths"`
}

// Paths points at the PEM files for an archived certificate snapshot.
type Paths struct {
	Cert      string `json:"cert"`
	Chain     string `json:"chain"`
	FullChain string `json:"fullchain"`
	PrivKey   string `json:"privkey"`
}

// Load reads and validates a cert-library manifest from path.
func Load(path string) (Library, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Library{}, fmt.Errorf("certlibrary: read %s: %w", path, err)
	}
	var lib Library
	if err := json.Unmarshal(data, &lib); err != nil {
		return Library{}, fmt.Errorf("certlibrary: parse %s: %w", path, err)
	}
	if err := lib.Validate(); err != nil {
		return Library{}, err
	}
	return lib, nil
}

// Validate checks the fields the target needs for deterministic selection.
func (l Library) Validate() error {
	var errs []error
	if l.Version != ManifestVersion {
		errs = append(errs, fmt.Errorf("certlibrary: unsupported manifest version %d", l.Version))
	}
	seen := make(map[string]struct{}, len(l.Entries))
	for i, entry := range l.Entries {
		prefix := fmt.Sprintf("certlibrary: entries[%d]", i)
		if entry.ID == "" {
			errs = append(errs, fmt.Errorf("%s.id is required", prefix))
		} else if _, ok := seen[entry.ID]; ok {
			errs = append(errs, fmt.Errorf("%s.id %q is duplicated", prefix, entry.ID))
		}
		seen[entry.ID] = struct{}{}
		if entry.Domain == "" {
			errs = append(errs, fmt.Errorf("%s.domain is required", prefix))
		}
		if entry.Profile == "" {
			errs = append(errs, fmt.Errorf("%s.profile is required", prefix))
		}
		if len(entry.Identifiers) == 0 {
			errs = append(errs, fmt.Errorf("%s.identifiers is required", prefix))
		}
		if entry.NotAfter.IsZero() {
			errs = append(errs, fmt.Errorf("%s.not_after is required", prefix))
		}
		if entry.Paths.Cert == "" {
			errs = append(errs, fmt.Errorf("%s.paths.cert is required", prefix))
		}
		if entry.Paths.FullChain == "" {
			errs = append(errs, fmt.Errorf("%s.paths.fullchain is required", prefix))
		}
		if entry.Paths.PrivKey == "" {
			errs = append(errs, fmt.Errorf("%s.paths.privkey is required", prefix))
		}
	}
	return errors.Join(errs...)
}

// Covering returns entries whose exact or wildcard identifiers cover host.
func (l Library) Covering(host string) []Entry {
	host = normalizeHost(host)
	if host == "" {
		return nil
	}
	var out []Entry
	for _, entry := range l.Entries {
		if entry.Covers(host) {
			out = append(out, entry)
		}
	}
	return out
}

// SelectExpiring returns the valid certificate whose NotAfter is closest to
// now + targetRemaining.
func (l Library) SelectExpiring(host string, now time.Time, targetRemaining time.Duration) (Entry, error) {
	if targetRemaining <= 0 {
		return Entry{}, fmt.Errorf("certlibrary: target remaining must be positive")
	}
	target := now.UTC().Add(targetRemaining)
	return l.selectClosest(host, target, func(e Entry) bool {
		return e.NotAfter.After(now)
	})
}

// SelectExpired returns the expired certificate whose NotAfter is closest to
// now - targetExpired.
func (l Library) SelectExpired(host string, now time.Time, targetExpired time.Duration) (Entry, error) {
	if targetExpired <= 0 {
		return Entry{}, fmt.Errorf("certlibrary: target expired duration must be positive")
	}
	target := now.UTC().Add(-targetExpired)
	return l.selectClosest(host, target, func(e Entry) bool {
		return !e.NotAfter.After(now)
	})
}

func (l Library) selectClosest(host string, target time.Time, keep func(Entry) bool) (Entry, error) {
	host = normalizeHost(host)
	if host == "" {
		return Entry{}, fmt.Errorf("certlibrary: host is required")
	}
	var best Entry
	var bestDistance time.Duration
	for _, entry := range l.Entries {
		if !entry.Covers(host) || !keep(entry) {
			continue
		}
		distance := absDuration(entry.NotAfter.Sub(target))
		if best.ID == "" || distance < bestDistance || (distance == bestDistance && entry.NotAfter.Before(best.NotAfter)) {
			best = entry
			bestDistance = distance
		}
	}
	if best.ID == "" {
		return Entry{}, fmt.Errorf("certlibrary: no certificate for host %q near %s", host, target.Format(time.RFC3339))
	}
	return best, nil
}

// Covers reports whether this entry's identifiers cover host.
func (e Entry) Covers(host string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, identifier := range e.Identifiers {
		if identifierCoversHost(identifier, host) {
			return true
		}
	}
	return false
}

func identifierCoversHost(identifier, host string) bool {
	identifier = normalizeHost(identifier)
	if identifier == "" {
		return false
	}
	if identifier == host {
		return true
	}
	if !strings.HasPrefix(identifier, "*.") {
		return false
	}
	base := strings.TrimPrefix(identifier, "*.")
	if !strings.HasSuffix(host, "."+base) {
		return false
	}
	left := strings.TrimSuffix(host, "."+base)
	return left != "" && !strings.Contains(left, ".")
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")
	return strings.ToLower(host)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
