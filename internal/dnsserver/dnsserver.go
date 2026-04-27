// Package dnsserver implements the authoritative DNS server used by the
// uptime-bench DNS fleet member. It listens on UDP and TCP, answers queries
// from a static zone map, and applies failure-injection rules registered
// in a control.FailureRegistry.
package dnsserver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

// TCPReadTimeout bounds how long the TCP handler will wait for a query.
// A misbehaving or stalled client cannot hold a goroutine indefinitely.
const TCPReadTimeout = 5 * time.Second

// ZoneEntry is an A record with its TTL.
type ZoneEntry struct {
	IP  net.IP
	TTL uint32
}

// ZoneMap holds name→ZoneEntry for all authoritative A records.
// Names are stored lowercase without trailing dot.
type ZoneMap map[string]ZoneEntry

// BuildFromFleet builds a ZoneMap from fleet.toml for the given nameserver
// member. It includes A records for all target sites whose hostnames fall
// under the domains this nameserver is authoritative for, with TTLs from
// [[domains]].
func BuildFromFleet(fl *fleet.Config, memberID string) (ZoneMap, error) {
	var ns *fleet.Nameserver
	for i := range fl.Nameservers {
		if fl.Nameservers[i].ID == memberID {
			ns = &fl.Nameservers[i]
			break
		}
	}
	if ns == nil {
		return nil, fmt.Errorf("nameserver %q not found in fleet config", memberID)
	}

	domainTTL := make(map[string]uint32)
	for _, d := range fl.Domains {
		ttl := uint32(d.TTL)
		if ttl == 0 {
			ttl = 30
		}
		domainTTL[strings.ToLower(d.Name)] = ttl
	}

	servedDomains := make(map[string]uint32)
	for _, domain := range ns.Domains {
		d := strings.ToLower(domain)
		ttl, ok := domainTTL[d]
		if !ok {
			ttl = 30
		}
		servedDomains[d] = ttl
	}

	m := make(ZoneMap)
	for _, t := range fl.Targets {
		ip := ResolveIPv4(t.Address)
		if ip == nil {
			return nil, fmt.Errorf("target %s: cannot resolve %q to IPv4", t.ID, t.Address)
		}
		for _, site := range t.Sites {
			host := strings.ToLower(site.Host)
			for domain, ttl := range servedDomains {
				if host == domain || strings.HasSuffix(host, "."+domain) {
					m[host] = ZoneEntry{IP: ip, TTL: ttl}
					break
				}
			}
		}
	}
	return m, nil
}

// MergeFlagZones parses -zone name:addr pairs and adds them to the zone map.
// An addr may be a dotted IPv4 address or a hostname to resolve at startup.
// Entries from flags override any fleet-derived entry for the same name.
func MergeFlagZones(m ZoneMap, rawZones []string) error {
	for _, entry := range rawZones {
		name, addr, ok := strings.Cut(entry, ":")
		if !ok || name == "" || addr == "" {
			return fmt.Errorf("invalid zone %q: expected name:addr", entry)
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		ip := ResolveIPv4(addr)
		if ip == nil {
			return fmt.Errorf("zone %s: cannot resolve %q to IPv4", name, addr)
		}
		m[name] = ZoneEntry{IP: ip, TTL: 30}
	}
	return nil
}

// ResolveIPv4 returns the IPv4 address for addr, which may be a dotted IP
// or a hostname. Returns nil if addr cannot be resolved to an IPv4 address.
func ResolveIPv4(addr string) net.IP {
	if ip := net.ParseIP(addr); ip != nil {
		return ip.To4()
	}
	addrs, err := net.LookupHost(addr)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if parsed := net.ParseIP(a); parsed != nil {
			if v4 := parsed.To4(); v4 != nil {
				return v4
			}
		}
	}
	return nil
}

// ServeUDP runs an authoritative DNS server on the given UDP listener. Each
// query is dispatched to a goroutine so a sleep injected by `dns_latency`
// affects only the delayed response — not subsequent queries.
//
// txt may be nil if no ACME DNS-01 challenge support is wired in.
//
// Returns when conn returns a permanent read error (e.g. on close).
func ServeUDP(conn net.PacketConn, registry *control.FailureRegistry, zones ZoneMap, txt *TXTStore) {
	buf := make([]byte, 4096) // EDNS0 allows up to 4096; classic DNS is 512
	for {
		n, src, err := conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("dns: udp read: %v", err)
			continue
		}
		// Copy: buf is reused on the next iteration.
		query := append([]byte(nil), buf[:n]...)
		go handleUDPQuery(conn, src, query, registry, zones, txt)
	}
}

