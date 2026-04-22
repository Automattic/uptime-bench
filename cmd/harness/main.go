package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmon"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/measurement"
	"github.com/Automattic/uptime-bench/internal/runner"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
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

	// Registry of all available adapters.
	allAdapters := map[string]adapter.Adapter{
		"jetmon": jetmon.New("", ""),
	}
	var adapters []adapter.Adapter
	for _, monitorID := range sc.Monitors {
		a, ok := allAdapters[monitorID]
		if !ok {
			log.Fatalf("harness: unknown monitor %q (no adapter registered)", monitorID)
		}
		adapters = append(adapters, a)
	}
	log.Printf("harness: %d adapter(s) loaded", len(adapters))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Printf("harness: starting scenario: %s", sc.ID)
	runID, runErr := runner.Run(ctx, sc, fl, database, adapters)

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
