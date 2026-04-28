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
	"github.com/Automattic/uptime-bench/internal/adapter/betteruptime"
	"github.com/Automattic/uptime-bench/internal/adapter/datadog"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv1"
	"github.com/Automattic/uptime-bench/internal/adapter/jetmonv2"
	"github.com/Automattic/uptime-bench/internal/adapter/pingdom"
	"github.com/Automattic/uptime-bench/internal/adapter/uptimerobot"
	"github.com/Automattic/uptime-bench/internal/campaign"
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
	"uptimerobot": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		key := auth["api_key"]
		if key == "" {
			return nil, fmt.Errorf("uptimerobot: auth.api_key is required")
		}
		return uptimerobot.New(id, apiURL, key), nil
	},
	"pingdom": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("pingdom: auth.token is required")
		}
		return pingdom.New(id, apiURL, token), nil
	},
	"better-uptime": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("better-uptime: auth.token is required")
		}
		return betteruptime.New(id, apiURL, token), nil
	},
	"datadog-synthetics": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		apiKey := auth["api_key"]
		appKey := auth["app_key"]
		if apiKey == "" || appKey == "" {
			return nil, fmt.Errorf("datadog-synthetics: auth.api_key and auth.app_key are both required")
		}
		return datadog.New(id, apiURL, apiKey, appKey), nil
	},
}

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
	servicesPath := flag.String("services", "services.toml", "path to services configuration file")
	scenarioPath := flag.String("scenario", "", "path to scenario TOML file to run")
	campaignPath := flag.String("campaign", "", "path to campaign TOML file to run")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	flag.Parse()

	if (*scenarioPath == "") == (*campaignPath == "") {
		log.Fatal("harness: set exactly one of -scenario or -campaign")
	}

	fl, err := fleet.Load(*fleetPath)
	if err != nil {
		log.Fatalf("harness: fleet: %v", err)
	}
	certmintLabel := "none"
	if url := fl.Certmint.LibraryURL(); url != "" {
		certmintLabel = url
	}
	log.Printf("harness: fleet loaded (%d targets, %d nameservers, certmint=%s)", len(fl.Targets), len(fl.Nameservers), certmintLabel)

	var sc *scenario.Scenario
	var c *campaign.Campaign
	var campaignData []byte
	if *scenarioPath != "" {
		scData, err := os.ReadFile(*scenarioPath)
		if err != nil {
			log.Fatalf("harness: scenario: read: %v", err)
		}
		sc, err = scenario.Parse(scData)
		if err != nil {
			log.Fatalf("harness: scenario: parse: %v", err)
		}
		log.Printf("harness: scenario loaded: %s v%s", sc.ID, sc.Version)
	} else {
		campaignData, err = os.ReadFile(*campaignPath)
		if err != nil {
			log.Fatalf("harness: campaign: read: %v", err)
		}
		c, err = campaign.Parse(campaignData)
		if err != nil {
			log.Fatalf("harness: campaign: parse: %v", err)
		}
		log.Printf("harness: campaign loaded: %s", c.ID)
	}

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

	var adapters []adapter.Adapter
	if sc != nil {
		adapters, err = adaptersForScenario(svcCfg, sc.Monitors)
		if err != nil {
			log.Fatalf("harness: %v", err)
		}
	} else {
		adapters, err = enabledAdapters(svcCfg)
		if err != nil {
			log.Fatalf("harness: %v", err)
		}
	}
	log.Printf("harness: %d adapter(s) loaded", len(adapters))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if sc != nil {
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
		return
	}

	log.Printf("harness: starting campaign: %s", c.ID)
	campaignRunID, runErr := runner.RunCampaign(ctx, c, c.Seed, fl, database, adapters, svcCfg, runner.RunCampaignOptions{
		ConfigTOML: string(campaignData),
	})
	if campaignRunID != "" {
		log.Printf("harness: campaign run recorded: %s", campaignRunID)
		log.Printf("harness: deriving metrics for campaign run %s", campaignRunID)
		if err := measurement.DeriveCampaign(ctx, database, campaignRunID); err != nil {
			log.Printf("harness: campaign metric derivation: %v", err)
		}
	}
	if runErr != nil {
		log.Printf("harness: campaign failed: %v", runErr)
		os.Exit(1)
	}
	log.Println("harness: done")
}

func adaptersForScenario(svcCfg *serviceconfig.Config, monitorIDs []string) ([]adapter.Adapter, error) {
	wantedIDs := make(map[string]bool, len(monitorIDs))
	for _, id := range monitorIDs {
		wantedIDs[id] = true
	}

	allAdapters := make(map[string]adapter.Adapter, len(monitorIDs))
	for _, svc := range svcCfg.Services {
		if !svc.Enabled || !wantedIDs[svc.ID] {
			continue
		}
		a, err := adapterForService(svc)
		if err != nil {
			return nil, err
		}
		allAdapters[svc.ID] = a
	}

	adapters := make([]adapter.Adapter, 0, len(monitorIDs))
	for _, id := range monitorIDs {
		a, ok := allAdapters[id]
		if !ok {
			return nil, fmt.Errorf("scenario monitor %q not found in services config (check id and enabled)", id)
		}
		adapters = append(adapters, a)
	}
	return adapters, nil
}

func enabledAdapters(svcCfg *serviceconfig.Config) ([]adapter.Adapter, error) {
	var adapters []adapter.Adapter
	for _, svc := range svcCfg.Services {
		if !svc.Enabled {
			continue
		}
		a, err := adapterForService(svc)
		if err != nil {
			return nil, err
		}
		adapters = append(adapters, a)
	}
	if len(adapters) == 0 {
		return nil, fmt.Errorf("no enabled services found in services config")
	}
	return adapters, nil
}

func adapterForService(svc serviceconfig.Service) (adapter.Adapter, error) {
	factory, ok := registry[svc.Type]
	if !ok {
		return nil, fmt.Errorf("service %q: unknown type %q", svc.ID, svc.Type)
	}
	a, err := factory(svc.ID, svc.URL, svc.Auth)
	if err != nil {
		return nil, fmt.Errorf("service %q: %w", svc.ID, err)
	}
	return a, nil
}
