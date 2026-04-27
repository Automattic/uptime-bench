package dnsserver

import (
	"encoding/binary"
	"strings"
)

// Zones bundles every kind of authoritative record the server answers
// for a benchmark domain. Records is the existing flat host→A map;
// Apex carries the per-zone NS / SOA needed to answer apex-typed
// queries (NS / SOA) and to populate the AUTHORITY section of real
// NXDOMAIN replies for negative caching (RFC 2308).
//
// "Real" matters here: the failure-injection paths (dns_nxdomain,
// dns_servfail) deliberately do *not* include SOA in AUTHORITY,
// because monitors-under-test observe those replies and the
// benchmark measures their response to a *bare* error. Only the
// "name truly isn't in the zone" path gets the negative-caching
// SOA — see BuildResponse.
type Zones struct {
	Records ZoneMap
	Apex    map[string]ZoneApex
}

// ZoneApex carries per-zone records — the hostnames the parent zone
// delegates to, plus the SOA the resolver needs for negative caching.
type ZoneApex struct {
	Name        string   // zone apex, lowercase, no trailing dot
	NSHostnames []string // public hostnames of every authoritative member
	SOA         SOA
}

// SOA is the RFC 1035 §3.3.13 zone-apex record.
type SOA struct {
	MName   string // primary nameserver (FQDN, no trailing dot)
	RName   string // admin email encoded as a DNS name
	Serial  uint32
	Refresh uint32
	Retry   uint32
	Expire  uint32
	Minimum uint32 // also the negative-caching TTL per RFC 2308 §5
}

