package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

func main() {
	dnsPort := flag.Int("dns-port", 53, "port for DNS traffic (UDP and TCP)")
	controlPort := flag.Int("control-port", 9100, "port for harness control API")
	memberID := flag.String("id", "dns", "fleet member ID for control status responses")
	tokenFile := flag.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env)")
	flag.Parse()

	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatalf("dns: %v", err)
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
	go serveDNS("udp", dnsAddr, registry)
	go serveDNS("tcp", dnsAddr, registry)

	log.Printf("dns: authoritative DNS on %s (UDP+TCP)", dnsAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("dns: shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	controlHTTP.Shutdown(ctx)
}

// serveDNS listens on the given network+address for DNS queries.
// It answers all queries with NXDOMAIN unless a dns_nxdomain failure
// is active (same result) or dns_servfail is active (SERVFAIL).
// dns_timeout holds the response. Full DNS injection is deferred to
// when the fleet needs DNS failure scenarios.
func serveDNS(network, addr string, registry *control.FailureRegistry) {
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
			resp := buildDNSResponse(buf[:n], registry)
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
			go handleDNSTCP(conn, registry)
		}
	}
}

func handleDNSTCP(conn net.Conn, registry *control.FailureRegistry) {
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
	resp := buildDNSResponse(buf, registry)
	if resp == nil {
		return
	}
	out := make([]byte, 2+len(resp))
	out[0] = byte(len(resp) >> 8)
	out[1] = byte(len(resp))
	copy(out[2:], resp)
	conn.Write(out)
}

// buildDNSResponse constructs a minimal DNS response for the given query.
// It applies active DNS failure modes from the registry.
//
// DNS message format (RFC 1035):
//
//	Bytes 0-1: transaction ID
//	Bytes 2-3: flags
//	Bytes 4-5: QDCOUNT
//	Bytes 6-7: ANCOUNT
//	Bytes 8-9: NSCOUNT
//	Bytes 10-11: ARCOUNT
//	Bytes 12+: question section
func buildDNSResponse(query []byte, registry *control.FailureRegistry) []byte {
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
		// "silent" mode: drop the packet.
		return nil
	}

	// dns_nxdomain or default: NXDOMAIN response.
	// Copy the question section back and set QR+AA+NXDOMAIN.
	resp := make([]byte, len(query))
	copy(resp, query)
	// Flags: QR=1 AA=1 RCODE=3 (NXDOMAIN)
	resp[2] = 0x84 // QR=1, Opcode=0, AA=1, TC=0, RD=0
	resp[3] = 0x03 // RA=0, Z=0, RCODE=3
	// ANCOUNT, NSCOUNT, ARCOUNT = 0 (already zero from copy)
	return resp
}

func dnsErrorResponse(txID []byte, rcode byte) []byte {
	resp := make([]byte, 12)
	copy(resp[:2], txID)
	resp[2] = 0x84        // QR=1, AA=1
	resp[3] = rcode & 0xF // RCODE
	return resp
}
