// Package report aggregates derived benchmark metrics for presentation.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Automattic/uptime-bench/internal/db"
)

// failureTypeUnrecorded is the sentinel a Summary row carries when the
// underlying scenario_runs row had no failure_start ground-truth events
// to derive a failure_type from. Deliberately not the literal string
// "unknown" because that name is already in use as a metric name (the
// Unknown retrieve outcome) and we don't want the two namespaces to
// collide if a future schema introduces a literal failure type called
// "unknown".
const failureTypeUnrecorded = "<no_failure>"

// Report is the full reporter output: aggregated rows plus the
// aggregation metadata downstream readers need to interpret the
// numbers (how many campaign_runs were folded together, the time
// span). Required for the methodology disclosure called out in
// ROADMAP.md.
type Report struct {
	Meta      Meta      `json:"meta"`
	Summaries []Summary `json:"summaries"`
}

// Meta describes how a Report was assembled. CampaignRuns counts how
// many campaign_runs rows the data spans; when > 1 the report is an
// aggregate across multiple executions of the same stable campaign id
// (see ROADMAP — repeated runs of one config are intentionally
// aggregated). EarliestStartedAt / LatestEndedAt frame the wall-clock
// window; LatestEndedAt is nil while at least one campaign in the set
// is still running.
//
// Future: a `--by-run` flag would let operators drill into per-run
// numbers when an aggregate would obscure a regression. Today the
// metadata header is enough disclosure for the headline summary.
type Meta struct {
	Input             string     `json:"input"`
	MatchedAsRunID    bool       `json:"matched_as_run_id"`
	MatchedAsConfigID bool       `json:"matched_as_config_id"`
	CampaignRuns      int        `json:"campaign_runs"`
	EarliestStartedAt *time.Time `json:"earliest_started_at,omitempty"`
	LatestEndedAt     *time.Time `json:"latest_ended_at,omitempty"`
}

// MetaFromLookup builds report metadata from a db.CampaignLookup.
// Pure helper; no I/O. Computing this here (rather than in db) keeps
// db focused on row shapes and the report package focused on what
// gets disclosed in published output.
//
// LatestEndedAt is suppressed (left nil) if any matched run is still
// in flight — reporting an end time that isn't actually the latest
// would mislead readers comparing two reports.
func MetaFromLookup(l *db.CampaignLookup) Meta {
	m := Meta{}
	if l == nil {
		return m
	}
	m.Input = l.Input
	m.MatchedAsRunID = l.MatchedAsRunID
	m.MatchedAsConfigID = l.MatchedAsConfigID
	m.CampaignRuns = len(l.Runs)

	anyInFlight := false
	for i, r := range l.Runs {
		if i == 0 || r.StartedAt.Before(*m.EarliestStartedAt) {
			t := r.StartedAt
			m.EarliestStartedAt = &t
		}
		if r.EndedAt == nil {
			anyInFlight = true
			continue
		}
		if m.LatestEndedAt == nil || r.EndedAt.After(*m.LatestEndedAt) {
			t := *r.EndedAt
			m.LatestEndedAt = &t
		}
	}
	if anyInFlight {
		m.LatestEndedAt = nil
	}
	return m
}

// Summary is one campaign report row for a failure type and service.
type Summary struct {
	FailureType           string   `json:"failure_type"`
	ServiceID             string   `json:"service_id"`
	Samples               int      `json:"samples"`
	DetectionRate         *float64 `json:"detection_rate,omitempty"`
	TruePositive          int      `json:"true_positive"`
	FalseNegative         int      `json:"false_negative"`
	FalsePositive         int      `json:"false_positive"`
	Unknown               int      `json:"unknown"`
	MaintenanceSuppressed int      `json:"maintenance_suppressed"`
	LatencyMinSeconds     *float64 `json:"latency_min_s,omitempty"`
	LatencyAvgSeconds     *float64 `json:"latency_avg_s,omitempty"`
	LatencyP50Seconds     *float64 `json:"latency_p50_s,omitempty"`
	LatencyP95Seconds     *float64 `json:"latency_p95_s,omitempty"`
	LatencyMaxSeconds     *float64 `json:"latency_max_s,omitempty"`
}

type summaryKey struct {
	failureType string
	serviceID   string
}