func handleUDPQuery(conn net.PacketConn, src net.Addr, query []byte, registry *control.FailureRegistry, zones ZoneMap, txt *TXTStore) {
	resp, delay := BuildResponse(query, registry, zones, txt)
	if delay > 0 {
		time.Sleep(delay)
	}
	if resp == nil {
		return
	}
	if _, err := conn.WriteTo(resp, src); err != nil {
		log.Printf("dns: udp write to %s: %v", src, err)
	}
}

// ServeTCP accepts TCP connections on the given listener and serves DNS
// queries over the standard 2-byte-length-prefixed framing.
//
// txt may be nil if no ACME DNS-01 challenge support is wired in.
//
// Returns when ln returns a permanent accept error (e.g. on close).
func ServeTCP(ln net.Listener, registry *control.FailureRegistry, zones ZoneMap, txt *TXTStore) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("dns: tcp accept: %v", err)
			continue
		}
		go HandleTCP(conn, registry, zones, txt)
	}
}

// HandleTCP processes one TCP DNS connection. It enforces TCPReadTimeout to
// prevent a stalled client from leaking a goroutine, and uses io.ReadFull
// because TCP reads can short-read.
//
// txt may be nil if no ACME DNS-01 challenge support is wired in.
func HandleTCP(conn net.Conn, registry *control.FailureRegistry, zones ZoneMap, txt *TXTStore) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(TCPReadTimeout))

	query, err := readTCPMessage(conn)
	if err != nil {
		return
	}
	resp, delay := BuildResponse(query, registry, zones, txt)
	if delay > 0 {
		time.Sleep(delay)
	}
	if resp == nil {
		return
	}
	out := make([]byte, 2+len(resp))
	binary.BigEndian.PutUint16(out[:2], uint16(len(resp)))
	copy(out[2:], resp)
	if _, err := conn.Write(out); err != nil {
		log.Printf("dns: tcp write: %v", err)
	}
}

// readTCPMessage reads one DNS message from r using the 2-byte length prefix
// framing. Uses io.ReadFull so partial reads are handled correctly.
func readTCPMessage(r io.Reader) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}
	msgLen := binary.BigEndian.Uint16(lenBuf[:])
	if msgLen == 0 {
		return nil, fmt.Errorf("dns: tcp: zero-length message")
	}
	buf := make([]byte, msgLen)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// acmeChallengePrefix is the label prefix on names that carry ACME
// DNS-01 validation TXT records. Queries for names starting with
// this prefix bypass failure injection entirely so certmint can mint
// certificates while a benchmark scenario is mid-run — certificate
// issuance is operational plumbing, not a measured failure mode.
const acmeChallengePrefix = "_acme-challenge."

