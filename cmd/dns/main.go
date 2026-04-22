package main

import (
	"flag"
	"log"
)

func main() {
	dnsPort := flag.Int("dns-port", 53, "port for DNS traffic (UDP and TCP)")
	controlPort := flag.Int("control-port", 9100, "port for harness control API")
	flag.Parse()

	log.Printf("dns: starting (dns :%d, control :%d)", *dnsPort, *controlPort)
	// TODO: start authoritative DNS server and control API
}
