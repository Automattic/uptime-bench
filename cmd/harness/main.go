package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
	flag.Parse()

	if _, err := os.Stat(*fleetPath); err != nil {
		log.Fatalf("harness: fleet config not found: %s\n  Copy fleet.example.toml to fleet.toml and edit for your environment.", *fleetPath)
	}

	log.Printf("harness: starting (fleet: %s)", *fleetPath)
	// TODO: load fleet config, connect to DB, run scenarios
}
