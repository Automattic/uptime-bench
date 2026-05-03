package capacitybench

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Finding is a human-facing capacity observation derived from one or more
// Prometheus summary series.
type Finding struct {
	Status string `json:"status"`
	Metric string `json:"metric"`
	Series string `json:"series"`
	Detail string `json:"detail"`
}

// WriteJSON writes the machine-readable capacity report.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// WriteTable writes the compact terminal-friendly capacity summary.
func WriteTable(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "Prometheus: %s\n", report.PrometheusURL); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Window:     %s to %s\n", report.Start.Format(time.RFC3339), report.End.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Step:       %s\n\n", report.Step); err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "METRIC\tSERIES\tUNIT\tSAMPLES\tAVG\tP95\tMAX\tLAST"); err != nil {
		return err
	}
	for _, s := range report.Summaries {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			s.Query,
			SeriesLabel(s.Labels),
			s.Unit,
			s.Samples,
			FormatValue(s.Unit, s.Avg),
			FormatValue(s.Unit, s.P95),
			FormatValue(s.Unit, s.Max),
			FormatValue(s.Unit, s.Last),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// WriteMarkdown writes a report intended to sit beside report.md for a
// completed scenario campaign.
func WriteMarkdown(w io.Writer, report Report) error {
	if _, err := fmt.Fprintln(w, "# Capacity Report"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\nPrometheus: `%s`\n", report.PrometheusURL); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Window: `%s` to `%s`\n", report.Start.Format(time.RFC3339), report.End.Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Step: `%s`\n", report.Step); err != nil {
		return err
	}
	if len(report.Instances) > 0 {
		if _, err := fmt.Fprintf(w, "Instances: `%s`\n", strings.Join(report.Instances, "`, `")); err != nil {
			return err
		}
	}

	findings := Analyze(report)
	if _, err := fmt.Fprint(w, "\n## Analysis\n\n"); err != nil {
		return err
	}
	if len(findings) == 0 {
		if _, err := fmt.Fprintln(w, "No capacity series were returned for this window."); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(w, "| Status | Metric | Series | Detail |"); err != nil {
			return err
		}
		if _, err := fmt.Fprintln(w, "| --- | --- | --- | --- |"); err != nil {
			return err
		}
		for _, f := range findings {
			if _, err := fmt.Fprintf(w, "| %s | %s | %s | %s |\n",
				f.Status,
				escapeMarkdownCell(f.Metric),
				escapeMarkdownCell(f.Series),
				escapeMarkdownCell(f.Detail),
			); err != nil {
				return err
			}
		}
	}

	if _, err := fmt.Fprint(w, "\n## Raw Window Summary\n\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| Metric | Series | Unit | Samples | Min | Avg | P50 | P95 | Max | Last |"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |"); err != nil {
		return err
	}
	for _, s := range report.Summaries {
		if _, err := fmt.Fprintf(w, "| %s | %s | %s | %d | %s | %s | %s | %s | %s | %s |\n",
			escapeMarkdownCell(s.Query),
			escapeMarkdownCell(SeriesLabel(s.Labels)),
			escapeMarkdownCell(s.Unit),
			s.Samples,
			FormatValue(s.Unit, s.Min),
			FormatValue(s.Unit, s.Avg),
			FormatValue(s.Unit, s.P50),
			FormatValue(s.Unit, s.P95),
			FormatValue(s.Unit, s.Max),
			FormatValue(s.Unit, s.Last),
		); err != nil {
			return err
		}
	}
	if len(report.Summaries) == 0 {
		if _, err := fmt.Fprintln(w, "| none | none | none | 0 | n/a | n/a | n/a | n/a | n/a | n/a |"); err != nil {
			return err
		}
	}
	return nil
}

// Analyze returns service-capacity observations that are useful in scenario
// reports without requiring service-specific assumptions.
func Analyze(report Report) []Finding {
	var findings []Finding
	findings = append(findings, scrapeFindings(report)...)
	findings = append(findings, highWatermarkFindings(report)...)
	findings = append(findings, topConsumerFindings(report)...)
	sort.SliceStable(findings, func(i, j int) bool {
		if severityRank(findings[i].Status) != severityRank(findings[j].Status) {
			return severityRank(findings[i].Status) < severityRank(findings[j].Status)
		}
		if findings[i].Metric != findings[j].Metric {
			return findings[i].Metric < findings[j].Metric
		}
		return findings[i].Series < findings[j].Series
	})
	return findings
}

// FormatValue formats a capacity summary number according to its unit.
func FormatValue(unit string, value float64) string {
	switch unit {
	case "percent", "percent_core":
		return fmt.Sprintf("%.2f", value)
	case "bytes":
		return formatBytes(value)
	case "bytes_per_second":
		return formatBytes(value) + "/s"
	case "state":
		return fmt.Sprintf("%.0f", value)
	case "count":
		return fmt.Sprintf("%.0f", value)
	default:
		return fmt.Sprintf("%.3f", value)
	}
}

func scrapeFindings(report Report) []Finding {
	var findings []Finding
	for _, s := range report.Summaries {
		switch s.Query {
		case "scrape_up", "dockerstats_scrape_success":
			status := "ok"
			detail := "all samples healthy"
			if s.Min < 1 {
				status = "fail"
				detail = fmt.Sprintf("minimum sample was %s; exporter or scrape target was unhealthy during the window", FormatValue(s.Unit, s.Min))
			}
			findings = append(findings, Finding{
				Status: status,
				Metric: s.Query,
				Series: SeriesLabel(s.Labels),
				Detail: detail,
			})
		}
	}
	return findings
}

func highWatermarkFindings(report Report) []Finding {
	thresholds := map[string]float64{
		"host_cpu_used":       85,
		"host_memory_used":    85,
		"host_root_disk_used": 90,
	}
	var findings []Finding
	for _, s := range report.Summaries {
		limit, ok := thresholds[s.Query]
		if !ok {
			continue
		}
		status := "ok"
		detail := fmt.Sprintf("max %s stayed below %.0f%%", FormatValue(s.Unit, s.Max), limit)
		if s.Max >= limit {
			status = "warn"
			detail = fmt.Sprintf("max %s reached or exceeded %.0f%%", FormatValue(s.Unit, s.Max), limit)
		}
		findings = append(findings, Finding{
			Status: status,
			Metric: s.Query,
			Series: SeriesLabel(s.Labels),
			Detail: detail,
		})
	}
	return findings
}

func topConsumerFindings(report Report) []Finding {
	forMetric := map[string]int{
		"container_cpu_used":                  3,
		"docker_container_cpu_used":           3,
		"docker_container_cpu_rate":           3,
		"container_memory_working_set":        3,
		"docker_container_memory_working_set": 3,
		"process_cpu_used":                    3,
		"process_memory_resident":             3,
		"process_open_fds":                    3,
		"process_threads":                     3,
	}
	var findings []Finding
	for metric, limit := range forMetric {
		top := topSeries(report.Summaries, metric, limit)
		for _, s := range top {
			findings = append(findings, Finding{
				Status: "info",
				Metric: s.Query,
				Series: SeriesLabel(s.Labels),
				Detail: fmt.Sprintf("p95 %s, max %s, last %s", FormatValue(s.Unit, s.P95), FormatValue(s.Unit, s.Max), FormatValue(s.Unit, s.Last)),
			})
		}
	}
	return findings
}

func topSeries(summaries []SeriesSummary, query string, limit int) []SeriesSummary {
	var out []SeriesSummary
	for _, s := range summaries {
		if s.Query == query {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95 != out[j].P95 {
			return out[i].P95 > out[j].P95
		}
		if out[i].Max != out[j].Max {
			return out[i].Max > out[j].Max
		}
		return SeriesLabel(out[i].Labels) < SeriesLabel(out[j].Labels)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func severityRank(status string) int {
	switch status {
	case "fail":
		return 0
	case "warn":
		return 1
	case "info":
		return 2
	case "ok":
		return 3
	default:
		return 4
	}
}

func formatBytes(value float64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%.0f B", value)
	}
	div := float64(unit)
	exp := 0
	for n := value / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", value/div, "KMGTPE"[exp])
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, "|", `\|`)
}
