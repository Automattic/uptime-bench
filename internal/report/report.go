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
// docs/roadmap.md.
type Report struct {
	Meta       Meta        `json:"meta"`
	BiasChecks []BiasCheck `json:"bias_checks,omitempty"`
	Summaries  []Summary   `json:"summaries"`
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
	FailureType            string         `json:"failure_type"`
	ServiceID              string         `json:"service_id"`
	Samples                int            `json:"samples"`
	DetectionRate          *float64       `json:"detection_rate,omitempty"`
	DetectionRateCI95      *CI            `json:"detection_rate_ci95,omitempty"`
	TruePositive           int            `json:"true_positive"`
	FalseNegative          int            `json:"false_negative"`
	FalsePositive          int            `json:"false_positive"`
	Unknown                int            `json:"unknown"`
	CapabilityMismatch     int            `json:"capability_mismatch"`
	ReasonCodes            map[string]int `json:"reason_codes,omitempty"`
	MaintenanceSuppressed  int            `json:"maintenance_suppressed"`
	CooldownSuppressed     int            `json:"cooldown_suppressed"`
	CooldownUncertain      int            `json:"cooldown_uncertain"`
	TLSAdvisoryDetected    int            `json:"tls_advisory_detected"`
	TLSAdvisoryMissed      int            `json:"tls_advisory_missed"`
	TLSAdvisoryFalseOutage int            `json:"tls_advisory_false_outage"`
	LatencyMinSeconds      *float64       `json:"latency_min_s,omitempty"`
	LatencyAvgSeconds      *float64       `json:"latency_avg_s,omitempty"`
	LatencyP50Seconds      *float64       `json:"latency_p50_s,omitempty"`
	LatencyP50CI95Seconds  *CI            `json:"latency_p50_ci95_s,omitempty"`
	LatencyP95Seconds      *float64       `json:"latency_p95_s,omitempty"`
	LatencyP95CI95Seconds  *CI            `json:"latency_p95_ci95_s,omitempty"`
	LatencyMaxSeconds      *float64       `json:"latency_max_s,omitempty"`
}

// CI is a two-sided 95% confidence interval for a report statistic.
type CI struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// BiasCheck is a pre-summary diagnostic. These checks intentionally
// use service-agnostic inputs only; report code must never special-case
// a vendor.
type BiasCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type summaryKey struct {
	failureType string
	serviceID   string
}

type accumulator struct {
	runs                   map[string]struct{}
	truePositive           int
	falseNegative          int
	falsePositive          int
	unknown                int
	maintenanceSuppressed  int
	cooldownSuppressed     int
	cooldownUncertain      int
	tlsAdvisoryDetected    int
	tlsAdvisoryMissed      int
	tlsAdvisoryFalseOutage int
	latencies              []float64
	reasonRuns             map[string]map[string]struct{}
}

