package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv1"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv2"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/measurement"
	"github.com/Automattic/uptime-bench/internal/runner"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

// adapterFactory builds an adapter from a service config entry.
type adapterFactory func(id, url string, auth map[string]string) (adapter.Adapter, error)

// registry maps service type names to their factory functions.
// To add a new service: implement its adapter package and add an entry here.
var registry = map[string]adapterFactory{
	"jetmon-v1": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		if apiURL == "" {
			return nil, fmt.Errorf("url is required (jetmon-v1 has no public API endpoint; point at jetmon-bridge)")
		}
		writeMode := auth["write_mode"] == "true"
		return jetmonv1.New(id, apiURL, auth["token"], writeMode), nil
	},
	"jetmon-v2": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		// Stub: Jetmon 2 has no public API yet. See internal/adapter/jetmonv2.
		return nil, jetmonv2.ErrNotImplemented
	},
}

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
	servicesPath := flag.String("services", "services.toml", "path to services configuration file")
	scenarioPath := flag.String("scenario", "", "path to scenario TOML file to run")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	flag.Parse()

	if *scenarioPath == "" {
		log.Fatal("harness: -scenario is required")
	}

	fl, err := fleet.Load(*fleetPath)
	if err != nil {
		log.Fatalf("harness: fleet: %v", err)
	}
	log.Printf("harness: fleet loaded (%d targets, %d nameservers)", len(fl.Targets), len(fl.Nameservers))

	scData, err := os.ReadFile(*scenarioPath)
	if err != nil {
		log.Fatalf("harness: scenario: read: %v", err)
	}
	sc, err := scenario.Parse(scData)
	if err != nil {
		log.Fatalf("harness: scenario: parse: %v", err)
	}
	log.Printf("harness: scenario loaded: %s v%s", sc.ID, sc.Version)

	svcCfg, err := serviceconfig.Load(*servicesPath)
	if err != nil {
		log.Fatalf("harness: services: %v", err)
	}

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		log.Fatal("harness: set -dsn flag or DB_DSN env var")
	}
	database, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("harness: db: %v", err)
	}
	defer database.Close()
	log.Println("harness: database connected")

	// Build the set of adapter IDs the scenario needs.
	wantedIDs := make(map[string]bool, len(sc.Monitors))
	for _, id := range sc.Monitors {
		wantedIDs[id] = true
	}

	// Instantiate enabled services that the scenario references.
	allAdapters := make(map[string]adapter.Adapter, len(sc.Monitors))
	for _, svc := range svcCfg.Services {
		if !svc.Enabled || !wantedIDs[svc.ID] {
			continue
		}
		factory, ok := registry[svc.Type]
		if !ok {
			log.Fatalf("harness: service %q: unknown type %q", svc.ID, svc.Type)
		}
		a, err := factory(svc.ID, svc.URL, svc.Auth)
		if err != nil {
			log.Fatalf("harness: service %q: %v", svc.ID, err)
		}
		allAdapters[svc.ID] = a
	}

	var adapters []adapter.Adapter
	for _, id := range sc.Monitors {
		a, ok := allAdapters[id]
		if !ok {
			log.Fatalf("harness: scenario monitor %q not found in services config (check id and enabled)", id)
		}
		adapters = append(adapters, a)
	}
	log.Printf("harness: %d adapter(s) loaded", len(adapters))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("harness: starting scenario: %s", sc.ID)
	runID, runErr := runner.Run(ctx, sc, fl, database, adapters, svcCfg)

	if runID != "" {
		// Derive metrics in a separate pass — never in the same transaction as events.
		log.Printf("harness: deriving metrics for run %s", runID)
		if err := measurement.Derive(ctx, database, runID); err != nil {
			log.Printf("harness: metric derivation: %v", err)
		}
	}

	if runErr != nil {
		log.Printf("harness: run failed: %v", runErr)
		os.Exit(1)
	}
	log.Println("harness: done")
}