type accumulator struct {
	runs                  map[string]struct{}
	truePositive          int
	falseNegative         int
	falsePositive         int
	unknown               int
	maintenanceSuppressed int
	latencies             []float64
}

// Summarize folds campaign metric rows into one row per
// (failure_type, service_id). It intentionally works from derived
// metrics only; support-matrix counts from monitor_reports.reason_code
// are a later reporting phase.
func Summarize(rows []db.CampaignMetricRow) []Summary {
	byKey := map[summaryKey]*accumulator{}
	for _, row := range rows {
		failureType := row.FailureType
		if failureType == "" {
			failureType = failureTypeUnrecorded
		}
		key := summaryKey{failureType: failureType, serviceID: row.ServiceID}
		acc := byKey[key]
		if acc == nil {
			acc = &accumulator{runs: map[string]struct{}{}}
			byKey[key] = acc
		}
		acc.runs[row.RunID] = struct{}{}

		value := metricValue(row)
		switch row.MetricName {
		case "true_positive":
			acc.truePositive += boolMetric(value)
		case "false_negative":
			acc.falseNegative += boolMetric(value)
		case "false_positive":
			acc.falsePositive += boolMetric(value)
		case "unknown":
			acc.unknown += boolMetric(value)
		case "maintenance_suppressed":
			acc.maintenanceSuppressed += boolMetric(value)
		case "detection_latency_s":
			if row.MetricValue != nil {
				acc.latencies = append(acc.latencies, *row.MetricValue)
			}
		}
	}

	out := make([]Summary, 0, len(byKey))
	for key, acc := range byKey {
		s := Summary{
			FailureType:           key.failureType,
			ServiceID:             key.serviceID,
			Samples:               len(acc.runs),
			TruePositive:          acc.truePositive,
			FalseNegative:         acc.falseNegative,
			FalsePositive:         acc.falsePositive,
			Unknown:               acc.unknown,
			MaintenanceSuppressed: acc.maintenanceSuppressed,
		}
		eligible := acc.truePositive + acc.falseNegative
		if eligible > 0 {
			rate := float64(acc.truePositive) / float64(eligible)
			s.DetectionRate = &rate
		}
		applyLatencyStats(&s, acc.latencies)
		out = append(out, s)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].FailureType != out[j].FailureType {
			return out[i].FailureType < out[j].FailureType
		}
		return out[i].ServiceID < out[j].ServiceID
	})
	return out
}

// metricValue coerces a nullable derived_metrics row into a float so
// the boolean metric switch can run uniformly. NULL → 0 is intentional
// today: every current writer in internal/measurement upserts a
// concrete 0 or 1 for the boolean metric names. If a future migration
// introduces a NULL-bearing metric where 0 means something other than
// "not detected" (e.g. a "skipped" outcome carrying its reason in
// metric_text), revisit — the better rule then is to skip NULL rows
// for the boolean accumulators rather than counting them as zero.
func metricValue(row db.CampaignMetricRow) float64 {
	if row.MetricValue == nil {
		return 0
	}
	return *row.MetricValue
}

func boolMetric(v float64) int {
	if v >= 0.5 {
		return 1
	}
	return 0
}

func applyLatencyStats(s *Summary, latencies []float64) {
	if len(latencies) == 0 {
		return
	}
	sort.Float64s(latencies)
	min := latencies[0]
	max := latencies[len(latencies)-1]
	sum := 0.0
	for _, v := range latencies {
		sum += v
	}
	avg := sum / float64(len(latencies))
	p50 := percentileNearest(latencies, 0.50)
	p95 := percentileNearest(latencies, 0.95)
	s.LatencyMinSeconds = &min
	s.LatencyAvgSeconds = &avg
	s.LatencyP50Seconds = &p50
	s.LatencyP95Seconds = &p95
	s.LatencyMaxSeconds = &max
}

