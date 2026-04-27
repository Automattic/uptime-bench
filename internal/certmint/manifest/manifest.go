// Package manifest stores the certificate library index consumed by uptime-bench.
package manifest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const Version = 1

// Manifest is the durable index of all archived certificate snapshots.
type Manifest struct {
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

// Paths are absolute paths to the archived PEM files.
type Paths struct {
	Cert      string `json:"cert"`
	Chain     string `json:"chain"`
	FullChain string `json:"fullchain"`
	PrivKey   string `json:"privkey"`
}

// Load returns an empty manifest when path does not exist.
func Load(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{Version: Version}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, err
	}
	if m.Version == 0 {
		m.Version = Version
	}
	return m, nil
}

// Save writes the manifest atomically.
func Save(path string, m Manifest) error {
	m.Version = Version
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// HasSlot reports whether a domain/profile/date/slot has already been archived.
func (m Manifest) HasSlot(domain, profile, slotDate string, slot int) bool {
	for _, entry := range m.Entries {
		if entry.Domain == domain && entry.Profile == profile && entry.SlotDate == slotDate && entry.Slot == slot {
			return true
		}
	}
	return false
}

// Append adds an entry if its ID is not already present.
func (m *Manifest) Append(entry Entry) {
	for i := range m.Entries {
		if m.Entries[i].ID == entry.ID {
			m.Entries[i] = entry
			return
		}
	}
	m.Entries = append(m.Entries, entry)
}