// Summarize folds campaign metric rows into one row per
// (failure_type, service_id). Optional reason-code rows add
// support-matrix counts from monitor_reports.reason_code without
// changing the derived_metrics math.
func Summarize(rows []db.CampaignMetricRow, reasonRows ...[]db.CampaignReasonRow) []Summary {
	byKey := map[summaryKey]*accumulator{}
	for _, row := range rows {
		failureType := row.FailureType
		if failureType == "" {
			failureType = failureTypeUnrecorded
		}
		key := summaryKey{failureType: failureType, serviceID: row.ServiceID}
		acc := byKey[key]
		if acc == nil {
			acc = newAccumulator()
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
		case "cooldown_suppressed":
			acc.cooldownSuppressed += boolMetric(value)
		case "cooldown_uncertain":
			acc.cooldownUncertain += boolMetric(value)
		case "tls_advisory_detected":
			acc.tlsAdvisoryDetected += boolMetric(value)
		case "tls_advisory_missed":
			acc.tlsAdvisoryMissed += boolMetric(value)
		case "tls_advisory_false_outage":
			acc.tlsAdvisoryFalseOutage += boolMetric(value)
		case "detection_latency_s":
			if row.MetricValue != nil {
				acc.latencies = append(acc.latencies, *row.MetricValue)
			}
		}
	}
	for _, rows := range reasonRows {
		for _, row := range rows {
			if row.ReasonCode == "" {
				continue
			}
			failureType := row.FailureType
			if failureType == "" {
				failureType = failureTypeUnrecorded
			}
			key := summaryKey{failureType: failureType, serviceID: row.ServiceID}
			acc := byKey[key]
			if acc == nil {
				acc = newAccumulator()
				byKey[key] = acc
			}
			acc.runs[row.RunID] = struct{}{}
			runs := acc.reasonRuns[row.ReasonCode]
			if runs == nil {
				runs = map[string]struct{}{}
				acc.reasonRuns[row.ReasonCode] = runs
			}
			runs[row.RunID] = struct{}{}
		}
	}

	out := make([]Summary, 0, len(byKey))
	for key, acc := range byKey {
		s := Summary{
			FailureType:            key.failureType,
			ServiceID:              key.serviceID,
			Samples:                len(acc.runs),
			TruePositive:           acc.truePositive,
			FalseNegative:          acc.falseNegative,
			FalsePositive:          acc.falsePositive,
			Unknown:                acc.unknown,
			MaintenanceSuppressed:  acc.maintenanceSuppressed,
			CooldownSuppressed:     acc.cooldownSuppressed,
			CooldownUncertain:      acc.cooldownUncertain,
			TLSAdvisoryDetected:    acc.tlsAdvisoryDetected,
			TLSAdvisoryMissed:      acc.tlsAdvisoryMissed,
			TLSAdvisoryFalseOutage: acc.tlsAdvisoryFalseOutage,
		}
		if len(acc.reasonRuns) > 0 {
			s.ReasonCodes = make(map[string]int, len(acc.reasonRuns))
			for code, runs := range acc.reasonRuns {
				count := len(runs)
				s.ReasonCodes[code] = count
				if code == "capability_mismatch" {
					s.CapabilityMismatch = count
				}
			}
		}
		eligible := acc.truePositive + acc.falseNegative
		if eligible > 0 {
			rate := float64(acc.truePositive) / float64(eligible)
			s.DetectionRate = &rate
			s.DetectionRateCI95 = wilsonCI(acc.truePositive, eligible)
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

func newAccumulator() *accumulator {
	return &accumulator{
		runs:       map[string]struct{}{},
		reasonRuns: map[string]map[string]struct{}{},
	}
}

// AnalyzeBias produces service-agnostic diagnostics that should be
// read before the latency table. The checks are deliberately simple
// and conservative: imbalance warnings are about whether the campaign
// data is comparable, not about why it became imbalanced.
func AnalyzeBias(summaries []Summary) []BiasCheck {
	if len(summaries) == 0 {
		return nil
	}
	return []BiasCheck{
		sampleBalanceCheck(summaries),
		cellBalanceCheck(summaries),
		capabilityMismatchCheck(summaries),
		uncategorizedUnknownCheck(summaries),
	}
}

func sampleBalanceCheck(summaries []Summary) BiasCheck {
	counts := map[string]int{}
	for _, s := range summaries {
		counts[s.ServiceID] += s.Samples
	}
	min, max := minMax(counts)
	status := "ok"
	if exceedsFivePercentSkew(min, max) {
		status = "warn"
	}
	return BiasCheck{
		Name:    "service_sample_balance",
		Status:  status,
		Message: fmt.Sprintf("per-service samples: %s", formatCounts(counts)),
	}
}

func cellBalanceCheck(summaries []Summary) BiasCheck {
	services := map[string]struct{}{}
	byFailure := map[string]map[string]int{}
	for _, s := range summaries {
		services[s.ServiceID] = struct{}{}
		counts := byFailure[s.FailureType]
		if counts == nil {
			counts = map[string]int{}
			byFailure[s.FailureType] = counts
		}
		counts[s.ServiceID] = s.Samples
	}
	var warnings []string
	for failureType, counts := range byFailure {
		for serviceID := range services {
			if _, ok := counts[serviceID]; !ok {
				counts[serviceID] = 0
			}
		}
		min, max := minMax(counts)
		if exceedsFivePercentSkew(min, max) {
			warnings = append(warnings, fmt.Sprintf("%s=%s", failureType, formatCounts(counts)))
		}
	}
	if len(warnings) == 0 {
		return BiasCheck{Name: "cell_sample_balance", Status: "ok", Message: "per-failure/service sample counts are balanced"}
	}
	sort.Strings(warnings)
	return BiasCheck{Name: "cell_sample_balance", Status: "warn", Message: strings.Join(warnings, "; ")}
}

func capabilityMismatchCheck(summaries []Summary) BiasCheck {
	var parts []string
	total := 0
	for _, s := range summaries {
		if s.CapabilityMismatch == 0 {
			continue
		}
		total += s.CapabilityMismatch
		parts = append(parts, fmt.Sprintf("%s/%s=%d", s.FailureType, s.ServiceID, s.CapabilityMismatch))
	}
	if total == 0 {
		return BiasCheck{Name: "capability_mismatch", Status: "ok", Message: "no capability_mismatch rows"}
	}
	sort.Strings(parts)
	return BiasCheck{Name: "capability_mismatch", Status: "info", Message: strings.Join(parts, "; ")}
}

func uncategorizedUnknownCheck(summaries []Summary) BiasCheck {
	var parts []string
	total := 0
	for _, s := range summaries {
		categorized := s.CapabilityMismatch
		for code, count := range s.ReasonCodes {
			if code != "capability_mismatch" {
				categorized += count
			}
		}
		uncategorized := s.Unknown - categorized
		if uncategorized <= 0 {
			continue
		}
		total += uncategorized
		parts = append(parts, fmt.Sprintf("%s/%s=%d", s.FailureType, s.ServiceID, uncategorized))
	}
	if total == 0 {
		return BiasCheck{Name: "uncategorized_unknown", Status: "ok", Message: "no uncategorized unknown rows"}
	}
	sort.Strings(parts)
	return BiasCheck{Name: "uncategorized_unknown", Status: "warn", Message: strings.Join(parts, "; ")}
}

func minMax(counts map[string]int) (int, int) {
	min, max := 0, 0
	first := true
	for _, v := range counts {
		if first || v < min {
			min = v
		}
		if first || v > max {
			max = v
		}
		first = false
	}
	return min, max
}

func exceedsFivePercentSkew(min, max int) bool {
	if max == 0 {
		return false
	}
	return float64(max-min)/float64(max) > 0.05
}

func formatCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
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
	s.LatencyP50CI95Seconds = percentileNearestCI(latencies, 0.50)
	s.LatencyP95Seconds = &p95
	s.LatencyP95CI95Seconds = percentileNearestCI(latencies, 0.95)
	s.LatencyMaxSeconds = &max
}

func wilsonCI(successes, total int) *CI {
	if total <= 0 {
		return nil
	}
	const z = 1.96
	n := float64(total)
	p := float64(successes) / n
	z2 := z * z
	denom := 1 + z2/n
	center := (p + z2/(2*n)) / denom
	margin := z * math.Sqrt((p*(1-p)+z2/(4*n))/n) / denom
	return &CI{
		Lower: math.Max(0, center-margin),
		Upper: math.Min(1, center+margin),
	}
}

// percentileNearestCI returns an approximate non-parametric 95%
// confidence interval for the nearest-rank percentile. It maps the
// percentile's binomial rank uncertainty back onto observed samples,
// so interval endpoints are real observed latencies, not interpolated
// values. This is intentionally deterministic for reproducible reports.
func percentileNearestCI(sorted []float64, p float64) *CI {
	if len(sorted) == 0 {
		return nil
	}
	if len(sorted) == 1 {
		return &CI{Lower: sorted[0], Upper: sorted[0]}
	}
	const z = 1.96
	n := float64(len(sorted))
	centerRank := p * n
	spread := z * math.Sqrt(n*p*(1-p))
	lowerRank := int(math.Floor(centerRank - spread))
	upperRank := int(math.Ceil(centerRank + spread))
	if lowerRank < 1 {
		lowerRank = 1
	}
	if upperRank < 1 {
		upperRank = 1
	}
	if lowerRank > len(sorted) {
		lowerRank = len(sorted)
	}
	if upperRank > len(sorted) {
		upperRank = len(sorted)
	}
	return &CI{Lower: sorted[lowerRank-1], Upper: sorted[upperRank-1]}
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
	for _, check := range r.BiasChecks {
		if _, err := fmt.Fprintf(w, "# bias %s=%s %s\n", check.Name, check.Status, check.Message); err != nil {
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
		"failure_type", "service", "n", "tp_rate", "tp_rate_ci95", "tp", "fn", "fp",
		"unknown", "cap_mismatch", "maint_suppressed", "cooldown_suppressed",
		"cooldown_uncertain", "tls_adv_detected", "tls_adv_missed",
		"tls_adv_false_outage", "min_s", "avg_s", "p50_s", "p50_ci95_s",
		"p95_s", "p95_ci95_s", "max_s",
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
			formatRatioCI(s.DetectionRateCI95),
			strconv.Itoa(s.TruePositive),
			strconv.Itoa(s.FalseNegative),
			strconv.Itoa(s.FalsePositive),
			strconv.Itoa(s.Unknown),
			strconv.Itoa(s.CapabilityMismatch),
			strconv.Itoa(s.MaintenanceSuppressed),
			strconv.Itoa(s.CooldownSuppressed),
			strconv.Itoa(s.CooldownUncertain),
			strconv.Itoa(s.TLSAdvisoryDetected),
			strconv.Itoa(s.TLSAdvisoryMissed),
			strconv.Itoa(s.TLSAdvisoryFalseOutage),
			formatSeconds(s.LatencyMinSeconds),
			formatSeconds(s.LatencyAvgSeconds),
			formatSeconds(s.LatencyP50Seconds),
			formatSecondsCI(s.LatencyP50CI95Seconds),
			formatSeconds(s.LatencyP95Seconds),
			formatSecondsCI(s.LatencyP95CI95Seconds),
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

func formatRatioCI(v *CI) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(v.Lower, 'f', 3, 64) + "-" + strconv.FormatFloat(v.Upper, 'f', 3, 64)
}

func formatSeconds(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', 1, 64)
}

func formatSecondsCI(v *CI) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(v.Lower, 'f', 1, 64) + "-" + strconv.FormatFloat(v.Upper, 'f', 1, 64)
}
