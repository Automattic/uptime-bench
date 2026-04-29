package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

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
	"github.com/Automattic/uptime-bench/internal/targetserver"
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
		if apiURL == "" {
			return nil, fmt.Errorf("url is required (jetmon-v2 needs the Jetmon 2 /api/v1 endpoint)")
		}
		token := auth["token"]
		if token == "" {
			return nil, fmt.Errorf("jetmon-v2: auth.token is required")
		}
		var opts []jetmonv2.Option
		if raw := auth["bucket_no"]; raw != "" {
			bucketNo, err := strconv.Atoi(raw)
			if err != nil {
				return nil, fmt.Errorf("jetmon-v2: auth.bucket_no must be an integer: %w", err)
			}
			if bucketNo < 0 {
				return nil, fmt.Errorf("jetmon-v2: auth.bucket_no must be non-negative")
			}
			opts = append(opts, jetmonv2.WithBucketNo(bucketNo))
		}
		return jetmonv2.New(id, apiURL, token, opts...), nil
	},
	"uptimerobot": func(id, apiURL string, auth map[string]string) (adapter.Adapter, error) {
		key := auth["api_key"]
		if key == "" {
			return nil, fmt.Errorf("uptimerobot: auth.api_key is required")
		}
		var opts []uptimerobot.Option
		if method := strings.TrimSpace(auth["http_method"]); method != "" {
			if _, err := uptimerobot.HTTPMethodCode(method); err != nil {
				return nil, err
			}
			opts = append(opts, uptimerobot.WithHTTPMethod(method))
		}
		if raw := strings.TrimSpace(auth["min_check_frequency"]); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("uptimerobot: auth.min_check_frequency must be a duration: %w", err)
			}
			opts = append(opts, uptimerobot.WithMinCheckFrequency(d))
		}
		return uptimerobot.New(id, apiURL, key, opts...), nil
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
	monitorOverride := flag.String("monitors", "", "comma-separated monitor IDs to use for a scenario run (overrides scenario monitors)")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	flag.Parse()

	if (*scenarioPath == "") == (*campaignPath == "") {
		log.Fatal("harness: set exactly one of -scenario or -campaign")
	}
	if *campaignPath != "" && strings.TrimSpace(*monitorOverride) != "" {
		log.Fatal("harness: -monitors is only valid with -scenario")
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

	if err := pushCertLibraryConfig(fl); err != nil {
		// Logged but not fatal: targets may have a working
		// configuration from a previous push, and a benchmark run
		// that doesn't exercise TLS scenarios doesn't need
		// cert-library state to be current. Operators see the line
		// in the harness log and can react.
		log.Printf("harness: cert-library config push: %v", err)
	}

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
		if strings.TrimSpace(*monitorOverride) != "" {
			monitors, err := parseMonitorOverride(*monitorOverride)
			if err != nil {
				log.Fatalf("harness: monitors: %v", err)
			}
			sc.Monitors = monitors
			log.Printf("harness: scenario monitors overridden: %s", strings.Join(monitors, ","))
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

func parseMonitorOverride(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	monitors := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			return nil, fmt.Errorf("empty monitor id in %q", raw)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate monitor id %q", id)
		}
		seen[id] = struct{}{}
		monitors = append(monitors, id)
	}
	return monitors, nil
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

// pushCertLibraryConfig forwards the certmint cert-library URL from
// fleet.toml's [certmint] section to every distinct target control
// address in the fleet. Same-address [[targets]] entries (the
// virtual-host pattern, where bench-a / bench-b / probe-a all share
// one server at one control port) collapse to a single push because the
// underlying CertLibraryController dedupes anyway and we want clean
// per-host log lines.
//
// No-op when fleet.toml has no [certmint] section configured. Errors
// from individual targets are aggregated; the harness keeps running
// regardless because the cert-library controller on each target keeps
// its previous config when a push fails.
func pushCertLibraryConfig(fl *fleet.Config) error {
	url := fl.Certmint.LibraryURL()
	if url == "" {
		return nil
	}
	token, err := readControlToken(fl)
	if err != nil {
		return fmt.Errorf("read control token: %w", err)
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}

	seen := make(map[string]struct{}, len(fl.Targets))
	var errs []error
	for _, t := range fl.Targets {
		addr := fmt.Sprintf("http://%s:%d", t.Address, t.ControlPort)
		if _, dup := seen[addr]; dup {
			continue
		}
		seen[addr] = struct{}{}

		client := targetserver.NewCertLibraryClient(addr, token, httpClient)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := client.Configure(ctx, targetserver.CertLibraryConfigRequest{URL: url})
		cancel()
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
			continue
		}
		log.Printf("harness: cert-library config pushed to %s (source=%s)", addr, url)
	}
	return errors.Join(errs...)
}

// readControlToken reads the shared bearer token at
// fl.Control.AuthTokenFile, mirroring runner.readFleetToken without
// taking a dependency on the runner package's internals.
func readControlToken(fl *fleet.Config) (string, error) {
	if fl.Control.AuthTokenFile == "" {
		return "", fmt.Errorf("control.auth_token_file is required")
	}
	data, err := os.ReadFile(fl.Control.AuthTokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
