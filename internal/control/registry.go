package control

import (
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

// activeFailure is a failure held in the registry with its expiry, optional
// RNG, and pre-parsed source CIDRs. CIDRs are parsed once at Set time so the
// hot path in LookupForIP only does net.IPNet.Contains, not net.ParseCIDR.
type activeFailure struct {
	spec    FailureSpec
	expires time.Time
	rng     *rand.Rand // non-nil when Rate < 1.0; seeded per run for reproducibility
	nets    []*net.IPNet
}

// FailureRegistry is a thread-safe in-memory store of active failures.
// Each fleet member (target or dns binary) owns one registry.
type FailureRegistry struct {
	mu       sync.Mutex
	failures map[string]activeFailure
}

// NewRegistry creates an empty FailureRegistry.
func NewRegistry() *FailureRegistry {
	return &FailureRegistry{failures: make(map[string]activeFailure)}
}

// Set registers a failure. It overwrites any existing failure with the same key.
// seed is the scenario run seed; a per-failure-type derivative is used so each
// failure type has an independent random stream.
//
// SourceCIDRs are pre-parsed; entries that fail to parse are logged and
// dropped. A failure with all-bad CIDRs is still stored but will never match
// in LookupForIP.
func (r *FailureRegistry) Set(spec FailureSpec, seed int64) {
	af := activeFailure{
		spec:    spec,
		expires: time.Now().Add(spec.Duration),
	}
	rate := spec.Rate
	if rate <= 0 || rate >= 1.0 {
		rate = 1.0
	}
	if rate < 1.0 {
		// Derive a per-failure-type seed so concurrent failures don't share a stream.
		af.rng = rand.New(rand.NewSource(seed ^ strHash(spec.Type)))
	}
	for _, cidrStr := range spec.SourceCIDRs {
		_, ipNet, err := net.ParseCIDR(cidrStr)
		if err != nil {
			log.Printf("control: registry: drop invalid CIDR %q on %s: %v", cidrStr, spec.Type, err)
			continue
		}
		af.nets = append(af.nets, ipNet)
	}
	r.mu.Lock()
	r.failures[failureKey(spec)] = af
	r.mu.Unlock()
}

// Remove removes a failure by its identifying fields.
func (r *FailureRegistry) Remove(failureType, host, path string) {
	r.mu.Lock()
	delete(r.failures, failureKey(FailureSpec{Type: failureType, Host: host, Path: path}))
	r.mu.Unlock()
}

// Active returns all non-expired failures, pruning stale entries.
func (r *FailureRegistry) Active() []FailureSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	out := make([]FailureSpec, 0, len(r.failures))
	for k, af := range r.failures {
		if now.After(af.expires) {
			delete(r.failures, k)
			continue
		}
		out = append(out, af.spec)
	}
	return out
}

// Lookup returns the active global failure for the given (type, host, path),
// applying the configured rate probabilistically. Failures with SourceCIDRs
// set are never returned — those are geo-restricted and handled by LookupForIP.
//
// Match priority: exact (type+host+path) → host wildcard (type+host) → full wildcard (type only).
func (r *FailureRegistry) Lookup(failureType, host, path string) (FailureSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()

	candidates := []string{
		failureKey(FailureSpec{Type: failureType, Host: host, Path: path}),
		failureKey(FailureSpec{Type: failureType, Host: host}),
		failureKey(FailureSpec{Type: failureType}),
	}
	for _, k := range candidates {
		af, ok := r.failures[k]
		if !ok {
			continue
		}
		if now.After(af.expires) {
			delete(r.failures, k)
			continue
		}
		if len(af.spec.SourceCIDRs) > 0 {
			continue // geo-restricted; use LookupForIP instead
		}
		if af.rng != nil && af.rng.Float64() >= af.spec.Rate {
			return FailureSpec{}, false
		}
		return af.spec, true
	}
	return FailureSpec{}, false
}

// LookupForIP returns the first active geo-restricted failure (one with
// non-empty SourceCIDRs) whose CIDR list contains ip. Returns zero value
// and false if no active geo failure covers the given IP.
//
// CIDRs are pre-parsed at Set time; this loop does no string parsing on
// the hot path.
func (r *FailureRegistry) LookupForIP(ip net.IP) (FailureSpec, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()

	for k, af := range r.failures {
		if now.After(af.expires) {
			delete(r.failures, k)
			continue
		}
		if len(af.nets) == 0 {
			continue
		}
		for _, ipNet := range af.nets {
			if ipNet.Contains(ip) {
				if af.rng != nil && af.rng.Float64() >= af.spec.Rate {
					return FailureSpec{}, false
				}
				return af.spec, true
			}
		}
	}
	return FailureSpec{}, false
}

func failureKey(s FailureSpec) string {
	return s.Type + ":" + s.Host + ":" + s.Path
}

// strHash returns a simple deterministic hash of s, used to derive
// per-failure-type seeds from the run seed.
func strHash(s string) int64 {
	var h int64
	for _, c := range s {
		h = h*31 + int64(c)
	}
	return h
}