// LookupApex returns the ZoneApex whose name equals or is an ancestor
// of host. Used for both apex queries (NS/SOA at the zone name) and
// for finding the right SOA to put in the AUTHORITY section of an
// NXDOMAIN reply for a name under that zone.
func (z *Zones) LookupApex(host string) (ZoneApex, bool) {
	if z == nil {
		return ZoneApex{}, false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for apex, za := range z.Apex {
		if host == apex || strings.HasSuffix(host, "."+apex) {
			return za, true
		}
	}
	return ZoneApex{}, false
}

// encodeName encodes name as DNS labels with a terminating zero byte.
func encodeName(name string) []byte {
	name = strings.TrimSuffix(name, ".")
	if name == "" {
		return []byte{0}
	}
	var out []byte
	for _, label := range strings.Split(name, ".") {
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	out = append(out, 0)
	return out
}

// soaRDATA encodes a SOA's RDATA per RFC 1035 §3.3.13.
func soaRDATA(s SOA) []byte {
	var rd []byte
	rd = append(rd, encodeName(s.MName)...)
	rd = append(rd, encodeName(s.RName)...)
	rd = binary.BigEndian.AppendUint32(rd, s.Serial)
	rd = binary.BigEndian.AppendUint32(rd, s.Refresh)
	rd = binary.BigEndian.AppendUint32(rd, s.Retry)
	rd = binary.BigEndian.AppendUint32(rd, s.Expire)
	rd = binary.BigEndian.AppendUint32(rd, s.Minimum)
	return rd
}

// nsResponse builds an NS answer at the zone apex, listing every
// hostname in the apex's NSHostnames. Each RR carries a fresh
// encoded name (no compression) — the trade-off saves complexity in
// exchange for slightly larger responses; classic DNS UDP capacity
// (512 bytes without EDNS) is plenty for two or three NS hosts.
func nsResponse(query []byte, qEnd int, apex ZoneApex, ttl uint32) []byte {
	questionSection := query[12:qEnd]

	var answers []byte
	for _, host := range apex.NSHostnames {
		rdata := encodeName(host)
		rr := make([]byte, 0, 12+len(rdata))
		rr = append(rr, 0xC0, 0x0C)               // name pointer → offset 12 (the qname)
		rr = binary.BigEndian.AppendUint16(rr, 2) // TYPE NS
		rr = binary.BigEndian.AppendUint16(rr, 1) // CLASS IN
		rr = binary.BigEndian.AppendUint32(rr, ttl)
		rr = binary.BigEndian.AppendUint16(rr, uint16(len(rdata))) // RDLENGTH
		rr = append(rr, rdata...)
		answers = append(answers, rr...)
	}

	resp := make([]byte, 0, 12+len(questionSection)+len(answers))
	resp = append(resp, query[0], query[1])                                   // transaction ID
	resp = append(resp, 0x84, 0x00)                                           // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)                                           // QDCOUNT=1
	resp = binary.BigEndian.AppendUint16(resp, uint16(len(apex.NSHostnames))) // ANCOUNT
	resp = append(resp, 0x00, 0x00)                                           // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)                                           // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, answers...)
	return resp
}

// soaResponse builds a SOA answer at the zone apex.
func soaResponse(query []byte, qEnd int, apex ZoneApex, ttl uint32) []byte {
	questionSection := query[12:qEnd]
	rdata := soaRDATA(apex.SOA)

	rr := make([]byte, 0, 12+len(rdata))
	rr = append(rr, 0xC0, 0x0C)               // name pointer
	rr = binary.BigEndian.AppendUint16(rr, 6) // TYPE SOA
	rr = binary.BigEndian.AppendUint16(rr, 1) // CLASS IN
	rr = binary.BigEndian.AppendUint32(rr, ttl)
	rr = binary.BigEndian.AppendUint16(rr, uint16(len(rdata))) // RDLENGTH
	rr = append(rr, rdata...)

	resp := make([]byte, 0, 12+len(questionSection)+len(rr))
	resp = append(resp, query[0], query[1]) // transaction ID
	resp = append(resp, 0x84, 0x00)         // QR=1 AA=1
	resp = append(resp, 0x00, 0x01)         // QDCOUNT=1
	resp = append(resp, 0x00, 0x01)         // ANCOUNT=1
	resp = append(resp, 0x00, 0x00)         // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)         // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, rr...)
	return resp
}

// nxdomainWithSOA builds a NOERROR-or-NXDOMAIN reply that includes
// the zone's SOA in the AUTHORITY section, so recursive resolvers
// can cache the negative answer per RFC 2308. Used only for "name
// genuinely isn't in the zone" — failure-injection NXDOMAIN keeps
// the bare errorResponse so monitors-under-test see the unsoftened
// failure.
func nxdomainWithSOA(query []byte, qEnd int, apex ZoneApex) []byte {
	questionSection := query[12:qEnd]
	rdata := soaRDATA(apex.SOA)

	// AUTHORITY-section SOA. The owner name is the zone apex; we
	// can't reuse the question-section pointer here because the
	// queried name isn't necessarily the apex (e.g. unknown.harmonic.party).
	// Emit the apex name uncompressed.
	apexName := encodeName(apex.Name)
	auth := make([]byte, 0, len(apexName)+10+len(rdata))
	auth = append(auth, apexName...)
	auth = binary.BigEndian.AppendUint16(auth, 6)                  // TYPE SOA
	auth = binary.BigEndian.AppendUint16(auth, 1)                  // CLASS IN
	auth = binary.BigEndian.AppendUint32(auth, apex.SOA.Minimum)   // SOA.MIN as the AUTHORITY TTL — RFC 2308
	auth = binary.BigEndian.AppendUint16(auth, uint16(len(rdata))) // RDLENGTH
	auth = append(auth, rdata...)

	resp := make([]byte, 0, 12+len(questionSection)+len(auth))
	resp = append(resp, query[0], query[1]) // transaction ID
	resp = append(resp, 0x84, 0x03)         // QR=1 AA=1 RCODE=3 (NXDOMAIN)
	resp = append(resp, 0x00, 0x01)         // QDCOUNT=1
	resp = append(resp, 0x00, 0x00)         // ANCOUNT=0
	resp = append(resp, 0x00, 0x01)         // NSCOUNT=1
	resp = append(resp, 0x00, 0x00)         // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, auth...)
	return resp
}
