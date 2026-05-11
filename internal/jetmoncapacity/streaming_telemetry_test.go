package jetmoncapacity

import (
	"testing"
	"time"
)

func TestParseStreamingSummaryLine(t *testing.T) {
	line := `2026/05/11 01:02:03 orchestrator: streaming summary active=50000 required_rate=166.67/s selected=200 dispatched=200 completed=198 side_effects=198 pending=4 active_checks=32 queue_depth=6 result_depth=7 side_effect_depth=8 workers=64 worker_target=80 sps=140 elapsed=1.5s max_lag=250ms avg_latency=85ms scale_latency=12ms successes=190 failures=8 failure_pressure=true error_timeout=1 error_connect=2 error_ssl=3 error_redirect=4 error_keyword=5 error_body_read=6 error_tls_expired=7 error_tls_deprecated=8 error_other=9 history_rows=188 ssl_rows=77 stale_results=3 backpressure_waits=11 side_effect_waits=12 result_pauses=13 side_effect_pauses=14 dispatch_limited=15`

	sample, ok := parseStreamingSummaryLine(line)
	if !ok {
		t.Fatal("parseStreamingSummaryLine returned false")
	}
	wantTime := time.Date(2026, 5, 11, 1, 2, 3, 0, time.UTC)
	if !sample.Timestamp.Equal(wantTime) {
		t.Fatalf("Timestamp = %s, want %s", sample.Timestamp, wantTime)
	}
	if sample.Active != 50000 || sample.RequiredRate != 166.67 || sample.Completed != 198 || sample.SPS != 140 {
		t.Fatalf("basic counters = active %d rate %.2f completed %d sps %d", sample.Active, sample.RequiredRate, sample.Completed, sample.SPS)
	}
	if sample.ElapsedMS != 1500 || sample.MaxLagMS != 250 || sample.AvgLatencyMS != 85 {
		t.Fatalf("durations = elapsed %d maxLag %d avgLatency %d", sample.ElapsedMS, sample.MaxLagMS, sample.AvgLatencyMS)
	}
	if !sample.FailurePressure || sample.ErrorTLSDeprecated != 8 || sample.BackpressureWaits != 11 || sample.DispatchLimited != 15 {
		t.Fatalf("pressure/error counters parsed incorrectly: %+v", sample)
	}
}

func TestAggregateStreamingSummarySamples(t *testing.T) {
	samples := []StreamingSummarySample{
		{Completed: 100, Selected: 110, Dispatched: 105, Successes: 98, Failures: 2, StaleResults: 1, HistoryRows: 97, SSLRows: 4, BackpressureWaits: 1, SideEffectWaits: 2, ResultPauses: 3, SideEffectPauses: 4, DispatchLimited: 5, SPS: 80, RequiredRate: 120, MaxLagMS: 50, Pending: 4, QueueDepth: 5, ResultDepth: 6, SideEffectDepth: 7, Workers: 8},
		{Completed: 200, Selected: 210, Dispatched: 205, Successes: 195, Failures: 5, StaleResults: 2, HistoryRows: 190, SSLRows: 6, BackpressureWaits: 2, SideEffectWaits: 3, ResultPauses: 4, SideEffectPauses: 5, DispatchLimited: 6, SPS: 120, RequiredRate: 180, MaxLagMS: 75, Pending: 6, QueueDepth: 7, ResultDepth: 8, SideEffectDepth: 9, Workers: 10},
	}

	agg := aggregateStreamingSummarySamples(samples)
	if agg.Samples != 2 || agg.Completed != 300 || agg.Failures != 7 || agg.StaleResults != 3 {
		t.Fatalf("totals = %+v", agg)
	}
	if agg.SPSAverage != 100 || agg.SPSMax != 120 || agg.RequiredRateAverage != 150 || agg.RequiredRateMax != 180 {
		t.Fatalf("rates = %+v", agg)
	}
	if agg.MaxLagMSMax != 75 || agg.PendingMax != 6 || agg.ResultDepthMax != 8 || agg.WorkerMax != 10 {
		t.Fatalf("maxima = %+v", agg)
	}
}
