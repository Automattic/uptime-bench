// Package measurement derives benchmark metrics from the ground-truth event
// log and monitor reports stored in the database.
//
// Metric derivation is always a separate pass from raw event writes —
// never in the same transaction. All metrics are recomputable from the
// raw tables at any time.
package measurement

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/Automattic/uptime-bench/internal/db"
)

type failureWindow struct{ start, end time.Time }

type serviceData struct {
	unknown bool
	reason  string
	alerts  []db.MonitorReportRow
}

// Derive computes all metrics for the given run and upserts them into
// derived_metrics. Safe to call multiple times — rows are idempotent.
func Derive(ctx context.Context, database *db.DB, runID string) error {
	events, err := database.GroundTruthEventsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("measurement: load events: %w", err)
	}
	reports, err := database.MonitorReportsForRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("measurement: load reports: %w", err)
	}

	// Build failure windows from ground-truth events.
	var failureWindows []failureWindow
	startsByType := make(map[string]time.Time)
	for _, e := range events {
		switch e.EventType {
		case "failure_start":
			startsByType[e.FailureType] = e.OccurredAt
		case "failure_end":
			if s, ok := startsByType[e.FailureType]; ok {
				failureWindows = append(failureWindows, failureWindow{start: s, end: e.OccurredAt})
				delete(startsByType, e.FailureType)
			}
		}
	}

	// Group reports by service.
	byService := make(map[string]*serviceData)
	for _, r := range reports {
		sd := byService[r.ServiceID]
		if sd == nil {
			sd = &serviceData{}
			byService[r.ServiceID] = sd
		}
		if r.RetrieveStatus == "unknown" {
			sd.unknown = true
			sd.reason = r.RetrieveUnknownReason
		}
		if r.EventType == "alert_fired" {
			sd.alerts = append(sd.alerts, r)
		}
	}

	now := time.Now()
	for serviceID, sr := range byService {
		metrics := computeMetrics(sr, failureWindows)
		for name, row := range metrics {
			row.RunID = runID
			row.ServiceID = serviceID
			row.MetricName = name
			row.ComputedAt = now
			if err := database.UpsertDerivedMetric(ctx, row); err != nil {
				log.Printf("measurement: upsert %s/%s: %v", serviceID, name, err)
			}
		}
	}
	return nil
}

func computeMetrics(sr *serviceData, windows []failureWindow) map[string]db.DerivedMetricRow {
	out := make(map[string]db.DerivedMetricRow)
	f64 := func(v float64) *float64 { return &v }

	if sr.unknown {
		out["unknown"] = db.DerivedMetricRow{MetricValue: f64(1), MetricText: sr.reason}
		return out
	}

	// Single pass over alerts: classify each one as in-window (true positive,
	// candidate detection-latency sample) or out-of-window (false positive).
	truePositive := false
	falseNegative := true
	falsePositive := false
	var detectionLatency *float64

	for _, alert := range sr.alerts {
		if alert.ReportedAt == nil {
			continue
		}
		inWindow := false
		for _, w := range windows {
			if !alert.ReportedAt.Before(w.start) && !alert.ReportedAt.After(w.end) {
				inWindow = true
				if detectionLatency == nil {
					latency := alert.ReportedAt.Sub(w.start).Seconds()
					detectionLatency = &latency
				}
				break
			}
		}
		if inWindow {
			truePositive = true
			falseNegative = false
		} else {
			falsePositive = true
		}
	}

	boolVal := func(b bool) *float64 {
		if b {
			return f64(1)
		}
		return f64(0)
	}

	out["true_positive"] = db.DerivedMetricRow{MetricValue: boolVal(truePositive)}
	out["false_negative"] = db.DerivedMetricRow{MetricValue: boolVal(falseNegative)}
	out["false_positive"] = db.DerivedMetricRow{MetricValue: boolVal(falsePositive)}
	out["unknown"] = db.DerivedMetricRow{MetricValue: f64(0)}

	if detectionLatency != nil {
		out["detection_latency_s"] = db.DerivedMetricRow{MetricValue: detectionLatency}
	}

	return out
}
