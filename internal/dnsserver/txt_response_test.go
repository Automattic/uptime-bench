package dnsserver

import (
	"bytes"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// buildQuery encodes a minimal DNS query for name with the given
// qtype, transaction id 0x1234. qtype 1 is A; qtype 16 is TXT.
func buildQuery(name string, qtype uint16) []byte {
	var qname []byte
	for _, label := range bytes.Split([]byte(name), []byte(".")) {
		qname = append(qname, byte(len(label)))
		qname = append(qname, label...)
	}
	qname = append(qname, 0)

	msg := []byte{
		0x12, 0x34, // ID
		0x01, 0x00, // RD=1
		0x00, 0x01, // QDCOUNT=1
		0x00, 0x00, // ANCOUNT
		0x00, 0x00, // NSCOUNT
		0x00, 0x00, // ARCOUNT
	}
	msg = append(msg, qname...)
	msg = binary.BigEndian.AppendUint16(msg, qtype)
	msg = binary.BigEndian.AppendUint16(msg, 1) // CLASS IN
	return msg
}

// parseTXTAnswers walks a DNS response and returns the TXT
// character-strings from each answer RR. Tests use this to assert
// that emitted RDATA round-trips through standard TXT decoding.
func parseTXTAnswers(t *testing.T, resp []byte) []string {
	t.Helper()
	if len(resp) < 12 {
		t.Fatalf("response too short: %d bytes", len(resp))
	}
	ancount := binary.BigEndian.Uint16(resp[6:8])

	// Skip question section.
	pos := 12
	for pos < len(resp) && resp[pos] != 0 {
		pos += int(resp[pos]) + 1
	}
	pos++        // null terminator
	pos += 2 + 2 // QTYPE + QCLASS

	var out []string
	for i := 0; i < int(ancount); i++ {
		// name (compression pointer or labels) — 2 bytes if pointer
		if resp[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for resp[pos] != 0 {
				pos += int(resp[pos]) + 1
			}
			pos++
		}
		qtype := binary.BigEndian.Uint16(resp[pos : pos+2])
		pos += 2
		pos += 2 // class
		pos += 4 // ttl
		rdlen := binary.BigEndian.Uint16(resp[pos : pos+2])
		pos += 2
		rdata := resp[pos : pos+int(rdlen)]
		pos += int(rdlen)

		if qtype != 16 {
			t.Fatalf("answer %d is qtype %d, want TXT (16)", i, qtype)
		}
		var value strings.Builder
		rp := 0
		for rp < len(rdata) {
			seg := int(rdata[rp])
			rp++
			value.Write(rdata[rp : rp+seg])
			rp += seg
		}
		out = append(out, value.String())
	}
	return out
}

func responseRCODE(resp []byte) byte {
	if len(resp) < 4 {
		return 0xff
	}
	return resp[3] & 0x0F
}

func TestBuildResponse_TXTReturnsAllStoredValues(t *testing.T) {
	store := NewTXTStore()
	store.Add("_acme-challenge.bench.example.com", "validation-apex", 30)
	store.Add("_acme-challenge.bench.example.com", "validation-wildcard", 30)

	resp, delay := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), ZoneMap{}, store)
	if delay != 0 {
		t.Fatalf("delay = %v, want 0", delay)
	}
	if rcode := responseRCODE(resp); rcode != 0 {
		t.Fatalf("RCODE = %d, want 0 (NOERROR)", rcode)
	}
	got := parseTXTAnswers(t, resp)
	if len(got) != 2 {
		t.Fatalf("answers = %v, want 2", got)
	}
	wantContains := func(s string) {
		for _, g := range got {
			if g == s {
				return
			}
		}
		t.Fatalf("answers %v do not contain %q", got, s)
	}
	wantContains("validation-apex")
	wantContains("validation-wildcard")
}

// TestBuildResponse_TXTAnsweredEvenWithoutAZoneEntry — ACME challenge
// names are not in the static zone map. The TXT path must answer
// regardless of zone presence.
func TestBuildResponse_TXTAnsweredEvenWithoutAZoneEntry(t *testing.T) {
	store := NewTXTStore()
	store.Add("_acme-challenge.bench.example.com", "validation-token", 30)

	resp, _ := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), ZoneMap{}, store)
	if rcode := responseRCODE(resp); rcode != 0 {
		t.Fatalf("RCODE = %d, want 0 (NOERROR)", rcode)
	}
	got := parseTXTAnswers(t, resp)
	if len(got) != 1 || got[0] != "validation-token" {
		t.Fatalf("answers = %v, want [validation-token]", got)
	}
}

