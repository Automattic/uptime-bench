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
	"hash/fnv"
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
	listenAddress := flag.String("listen-address", "", "address to bind DNS traffic to; default all interfaces")
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

	zones := &dnsserver.Zones{
		Records: make(dnsserver.ZoneMap),
		Apex:    map[string]dnsserver.ZoneApex{},
	}

	if *fleetFile != "" {
		fleetBytes, err := os.ReadFile(*fleetFile)
		if err != nil {
			log.Fatalf("dns: fleet read: %v", err)
		}
		fl, err := fleet.Parse(fleetBytes)
		if err != nil {
			log.Fatalf("dns: fleet: %v", err)
		}
		// SOA SERIAL is a hash of the fleet.toml bytes so all members
		// reading the same config publish the same value, and the
		// serial bumps automatically when the operator edits the file.
		// fnv32a on file content: collision probability is negligible
		// for the handful of edits a fleet sees over its lifetime,
		// and there's no persistent state to coordinate.
		h := fnv.New32a()
		h.Write(fleetBytes)
		serial := h.Sum32()
		fleetZones, err := dnsserver.BuildFromFleet(fl, *memberID, serial)
		if err != nil {
			log.Fatalf("dns: fleet zones: %v", err)
		}
		for name, e := range fleetZones.Records {
			zones.Records[name] = e
		}
		for apex, za := range fleetZones.Apex {
			zones.Apex[apex] = za
		}
		zones.Generated = append(zones.Generated, fleetZones.Generated...)
		log.Printf("dns: %d zone(s), %d generated range(s) loaded from fleet config", len(fleetZones.Records), len(fleetZones.Generated))
	}

	// -zone flags override fleet-derived zones.
	if err := dnsserver.MergeFlagZones(zones.Records, rawZones); err != nil {
		log.Fatalf("dns: zone setup: %v", err)
	}

	for name, e := range zones.Records {
		log.Printf("dns: zone %s → %s (ttl %ds)", name, e.IP, e.TTL)
	}
	for apex, za := range zones.Apex {
		log.Printf("dns: zone apex %s → SOA mname=%s ns=%v", apex, za.SOA.MName, za.NSHostnames)
	}
	for _, r := range zones.Generated {
		log.Printf("dns: generated range %s → %s", r.ID, r.IP)
	}

	registry := control.NewRegistry()
	txtStore := dnsserver.NewTXTStore()

	controlSrv := control.NewServer(*memberID, token, registry)
	mux := http.NewServeMux()
	controlSrv.RegisterRoutes(mux)
	dnsserver.RegisterACMEHandlers(mux, txtStore)
	controlHTTP := &http.Server{
		Addr:    fmt.Sprintf(":%d", *controlPort),
		Handler: control.AuthMiddleware(token)(mux),
	}

	go func() {
		log.Printf("dns: control API on :%d", *controlPort)
		if err := controlHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("dns: control server: %v", err)
		}
	}()

	dnsAddr := fmt.Sprintf(":%d", *dnsPort)
	if strings.TrimSpace(*listenAddress) != "" {
		dnsAddr = net.JoinHostPort(strings.TrimSpace(*listenAddress), fmt.Sprintf("%d", *dnsPort))
	}

	udpConn, err := net.ListenPacket("udp", dnsAddr)
	if err != nil {
		log.Fatalf("dns: listen udp %s: %v", dnsAddr, err)
	}
	defer udpConn.Close()
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