// BuildResponse constructs a DNS response, applying active failure modes.
// Returns (response, delay). A nil response means drop the query with no reply.
// The caller must sleep for delay before sending the response.
//
// txt may be nil if ACME DNS-01 challenge support is not wired in;
// in that case TXT queries fall through to the standard NXDOMAIN path.
//
// DNS message format (RFC 1035):
//
//	Bytes 0-1:   transaction ID
//	Bytes 2-3:   flags
//	Bytes 4-5:   QDCOUNT
//	Bytes 6-7:   ANCOUNT
//	Bytes 8-9:   NSCOUNT
//	Bytes 10-11: ARCOUNT
//	Bytes 12+:   question section
func BuildResponse(query []byte, registry *control.FailureRegistry, zones ZoneMap, txt *TXTStore) ([]byte, time.Duration) {
	if len(query) < 12 {
		return nil, 0
	}

	name, qtype, qEnd := parseQueryName(query)

	// ACME DNS-01 bypass: a TXT query for an _acme-challenge.* name
	// answers from the TXT store regardless of any active DNS failure
	// scenario. The bypass is scoped tightly so dns_timeout /
	// dns_servfail / dns_nxdomain still measure correctly for
	// every other query, including non-ACME TXT lookups.
	const qtypeTXT = 16
	if qtype == qtypeTXT && name != "" && strings.HasPrefix(name, acmeChallengePrefix) && txt != nil {
		if values, ttl, ok := txt.Lookup(name); ok {
			return txtResponse(query, qEnd, values, ttl), 0
		}
	}

	// dns_timeout: drop (no response, no delay).
	if _, ok := registry.Lookup("dns_timeout", "", ""); ok {
		return nil, 0
	}

	// dns_latency: record the delay; still send a valid response.
	var latency time.Duration
	if spec, ok := registry.Lookup("dns_latency", "", ""); ok {
		if raw, ok := spec.Params["added_latency"].(string); ok {
			if d, err := time.ParseDuration(raw); err == nil {
				latency = d
			}
		}
	}

	// dns_servfail.
	if _, ok := registry.Lookup("dns_servfail", "", ""); ok {
		return errorResponse(query, 2), latency // RCODE SERVFAIL
	}

	// dns_ns_unavailable.
	if spec, ok := registry.Lookup("dns_ns_unavailable", "", ""); ok {
		mode, _ := spec.Params["mode"].(string)
		if mode == "servfail" {
			return errorResponse(query, 2), latency
		}
		return nil, 0 // silent drop; no latency on a dropped response
	}

	if name == "" || qEnd == 0 {
		return errorResponse(query, 1), latency // FORMERR
	}

	// dns_nxdomain: return NXDOMAIN even for names present in the zone.
	if _, ok := registry.Lookup("dns_nxdomain", "", ""); ok {
		return errorResponse(query, 3), latency
	}

	// dns_cname_nxdomain: return a CNAME pointing to a non-existent target.
	if _, ok := registry.Lookup("dns_cname_nxdomain", "", ""); ok {
		ttl := uint32(30)
		if e, ok := zones[name]; ok {
			ttl = e.TTL
		}
		return cnameNXDomainResponse(query, qEnd, ttl), latency
	}

	const qtypeA = 1
	if qtype == qtypeA {
		if e, ok := zones[name]; ok {
			return aResponse(query, qEnd, e.IP, e.TTL), latency
		}
	}

	return errorResponse(query, 3), latency
}

// parseQueryName reads the question name, type, and the byte offset after the
// question section from a raw DNS message. Returns ("", 0, 0) on malformed input.
func parseQueryName(msg []byte) (name string, qtype uint16, qEnd int) {
	if len(msg) < 12 {
		return "", 0, 0
	}
	pos := 12
	var labels []string
	for pos < len(msg) {
		llen := int(msg[pos])
		if llen == 0 {
			pos++
			break
		}
		if llen&0xC0 != 0 { // compression pointer in question — reject
			return "", 0, 0
		}
		if pos+1+llen > len(msg) {
			return "", 0, 0
		}
		labels = append(labels, string(msg[pos+1:pos+1+llen]))
		pos += 1 + llen
	}
	if pos+4 > len(msg) {
		return "", 0, 0
	}
	qtype = binary.BigEndian.Uint16(msg[pos : pos+2])
	return strings.ToLower(strings.Join(labels, ".")), qtype, pos + 4
}

// aResponse builds a DNS answer with one A record.
func aResponse(query []byte, qEnd int, ip net.IP, ttl uint32) []byte {
	questionSection := query[12:qEnd]

	// Answer RR: name pointer to offset 12, TYPE A, CLASS IN, TTL, RDLENGTH=4, RDATA.
	rr := make([]byte, 16)
	rr[0] = 0xC0
	rr[1] = 0x0C                            // pointer → offset 12
	binary.BigEndian.PutUint16(rr[2:], 1)   // TYPE A
	binary.BigEndian.PutUint16(rr[4:], 1)   // CLASS IN
	binary.BigEndian.PutUint32(rr[6:], ttl) // TTL
	binary.BigEndian.PutUint16(rr[10:], 4)  // RDLENGTH
	copy(rr[12:], ip.To4())                 // RDATA

	resp := make([]byte, 0, 12+len(questionSection)+len(rr))
	resp = append(resp, query[:2]...) // transaction ID
	resp = append(resp, 0x84, 0x00)   // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)   // QDCOUNT=1
	resp = append(resp, 0x00, 0x01)   // ANCOUNT=1
	resp = append(resp, 0x00, 0x00)   // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)   // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, rr...)
	return resp
}

