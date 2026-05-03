package jetmoncapacity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

// SuiteReport is the rollup artifact for one Jetmon capacity growth suite.
type SuiteReport struct {
	ID                string             `json:"id"`
	ConfigPath        string             `json:"config_path"`
	OutDir            string             `json:"out_dir"`
	Apply             bool               `json:"apply"`
	CreatedAt         time.Time          `json:"created_at"`
	BatchDuration     string             `json:"batch_duration,omitempty"`
	Cooldown          string             `json:"cooldown,omitempty"`
	PrometheusURL     string             `json:"prometheus_url,omitempty"`
	Instances         []string           `json:"instances,omitempty"`
	BatchCount        int                `json:"batch_count"`
	TotalBatchCount   int                `json:"total_batch_count,omitempty"`
	BatchSizes        []int              `json:"batch_sizes,omitempty"`
	SuiteStartCount   int                `json:"suite_start_count,omitempty"`
	SuiteStartSource  string             `json:"suite_start_source,omitempty"`
	SuiteStatePath    string             `json:"suite_state_path,omitempty"`
	CompletedBatches  int                `json:"completed_batches"`
	LastCleanBatch    int                `json:"last_clean_batch,omitempty"`
	FirstProblemBatch int                `json:"first_problem_batch,omitempty"`
	StopRecommended   bool               `json:"stop_recommended,omitempty"`
	StopReason        string             `json:"stop_reason,omitempty"`
	Notes             []string           `json:"notes,omitempty"`
	Batches           []SuiteBatchReport `json:"batches"`
}

// SuiteBatchReport is a compact batch-level summary backed by the child
// run-batch manifest and its Prometheus window artifact.
type SuiteBatchReport struct {
	ActiveCount       int                           `json:"active_count"`
	OutDir            string                        `json:"out_dir"`
	Status            string                        `json:"status"`
	LifecycleStatus   string                        `json:"lifecycle_status,omitempty"`
	HealthStatus      string                        `json:"health_status,omitempty"`
	PrometheusStatus  string                        `json:"prometheus_status,omitempty"`
	PrometheusError   string                        `json:"prometheus_error,omitempty"`
	CleanupStatus     string                        `json:"cleanup_status,omitempty"`
	CleanupError      string                        `json:"cleanup_error,omitempty"`
	WindowStart       *time.Time                    `json:"window_start,omitempty"`
	WindowEnd         *time.Time                    `json:"window_end,omitempty"`
	StopRecommended   bool                          `json:"stop_recommended,omitempty"`
	StopReason        string                        `json:"stop_reason,omitempty"`
	Error             string                        `json:"error,omitempty"`
	Health            []ServiceHealth               `json:"health,omitempty"`
	Thresholds        []ThresholdFinding            `json:"thresholds,omitempty"`
	PrometheusSummary []capacitybench.SeriesSummary `json:"prometheus_summary,omitempty"`
}

func buildSuiteReport(parent RunManifest, children []RunManifest) SuiteReport {
	report := SuiteReport{
		ID:               parent.ID,
		ConfigPath:       parent.ConfigPath,
		OutDir:           parent.OutDir,
		Apply:            parent.Apply,
		CreatedAt:        parent.CreatedAt,
		BatchDuration:    parent.BatchDuration,
		Cooldown:         parent.Cooldown,
		PrometheusURL:    parent.PrometheusURL,
		Instances:        append([]string(nil), parent.Instances...),
		BatchCount:       parent.BatchCount,
		TotalBatchCount:  parent.TotalBatchCount,
		BatchSizes:       append([]int(nil), parent.BatchSizes...),
		SuiteStartCount:  parent.SuiteStartCount,
		SuiteStartSource: parent.SuiteStartSource,
		SuiteStatePath:   parent.SuiteStatePath,
		StopRecommended:  parent.StopRecommended,
		StopReason:       parent.StopReason,
		Notes:            append([]string(nil), parent.Notes...),
	}
	for _, child := range children {
		batch := SuiteBatchReport{
			ActiveCount:      child.ActiveCount,
			OutDir:           child.OutDir,
			Status:           suiteBatchStatus(child),
			LifecycleStatus:  child.LifecycleStatus,
			HealthStatus:     child.HealthStatus,
			PrometheusStatus: child.PrometheusStatus,
			PrometheusError:  child.PrometheusError,
			CleanupStatus:    child.CleanupStatus,
			CleanupError:     child.CleanupError,
			WindowStart:      child.WindowStart,
			WindowEnd:        child.WindowEnd,
			StopRecommended:  child.StopRecommended,
			StopReason:       child.StopReason,
			Error:            child.Error,
			Health:           append([]ServiceHealth(nil), child.Health...),
			Thresholds:       append([]ThresholdFinding(nil), child.Thresholds...),
		}
		if prom := child.loadPrometheusReport(child.OutDir); prom != nil {
			batch.PrometheusSummary = append([]capacitybench.SeriesSummary(nil), prom.Summaries...)
		}
		report.Batches = append(report.Batches, batch)
	}
	report.CompletedBatches = len(report.Batches)
	for _, batch := range report.Batches {
		if batch.Status == "pass" {
			report.LastCleanBatch = batch.ActiveCount
			continue
		}
		if report.FirstProblemBatch == 0 {
			report.FirstProblemBatch = batch.ActiveCount
		}
	}
	return report
}

