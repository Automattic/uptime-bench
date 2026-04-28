package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench/internal/certmint/certbot"
	"github.com/Automattic/uptime-bench/internal/certmint/config"
	"github.com/Automattic/uptime-bench/internal/certmint/library"
	"github.com/Automattic/uptime-bench/internal/certmint/lockfile"
	"github.com/Automattic/uptime-bench/internal/certmint/manifest"
	"github.com/Automattic/uptime-bench/internal/certmint/planner"
	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/tokenfile"
)

// loadFleetEnv reads fleet.toml at path and returns the env-var
// settings certbot's manual hooks need. UPTIME_BENCH_DNS_CONTROL_URLS
// is derived from [[nameservers]] so the operator maintains DNS
// topology in one place — fleet.toml on the harness — instead of
// duplicating it into every box's env file.
//
// Returns (nil, nil) when path is empty: callers opted out of
// fleet-derived env. Returns an error if path is non-empty but the
// file is unreadable or has no nameservers — running certmint with a
// broken fleet pointer is a configuration mistake worth surfacing
// loudly rather than silently falling back to env-only.
func loadFleetEnv(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	fl, err := fleet.Load(path)
	if err != nil {
		return nil, fmt.Errorf("certmint: load fleet %s: %w", path, err)
	}
	if len(fl.Nameservers) == 0 {
		return nil, fmt.Errorf("certmint: fleet %s has no [[nameservers]] entries", path)
	}
	urls := make([]string, 0, len(fl.Nameservers))
	for _, ns := range fl.Nameservers {
		port := ns.ControlPort
		if port == 0 {
			port = 9100
		}
		urls = append(urls, fmt.Sprintf("http://%s:%d", ns.Address, port))
	}
	return []string{"UPTIME_BENCH_DNS_CONTROL_URLS=" + strings.Join(urls, " ")}, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if err := run(context.Background(), os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "plan":
		return runPlan(args[1:])
	case "once":
		return runOnceCommand(ctx, args[1:])
	case "daemon":
		return runDaemon(ctx, args[1:])
	case "inspect":
		return runInspect(args[1:])
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("usage: uptime-bench-certmint <plan|once|daemon|inspect> -config PATH")
}

func runPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	current, err := manifest.Load(library.ManifestPathForConfig(cfg))
	if err != nil {
		return err
	}
	orders := planner.Due(cfg, current, time.Now())
	type planned struct {
		planner.Order
		Command string `json:"command"`
	}
	out := make([]planned, 0, len(orders))
	for _, order := range orders {
		out = append(out, planned{Order: order, Command: certbot.CommandLine(cfg.Certbot, order)})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func runOnceCommand(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("once", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path")
	fleetPath := fs.String("fleet", "", "path to fleet.toml — DNS control URLs for ACME hooks are derived from [[nameservers]]. Production systemd unit passes /etc/uptime-bench/fleet.toml; leave empty for ad-hoc local runs")
	dryRun := fs.Bool("dry-run", false, "print due certbot commands without issuing certs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	fleetEnv, err := loadFleetEnv(*fleetPath)
	if err != nil {
		return err
	}
	lock, err := acquireLock(cfg)
	if err != nil {
		return err
	}
	defer lock.Release()
	current, err := manifest.Load(library.ManifestPathForConfig(cfg))
	if err != nil {
		return err
	}
	return runOnce(ctx, cfg, &current, fleetEnv, *dryRun)
}

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path")
	fleetPath := fs.String("fleet", "", "path to fleet.toml — DNS control URLs for ACME hooks are derived from [[nameservers]]. Production systemd unit passes /etc/uptime-bench/fleet.toml; leave empty for ad-hoc local runs")
	libraryPort := fs.Int("library-port", 9200, "port for the read-only cert-library HTTP API targets poll. 0 disables the server (issuance still runs)")
	tokenFile := fs.String("token-file", "", "path to control token file (default: CONTROL_TOKEN env var)")
	dryRun := fs.Bool("dry-run", false, "log due certbot commands without issuing certs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	fleetEnv, err := loadFleetEnv(*fleetPath)
	if err != nil {
		return err
	}
	lock, err := acquireLock(cfg)
	if err != nil {
		return err
	}
	defer lock.Release()
	current, err := manifest.Load(library.ManifestPathForConfig(cfg))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Optionally start the cert-library HTTP API. Targets poll
	// /library/manifest.json and /library/{entry_id}/{file} from
	// here; the port is restricted to a known target list at the
	// firewall (see deploy/provision-server.sh) and the bearer-token
	// middleware gates every request.
	var librarySrv *http.Server
	if *libraryPort > 0 {
		token, err := tokenfile.Read(*tokenFile)
		if err != nil {
			return fmt.Errorf("certmint: cert-library server token: %w", err)
		}
		mux := http.NewServeMux()
		library.RegisterServerHandlers(mux, library.ManifestPathForConfig(cfg))
		librarySrv = &http.Server{
			Addr:    fmt.Sprintf(":%d", *libraryPort),
			Handler: control.AuthMiddleware(token)(mux),
		}
		ln, err := net.Listen("tcp", librarySrv.Addr)
		if err != nil {
			return fmt.Errorf("certmint: cert-library listen %s: %w", librarySrv.Addr, err)
		}
		go func() {
			log.Printf("certmint: cert-library API on :%d", *libraryPort)
			if err := librarySrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("certmint: cert-library server: %v", err)
			}
		}()
	}

	defer func() {
		if librarySrv != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := librarySrv.Shutdown(shutdownCtx); err != nil {
				log.Printf("certmint: cert-library shutdown: %v", err)
			}
		}
	}()

	for {
		// Reload fleet on each iteration so a fleet.toml edit
		// (e.g. adding a DNS member) propagates without a daemon
		// restart. Failure to reload is logged but doesn't stop
		// issuance — we keep using the last-good env.
		if reloaded, err := loadFleetEnv(*fleetPath); err != nil {
			log.Printf("certmint: fleet reload failed, keeping last env: %v", err)
		} else {
			fleetEnv = reloaded
		}
		if err := runOnce(ctx, cfg, &current, fleetEnv, *dryRun); err != nil {
			log.Printf("certmint: run failed: %v", err)
		}

		timer := time.NewTimer(cfg.PollInterval.Duration)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func runInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	libraryDir := fs.String("library", "/var/lib/uptime-bench/certs", "certificate library directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	current, err := manifest.Load(library.ManifestPath(*libraryDir))
	if err != nil {
		return err
	}
	sort.Slice(current.Entries, func(i, j int) bool {
		return current.Entries[i].NotAfter.Before(current.Entries[j].NotAfter)
	})
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(current)
}

func runOnce(ctx context.Context, cfg config.Config, current *manifest.Manifest, fleetEnv []string, dryRun bool) error {
	orders := planner.Due(cfg, *current, time.Now())
	if len(orders) == 0 {
		return nil
	}
	var errs []error
	// Track when the last order for each domain finished so we can
	// honor cfg.InterOrderQuiet between same-domain orders. Wildcard
	// identifiers in two consecutive orders share a single
	// _acme-challenge.<domain> TXT name; without a quiet period
	// Let's Encrypt's recursive resolver can answer the new order
	// from cached old TXT values, validation fails, and the order
	// rolls back the just-deleted TXT. The quiet period is bounded
	// to same-domain orders so cross-domain throughput is unaffected.
	lastDone := make(map[string]time.Time, len(cfg.Domains))
	for _, order := range orders {
		if dryRun {
			fmt.Println(certbot.CommandLine(cfg.Certbot, order))
			continue
		}
		if err := waitForQuietPeriod(ctx, cfg.InterOrderQuiet.Duration, lastDone[order.DomainName], order); err != nil {
			return err
		}
		if err := issueAndArchive(ctx, cfg, current, order, fleetEnv); err != nil {
			log.Printf("certmint: order %s failed: %v", order.CertName, err)
			errs = append(errs, fmt.Errorf("%s: %w", order.CertName, err))
			lastDone[order.DomainName] = time.Now()
			continue
		}
		lastDone[order.DomainName] = time.Now()
	}
	return errors.Join(errs...)
}

// waitForQuietPeriod sleeps until quiet has elapsed since lastDone for
// the given order's domain, or returns immediately if no prior order
// for the domain has been recorded. Honors ctx cancellation.
func waitForQuietPeriod(ctx context.Context, quiet time.Duration, lastDone time.Time, order planner.Order) error {
	if quiet <= 0 || lastDone.IsZero() {
		return nil
	}
	wakeAt := lastDone.Add(quiet)
	delay := time.Until(wakeAt)
	if delay <= 0 {
		return nil
	}
	log.Printf("certmint: waiting %v before %s (inter-order quiet for %s)", delay.Round(time.Second), order.CertName, order.DomainName)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func issueAndArchive(ctx context.Context, cfg config.Config, current *manifest.Manifest, order planner.Order, extraEnv []string) error {
	if !library.LiveCertExists(cfg.Certbot, order.CertName) {
		log.Printf("certmint: issuing %s profile=%s identifiers=%v", order.DomainName, order.ProfileName, order.Identifiers)
		issueCtx, cancel := context.WithTimeout(ctx, cfg.Certbot.IssuanceTimeout.Duration)
		out, err := certbot.Run(issueCtx, cfg.Certbot, order, extraEnv)
		cancel()
		if out != "" {
			log.Print(out)
		}
		if err != nil {
			return err
		}
	} else {
		log.Printf("certmint: archiving existing certbot lineage %s", order.CertName)
	}

	entry, err := library.Archive(cfg, order, time.Now())
	if err != nil {
		return fmt.Errorf("archive: %w", err)
	}
	current.Append(entry)
	if err := manifest.Save(library.ManifestPathForConfig(cfg), *current); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	log.Printf("certmint: archived %s not_after=%s fingerprint=%s", entry.ID, entry.NotAfter.Format(time.RFC3339), entry.FingerprintSHA256)
	return nil
}

func acquireLock(cfg config.Config) (*lockfile.Lock, error) {
	lock, err := lockfile.Acquire(cfg.LockPath)
	if err != nil {
		return nil, err
	}
	log.Printf("certmint: acquired lock %s", lock.Path)
	return lock, nil
}

func loadConfig(configPath string) (config.Config, error) {
	if configPath == "" {
		return config.Config{}, fmt.Errorf("-config is required")
	}
	return config.Load(configPath)
}
