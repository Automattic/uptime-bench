package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Automattic/uptime-bench/internal/jetmoncapacity"
)

func main() {
	configPath := flag.String("config", "configs/capacity/jetmon.example.toml", "Jetmon capacity TOML config")
	mode := flag.String("mode", "plan", "mode: plan, seed, activate, deactivate, verify, run-batch, or run-suite")
	servicesFlag := flag.String("services", "all", "comma-separated services: all, jetmon-v1, jetmon-v2")
	activeCount := flag.Int("active-count", 0, "active monitor count for activate or run-batch; defaults to the first configured batch size")
	durationOverride := flag.Duration("duration", 0, "override batches.duration for run-batch")
	outDir := flag.String("out-dir", "", "artifact directory; default reports/capacity/<id>-<timestamp>Z")
	apply := flag.Bool("apply", false, "apply SQL to live DBs; without this flag the command only writes artifacts")
	forceReseed := flag.Bool("force-reseed", false, "allow seed to delete and recreate existing benchmark-owned generated rows")
	promURL := flag.String("prometheus-url", "", "Prometheus base URL override")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	manifest, err := jetmoncapacity.Runner{}.Run(ctx, jetmoncapacity.RunOptions{
		ConfigPath:       *configPath,
		Mode:             strings.ToLower(strings.TrimSpace(*mode)),
		Services:         splitCSV(*servicesFlag),
		ActiveCount:      *activeCount,
		DurationOverride: *durationOverride,
		OutDir:           *outDir,
		Apply:            *apply,
		ForceReseed:      *forceReseed,
		PrometheusURL:    *promURL,
	})
	if err != nil {
		log.Fatalf("capacity-run: %v", err)
	}
	fmt.Printf("capacity-run: wrote artifacts to %s\n", manifest.OutDir)
	if !*apply {
		fmt.Println("capacity-run: dry-run complete; rerun with -apply during the live window")
	}
	if manifest.StopRecommended {
		fmt.Printf("capacity-run: stop recommended: %s\n", manifest.StopReason)
	}
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
