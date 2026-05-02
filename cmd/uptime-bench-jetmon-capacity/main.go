package main

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/Automattic/uptime-bench/internal/jetmoncapacity"
)

func main() {
	action := flag.String("action", "verify", "lifecycle action: seed, activate, deactivate, or verify")
	schema := flag.String("schema", jetmoncapacity.SchemaV2, "Jetmon schema variant: v1 or v2")
	blogIDStart := flag.Int64("blog-id-start", 8_000_000_000_000_000, "first benchmark-owned blog_id")
	count := flag.Int("count", 1_000_000, "number of benchmark-owned blog_id rows in the reserved range")
	activeCount := flag.Int("active-count", 0, "number of reserved rows to activate; required for -action=activate")
	urlPattern := flag.String("url-pattern", "http://site-%07d.load.example.com/", "URL pattern with exactly one fmt integer placeholder")
	urlStart := flag.Int64("url-start", 1, "first generated number used with -url-pattern")
	bucketMin := flag.Int("bucket-min", 0, "minimum Jetmon bucket_no assigned to generated rows")
	bucketMax := flag.Int("bucket-max", 99, "maximum Jetmon bucket_no assigned to generated rows")
	checkInterval := flag.Int("check-interval", 1, "Jetmon check_interval in minutes")
	batchSize := flag.Int("batch-size", 1000, "rows per INSERT statement for -action=seed")
	freshSince := flag.Int("fresh-since-minutes", 5, "freshness window used by -action=verify v2 lag queries")
	format := flag.String("format", "sql", "output format: sql")
	flag.Parse()

	if strings.ToLower(*format) != "sql" {
		log.Fatalf("jetmon-capacity: unsupported -format %q (want sql)", *format)
	}

	plan := jetmoncapacity.Plan{
		Action: jetmoncapacity.Operation(strings.ToLower(strings.TrimSpace(*action))),
		Config: jetmoncapacity.Config{
			Schema:               strings.ToLower(strings.TrimSpace(*schema)),
			BlogIDStart:          *blogIDStart,
			Count:                *count,
			URLPattern:           *urlPattern,
			URLNumberStart:       *urlStart,
			BucketMin:            *bucketMin,
			BucketMax:            *bucketMax,
			CheckIntervalMinutes: *checkInterval,
			BatchSize:            *batchSize,
		},
		ActiveCount:       *activeCount,
		FreshSinceMinutes: *freshSince,
	}

	if err := jetmoncapacity.WriteSQL(os.Stdout, plan); err != nil {
		log.Fatalf("jetmon-capacity: %v", err)
	}
}
