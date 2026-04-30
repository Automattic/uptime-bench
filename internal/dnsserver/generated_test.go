package dnsserver

import (
	"net"
	"testing"
)

func TestGeneratedRecordRangeLookup(t *testing.T) {
	r, err := NewGeneratedRecordRange("load", "site-%07d.load.example.com", 1, 1_000_000, net.ParseIP("10.0.0.10"), 30)
	if err != nil {
		t.Fatalf("NewGeneratedRecordRange: %v", err)
	}

	for _, host := range []string{
		"site-0000001.load.example.com",
		"site-1000000.load.example.com",
		"SITE-0000042.LOAD.EXAMPLE.COM.",
	} {
		got, ok := r.Lookup(host)
		if !ok {
			t.Fatalf("Lookup(%q) returned ok=false", host)
		}
		if !got.IP.Equal(net.ParseIP("10.0.0.10")) || got.TTL != 30 {
			t.Fatalf("Lookup(%q) = %+v, want 10.0.0.10 ttl=30", host, got)
		}
	}

	for _, host := range []string{
		"site-0000000.load.example.com",
		"site-1000001.load.example.com",
		"site-42.load.example.com",
		"other-0000042.load.example.com",
		"site-0000042.other.example.com",
	} {
		if _, ok := r.Lookup(host); ok {
			t.Fatalf("Lookup(%q) returned ok=true, want false", host)
		}
	}
}

func TestGeneratedRecordRangePlainPlaceholder(t *testing.T) {
	r, err := NewGeneratedRecordRange("load", "site-%d.load.example.com", 0, 2, net.ParseIP("10.0.0.10"), 30)
	if err != nil {
		t.Fatalf("NewGeneratedRecordRange: %v", err)
	}
	if _, ok := r.Lookup("site-0.load.example.com"); !ok {
		t.Fatal("site-0.load.example.com did not match plain integer pattern")
	}
	if _, ok := r.Lookup("site-01.load.example.com"); ok {
		t.Fatal("site-01.load.example.com matched range 0..1, want out of range")
	}
}

func TestGeneratedRecordRangeRejectsInvalidPatterns(t *testing.T) {
	for _, pattern := range []string{
		"site.load.example.com",
		"site-%s.load.example.com",
		"site-%07d-%02d.load.example.com",
		"site-%0d.load.example.com",
	} {
		if _, err := NewGeneratedRecordRange("load", pattern, 1, 10, net.ParseIP("10.0.0.10"), 30); err == nil {
			t.Fatalf("NewGeneratedRecordRange(%q) returned nil error", pattern)
		}
	}
}
