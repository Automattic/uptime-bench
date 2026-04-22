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
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

// zoneMap holds name→IP mappings for A record responses.
// Names are stored lowercase without trailing dot.
type zoneMap map[string]net.IP

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
	memberID := flag.String("id", "dns", "fleet member ID for control status responses")
	tokenFile := flag.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env)")

	var rawZones zoneFlags
	flag.Var(&rawZones, "zone", "authoritative A record: name:addr (addr may be an IP or resolvable hostname; repeat for multiple)")

	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatalf("dns: %v", err)
	}

	zones, err := buildZoneMap(rawZones)
	if err != nil {
		log.Fatalf("dns: zone setup: %v", err)
	}
	if len(zones) > 0 {
		for name, ip := range zones {
			log.Printf("dns: zone %s → %s", name, ip)
		}
	}

	registry := control.NewRegistry()

	// Control API server.
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

	// DNS server — UDP and TCP on dnsPort.
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

// buildZoneMap resolves -zone name:addr pairs into a name→IP map.
// addr may be a dotted IPv4 address or a hostname to resolve at startup.
func buildZoneMap(rawZones []string) (zoneMap, error) {
	m := make(zoneMap, len(rawZones))
	for _, entry := range rawZones {
		name, addr, ok := strings.Cut(entry, ":")
		if !ok || name == "" || addr == "" {
			return nil, fmt.Errorf("invalid zone %q: expected name:addr", entry)
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))

		ip := net.ParseIP(addr)
		if ip == nil {
			// Treat as hostname; resolve at startup using the OS resolver.
			addrs, err := net.LookupHost(addr)
			if err != nil {
				return nil, fmt.Errorf("zone %s: resolve %q: %w", name, addr, err)
			}
			for _, a := range addrs {
				if parsed := net.ParseIP(a); parsed != nil {
					if v4 := parsed.To4(); v4 != nil {
						ip = v4
						break
					}
				}
			}
			if ip == nil {
				return nil, fmt.Errorf("zone %s: no IPv4 address found for %q", name, addr)
			}
		} else if v4 := ip.To4(); v4 != nil {
			ip = v4
		} else {
			return nil, fmt.Errorf("zone %s: %q is IPv6; only IPv4 A records are supported", name, addr)
		}
		m[name] = ip
	}
	return m, nil
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
			resp := buildDNSResponse(buf[:n], registry, zones)
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
	resp := buildDNSResponse(buf, registry, zones)
	if resp == nil {
		return
	}
	out := make([]byte, 2+len(resp))
	out[0] = byte(len(resp) >> 8)
	out[1] = byte(len(resp))
	copy(out[2:], resp)
	conn.Write(out)
}

// buildDNSResponse constructs a DNS response for the given query, applying
// active failure modes from the registry. If a zone A record is configured
// for the queried name and no failure is active, it returns an A record answer.
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
func buildDNSResponse(query []byte, registry *control.FailureRegistry, zones zoneMap) []byte {
	if len(query) < 12 {
		return nil
	}

	txID := query[:2]

	// dns_timeout: drop the response (return nil — don't reply).
	if _, ok := registry.Lookup("dns_timeout", "", ""); ok {
		return nil
	}

	// dns_servfail or dns_ns_unavailable in servfail mode.
	if _, ok := registry.Lookup("dns_servfail", "", ""); ok {
		return dnsErrorResponse(txID, 2) // SERVFAIL rcode=2
	}
	if spec, ok := registry.Lookup("dns_ns_unavailable", "", ""); ok {
		mode, _ := spec.Params["mode"].(string)
		if mode == "servfail" {
			return dnsErrorResponse(txID, 2)
		}
		return nil // "silent" mode: drop
	}

	// No failure active: serve A record if we have a zone entry, else NXDOMAIN.
	if len(zones) > 0 {
		name, qtype, qEnd := parseQueryName(query)
		if name != "" && qEnd > 0 {
			const qtypeA = 1
			if qtype == qtypeA {
				if ip, ok := zones[name]; ok {
					return buildAResponse(query, qEnd, ip, 5)
				}
			}
		}
	}

	// dns_nxdomain or default: NXDOMAIN.
	resp := make([]byte, len(query))
	copy(resp, query)
	resp[2] = 0x84 // QR=1, Opcode=0, AA=1, TC=0, RD=0
	resp[3] = 0x03 // RA=0, Z=0, RCODE=3 (NXDOMAIN)
	return resp
}

// parseQueryName reads the question name, type, and the offset after the question
// section from a raw DNS message. Returns ("", 0, 0) on malformed input.
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
		// Reject compression pointers (top 2 bits set) in the question.
		if llen&0xC0 != 0 {
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

// buildAResponse constructs a DNS answer with one A record.
func buildAResponse(query []byte, qEnd int, ip net.IP, ttlSec uint32) []byte {
	// Reuse question section bytes up to qEnd.
	questionSection := query[12:qEnd]

	// Answer RR: name pointer to offset 12, TYPE A, CLASS IN, TTL, RDLENGTH=4, RDATA.
	answer := make([]byte, 16)
	answer[0] = 0xC0 // pointer
	answer[1] = 0x0C // → offset 12 (start of question)
	binary.BigEndian.PutUint16(answer[2:], 1)        // TYPE A
	binary.BigEndian.PutUint16(answer[4:], 1)        // CLASS IN
	binary.BigEndian.PutUint32(answer[6:], ttlSec)   // TTL
	binary.BigEndian.PutUint16(answer[10:], 4)       // RDLENGTH
	copy(answer[12:], ip.To4())                      // RDATA

	resp := make([]byte, 0, 12+len(questionSection)+len(answer))
	resp = append(resp, query[:2]...)       // transaction ID
	resp = append(resp, 0x84, 0x00)        // QR=1 AA=1 RCODE=0
	resp = append(resp, 0x00, 0x01)        // QDCOUNT=1
	resp = append(resp, 0x00, 0x01)        // ANCOUNT=1
	resp = append(resp, 0x00, 0x00)        // NSCOUNT=0
	resp = append(resp, 0x00, 0x00)        // ARCOUNT=0
	resp = append(resp, questionSection...)
	resp = append(resp, answer...)
	return resp
}

func dnsErrorResponse(txID []byte, rcode byte) []byte {
	resp := make([]byte, 12)
	copy(resp[:2], txID)
	resp[2] = 0x84        // QR=1, AA=1
	resp[3] = rcode & 0xF // RCODE
	return resp
}
