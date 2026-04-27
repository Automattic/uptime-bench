package dnsserver

import (
	"encoding/binary"
	"net"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

// testZones is the helper every BuildResponse test uses to wrap a
// flat ZoneMap into a *Zones with no apex info — the tests that
// need apex info construct a *Zones directly.
func testZones(records ZoneMap) *Zones {
	if records == nil {
		records = ZoneMap{}
	}
	return &Zones{Records: records, Apex: map[string]ZoneApex{}}
}

func zonesFromFleet(t *testing.T) *Zones {
	t.Helper()
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{ID: "ns-01", Address: "10.0.0.1", ControlPort: 9100, DNSPort: 53,
				Domains: []string{"example.com"}, Hosts: []string{"ns1.example.com"}},
			{ID: "ns-02", Address: "10.0.0.2", ControlPort: 9100, DNSPort: 53,
				Domains: []string{"example.com"}, Hosts: []string{"ns2.example.com"}},
		},
		Targets: []fleet.Target{
			{ID: "t1", Address: "10.0.0.10", Sites: []fleet.Site{{ID: "bench-a", Host: "bench-a.example.com"}}},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01", "ns-02"}, TTL: 30},
		},
	}
	z, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	return z
}

func TestBuildFromFleet_BuildsZoneApex(t *testing.T) {
	z := zonesFromFleet(t)
	apex, ok := z.Apex["example.com"]
	if !ok {
		t.Fatal("apex for example.com missing")
	}
	if got := apex.NSHostnames; len(got) != 2 || got[0] != "ns1.example.com" || got[1] != "ns2.example.com" {
		t.Fatalf("NSHostnames = %v, want sorted [ns1.example.com ns2.example.com]", got)
	}
	if apex.SOA.MName != "ns1.example.com" {
		t.Fatalf("MName = %q, want ns1.example.com (alphabetically first ns)", apex.SOA.MName)
	}
	if apex.SOA.RName != "hostmaster.example.com" {
		t.Fatalf("RName = %q, want hostmaster.example.com", apex.SOA.RName)
	}
	if apex.SOA.Serial != 1700000000 {
		t.Fatalf("Serial = %d, want 1700000000", apex.SOA.Serial)
	}
	if apex.SOA.Minimum != 30 {
		t.Fatalf("Minimum = %d, want 30 (the zone TTL)", apex.SOA.Minimum)
	}
}

func TestBuildResponse_NSAtApex(t *testing.T) {
	z := zonesFromFleet(t)
	resp, _ := BuildResponse(buildQuery("example.com", 2), control.NewRegistry(), z, nil)

	if rcode := resp[3] & 0x0F; rcode != 0 {
		t.Fatalf("RCODE = %d, want 0 (NOERROR)", rcode)
	}
	ancount := binary.BigEndian.Uint16(resp[6:8])
	if ancount != 2 {
		t.Fatalf("ANCOUNT = %d, want 2", ancount)
	}
	got := decodeNSHostnames(t, resp)
	if len(got) != 2 || got[0] != "ns1.example.com" || got[1] != "ns2.example.com" {
		t.Fatalf("NS answers = %v, want [ns1.example.com ns2.example.com]", got)
	}
}

func TestBuildResponse_SOAAtApex(t *testing.T) {
	z := zonesFromFleet(t)
	resp, _ := BuildResponse(buildQuery("example.com", 6), control.NewRegistry(), z, nil)

	if rcode := resp[3] & 0x0F; rcode != 0 {
		t.Fatalf("RCODE = %d, want 0 (NOERROR)", rcode)
	}
	if got := binary.BigEndian.Uint16(resp[6:8]); got != 1 {
		t.Fatalf("ANCOUNT = %d, want 1", got)
	}
	mname, rname, serial, minimum := decodeSOA(t, resp, true)
	if mname != "ns1.example.com" {
		t.Fatalf("SOA MNAME = %q, want ns1.example.com", mname)
	}
	if rname != "hostmaster.example.com" {
		t.Fatalf("SOA RNAME = %q, want hostmaster.example.com", rname)
	}
	if serial != 1700000000 {
		t.Fatalf("SOA SERIAL = %d, want 1700000000", serial)
	}
	if minimum != 30 {
		t.Fatalf("SOA MINIMUM = %d, want 30", minimum)
	}
}

// TestBuildResponse_NXDOMAINIncludesSOAInAuthority — RFC 2308. A
// real "name not in zone" reply must carry the zone's SOA in the
// AUTHORITY section so resolvers cache the negative answer using
// SOA.MIN as the TTL.
func TestBuildResponse_NXDOMAINIncludesSOAInAuthority(t *testing.T) {
	z := zonesFromFleet(t)
	resp, _ := BuildResponse(buildQuery("nonexistent.example.com", 1), control.NewRegistry(), z, nil)

	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("RCODE = %d, want 3 (NXDOMAIN)", rcode)
	}
	if got := binary.BigEndian.Uint16(resp[8:10]); got != 1 {
		t.Fatalf("NSCOUNT = %d, want 1 (SOA in AUTHORITY)", got)
	}
	mname, _, _, minimum := decodeSOA(t, resp, false)
	if mname != "ns1.example.com" {
		t.Fatalf("AUTHORITY SOA MNAME = %q, want ns1.example.com", mname)
	}
	if minimum != 30 {
		t.Fatalf("AUTHORITY SOA MINIMUM = %d, want 30", minimum)
	}
}

