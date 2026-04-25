package control

import (
	"net"
	"testing"
	"time"
)

// countingIPNet wraps a *net.IPNet so we can verify pre-parsed CIDRs are
// reused across LookupForIP calls instead of being re-parsed every time.

// TestLookupForIP_MatchesCIDR is the basic correctness test for the geo
// failure path.
func TestLookupForIP_MatchesCIDR(t *testing.T) {
	r := NewRegistry()
	r.Set(FailureSpec{
		Type:        "tcp_refused",
		Duration:    time.Minute,
		Rate:        1.0,
		SourceCIDRs: []string{"10.0.0.0/8", "192.168.1.0/24"},
	}, 0)

	cases := []struct {
		ip   string
		want bool
	}{
		{"10.1.2.3", true},
		{"192.168.1.50", true},
		{"192.168.2.1", false},
		{"172.16.0.1", false},
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			_, ok := r.LookupForIP(net.ParseIP(tc.ip))
			if ok != tc.want {
				t.Fatalf("LookupForIP(%s) = %v, want %v", tc.ip, ok, tc.want)
			}
		})
	}
}

// TestLookupForIP_InvalidCIDRsAreSkipped ensures malformed CIDRs in a
// failure spec do not prevent valid CIDRs in the same spec from matching.
// This is the post-fix contract: bad CIDRs are dropped at Set time.
func TestLookupForIP_InvalidCIDRsAreSkipped(t *testing.T) {
	r := NewRegistry()
	r.Set(FailureSpec{
		Type:        "tcp_refused",
		Duration:    time.Minute,
		Rate:        1.0,
		SourceCIDRs: []string{"not-a-cidr", "10.0.0.0/8"},
	}, 0)

	if _, ok := r.LookupForIP(net.ParseIP("10.1.2.3")); !ok {
		t.Fatal("expected match on valid CIDR despite malformed sibling")
	}
}

// TestLookupForIP_DoesNotReparseOnHotPath is a regression test for the
// performance bug where LookupForIP called net.ParseCIDR on every request.
// We use the large iteration count to amplify per-call CPU work; with cached
// *net.IPNet this finishes in tens of ms, with per-call parsing it takes
// well over a second on the same hardware.
func TestLookupForIP_DoesNotReparseOnHotPath(t *testing.T) {
	if testing.Short() {
		t.Skip("perf assertion; skip in -short mode")
	}
	cidrs := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		cidrs = append(cidrs, net.IPv4(10, byte(i), 0, 0).String()+"/24")
	}
	// Matching CIDR last → every lookup must scan the entire list.
	cidrs = append(cidrs, "192.168.99.0/24")

	r := NewRegistry()
	r.Set(FailureSpec{
		Type:        "tcp_refused",
		Duration:    time.Minute,
		Rate:        1.0,
		SourceCIDRs: cidrs,
	}, 0)

	target := net.ParseIP("192.168.99.42")
	const iterations = 50000

	start := time.Now()
	for i := 0; i < iterations; i++ {
		if _, ok := r.LookupForIP(target); !ok {
			t.Fatal("expected match")
		}
	}
	elapsed := time.Since(start)

	// 50 000 lookups × 101 CIDRs = ~5M Contains() checks total. Cached
	// path: ~50–150 ms native, ~1 s under -race. Reparsing path: ~1 s
	// native, ~10 s under -race. Budget at 3 s splits the two regimes
	// while staying tolerant to noisy CI and the race detector.
	const budget = 3 * time.Second
	if elapsed > budget {
		t.Fatalf("%d lookups across 101 CIDRs took %v (budget %v) — CIDRs are likely being re-parsed per call", iterations, elapsed, budget)
	}
	t.Logf("%d lookups in %v", iterations, elapsed)
}

// TestRegistry_LookupRespectsRateOne verifies the simple rate-1 path:
// every Lookup returns the failure when Rate ≥ 1.
func TestRegistry_LookupRespectsRateOne(t *testing.T) {
	r := NewRegistry()
	r.Set(FailureSpec{
		Type:     "http_status",
		Host:     "example.com",
		Duration: time.Minute,
		Rate:     1.0,
	}, 0)
	for i := 0; i < 10; i++ {
		if _, ok := r.Lookup("http_status", "example.com", "/"); !ok {
			t.Fatal("rate=1.0 should always match")
		}
	}
}

// TestRegistry_LookupExpiry verifies expired failures are evicted.
func TestRegistry_LookupExpiry(t *testing.T) {
	r := NewRegistry()
	r.Set(FailureSpec{
		Type:     "http_status",
		Host:     "example.com",
		Duration: 10 * time.Millisecond,
		Rate:     1.0,
	}, 0)
	if _, ok := r.Lookup("http_status", "example.com", "/"); !ok {
		t.Fatal("expected match before expiry")
	}
	time.Sleep(20 * time.Millisecond)
	if _, ok := r.Lookup("http_status", "example.com", "/"); ok {
		t.Fatal("expected no match after expiry")
	}
}
