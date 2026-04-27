// Command uptime-bench-dns is the authoritative DNS fleet member.
//
// It loads zone records from fleet.toml (and optional -zone flags), listens
// on UDP+TCP for DNS queries, and serves answers shaped by failure-injection
// rules registered via the control plane.
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
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/dnsserver"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

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

	zones := make(dnsserver.ZoneMap)

	if *fleetFile != "" {
		fl, err := fleet.Load(*fleetFile)
		if err != nil {
			log.Fatalf("dns: fleet: %v", err)
		}
		fleetZones, err := dnsserver.BuildFromFleet(fl, *memberID)
		if err != nil {
			log.Fatalf("dns: fleet zones: %v", err)
		}
		for name, e := range fleetZones {
			zones[name] = e
		}
		log.Printf("dns: %d zone(s) loaded from fleet config", len(fleetZones))
	}

	// -zone flags override fleet-derived zones.
	if err := dnsserver.MergeFlagZones(zones, rawZones); err != nil {
		log.Fatalf("dns: zone setup: %v", err)
	}

	for name, e := range zones {
		log.Printf("dns: zone %s → %s (ttl %ds)", name, e.IP, e.TTL)
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

	udpConn, err := net.ListenPacket("udp", dnsAddr)
	if err != nil {
		log.Fatalf("dns: listen udp %s: %v", dnsAddr, err)
	}
	defer udpConn.Close()
	txtStore := dnsserver.NewTXTStore()

	go dnsserver.ServeUDP(udpConn, registry, zones, txtStore)

	tcpLn, err := net.Listen("tcp", dnsAddr)
	if err != nil {
		log.Fatalf("dns: listen tcp %s: %v", dnsAddr, err)
	}
	defer tcpLn.Close()
	go dnsserver.ServeTCP(tcpLn, registry, zones, txtStore)

	log.Printf("dns: authoritative DNS on %s (UDP+TCP)", dnsAddr)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("dns: shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := controlHTTP.Shutdown(ctx); err != nil {
		log.Printf("dns: control shutdown: %v", err)
	}
}
