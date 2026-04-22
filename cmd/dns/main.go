package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

// zoneEntry is an A record with its TTL.
type zoneEntry struct {
	ip  net.IP
	ttl uint32
}

// zoneMap holds name→zoneEntry for all authoritative A records.
// Names are stored lowercase without trailing dot.
type zoneMap map[string]zoneEntry

// zoneFlags collects repeated -zone name:addr flag values.
type zoneFlags []string

func (z *zoneFlags) String() string { return strings.Join(*z, ",") }
func (z *zoneFlags) Set(v string) error {
	*z = append(*z, v)
	return nil
}

func main() {
	dnsPort := flag.Int("dns-port", 53, "port for DNS traffic (UDP and TCP)")
	controlPort := flag.Int("control-port", 9100, "port for harness control API")
	memberID := flag.String("id", "dns", "fleet member ID (must match a [[nameservers]] id in fleet.toml)")
	fleetFile := flag.String("fleet", "", "path to fleet.toml; derives zone records for this member's domains")
	tokenFile := flag.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env)")

	var rawZones zoneFlags
	flag.Var(&rawZones, "zone", "authoritative A record: name:addr (addr may be IP or resolvable hostname; overrides fleet zones; repeat for multiple)")

	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatalf("dns: %v", err)
	}

	zones := make(zoneMap)

	if *fleetFile != "" {
		fl, err := fleet.Load(*fleetFile)
		if err != nil {
			log.Fatalf("dns: fleet: %v", err)
		}
		fleetZones, err := buildZoneMapFromFleet(fl, *memberID)
		if err != nil {
			log.Fatalf("dns: fleet zones: %v", err)
		}
		for name, e := range fleetZones {
			zones[name] = e
		}
		log.Printf("dns: %d zone(s) loaded from fleet config", len(fleetZones))
	}

	// -zone flags override fleet-derived zones.
	if err := mergeZoneFlags(zones, rawZones); err != nil {
		log.Fatalf("dns: zone setup: %v", err)
	}

	for name, e := range zones {
		log.Printf("dns: zone %s → %s (ttl %ds)", name, e.ip, e.ttl)
	}

	registry := control.NewRegistry()

	controlSrv := control.NewServer(*memberID, token, registry)
	controlHTTP := &http.Server{
		Addr:    fmt.Sprintf(":%d", *controlPort),
		Handler: controlSrv.Handler(),
	}

	go func() {
		log.Printf("dns: control API on :%d", *controlPort)
		if err := controlHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("dns: control server: %v", err)
		}
	}()

	dnsAddr := fmt.Sprintf(":%d", *dnsPort)
	go serveDNS("udp", dnsAddr, registry, zones)
	go serveDNS("tcp", dnsAddr, registry, zones)

	log.Printf("dns: authoritative DNS on %s (UDP+TCP)", dnsAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("dns: shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controlHTTP.Shutdown(ctx)
}

// buildZoneMapFromFleet builds a zone map from fleet.toml for the given nameserver
// member. It includes A records for all target sites whose hostnames fall under
// the domains this nameserver is authoritative for, with TTLs from [[domains]].
func buildZoneMapFromFleet(fl *fleet.Config, memberID string) (zoneMap, error) {
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

	// Build domain → TTL from [[domains]] blocks.
	domainTTL := make(map[string]uint32)
	for _, d := range fl.Domains {
		ttl := uint32(d.TTL)
		if ttl == 0 {
			ttl = 30
		}
		domainTTL[strings.ToLower(d.Name)] = ttl
	}

	// Determine which domains this nameserver serves and their TTLs.
	servedDomains := make(map[string]uint32)
	for _, domain := range ns.Domains {
		d := strings.ToLower(domain)
		ttl, ok := domainTTL[d]
		if !ok {
			ttl = 30
		}
		servedDomains[d] = ttl
	}

	m := make(zoneMap)
	for _, t := range fl.Targets {
		ip := resolveIPv4(t.Address)
		if ip == nil {
			return nil, fmt.Errorf("target %s: cannot resolve %q to IPv4", t.ID, t.Address)
		}
		for _, site := range t.Sites {
			host := strings.ToLower(site.Host)
			for domain, ttl := range servedDomains {
				if host == domain || strings.HasSuffix(host, "."+domain) {
					m[host] = zoneEntry{ip: ip, ttl: ttl}
					break
				}
			}
		}
	}
	return m, nil
}

// mergeZoneFlags parses -zone name:addr pairs and adds them to the zone map.
// An addr may be a dotted IPv4 address or a hostname to resolve at startup.
// Entries from flags override any fleet-derived entry for the same name.
func mergeZoneFlags(m zoneMap, rawZones []string) error {
	for _, entry := range rawZones {
		name, addr, ok := strings.Cut(entry, ":")
		if !ok || name == "" || addr == "" {
			return fmt.Errorf("invalid zone %q: expected name:addr", entry)
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		ip := resolveIPv4(addr)
		if ip == nil {
			return fmt.Errorf("zone %s: cannot resolve %q to IPv4", name, addr)
		}
		m[name] = zoneEntry{ip: ip, ttl: 30}
	}
	return nil
}

// resolveIPv4 returns the IPv4 address for addr, which may be a dotted IP
// or a hostname. Returns nil if addr cannot be resolved to an IPv4 address.
func resolveIPv4(addr string) net.IP {
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

// serveDNS listens on the given network+address for DNS queries.
func serveDNS(network, addr string, registry *control.FailureRegistry, zones zoneMap) {
	switch network {
	case "udp":
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			log.Fatalf("dns: listen udp %s: %v", addr, err)
		}
		defer conn.Close()
		buf := make([]byte, 512)
		for {
			n, src, err := conn.ReadFrom(buf)
			if err != nil {
				log.Printf("dns: udp read: %v", err)
				continue
			}
			resp, delay := buildDNSResponse(buf[:n], registry, zones)
			if delay > 0 {
				time.Sleep(delay)
			}
			if resp != nil {
				conn.WriteTo(resp, src)
			}
		}

	case "tcp":
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("dns: listen tcp %s: %v", addr, err)
		}
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				log.Printf("dns: tcp accept: %v", err)
				continue
			}
			go handleDNSTCP(conn, registry, zones)
		}
	}
}

