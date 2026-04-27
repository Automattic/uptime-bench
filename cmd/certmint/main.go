package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/certbot"
	"github.com/Automattic/uptime-bench-certmint/internal/config"
	"github.com/Automattic/uptime-bench-certmint/internal/library"
	"github.com/Automattic/uptime-bench-certmint/internal/manifest"
	"github.com/Automattic/uptime-bench-certmint/internal/planner"
)

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
	cfg, current, err := loadConfigAndManifest(*configPath)
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
	dryRun := fs.Bool("dry-run", false, "print due certbot commands without issuing certs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, current, err := loadConfigAndManifest(*configPath)
	if err != nil {
		return err
	}
	return runOnce(ctx, cfg, current, *dryRun)
}

func runDaemon(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	configPath := fs.String("config", "", "config file path")
	dryRun := fs.Bool("dry-run", false, "log due certbot commands without issuing certs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, current, err := loadConfigAndManifest(*configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	for {
		if err := runOnce(ctx, cfg, current, *dryRun); err != nil {
			log.Printf("certmint: run failed: %v", err)
		}
		refreshed, err := manifest.Load(library.ManifestPath(cfg.LibraryDir))
		if err != nil {
			log.Printf("certmint: reload manifest: %v", err)
		} else {
			current = refreshed
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

func runOnce(ctx context.Context, cfg config.Config, current manifest.Manifest, dryRun bool) error {
	orders := planner.Due(cfg, current, time.Now())
	if len(orders) == 0 {
		log.Print("certmint: no issuance slots due")
		return nil
	}
	for _, order := range orders {
		if dryRun {
			fmt.Println(certbot.CommandLine(cfg.Certbot, order))
			continue
		}
		if !library.LiveCertExists(cfg.Certbot, order.CertName) {
			log.Printf("certmint: issuing %s profile=%s identifiers=%v", order.DomainName, order.ProfileName, order.Identifiers)
			out, err := certbot.Run(ctx, cfg.Certbot, order)
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
			return fmt.Errorf("archive %s: %w", order.CertName, err)
		}
		current.Append(entry)
		if err := manifest.Save(library.ManifestPath(cfg.LibraryDir), current); err != nil {
			return err
		}
		log.Printf("certmint: archived %s not_after=%s fingerprint=%s", entry.ID, entry.NotAfter.Format(time.RFC3339), entry.FingerprintSHA256)
	}
	return nil
}

func loadConfigAndManifest(configPath string) (config.Config, manifest.Manifest, error) {
	if configPath == "" {
		return config.Config{}, manifest.Manifest{}, fmt.Errorf("-config is required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return config.Config{}, manifest.Manifest{}, err
	}
	current, err := manifest.Load(library.ManifestPath(cfg.LibraryDir))
	if err != nil {
		return config.Config{}, manifest.Manifest{}, err
	}
	return cfg, current, nil
}
