package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/Automattic/uptime-bench/internal/activeguard"
	"github.com/Automattic/uptime-bench/internal/jetmoncapacity"
)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	configPath := flag.String("config", "configs/capacity/jetmon.example.toml", "Jetmon capacity TOML config")
	mode := flag.String("mode", "plan", "mode: plan, seed, activate, deactivate, verify, run-batch, or run-suite")
	servicesFlag := flag.String("services", "all", "comma-separated services: all, jetmon-v1, jetmon-v2")
	activeCount := flag.Int("active-count", 0, "active monitor count for activate or run-batch; defaults to the first configured batch size")
	durationOverride := flag.Duration("duration", 0, "override batches.duration for run-batch or run-suite")
	cooldownOverride := flag.Duration("cooldown", 0, "override batches.cooldown for run-suite")
	batchSizes := flag.String("batch-sizes", "", "comma-separated batch sizes for plan or run-suite; default uses config")
	suiteStartCount := flag.Int("suite-start-count", 0, "for run-suite, start at the first configured/overridden batch >= this count")
	fullSuite := flag.Bool("full-suite", false, "for run-suite, ignore prior suite state and start from the first batch")
	suiteStatePath := flag.String("suite-state-path", "", "path to persisted run-suite state; default is beside the run output directory")
	outDir := flag.String("out-dir", "", "artifact directory; default reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION>")
	description := flag.String("description", "", "short description slug for the default report directory")
	apply := flag.Bool("apply", false, "apply SQL to live DBs; without this flag the command only writes artifacts")
	forceReseed := flag.Bool("force-reseed", false, "allow seed to delete and recreate existing benchmark-owned generated rows")
	promURL := flag.String("prometheus-url", "", "Prometheus base URL override")
	activeRunLock := flag.String("active-run-lock", "", "path to active-run lock file for -apply (default UPTIME_BENCH_ACTIVE_RUN_LOCK or /tmp/uptime-bench-active-run.lock)")
	allowActiveRun := flag.Bool("allow-active-run", false, "allow -apply even when an active-run lock exists")
	flag.Parse()

	parsedBatchSizes, err := parseBatchSizes(*batchSizes)
	if err != nil {
		log.Printf("capacity-run: -batch-sizes: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var lock *activeguard.Lock
	if *apply {
		lock, err = activeguard.Acquire(*activeRunLock, "uptime-bench-jetmon-capacity-run "+strings.ToLower(strings.TrimSpace(*mode)), *allowActiveRun)
		if err != nil {
			log.Printf("capacity-run: refusing -apply while another run appears active: %v (rerun with -allow-active-run only if this is intentional)", err)
			return 1
		}
		defer func() {
			if err := lock.Release(); err != nil {
				log.Printf("capacity-run: release active-run lock: %v", err)
			}
		}()
	}

	manifest, err := jetmoncapacity.Runner{}.Run(ctx, jetmoncapacity.RunOptions{
		ConfigPath:       *configPath,
		Mode:             strings.ToLower(strings.TrimSpace(*mode)),
		Services:         splitCSV(*servicesFlag),
		ActiveCount:      *activeCount,
		DurationOverride: *durationOverride,
		CooldownOverride: *cooldownOverride,
		BatchSizes:       parsedBatchSizes,
		SuiteStartCount:  *suiteStartCount,
		FullSuite:        *fullSuite,
		SuiteStatePath:   *suiteStatePath,
		OutDir:           *outDir,
		Description:      *description,
		Apply:            *apply,
		ForceReseed:      *forceReseed,
		PrometheusURL:    *promURL,
	})
	if err != nil {
		log.Printf("capacity-run: %v", err)
		return 1
	}
	fmt.Printf("capacity-run: wrote artifacts to %s\n", manifest.OutDir)
	if !*apply {
		fmt.Println("capacity-run: dry-run complete; rerun with -apply during the live window")
	}
	if manifest.StopRecommended {
		fmt.Printf("capacity-run: stop recommended: %s\n", manifest.StopReason)
	}
	return 0
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

func parseBatchSizes(raw string) ([]int, error) {
	var out []int
	for _, part := range splitCSV(raw) {
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", part)
		}
		if value <= 0 {
			return nil, fmt.Errorf("%d must be positive", value)
		}
		out = append(out, value)
	}
	return out, nil
}