// txtResponse builds a DNS answer with one TXT record per stored
// value, all sharing the same name (via the question-section
// pointer) and ttl. RDATA for each TXT RR is one or more
// length-prefixed character-strings; this implementation emits one
// character-string per RR, splitting any value longer than 255 bytes
// across additional length-prefixed segments inside the same RDATA.
// ACME challenge tokens are 43 characters (base64url of a 256-bit
// hash) so the split path is reserved for non-ACME use; cover it
// anyway because RFC 1035 mandates it for TXT.
func txtResponse(query []byte, qEnd int, values []string, ttl uint32) []byte {
	questionSection := query[12:qEnd]

	var answers []byte
	for _, value := range values {
		rdata := encodeTXTStrings(value)
		rr := make([]byte, 0, 12+len(rdata))
		rr = append(rr, 0xC0, 0x0C)                // name pointer → offset 12
		rr = binary.BigEndian.AppendUint16(rr, 16) // TYPE TXT
		rr = binary.BigEndian.AppendUint16(rr, 1)  // CLASS IN
		rr = binary.BigEndian.AppendUint32(rr, ttl)
		rr = binary.BigEndian.AppendUint16(rr, uint16(len(rdata))) // RDLENGTH
		rr = append(rr, rdata...)
		answers = append(answers, rr...)
	}

	resp := make([]byte, 0, 12+len(questionSection)+len(answers))
	resp = append(resp, query[:2]...)                               // transaction ID
	resp = append(resp, 0x84, 0x00)                                 // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)                                 // QDCOUNT=1
	resp = binary.BigEndian.AppendUint16(resp, uint16(len(values))) // ANCOUNT
	resp = append(resp, 0x00, 0x00)                                 // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)                                 // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, answers...)
	return resp
}

// encodeTXTStrings converts a single TXT value into the DNS character-string
// encoding (one or more length-prefixed segments, each ≤255 bytes).
func encodeTXTStrings(value string) []byte {
	const maxSegment = 255
	out := make([]byte, 0, len(value)+1+(len(value)/maxSegment))
	for len(value) > maxSegment {
		out = append(out, byte(maxSegment))
		out = append(out, value[:maxSegment]...)
		value = value[maxSegment:]
	}
	out = append(out, byte(len(value)))
	out = append(out, value...)
	return out
}

// cnameNXDomainResponse returns a CNAME answer pointing to dead.invalid., a
// reserved domain that will not resolve. The resolver follows the CNAME and
// gets NXDOMAIN from whatever nameserver handles .invalid.
func cnameNXDomainResponse(query []byte, qEnd int, ttl uint32) []byte {
	questionSection := query[12:qEnd]

	cnameTarget := []byte{
		4, 'd', 'e', 'a', 'd',
		7, 'i', 'n', 'v', 'a', 'l', 'i', 'd',
		0,
	}

	rr := make([]byte, 0, 2+2+2+4+2+len(cnameTarget))
	rr = append(rr, 0xC0, 0x0C) // name pointer → offset 12
	rr = append(rr, 0x00, 0x05) // TYPE CNAME
	rr = append(rr, 0x00, 0x01) // CLASS IN
	rr = binary.BigEndian.AppendUint32(rr, ttl)
	rr = append(rr, byte(0), byte(len(cnameTarget))) // RDLENGTH
	rr = append(rr, cnameTarget...)                  // RDATA

	resp := make([]byte, 0, 12+len(questionSection)+len(rr))
	resp = append(resp, query[:2]...)
	resp = append(resp, 0x84, 0x00)
	resp = append(resp, 0x00, 0x01)
	resp = append(resp, 0x00, 0x01)
	resp = append(resp, 0x00, 0x00)
	resp = append(resp, 0x00, 0x00)
	resp = append(resp, questionSection...)
	resp = append(resp, rr...)
	return resp
}

// errorResponse returns a DNS error response with the given RCODE, echoing
// the query's question section. RCODE 3 is NXDOMAIN; RCODE 2 is SERVFAIL;
// RCODE 1 is FORMERR.
func errorResponse(query []byte, rcode byte) []byte {
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] = 0x84        // QR=1, AA=1
	resp[3] = rcode & 0xF // RCODE
	resp[6], resp[7] = 0, 0
	resp[8], resp[9] = 0, 0
	resp[10], resp[11] = 0, 0
	return resp
}
