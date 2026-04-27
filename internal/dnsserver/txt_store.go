package dnsserver

import (
	"strings"
	"sync"
)

// TXTStore is a process-local map of FQDN → TXT record values used to
// answer ACME DNS-01 challenges. Names are normalized lowercase without
// trailing dot. Multiple values per name are preserved so apex and
// wildcard authorizations active in the same Let's Encrypt order can
// both be answered: the responder emits one TXT RR per stored value.
//
// Persistence is intentionally absent: ACME challenge records live
// only between certbot's manual-auth hook and its manual-cleanup hook
// (seconds to minutes). A DNS-server restart mid-issuance is rare
// enough that re-running certmint is the simpler recovery path than
// implementing on-disk durability.
type TXTStore struct {
	mu      sync.RWMutex
	records map[string]txtEntry
}

type txtEntry struct {
	values []string
	ttl    uint32
}

// NewTXTStore returns an empty TXTStore.
func NewTXTStore() *TXTStore {
	return &TXTStore{records: make(map[string]txtEntry)}
}

// Add appends value to name's TXT record set and updates the name's
// TTL to ttl (last write wins). A duplicate (name, value) pair is a
// no-op so an idempotent retry from certmint's hook is safe.
func (s *TXTStore) Add(name, value string, ttl uint32) {
	name = normalizeTXTName(name)
	if name == "" || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.records[name]
	for _, existing := range entry.values {
		if existing == value {
			entry.ttl = ttl
			s.records[name] = entry
			return
		}
	}
	entry.values = append(entry.values, value)
	entry.ttl = ttl
	s.records[name] = entry
}

// Remove deletes value from name's TXT record set, leaving sibling
// values intact. Removing the last value drops the name entirely so
// Lookup reports it as missing.
func (s *TXTStore) Remove(name, value string) {
	name = normalizeTXTName(name)
	if name == "" || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.records[name]
	if !ok {
		return
	}
	out := entry.values[:0]
	for _, existing := range entry.values {
		if existing != value {
			out = append(out, existing)
		}
	}
	if len(out) == 0 {
		delete(s.records, name)
		return
	}
	entry.values = append([]string(nil), out...)
	s.records[name] = entry
}

// Lookup returns the TXT values and TTL configured for name. The
// returned slice is a copy so callers can iterate without holding the
// store lock. ok is false if no values are configured.
func (s *TXTStore) Lookup(name string) (values []string, ttl uint32, ok bool) {
	name = normalizeTXTName(name)
	if name == "" {
		return nil, 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.records[name]
	if !ok {
		return nil, 0, false
	}
	out := make([]string, len(entry.values))
	copy(out, entry.values)
	return out, entry.ttl, true
}

func normalizeTXTName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimSuffix(name, ".")
	return strings.ToLower(name)
}
