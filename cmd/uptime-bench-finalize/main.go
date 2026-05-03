package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/measurement"
	"github.com/Automattic/uptime-bench/internal/report"
)

const defaultCapacityInstances = "jetmon-v1.example.com,jetmon-v2.example.com"

func main() {
	campaign := flag.String("campaign", "", "campaign run ID or campaign config ID to finalize")
	outDir := flag.String("out-dir", "", "output directory (default reports/<campaign>)")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	derive := flag.Bool("derive", true, "recompute derived metrics before writing reports")
	capacity := flag.Bool("capacity", false, "collect Prometheus capacity metrics for the finalized campaign window and write capacity.md/capacity.json")
	capacityPromURL := flag.String("capacity-prometheus-url", "", "Prometheus base URL for -capacity (overrides CAPACITY_PROMETHEUS_URL/PROMETHEUS_URL env vars)")
	capacityInstances := flag.String("capacity-instances", "", "comma-separated Prometheus instance labels for -capacity (default CAPACITY_INSTANCES env var or Jetmon v1/v2 examples)")
	capacityStep := flag.Duration("capacity-step", 15*time.Second, "Prometheus query_range step for -capacity")
	capacityRateWindow := flag.Duration("capacity-rate-window", 2*time.Minute, "PromQL rate() window for counter-based capacity metrics")
	flag.Parse()

	if *campaign == "" {
		log.Fatal("finalize: -campaign is required")
	}
	if *outDir == "" {
		*outDir = filepath.Join("reports", sanitizePathComponent(*campaign))
	}

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		log.Fatal("finalize: set -dsn flag or DB_DSN env var")
	}

	database, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("finalize: db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()
	lookup, err := database.ResolveCampaign(ctx, *campaign)
	if err != nil {
		log.Fatalf("finalize: resolve campaign: %v", err)
	}
	if len(lookup.Runs) == 0 {
		log.Fatalf("finalize: no campaign_runs matched %q", *campaign)
	}
	if *derive {
		for _, run := range lookup.Runs {
			if err := measurement.DeriveCampaign(ctx, database, run.ID); err != nil {
				log.Printf("finalize: derive %s: %v", run.ID, err)
			}
		}
	}

	out, err := loadReport(ctx, database, lookup)
	if err != nil {
		log.Fatalf("finalize: report: %v", err)
	}

	var capacityReport *capacitybench.Report
	if *capacity {
		report, err := collectCapacityReport(ctx, out.Meta, capacityOptions{
			prometheusURL: *capacityPromURL,
			instancesRaw:  *capacityInstances,
			step:          *capacityStep,
			rateWindow:    *capacityRateWindow,
		})
		if err != nil {
			log.Fatalf("finalize: capacity: %v", err)
		}
		capacityReport = &report
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("finalize: mkdir %s: %v", *outDir, err)
	}
	if err := writeReportFiles(*outDir, out, capacityReport); err != nil {
		log.Fatalf("finalize: write: %v", err)
	}
	log.Printf("finalize: wrote %s", *outDir)
}

type capacityOptions struct {
	prometheusURL string
	instancesRaw  string
	step          time.Duration
	rateWindow    time.Duration
}

func loadReport(ctx context.Context, database *db.DB, lookup *db.CampaignLookup) (report.Report, error) {
	runIDs := make([]string, len(lookup.Runs))
	for i, r := range lookup.Runs {
		runIDs[i] = r.ID
	}
	rows, err := database.CampaignMetricRows(ctx, runIDs)
	if err != nil {
		return report.Report{}, fmt.Errorf("load campaign metrics: %w", err)
	}
	reasonRows, err := database.CampaignReasonRows(ctx, runIDs)
	if err != nil {
		return report.Report{}, fmt.Errorf("load campaign reason codes: %w", err)
	}
	summaries := report.Summarize(rows, reasonRows)
	return report.Report{
		Meta:          report.MetaFromLookup(lookup),
		BiasChecks:    report.AnalyzeBias(summaries),
		ServiceScores: report.ScoreMetrics(rows, reasonRows),
		Summaries:     summaries,
	}, nil
}

