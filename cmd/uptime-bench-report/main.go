package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/Automattic/uptime-bench/internal/db"
	"github.com/Automattic/uptime-bench/internal/report"
)

func main() {
	campaign := flag.String("campaign", "", "campaign run ID or campaign config ID to report")
	format := flag.String("format", "table", "output format: table, tsv, or json")
	dsnFlag := flag.String("dsn", "", "MySQL DSN (overrides DB_DSN env var)")
	flag.Parse()

	if *campaign == "" {
		log.Fatal("report: -campaign is required")
	}

	dsn := *dsnFlag
	if dsn == "" {
		dsn = os.Getenv("DB_DSN")
	}
	if dsn == "" {
		log.Fatal("report: set -dsn flag or DB_DSN env var")
	}

	database, err := db.Open(dsn)
	if err != nil {
		log.Fatalf("report: db: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	lookup, err := database.ResolveCampaign(ctx, *campaign)
	if err != nil {
		log.Fatalf("report: resolve campaign: %v", err)
	}
	logLookup(lookup)
	if len(lookup.Runs) == 0 {
		// Render an empty report so the chosen format still emits a
		// valid document (TSV header, JSON envelope) — downstream
		// pipelines shouldn't have to special-case the no-match path.
		if err := report.Write(os.Stdout, *format, report.Report{Meta: report.MetaFromLookup(lookup)}); err != nil {
			log.Fatalf("report: write: %v", err)
		}
		return
	}

	runIDs := make([]string, len(lookup.Runs))
	for i, r := range lookup.Runs {
		runIDs[i] = r.ID
	}
	rows, err := database.CampaignMetricRows(ctx, runIDs)
	if err != nil {
		log.Fatalf("report: load campaign metrics: %v", err)
	}
	reasonRows, err := database.CampaignReasonRows(ctx, runIDs)
	if err != nil {
		log.Fatalf("report: load campaign reason codes: %v", err)
	}
	summaries := report.Summarize(rows, reasonRows)
	out := report.Report{
		Meta:       report.MetaFromLookup(lookup),
		BiasChecks: report.AnalyzeBias(summaries),
		Summaries:  summaries,
	}
	if err := report.Write(os.Stdout, *format, out); err != nil {
		log.Fatalf("report: write: %v", err)
	}
}

// logLookup prints which interpretation matched the user input so a
// silent empty result can be diagnosed from the CLI output. Goes to
// stderr (via log) rather than stdout so machine pipelines reading
// stdout for TSV / JSON aren't disturbed.
func logLookup(l *db.CampaignLookup) {
	switch {
	case l == nil:
		log.Print("report: no campaign_runs matched \"\" (neither id nor stable campaign_id)")
	case len(l.Runs) == 0:
		log.Printf("report: no campaign_runs matched %q (neither id nor stable campaign_id)", l.Input)
	case l.MatchedAsRunID && l.MatchedAsConfigID:
		log.Printf("report: %q matched both a campaign_runs.id and a stable campaign_id (%d run(s) total — review for accidental id collision)", l.Input, len(l.Runs))
	case l.MatchedAsRunID:
		log.Printf("report: %q matched as campaign_runs.id (1 run)", l.Input)
	case l.MatchedAsConfigID:
		log.Printf("report: %q matched as stable campaign_id (%d run(s) aggregated)", l.Input, len(l.Runs))
	}
}
