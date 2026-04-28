package report

import (
	"fmt"
	"io"
	"testing"

	"github.com/Automattic/uptime-bench/internal/db"
)

var benchmarkSummaries []Summary

func BenchmarkSummarizeCampaignMetricRows(b *testing.B) {
	rows := benchmarkCampaignMetricRows()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		benchmarkSummaries = Summarize(rows)
	}
}

func BenchmarkWriteTSV(b *testing.B) {
	r := Report{Summaries: Summarize(benchmarkCampaignMetricRows())}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := Write(io.Discard, "tsv", r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWriteJSON(b *testing.B) {
	r := Report{Summaries: Summarize(benchmarkCampaignMetricRows())}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := Write(io.Discard, "json", r); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkCampaignMetricRows() []db.CampaignMetricRow {
	failureTypes := []string{"content_keyword", "dns_blackhole", "http_status", "tcp_refused"}
	serviceIDs := []string{"svc-a", "svc-b", "svc-c", "svc-d", "svc-e", "svc-f"}
	metricNames := []string{
		"true_positive",
		"false_negative",
		"false_positive",
		"unknown",
		"maintenance_suppressed",
		"cooldown_suppressed",
		"cooldown_uncertain",
	}

	const replays = 150
	rows := make([]db.CampaignMetricRow, 0, replays*len(failureTypes)*len(serviceIDs)*(len(metricNames)+1))
	for replay := 0; replay < replays; replay++ {
		for failureIndex, failureType := range failureTypes {
			runID := fmt.Sprintf("run-%s-%03d", failureType, replay)
			for serviceIndex, serviceID := range serviceIDs {
				for metricIndex, metricName := range metricNames {
					value := boolBenchmarkMetric(replay, failureIndex, serviceIndex, metricIndex)
					rows = append(rows, db.CampaignMetricRow{
						RunID:       runID,
						FailureType: failureType,
						ServiceID:   serviceID,
						MetricName:  metricName,
						MetricValue: &value,
					})
				}
				if (replay+failureIndex+serviceIndex)%3 == 0 {
					latency := float64(10 + replay%90 + serviceIndex)
					rows = append(rows, db.CampaignMetricRow{
						RunID:       runID,
						FailureType: failureType,
						ServiceID:   serviceID,
						MetricName:  "detection_latency_s",
						MetricValue: &latency,
					})
				}
			}
		}
	}
	return rows
}

func boolBenchmarkMetric(replay, failureIndex, serviceIndex, metricIndex int) float64 {
	if (replay+failureIndex+serviceIndex+metricIndex)%4 == 0 {
		return 1
	}
	return 0
}