func collectCapacityReport(ctx context.Context, meta report.Meta, opts capacityOptions) (capacitybench.Report, error) {
	if meta.EarliestStartedAt == nil {
		return capacitybench.Report{}, fmt.Errorf("campaign start time is not available")
	}
	if meta.LatestEndedAt == nil {
		return capacitybench.Report{}, fmt.Errorf("campaign is still in progress; capacity capture requires a completed end time")
	}
	if !meta.LatestEndedAt.After(*meta.EarliestStartedAt) {
		return capacitybench.Report{}, fmt.Errorf("campaign end time must be after start time")
	}
	if opts.step <= 0 {
		return capacitybench.Report{}, fmt.Errorf("-capacity-step must be positive")
	}
	if opts.rateWindow <= 0 {
		return capacitybench.Report{}, fmt.Errorf("-capacity-rate-window must be positive")
	}

	promURL := strings.TrimSpace(opts.prometheusURL)
	if promURL == "" {
		promURL = strings.TrimSpace(os.Getenv("CAPACITY_PROMETHEUS_URL"))
	}
	if promURL == "" {
		promURL = strings.TrimSpace(os.Getenv("PROMETHEUS_URL"))
	}
	if promURL == "" {
		return capacitybench.Report{}, fmt.Errorf("set -capacity-prometheus-url, CAPACITY_PROMETHEUS_URL, or PROMETHEUS_URL")
	}

	instancesRaw := strings.TrimSpace(opts.instancesRaw)
	if env := strings.TrimSpace(os.Getenv("CAPACITY_INSTANCES")); env != "" && instancesRaw == "" {
		instancesRaw = env
	}
	if instancesRaw == "" {
		instancesRaw = defaultCapacityInstances
	}
	instances := splitCSV(instancesRaw)
	instanceRegex, err := capacitybench.InstanceRegex(instances)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("instances: %w", err)
	}

	client := &capacitybench.PrometheusClient{
		BaseURL: promURL,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
	summaries, err := capacitybench.Collect(ctx, client, capacitybench.DefaultQueries(instanceRegex, opts.rateWindow), *meta.EarliestStartedAt, *meta.LatestEndedAt, opts.step)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("collect: %w", err)
	}
	return capacitybench.Report{
		PrometheusURL: promURL,
		Start:         meta.EarliestStartedAt.UTC(),
		End:           meta.LatestEndedAt.UTC(),
		Step:          opts.step.String(),
		Instances:     instances,
		Summaries:     summaries,
	}, nil
}

func writeReportFiles(dir string, r report.Report, capacityReport *capacitybench.Report) error {
	if err := writeReport(dir, "report.md", "markdown", r); err != nil {
		return err
	}
	if err := writeReport(dir, "report.json", "json", r); err != nil {
		return err
	}
	files := []string{"report.md", "report.json"}
	if capacityReport != nil {
		if err := writeCapacityFiles(dir, *capacityReport); err != nil {
			return err
		}
		files = append(files, "capacity.md", "capacity.json")
	}
	manifest := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"input":         r.Meta.Input,
		"campaign_runs": r.Meta.CampaignRuns,
		"files":         files,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "manifest.json"), append(data, '\n'))
}

func writeReport(dir, name, format string, r report.Report) error {
	var b strings.Builder
	if err := report.Write(&b, format, r); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name), []byte(b.String()))
}

func writeCapacityFiles(dir string, r capacitybench.Report) error {
	var md strings.Builder
	if err := capacitybench.WriteMarkdown(&md, r); err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, "capacity.md"), []byte(md.String())); err != nil {
		return err
	}
	var js strings.Builder
	if err := capacitybench.WriteJSON(&js, r); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "capacity.json"), []byte(js.String()))
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sanitizePathComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "campaign"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
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