// percentileNearest returns the value at percentile p using the
// nearest-rank method (NIST / Wikipedia "C = 1": idx = ⌈p·N⌉ − 1, on
// the sorted array). This is *not* linear interpolation — R's default
// quantile() (type 7) and numpy's default percentile() will produce
// slightly different numbers for the same data. The choice is
// methodological, not arbitrary: nearest-rank always returns an
// observed sample value, so the reported p95 is a number that actually
// occurred in the campaign. Document the choice in any published
// methodology section so skeptics can reproduce.
func percentileNearest(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// Write renders a Report in table, tsv, or json format. Table format
// emits a `# ...` comment line documenting how the data was scoped
// (resolved interpretation, campaign_runs count, time span); JSON
// wraps the same metadata as a top-level `meta` field. TSV stays
// metadata-free so machine pipelines that already consume it don't
// have to skip a header line.
func Write(w io.Writer, format string, r Report) error {
	switch strings.ToLower(format) {
	case "", "table":
		return writeTable(w, r)
	case "tsv":
		return writeDelimited(w, r.Summaries, "\t", false)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	default:
		return fmt.Errorf("report: unknown output format %q", format)
	}
}

func writeTable(w io.Writer, r Report) error {
	if line := metaCommentLine(r.Meta); line != "" {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return writeDelimited(w, r.Summaries, "\t", true)
}

// metaCommentLine renders Meta as a one-line `#`-prefixed comment for
// the table format. Returns empty when there's nothing useful to
// disclose (e.g. the input matched nothing).
func metaCommentLine(m Meta) string {
	if m.CampaignRuns == 0 {
		return ""
	}
	var match string
	switch {
	case m.MatchedAsRunID && m.MatchedAsConfigID:
		match = "campaign_run_id+config_id"
	case m.MatchedAsRunID:
		match = "campaign_run_id"
	case m.MatchedAsConfigID:
		match = "config_id"
	default:
		match = "unknown"
	}
	parts := []string{
		fmt.Sprintf("# input=%q matched_as=%s campaign_runs=%d", m.Input, match, m.CampaignRuns),
	}
	if m.EarliestStartedAt != nil {
		parts = append(parts, fmt.Sprintf("earliest_started_at=%s", m.EarliestStartedAt.UTC().Format(time.RFC3339)))
	}
	if m.LatestEndedAt != nil {
		parts = append(parts, fmt.Sprintf("latest_ended_at=%s", m.LatestEndedAt.UTC().Format(time.RFC3339)))
	} else if m.CampaignRuns > 0 {
		parts = append(parts, "latest_ended_at=in_progress")
	}
	return strings.Join(parts, " ")
}

func writeDelimited(w io.Writer, summaries []Summary, sep string, align bool) error {
	out := w
	var tw *tabwriter.Writer
	if align {
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		out = tw
	}
	header := []string{
		"failure_type", "service", "n", "tp_rate", "tp", "fn", "fp",
		"unknown", "maint_suppressed", "min_s", "avg_s", "p50_s",
		"p95_s", "max_s",
	}
	if _, err := fmt.Fprintln(out, strings.Join(header, sep)); err != nil {
		return err
	}
	for _, s := range summaries {
		fields := []string{
			s.FailureType,
			s.ServiceID,
			// strconv.Itoa is intentional throughout this hot loop —
			// fmt.Sprintf("%d", n) re-runs the format-state machine on
			// every value and is ~3× slower per row at TSV scale. See
			// BenchmarkWriteTSV; don't "clean up" to fmt.Sprintf.
			strconv.Itoa(s.Samples),
			formatRatio(s.DetectionRate),
			strconv.Itoa(s.TruePositive),
			strconv.Itoa(s.FalseNegative),
			strconv.Itoa(s.FalsePositive),
			strconv.Itoa(s.Unknown),
			strconv.Itoa(s.MaintenanceSuppressed),
			formatSeconds(s.LatencyMinSeconds),
			formatSeconds(s.LatencyAvgSeconds),
			formatSeconds(s.LatencyP50Seconds),
			formatSeconds(s.LatencyP95Seconds),
			formatSeconds(s.LatencyMaxSeconds),
		}
		if _, err := fmt.Fprintln(out, strings.Join(fields, sep)); err != nil {
			return err
		}
	}
	if tw != nil {
		return tw.Flush()
	}
	return nil
}

// formatRatio / formatSeconds use strconv rather than fmt.Sprintf for
// the same reason as the integer fields above: hot path, allocation
// sensitive. See BenchmarkWriteTSV.
func formatRatio(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 3, 64)
}

func formatSeconds(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}
