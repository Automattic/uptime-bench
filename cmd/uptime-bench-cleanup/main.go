package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/activeguard"
	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/adapterfactory"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/providerstate"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
	servicesPath := flag.String("services", "services.toml", "path to services configuration file")
	dryRun := flag.Bool("dry-run", true, "list stale benchmark-owned provider resources without deleting them")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall cleanup timeout")
	activeRunLock := flag.String("active-run-lock", "", "path to active-run lock file (default UPTIME_BENCH_ACTIVE_RUN_LOCK or /tmp/uptime-bench-active-run.lock)")
	allowActiveRun := flag.Bool("allow-active-run", false, "allow delete cleanup even when an active-run lock exists")
	flag.Parse()

	if !*dryRun {
		if err := activeguard.EnsureInactive(*activeRunLock, *allowActiveRun); err != nil {
			log.Fatalf("cleanup: refusing mutating cleanup while another run appears active: %v (rerun with -allow-active-run only for emergency cleanup)", err)
		}
	}

	fl, err := fleet.Load(*fleetPath)
	if err != nil {
		log.Fatalf("cleanup: fleet: %v", err)
	}
	svcCfg, err := serviceconfig.Load(*servicesPath)
	if err != nil {
		log.Fatalf("cleanup: services: %v", err)
	}
	adapters, err := adapterfactory.Enabled(svcCfg)
	if err != nil {
		log.Fatalf("cleanup: adapters: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	scope := providerstate.ScopeFromFleet(fl)
	summaries := providerstate.Run(ctx, adapters, adapter.CleanupOptions{
		DryRun: *dryRun,
		Scope:  scope,
	})

	mode := "dry-run"
	if !*dryRun {
		mode = "delete"
	}
	fmt.Printf("provider-state cleanup (%s): services=%d target_hosts=%d target_urls=%d\n", mode, len(summaries), len(scope.TargetHosts), len(scope.TargetURLs))

	exitCode := 0
	for _, summary := range summaries {
		fmt.Println(providerstate.FormatSummary(summary))
		for _, action := range summary.Actions {
			fmt.Printf("  - %s %s %s %q %s%s\n",
				action.Action,
				action.Candidate.Kind,
				action.Candidate.ResourceID,
				action.Candidate.Name,
				action.Candidate.URL,
				actionSuffix(action),
			)
		}
		if summary.Errors > 0 {
			exitCode = 1
		}
	}
	os.Exit(exitCode)
}

func actionSuffix(action adapter.CleanupAction) string {
	var parts []string
	if action.Candidate.Reason != "" {
		parts = append(parts, "reason="+action.Candidate.Reason)
	}
	if action.Candidate.Ambiguous {
		parts = append(parts, "ambiguous=true")
	}
	if action.Error != "" {
		parts = append(parts, "error="+action.Error)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}