func writeSuiteReport(dir string, parent RunManifest, children []RunManifest) error {
	report := buildSuiteReport(parent, children)
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "capacity.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "capacity.md"), []byte(formatSuiteReportMarkdown(report)), 0o644)
}

func recordArtifactOnce(m *RunManifest, artifact Artifact) {
	for _, existing := range m.Artifacts {
		if existing.Service == artifact.Service && existing.Action == artifact.Action && existing.Path == artifact.Path {
			return
		}
	}
	m.Artifacts = append(m.Artifacts, artifact)
}

func formatSuiteReportMarkdown(report SuiteReport) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Jetmon Capacity Suite Report")
	fmt.Fprintf(&b, "\nID: `%s`\n", report.ID)
	fmt.Fprintf(&b, "Apply: `%t`\n", report.Apply)
	fmt.Fprintf(&b, "Config: `%s`\n", report.ConfigPath)
	fmt.Fprintf(&b, "Output: `%s`\n", report.OutDir)
	fmt.Fprintf(&b, "Created: `%s`\n", report.CreatedAt.Format(time.RFC3339))
	if report.PrometheusURL != "" {
		fmt.Fprintf(&b, "Prometheus: `%s`\n", report.PrometheusURL)
	}
	if len(report.Instances) > 0 {
		fmt.Fprintf(&b, "Instances: `%s`\n", strings.Join(report.Instances, "`, `"))
	}
	if report.BatchDuration != "" {
		fmt.Fprintf(&b, "Batch Duration: `%s`\n", report.BatchDuration)
	}
	if report.Cooldown != "" {
		fmt.Fprintf(&b, "Cooldown: `%s`\n", report.Cooldown)
	}
	if report.SuiteStartSource != "" {
		fmt.Fprintf(&b, "Suite Start: `%s` at `%d`\n", report.SuiteStartSource, report.SuiteStartCount)
	}

	fmt.Fprint(&b, "\n## Analysis\n\n")
	fmt.Fprintf(&b, "- Completed batches: `%d` of `%d` selected", report.CompletedBatches, report.BatchCount)
	if report.TotalBatchCount > 0 && report.TotalBatchCount != report.BatchCount {
		fmt.Fprintf(&b, " (`%d` configured)", report.TotalBatchCount)
	}
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Last clean batch: `%s`\n", formatMaybeInt(report.LastCleanBatch))
	fmt.Fprintf(&b, "- First problem batch: `%s`\n", formatMaybeInt(report.FirstProblemBatch))
	fmt.Fprintf(&b, "- Stop recommended: `%t`\n", report.StopRecommended)
	if report.StopReason != "" {
		fmt.Fprintf(&b, "- Stop reason: %s\n", escapeSuiteCell(report.StopReason))
	}

	fmt.Fprint(&b, "\n## Batch Results\n\n")
	fmt.Fprintln(&b, "| Active | Status | Window | Health | Prometheus | Cleanup | Stop | Reason |")
	fmt.Fprintln(&b, "| ---: | --- | --- | --- | --- | --- | --- | --- |")
	for _, batch := range report.Batches {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %t | %s |\n",
			batch.ActiveCount,
			batch.Status,
			escapeSuiteCell(formatWindow(batch.WindowStart, batch.WindowEnd)),
			escapeSuiteCell(batch.HealthStatus),
			escapeSuiteCell(batch.PrometheusStatus),
			escapeSuiteCell(batch.CleanupStatus),
			batch.StopRecommended,
			escapeSuiteCell(firstNonEmpty(batch.Error, batch.StopReason, batch.PrometheusError, batch.CleanupError)),
		)
	}
	if len(report.Batches) == 0 {
		fmt.Fprintln(&b, "| 0 | none | not recorded | - | - | - | false | no completed batches |")
	}

	writeSuiteServiceHealthMarkdown(&b, report)
	writeSuiteThresholdMarkdown(&b, report)
	writeSuitePrometheusMarkdown(&b, report)
	return b.String()
}

func writeSuiteServiceHealthMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch  int
		Health ServiceHealth
	}
	for _, batch := range report.Batches {
		for _, health := range batch.Health {
			if health.Action != "window-end-verify" {
				continue
			}
			rows = append(rows, struct {
				Batch  int
				Health ServiceHealth
			}{Batch: batch.ActiveCount, Health: health})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Service Health\n\n")
	fmt.Fprintln(b, "| Active | Service | Status | Active Sites | Stale Sites | Missed % | Recent/Min | P95 Age Sec | Oldest Age Sec | Reason |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, row := range rows {
		h := row.Health
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(h.Service),
			escapeSuiteCell(h.Status),
			formatIntPtr(h.ActiveSites),
			formatIntPtr(h.StaleActiveSites),
			formatFloatPtr(h.MissedCheckPercent),
			formatFloatPtr(h.RecentChecksPerMinute),
			formatFloatPtr(h.P95CheckAgeSec),
			formatFloatPtr(h.OldestCheckAgeSec),
			escapeSuiteCell(h.Reason),
		)
	}
}

func writeSuiteThresholdMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch     int
		Threshold ThresholdFinding
	}
	for _, batch := range report.Batches {
		for _, threshold := range batch.Thresholds {
			rows = append(rows, struct {
				Batch     int
				Threshold ThresholdFinding
			}{Batch: batch.ActiveCount, Threshold: threshold})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Thresholds\n\n")
	fmt.Fprintln(b, "| Active | Name | Status | Series | Value | Limit | Reason |")
	fmt.Fprintln(b, "| ---: | --- | --- | --- | ---: | ---: | --- |")
	for _, row := range rows {
		t := row.Threshold
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(t.Name),
			escapeSuiteCell(t.Status),
			escapeSuiteCell(t.Series),
			formatThresholdValue(t.Value, t.Status),
			formatThresholdValue(t.Limit, ""),
			escapeSuiteCell(t.Reason),
		)
	}
}

func writeSuitePrometheusMarkdown(b *strings.Builder, report SuiteReport) {
	rows := suitePrometheusRows(report)
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Prometheus Highlights\n\n")
	fmt.Fprintln(b, "| Active | Metric | Series | Unit | Samples | Avg | P95 | Max | Last |")
	fmt.Fprintln(b, "| ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: |")
	for _, row := range rows {
		s := row.Summary
		fmt.Fprintf(b, "| %d | %s | %s | %s | %d | %s | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(s.Query),
			escapeSuiteCell(capacitybench.SeriesLabel(s.Labels)),
			escapeSuiteCell(s.Unit),
			s.Samples,
			capacitybench.FormatValue(s.Unit, s.Avg),
			capacitybench.FormatValue(s.Unit, s.P95),
			capacitybench.FormatValue(s.Unit, s.Max),
			capacitybench.FormatValue(s.Unit, s.Last),
		)
	}
}

type suitePrometheusRow struct {
	Batch   int
	Summary capacitybench.SeriesSummary
}

func suitePrometheusRows(report SuiteReport) []suitePrometheusRow {
	interesting := map[string]bool{
		"host_cpu_used":                       true,
		"host_memory_used":                    true,
		"host_root_disk_used":                 true,
		"scrape_up":                           true,
		"dockerstats_scrape_success":          true,
		"process_cpu_used":                    true,
		"process_memory_resident":             true,
		"docker_container_cpu_used":           true,
		"docker_container_memory_working_set": true,
	}
	var rows []suitePrometheusRow
	for _, batch := range report.Batches {
		for _, summary := range batch.PrometheusSummary {
			if interesting[summary.Query] {
				rows = append(rows, suitePrometheusRow{Batch: batch.ActiveCount, Summary: summary})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Batch != rows[j].Batch {
			return rows[i].Batch < rows[j].Batch
		}
		if rows[i].Summary.Query != rows[j].Summary.Query {
			return rows[i].Summary.Query < rows[j].Summary.Query
		}
		return capacitybench.SeriesLabel(rows[i].Summary.Labels) < capacitybench.SeriesLabel(rows[j].Summary.Labels)
	})
	return rows
}

func suiteBatchStatus(m RunManifest) string {
	if m.Error != "" || m.CleanupStatus == "fail" || m.LifecycleStatus == "fail" || m.HealthStatus == "fail" || m.PrometheusStatus == "fail" || m.PrometheusStatus == "preflight_failed" {
		return "fail"
	}
	for _, finding := range m.Thresholds {
		if finding.Status == "fail" {
			return "fail"
		}
	}
	if m.StopRecommended {
		return "fail"
	}
	if m.LifecycleStatus == "pass" {
		return "pass"
	}
	return firstNonEmpty(m.LifecycleStatus, "unknown")
}

func formatWindow(start, end *time.Time) string {
	if start == nil && end == nil {
		return "not recorded"
	}
	return formatMaybeTime(start) + " to " + formatMaybeTime(end)
}

func formatMaybeInt(value int) string {
	if value == 0 {
		return "none"
	}
	return strconv.Itoa(value)
}

func escapeSuiteCell(s string) string {
	if s == "" {
		return "-"
	}
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
