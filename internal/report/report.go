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

	"github.com/Automattic/uptime-bench/internal/db"
)

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
			failureType = "unknown"
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

// Write renders summaries in table, tsv, or json format.
func Write(w io.Writer, format string, summaries []Summary) error {
	switch strings.ToLower(format) {
	case "", "table":
		return writeDelimited(w, summaries, "\t", true)
	case "tsv":
		return writeDelimited(w, summaries, "\t", false)
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(summaries)
	default:
		return fmt.Errorf("report: unknown output format %q", format)
	}
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
