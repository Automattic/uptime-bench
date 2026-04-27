package dnsserver

import (
	"slices"
	"sync"
	"testing"
)

func TestTXTStore_AddAndLookupNormalizesName(t *testing.T) {
	s := NewTXTStore()
	s.Add("_ACME-Challenge.Bench.Example.com.", "token-1", 30)

	values, ttl, ok := s.Lookup("_acme-challenge.bench.example.com")
	if !ok {
		t.Fatal("Lookup found nothing for normalized name")
	}
	if !slices.Equal(values, []string{"token-1"}) {
		t.Fatalf("values = %v, want [token-1]", values)
	}
	if ttl != 30 {
		t.Fatalf("ttl = %d, want 30", ttl)
	}
}

func TestTXTStore_PreservesMultipleValuesAtSameName(t *testing.T) {
	s := NewTXTStore()
	s.Add("_acme-challenge.bench.example.com", "token-apex", 30)
	s.Add("_acme-challenge.bench.example.com", "token-wildcard", 30)

	values, _, ok := s.Lookup("_acme-challenge.bench.example.com")
	if !ok {
		t.Fatal("Lookup found nothing")
	}
	if len(values) != 2 || !slices.Contains(values, "token-apex") || !slices.Contains(values, "token-wildcard") {
		t.Fatalf("values = %v, want both apex and wildcard tokens", values)
	}
}

func TestTXTStore_AddDuplicateValueIsNoOp(t *testing.T) {
	s := NewTXTStore()
	s.Add("name.example.com", "v", 30)
	s.Add("name.example.com", "v", 30)

	values, _, _ := s.Lookup("name.example.com")
	if len(values) != 1 {
		t.Fatalf("values = %v, want exactly one entry — duplicate Add must be idempotent", values)
	}
}

func TestTXTStore_AddUpdatesTTLOnRepeat(t *testing.T) {
	s := NewTXTStore()
	s.Add("name.example.com", "v", 30)
	s.Add("name.example.com", "v", 60)

	_, ttl, _ := s.Lookup("name.example.com")
	if ttl != 60 {
		t.Fatalf("ttl = %d, want 60 — last write should win", ttl)
	}
}

func TestTXTStore_RemoveOnlyOneValuePreservesSiblings(t *testing.T) {
	s := NewTXTStore()
	s.Add("_acme-challenge.bench.example.com", "token-apex", 30)
	s.Add("_acme-challenge.bench.example.com", "token-wildcard", 30)

	s.Remove("_acme-challenge.bench.example.com", "token-apex")

	values, _, ok := s.Lookup("_acme-challenge.bench.example.com")
	if !ok {
		t.Fatal("removing one of two values should leave the name in the store")
	}
	if !slices.Equal(values, []string{"token-wildcard"}) {
		t.Fatalf("values = %v, want [token-wildcard]", values)
	}
}

func TestTXTStore_RemoveLastValueDeletesName(t *testing.T) {
	s := NewTXTStore()
	s.Add("name.example.com", "only", 30)
	s.Remove("name.example.com", "only")

	if _, _, ok := s.Lookup("name.example.com"); ok {
		t.Fatal("removing the last value should drop the name from Lookup")
	}
}

func TestTXTStore_RemoveUnknownValueIsNoOp(t *testing.T) {
	s := NewTXTStore()
	s.Add("name.example.com", "v", 30)
	s.Remove("name.example.com", "different-value")

	values, _, _ := s.Lookup("name.example.com")
	if !slices.Equal(values, []string{"v"}) {
		t.Fatalf("values = %v, want [v]", values)
	}
}

// TestTXTStore_LookupReturnsCopy — callers must be able to mutate the
// returned slice without corrupting the store. The store is hit
// concurrently by the DNS read loops; aliasing the internal slice
// would race.
func TestTXTStore_LookupReturnsCopy(t *testing.T) {
	s := NewTXTStore()
	s.Add("name.example.com", "v1", 30)

	values, _, _ := s.Lookup("name.example.com")
	values[0] = "tampered"

	got, _, _ := s.Lookup("name.example.com")
	if !slices.Equal(got, []string{"v1"}) {
		t.Fatalf("internal state was mutated through Lookup result: %v", got)
	}
}

// TestTXTStore_ConcurrentAccess — the DNS read loop calls Lookup on
// every TXT query while certmint hooks call Add/Remove via the
// control API; the store must survive the mix without races.
func TestTXTStore_ConcurrentAccess(t *testing.T) {
	s := NewTXTStore()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.Add("name.example.com", "value", 30)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.Remove("name.example.com", "value")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.Lookup("name.example.com")
		}
	}()
	wg.Wait()
}