// TestBuildResponse_FailureInjectionNXDOMAINStaysBare — failure-
// injected NXDOMAIN (dns_nxdomain) must NOT carry the SOA in
// AUTHORITY. Monitors-under-test observe that response and the
// benchmark measures their reaction to a *bare* error; softening
// it with negative-caching SOA would change the experiment.
func TestBuildResponse_FailureInjectionNXDOMAINStaysBare(t *testing.T) {
	z := zonesFromFleet(t)
	registry := control.NewRegistry()
	registry.Set(control.FailureSpec{
		Type:     "dns_nxdomain",
		Duration: 24 * 60 * 60 * 1e9, // 1 day in nanoseconds
		Rate:     1.0,
	}, 0)

	resp, _ := BuildResponse(buildQuery("bench-a.example.com", 1), registry, z, nil)
	if rcode := resp[3] & 0x0F; rcode != 3 {
		t.Fatalf("RCODE = %d, want 3 (NXDOMAIN)", rcode)
	}
	if got := binary.BigEndian.Uint16(resp[8:10]); got != 0 {
		t.Fatalf("NSCOUNT = %d, want 0 — failure-injection NXDOMAIN must stay bare", got)
	}
}

// TestBuildResponse_AAtApexStillWorks — an apex name that *also* has
// an A record (as targets sometimes do) keeps NOERROR for an A
// query; only NS and SOA queries hit the apex-record path.
func TestBuildResponse_AAtApexStillWorks(t *testing.T) {
	z := zonesFromFleet(t)
	resp, _ := BuildResponse(buildQuery("bench-a.example.com", 1), control.NewRegistry(), z, nil)
	if rcode := resp[3] & 0x0F; rcode != 0 {
		t.Fatalf("RCODE = %d, want 0", rcode)
	}
	if got := binary.BigEndian.Uint16(resp[6:8]); got != 1 {
		t.Fatalf("ANCOUNT = %d, want 1", got)
	}
}

// decodeNSHostnames pulls every NS RR's RDATA name out of a response
// and returns them in answer order.
func decodeNSHostnames(t *testing.T, resp []byte) []string {
	t.Helper()
	pos := skipQuestion(t, resp)
	ancount := binary.BigEndian.Uint16(resp[6:8])
	out := make([]string, 0, ancount)
	for i := 0; i < int(ancount); i++ {
		// owner name (compression pointer = 2 bytes when high bits 0xC0 are set)
		if resp[pos]&0xC0 == 0xC0 {
			pos += 2
		} else {
			for resp[pos] != 0 {
				pos += int(resp[pos]) + 1
			}
			pos++
		}
		pos += 2 + 2 + 4 // type, class, ttl
		rdlen := binary.BigEndian.Uint16(resp[pos : pos+2])
		pos += 2
		out = append(out, decodeName(resp[pos:pos+int(rdlen)]))
		pos += int(rdlen)
	}
	return out
}

// decodeSOA returns (mname, rname, serial, minimum) for the first
// SOA RR found, looking in the answer section if fromAnswers is
// true, otherwise the authority section.
func decodeSOA(t *testing.T, resp []byte, fromAnswers bool) (string, string, uint32, uint32) {
	t.Helper()
	pos := skipQuestion(t, resp)
	count := binary.BigEndian.Uint16(resp[6:8])
	if !fromAnswers {
		// Skip answer RRs to reach authority.
		for i := 0; i < int(count); i++ {
			pos = skipRR(resp, pos)
		}
		count = binary.BigEndian.Uint16(resp[8:10])
	}
	if count == 0 {
		t.Fatal("no SOA RR found")
	}
	// owner name
	if resp[pos]&0xC0 == 0xC0 {
		pos += 2
	} else {
		for resp[pos] != 0 {
			pos += int(resp[pos]) + 1
		}
		pos++
	}
	pos += 2 + 2 + 4 // type, class, ttl
	rdlen := binary.BigEndian.Uint16(resp[pos : pos+2])
	pos += 2
	end := pos + int(rdlen)

	mname, mlen := readName(resp, pos)
	pos += mlen
	rname, rlen := readName(resp, pos)
	pos += rlen
	serial := binary.BigEndian.Uint32(resp[pos : pos+4])
	pos += 4 + 4 + 4 + 4 // serial+refresh+retry+expire
	minimum := binary.BigEndian.Uint32(resp[pos : pos+4])
	if pos+4 != end {
		t.Fatalf("SOA RDATA length mismatch: expected end at %d, current %d", end, pos+4)
	}
	return mname, rname, serial, minimum
}

func skipQuestion(t *testing.T, resp []byte) int {
	t.Helper()
	pos := 12
	for resp[pos] != 0 {
		pos += int(resp[pos]) + 1
	}
	pos++        // null terminator
	pos += 2 + 2 // qtype + qclass
	return pos
}

func skipRR(resp []byte, pos int) int {
	if resp[pos]&0xC0 == 0xC0 {
		pos += 2
	} else {
		for resp[pos] != 0 {
			pos += int(resp[pos]) + 1
		}
		pos++
	}
	pos += 2 + 2 + 4 // type, class, ttl
	rdlen := binary.BigEndian.Uint16(resp[pos : pos+2])
	pos += 2 + int(rdlen)
	return pos
}

func decodeName(data []byte) string {
	name, _ := readName(data, 0)
	return name
}

func readName(buf []byte, pos int) (string, int) {
	var labels []string
	start := pos
	for pos < len(buf) {
		l := int(buf[pos])
		if l == 0 {
			pos++
			break
		}
		pos++
		labels = append(labels, string(buf[pos:pos+l]))
		pos += l
	}
	return strings.Join(labels, "."), pos - start
}

// silence unused-import warnings for net.IP if a future test removes it.
var _ = net.IP{}
