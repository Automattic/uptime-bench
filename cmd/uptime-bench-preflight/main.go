package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/Automattic/uptime-bench/internal/preflight"
)

func main() {
	fleetPath := flag.String("fleet", "fleet.toml", "path to fleet configuration file")
	servicesPath := flag.String("services", "services.toml", "path to services configuration file")
	scenarioList := flag.String("scenario", "", "comma-separated scenario TOML paths")
	campaignList := flag.String("campaign", "", "comma-separated campaign TOML paths")
	format := flag.String("format", "table", "output format: table or json")
	flag.Parse()

	scenarioPaths := splitCSV(*scenarioList)
	campaignPaths := splitCSV(*campaignList)
	scenarioPaths = append(scenarioPaths, flag.Args()...)
	if len(scenarioPaths) == 0 && len(campaignPaths) == 0 {
		log.Fatal("preflight: provide -scenario, -campaign, or scenario paths as arguments")
	}

	report := preflight.Check(*fleetPath, *servicesPath, scenarioPaths, campaignPaths)
	if err := write(os.Stdout, *format, report); err != nil {
		log.Fatalf("preflight: write: %v", err)
	}
	if report.HasErrors() {
		os.Exit(1)
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

func write(w io.Writer, format string, r preflight.Report) error {
	switch strings.ToLower(format) {
	case "", "table":
		return writeTable(w, r)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

func writeTable(w io.Writer, r preflight.Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(r.Scenarios) > 0 {
		if _, err := fmt.Fprintln(tw, "scenario\tid\ttarget\tmonitors\truntime"); err != nil {
			return err
		}
		for _, sc := range r.Scenarios {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
				sc.Path, sc.ID, sc.Target, strings.Join(sc.Monitors, ","), sc.Runtime); err != nil {
				return err
			}
		}
	}
	if len(r.Campaigns) > 0 {
		if len(r.Scenarios) > 0 {
			if _, err := fmt.Fprintln(tw); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(tw, "campaign\tid\tdesigns\treplays\tconfigured\tscheduled_span\tserial_runtime\tmax_scenario_runtime\tmax_starts_hour\tmax_parallel"); err != nil {
			return err
		}
		for _, c := range r.Campaigns {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%d\t%d\n",
				c.Path, c.ID, c.Designs, c.Replays, c.ConfiguredDuration, c.ScheduledSpan, c.SerialRuntime,
				c.MaxScenarioRuntime, c.MaxStartsInHour, c.MaxConcurrentSamples); err != nil {
				return err
			}
		}
	}
	if len(r.Diagnostics) > 0 {
		if len(r.Scenarios) > 0 || len(r.Campaigns) > 0 {
			if _, err := fmt.Fprintln(tw); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(tw, "level\tsubject\tmessage"); err != nil {
			return err
		}
		for _, d := range r.Diagnostics {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Level, d.Subject, d.Message); err != nil {
				return err
			}
		}
	}
	if len(r.Scenarios) == 0 && len(r.Campaigns) == 0 && len(r.Diagnostics) == 0 {
		if _, err := fmt.Fprintln(tw, "ok"); err != nil {
			return err
		}
	}
	return tw.Flush()
}
