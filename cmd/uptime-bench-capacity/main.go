package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

func main() {
	promURL := flag.String("prometheus-url", "", "Prometheus base URL (overrides PROMETHEUS_URL env var)")
	instancesFlag := flag.String("instances", "jetmon-v1.example.com,jetmon-v2.example.com", "comma-separated Prometheus instance labels to collect")
	startFlag := flag.String("start", "", "window start time (RFC3339 or Unix timestamp); default is end-duration")
	endFlag := flag.String("end", "", "window end time (RFC3339 or Unix timestamp); default is now")
	duration := flag.Duration("duration", 15*time.Minute, "window duration when -start is omitted")
	step := flag.Duration("step", 15*time.Second, "Prometheus query_range step")
	rateWindow := flag.Duration("rate-window", 2*time.Minute, "PromQL rate() window for counter-based metrics")
	format := flag.String("format", "table", "output format: table or json")
	flag.Parse()

	if *promURL == "" {
		*promURL = os.Getenv("PROMETHEUS_URL")
	}
	if *promURL == "" {
		log.Fatal("capacity: set -prometheus-url or PROMETHEUS_URL")
	}

	instances := splitCSV(*instancesFlag)
	instanceRegex, err := capacitybench.InstanceRegex(instances)
	if err != nil {
		log.Fatalf("capacity: instances: %v", err)
	}
	start, end, err := resolveWindow(*startFlag, *endFlag, *duration)
	if err != nil {
		log.Fatalf("capacity: window: %v", err)
	}
	if *step <= 0 {
		log.Fatal("capacity: -step must be positive")
	}
	if *rateWindow <= 0 {
		log.Fatal("capacity: -rate-window must be positive")
	}

	client := &capacitybench.PrometheusClient{
		BaseURL: *promURL,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
	queries := capacitybench.DefaultQueries(instanceRegex, *rateWindow)

	ctx := context.Background()
	summaries, err := capacitybench.Collect(ctx, client, queries, start, end, *step)
	if err != nil {
		log.Fatalf("capacity: collect: %v", err)
	}
	report := capacitybench.Report{
		PrometheusURL: *promURL,
		Start:         start,
		End:           end,
		Step:          step.String(),
		Instances:     instances,
		Summaries:     summaries,
	}

	switch strings.ToLower(*format) {
	case "json":
		if err := capacitybench.WriteJSON(os.Stdout, report); err != nil {
			log.Fatalf("capacity: write json: %v", err)
		}
	case "table":
		if err := capacitybench.WriteTable(os.Stdout, report); err != nil {
			log.Fatalf("capacity: write table: %v", err)
		}
	default:
		log.Fatalf("capacity: unsupported -format %q (want table or json)", *format)
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

func resolveWindow(startRaw, endRaw string, duration time.Duration) (time.Time, time.Time, error) {
	var (
		start time.Time
		end   time.Time
		err   error
	)
	if endRaw != "" {
		end, err = parseTimeArg(endRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse -end: %w", err)
		}
	} else {
		end = time.Now().UTC()
	}

	if startRaw != "" {
		start, err = parseTimeArg(startRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("parse -start: %w", err)
		}
	} else {
		if duration <= 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("-duration must be positive when -start is omitted")
		}
		start = end.Add(-duration)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must be after start")
	}
	return start.UTC(), end.UTC(), nil
}

func parseTimeArg(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	if sec, err := strconv.ParseFloat(raw, 64); err == nil {
		whole := int64(sec)
		frac := sec - float64(whole)
		return time.Unix(whole, int64(frac*1e9)).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 or Unix timestamp")
}