// TestBuildResponse_TXTBypassesDNSFailureScenarios — every
// DNS failure mode must be ignored for _acme-challenge TXT queries
// when the store has a value, so certmint can mint certs while a
// benchmark scenario is in flight.
func TestBuildResponse_TXTBypassesDNSFailureScenarios(t *testing.T) {
	cases := []struct {
		name        string
		failureType string
		params      map[string]any
	}{
		{"dns_timeout", "dns_timeout", nil},
		{"dns_servfail", "dns_servfail", nil},
		{"dns_nxdomain", "dns_nxdomain", nil},
		{"dns_cname_nxdomain", "dns_cname_nxdomain", nil},
		{"dns_ns_unavailable_silent", "dns_ns_unavailable", map[string]any{"mode": "silent"}},
		{"dns_ns_unavailable_servfail", "dns_ns_unavailable", map[string]any{"mode": "servfail"}},
		{"dns_latency", "dns_latency", map[string]any{"added_latency": "500ms"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := control.NewRegistry()
			registry.Set(control.FailureSpec{
				Type:     tc.failureType,
				Duration: time.Hour,
				Rate:     1.0,
				Params:   tc.params,
			}, 0)
			store := NewTXTStore()
			store.Add("_acme-challenge.bench.example.com", "validation", 30)

			resp, delay := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), registry, ZoneMap{}, store)
			if delay != 0 {
				t.Fatalf("delay = %v, want 0 (failure injection must be bypassed)", delay)
			}
			if rcode := responseRCODE(resp); rcode != 0 {
				t.Fatalf("RCODE = %d, want 0 (NOERROR) — failure injection should be bypassed", rcode)
			}
			got := parseTXTAnswers(t, resp)
			if len(got) != 1 || got[0] != "validation" {
				t.Fatalf("answers = %v, want [validation]", got)
			}
		})
	}
}

// TestBuildResponse_NonChallengeTXTRespectsFailureInjection — the
// bypass is scoped tightly to _acme-challenge.* names. A regular TXT
// query should still feel an active dns_servfail. This guards
// against accidentally widening the bypass to "any TXT".
func TestBuildResponse_NonChallengeTXTRespectsFailureInjection(t *testing.T) {
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "dns_servfail",
		Duration: time.Hour,
		Rate:     1.0,
	}, 0)
	store := NewTXTStore()
	store.Add("bench.example.com", "non-challenge-value", 30)

	resp, _ := BuildResponse(buildQuery("bench.example.com", 16), registry, ZoneMap{}, store)
	if rcode := responseRCODE(resp); rcode != 2 {
		t.Fatalf("RCODE = %d, want 2 (SERVFAIL) — non-ACME TXT must respect failure injection", rcode)
	}
}

// TestBuildResponse_TXTNoStoreFallsThroughToNXDOMAIN — when no TXT
// store is wired in (txt == nil), or the store is empty, an
// _acme-challenge query falls through to the standard miss path
// (NXDOMAIN). The bypass only fires when there is something to
// answer with.
func TestBuildResponse_TXTNoStoreFallsThroughToNXDOMAIN(t *testing.T) {
	cases := []struct {
		name  string
		store *TXTStore
	}{
		{"nil store", nil},
		{"empty store", NewTXTStore()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), ZoneMap{}, tc.store)
			if rcode := responseRCODE(resp); rcode != 3 {
				t.Fatalf("RCODE = %d, want 3 (NXDOMAIN)", rcode)
			}
		})
	}
}

// TestBuildResponse_AStillWorksWhileTXTStorePopulated — a populated
// TXT store must not interfere with normal A-record serving.
func TestBuildResponse_AStillWorksWhileTXTStorePopulated(t *testing.T) {
	store := NewTXTStore()
	store.Add("_acme-challenge.bench.example.com", "token", 30)
	zones := ZoneMap{
		"bench.example.com": {IP: net.ParseIP("10.0.0.1"), TTL: 30},
	}

	resp, _ := BuildResponse(buildQuery("bench.example.com", 1), control.NewRegistry(), zones, store)
	if rcode := responseRCODE(resp); rcode != 0 {
		t.Fatalf("RCODE = %d, want 0 (NOERROR)", rcode)
	}
	if len(resp) < 12+5+16 {
		t.Fatalf("response too short for an A answer: %d bytes", len(resp))
	}
	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount != 1 {
		t.Fatalf("ANCOUNT = %d, want 1", ancount)
	}
}

// TestBuildResponse_TXTLongValueSplitsAcross255ByteSegments — RFC 1035
// requires TXT character-strings be ≤255 bytes. Values longer than
// that must be emitted as multiple length-prefixed segments inside
// the same RDATA, and a standards-compliant parser concatenates them
// back into the original value.
func TestBuildResponse_TXTLongValueSplitsAcross255ByteSegments(t *testing.T) {
	store := NewTXTStore()
	long := strings.Repeat("a", 600)
	store.Add("_acme-challenge.bench.example.com", long, 30)

	resp, _ := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), ZoneMap{}, store)
	got := parseTXTAnswers(t, resp)
	if len(got) != 1 || got[0] != long {
		t.Fatalf("round-trip failed: got %d-char value, want %d-char", len(got[0]), len(long))
	}
}