func handleDNSTCP(conn net.Conn, registry *control.FailureRegistry, zones zoneMap) {
	defer conn.Close()
	// TCP DNS: 2-byte length prefix.
	lenBuf := make([]byte, 2)
	if _, err := conn.Read(lenBuf); err != nil {
		return
	}
	msgLen := int(lenBuf[0])<<8 | int(lenBuf[1])
	buf := make([]byte, msgLen)
	if _, err := conn.Read(buf); err != nil {
		return
	}
	resp, delay := buildDNSResponse(buf, registry, zones)
	if delay > 0 {
		time.Sleep(delay)
	}
	if resp == nil {
		return
	}
	out := make([]byte, 2+len(resp))
	out[0] = byte(len(resp) >> 8)
	out[1] = byte(len(resp))
	copy(out[2:], resp)
	conn.Write(out)
}

// buildDNSResponse constructs a DNS response, applying active failure modes.
// Returns (response, delay). A nil response means drop the query with no reply.
// The caller must sleep for delay before sending the response.
//
// DNS message format (RFC 1035):
//
//	Bytes 0-1:  transaction ID
//	Bytes 2-3:  flags
//	Bytes 4-5:  QDCOUNT
//	Bytes 6-7:  ANCOUNT
//	Bytes 8-9:  NSCOUNT
//	Bytes 10-11: ARCOUNT
//	Bytes 12+:  question section
func buildDNSResponse(query []byte, registry *control.FailureRegistry, zones zoneMap) ([]byte, time.Duration) {
	if len(query) < 12 {
		return nil, 0
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

	// Parse the query name and type before checking remaining failure modes.
	name, qtype, qEnd := parseQueryName(query)
	if name == "" || qEnd == 0 {
		return errorResponse(query, 1), latency // FORMERR
	}

	// dns_nxdomain: return NXDOMAIN even for names present in the zone.
	if _, ok := registry.Lookup("dns_nxdomain", "", ""); ok {
		return nxdomainResponse(query), latency
	}

	// dns_cname_nxdomain: return a CNAME pointing to a non-existent target.
	// The resolver follows the CNAME and gets NXDOMAIN from whatever nameserver
	// is authoritative for the CNAME target domain.
	if _, ok := registry.Lookup("dns_cname_nxdomain", "", ""); ok {
		ttl := uint32(30)
		if e, ok := zones[name]; ok {
			ttl = e.ttl
		}
		return cnameNXDomainResponse(query, qEnd, ttl), latency
	}

	// No failure active: serve A record if known, else NXDOMAIN.
	const qtypeA = 1
	if qtype == qtypeA {
		if e, ok := zones[name]; ok {
			return aResponse(query, qEnd, e.ip, e.ttl), latency
		}
	}

	return nxdomainResponse(query), latency
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
	rr[1] = 0x0C // pointer → offset 12
	binary.BigEndian.PutUint16(rr[2:], 1)      // TYPE A
	binary.BigEndian.PutUint16(rr[4:], 1)      // CLASS IN
	binary.BigEndian.PutUint32(rr[6:], ttl)    // TTL
	binary.BigEndian.PutUint16(rr[10:], 4)     // RDLENGTH
	copy(rr[12:], ip.To4())                    // RDATA

	resp := make([]byte, 0, 12+len(questionSection)+len(rr))
	resp = append(resp, query[:2]...)      // transaction ID
	resp = append(resp, 0x84, 0x00)       // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)       // QDCOUNT=1
	resp = append(resp, 0x00, 0x01)       // ANCOUNT=1
	resp = append(resp, 0x00, 0x00)       // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)       // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, rr...)
	return resp
}

// cnameNXDomainResponse returns a CNAME answer pointing to dead.invalid., a
// reserved domain that will not resolve. The resolver follows the CNAME and
// gets NXDOMAIN from whatever nameserver handles .invalid.
func cnameNXDomainResponse(query []byte, qEnd int, ttl uint32) []byte {
	questionSection := query[12:qEnd]

	// CNAME target: dead.invalid. encoded as DNS labels.
	cnameTarget := []byte{
		4, 'd', 'e', 'a', 'd',
		7, 'i', 'n', 'v', 'a', 'l', 'i', 'd',
		0,
	}

	rr := make([]byte, 0, 2+2+2+4+2+len(cnameTarget))
	rr = append(rr, 0xC0, 0x0C)                                      // name pointer → offset 12
	rr = append(rr, 0x00, 0x05)                                      // TYPE CNAME
	rr = append(rr, 0x00, 0x01)                                      // CLASS IN
	rr = append(rr, byte(ttl>>24), byte(ttl>>16), byte(ttl>>8), byte(ttl)) // TTL
	rr = append(rr, byte(0), byte(len(cnameTarget)))                 // RDLENGTH
	rr = append(rr, cnameTarget...)                                   // RDATA

	resp := make([]byte, 0, 12+len(questionSection)+len(rr))
	resp = append(resp, query[:2]...)      // transaction ID
	resp = append(resp, 0x84, 0x00)       // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)       // QDCOUNT=1
	resp = append(resp, 0x00, 0x01)       // ANCOUNT=1
	resp = append(resp, 0x00, 0x00)       // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)       // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, rr...)
	return resp
}

// nxdomainResponse returns an NXDOMAIN response echoing the query's question section.
func nxdomainResponse(query []byte) []byte {
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] = 0x84 // QR=1, AA=1
	resp[3] = 0x03 // RCODE NXDOMAIN
	// Zero the answer/authority/additional counts; question section is retained.
	resp[6] = 0; resp[7] = 0
	resp[8] = 0; resp[9] = 0
	resp[10] = 0; resp[11] = 0
	return resp
}

// errorResponse returns a DNS error response with the given RCODE, echoing
// the query's question section.
func errorResponse(query []byte, rcode byte) []byte {
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] = 0x84        // QR=1, AA=1
	resp[3] = rcode & 0xF // RCODE
	resp[6] = 0; resp[7] = 0
	resp[8] = 0; resp[9] = 0
	resp[10] = 0; resp[11] = 0
	return resp
}
