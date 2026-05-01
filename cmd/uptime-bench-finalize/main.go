package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/measurement"
	"github.com/Automattic/uptime-bench/internal/report"
)

func main() {
	campaign := flag.String("campaign", "", "campaign run ID or campaign config ID to finalize")
	outDir := flag.String("out-dir", "", "output directory (default reports/<campaign>)")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	derive := flag.Bool("derive", true, "recompute derived metrics before writing reports")
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
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("finalize: mkdir %s: %v", *outDir, err)
	}
	if err := writeReportFiles(*outDir, out); err != nil {
		log.Fatalf("finalize: write: %v", err)
	}
	log.Printf("finalize: wrote %s", *outDir)
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

func writeReportFiles(dir string, r report.Report) error {
	if err := writeReport(dir, "report.md", "markdown", r); err != nil {
		return err
	}
	if err := writeReport(dir, "report.json", "json", r); err != nil {
		return err
	}
	manifest := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"input":         r.Meta.Input,
		"campaign_runs": r.Meta.CampaignRuns,
		"files":         []string{"report.md", "report.json"},
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
