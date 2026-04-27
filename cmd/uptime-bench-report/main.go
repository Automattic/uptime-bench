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

	rows, err := database.CampaignMetricRows(context.Background(), *campaign)
	if err != nil {
		log.Fatalf("report: load campaign metrics: %v", err)
	}
	summaries := report.Summarize(rows)
	if err := report.Write(os.Stdout, *format, summaries); err != nil {
		log.Fatalf("report: write: %v", err)
	}
}
