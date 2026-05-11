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
	"github.com/Automattic/uptime-bench/internal/targetserver"
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
	Target            TargetManifest     `json:"target,omitempty"`
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
	ActiveCount              int                                   `json:"active_count"`
	OutDir                   string                                `json:"out_dir"`
	Status                   string                                `json:"status"`
	LifecycleStatus          string                                `json:"lifecycle_status,omitempty"`
	HealthStatus             string                                `json:"health_status,omitempty"`
	PrometheusStatus         string                                `json:"prometheus_status,omitempty"`
	PrometheusError          string                                `json:"prometheus_error,omitempty"`
	TargetObserverStatus     string                                `json:"target_observer_status,omitempty"`
	TargetObserverError      string                                `json:"target_observer_error,omitempty"`
	CapacityReplayStatus     string                                `json:"capacity_replay_status,omitempty"`
	CapacityReplayError      string                                `json:"capacity_replay_error,omitempty"`
	ReplayDetectionStatus    string                                `json:"replay_detection_status,omitempty"`
	ReplayDetectionError     string                                `json:"replay_detection_error,omitempty"`
	NetworkBucketStatus      string                                `json:"network_bucket_status,omitempty"`
	NetworkBucketError       string                                `json:"network_bucket_error,omitempty"`
	StreamingTelemetryStatus string                                `json:"streaming_telemetry_status,omitempty"`
	StreamingTelemetryError  string                                `json:"streaming_telemetry_error,omitempty"`
	CleanupStatus            string                                `json:"cleanup_status,omitempty"`
	CleanupError             string                                `json:"cleanup_error,omitempty"`
	WindowStart              *time.Time                            `json:"window_start,omitempty"`
	WindowEnd                *time.Time                            `json:"window_end,omitempty"`
	StopRecommended          bool                                  `json:"stop_recommended,omitempty"`
	StopReason               string                                `json:"stop_reason,omitempty"`
	Error                    string                                `json:"error,omitempty"`
	Health                   []ServiceHealth                       `json:"health,omitempty"`
	Thresholds               []ThresholdFinding                    `json:"thresholds,omitempty"`
	ThroughputMargins        []ThroughputMargin                    `json:"throughput_margins,omitempty"`
	PrometheusSummary        []capacitybench.SeriesSummary         `json:"prometheus_summary,omitempty"`
	TargetPreflights         []TargetPreflight                     `json:"target_preflights,omitempty"`
	TargetObservations       []targetserver.CapacityObserveSummary `json:"target_observations,omitempty"`
	CapacityReplays          []CapacityReplayRun                   `json:"capacity_replays,omitempty"`
	ReplayDetections         []ReplayDetectionRun                  `json:"replay_detections,omitempty"`
	NetworkBuckets           []NetworkBucketHostSnapshot           `json:"network_buckets,omitempty"`
	StreamingTelemetry       []StreamingTelemetryRun               `json:"streaming_telemetry,omitempty"`
}

// ThroughputMargin is a derived freshness-capacity view for one service in one
// batch. It compares recent check throughput to the minimum rate needed to keep
// every active site fresh inside the verifier's freshness window.
type ThroughputMargin struct {
	Service                 string   `json:"service"`
	Status                  string   `json:"status"`
	ActiveSites             *int64   `json:"active_sites,omitempty"`
	FreshnessWindowMinutes  int      `json:"freshness_window_minutes,omitempty"`
	RequiredChecksPerMinute *float64 `json:"required_checks_per_minute,omitempty"`
	RecentChecksPerMinute   *float64 `json:"recent_checks_per_minute,omitempty"`
	MarginChecksPerMinute   *float64 `json:"margin_checks_per_minute,omitempty"`
	MarginPercent           *float64 `json:"margin_percent,omitempty"`
	Reason                  string   `json:"reason,omitempty"`
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
		Target:           parent.Target,
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
			ActiveCount:              child.ActiveCount,
			OutDir:                   child.OutDir,
			Status:                   suiteBatchStatus(child),
			LifecycleStatus:          child.LifecycleStatus,
			HealthStatus:             child.HealthStatus,
			PrometheusStatus:         child.PrometheusStatus,
			PrometheusError:          child.PrometheusError,
			TargetObserverStatus:     child.TargetObserverStatus,
			TargetObserverError:      child.TargetObserverError,
			CapacityReplayStatus:     child.CapacityReplayStatus,
			CapacityReplayError:      child.CapacityReplayError,
			ReplayDetectionStatus:    child.ReplayDetectionStatus,
			ReplayDetectionError:     child.ReplayDetectionError,
			NetworkBucketStatus:      child.NetworkBucketStatus,
			NetworkBucketError:       child.NetworkBucketError,
			StreamingTelemetryStatus: child.StreamingTelemetryStatus,
			StreamingTelemetryError:  child.StreamingTelemetryError,
			CleanupStatus:            child.CleanupStatus,
			CleanupError:             child.CleanupError,
			WindowStart:              child.WindowStart,
			WindowEnd:                child.WindowEnd,
			StopRecommended:          child.StopRecommended,
			StopReason:               child.StopReason,
			Error:                    child.Error,
			Health:                   append([]ServiceHealth(nil), child.Health...),
			Thresholds:               append([]ThresholdFinding(nil), child.Thresholds...),
			ThroughputMargins:        throughputMarginsFromHealth(child.Health),
			TargetPreflights:         append([]TargetPreflight(nil), child.TargetPreflights...),
			TargetObservations:       append([]targetserver.CapacityObserveSummary(nil), child.TargetObservations...),
			CapacityReplays:          append([]CapacityReplayRun(nil), child.CapacityReplays...),
			ReplayDetections:         append([]ReplayDetectionRun(nil), child.ReplayDetections...),
			NetworkBuckets:           append([]NetworkBucketHostSnapshot(nil), child.NetworkBuckets...),
			StreamingTelemetry:       append([]StreamingTelemetryRun(nil), child.StreamingTelemetry...),
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
	if report.Target.HostPattern != "" || report.Target.URLPattern != "" {
		fmt.Fprintf(&b, "Target Host Pattern: `%s`\n", report.Target.HostPattern)
		fmt.Fprintf(&b, "Target URL Pattern: `%s`\n", report.Target.URLPattern)
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
	fmt.Fprintln(&b, "| Active | Status | Window | Target | Observer | Replay | Detection | Streaming | Network | Health | Prometheus | Cleanup | Stop | Reason |")
	fmt.Fprintln(&b, "| ---: | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, batch := range report.Batches {
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %t | %s |\n",
			batch.ActiveCount,
			batch.Status,
			escapeSuiteCell(formatWindow(batch.WindowStart, batch.WindowEnd)),
			escapeSuiteCell(suiteTargetStatus(batch.TargetPreflights)),
			escapeSuiteCell(suiteTargetObserverStatus(batch)),
			escapeSuiteCell(suiteCapacityReplayStatus(batch)),
			escapeSuiteCell(suiteReplayDetectionStatus(batch)),
			escapeSuiteCell(suiteStreamingTelemetryStatus(batch)),
			escapeSuiteCell(suiteNetworkBucketStatus(batch)),
			escapeSuiteCell(batch.HealthStatus),
			escapeSuiteCell(batch.PrometheusStatus),
			escapeSuiteCell(batch.CleanupStatus),
			batch.StopRecommended,
			escapeSuiteCell(firstNonEmpty(batch.Error, batch.StopReason, batch.PrometheusError, batch.StreamingTelemetryError, batch.CleanupError)),
		)
	}
	if len(report.Batches) == 0 {
		fmt.Fprintln(&b, "| 0 | none | not recorded | - | - | - | - | - | - | - | - | - | false | no completed batches |")
	}

	writeSuiteServiceHealthMarkdown(&b, report)
	writeSuiteCheckIntervalMarkdown(&b, report)
	writeSuiteThroughputMarginMarkdown(&b, report)
	writeSuiteTargetObserverMarkdown(&b, report)
	writeSuiteCapacityReplayMarkdown(&b, report)
	writeSuiteReplayDetectionMarkdown(&b, report)
	writeSuiteStreamingTelemetryMarkdown(&b, report)
	writeSuiteNetworkBucketMarkdown(&b, report)
	writeSuiteTargetPreflightMarkdown(&b, report)
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
	fmt.Fprintln(b, "| Active | Service | Verify | Missed Check Threshold | Freshness Source | Active Sites | Stale Sites | Missed % | Legacy Stale | Legacy Missed % | Recent/Min | P95 Age Sec | Oldest Age Sec | Reason |")
	fmt.Fprintln(b, "| ---: | --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, row := range rows {
		h := row.Health
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(h.Service),
			escapeSuiteCell(h.Status),
			escapeSuiteCell(serviceMissedCheckThresholdStatus(reportBatch(report, row.Batch), h.Service)),
			escapeSuiteCell(h.FreshnessSource),
			formatIntPtr(h.ActiveSites),
			formatIntPtr(h.StaleActiveSites),
			formatFloatPtr(h.MissedCheckPercent),
			formatIntPtr(h.LegacyProjectionStaleActiveSites),
			formatFloatPtr(h.LegacyProjectionMissedCheckPercent),
			formatFloatPtr(h.RecentChecksPerMinute),
			formatFloatPtr(h.P95CheckAgeSec),
			formatFloatPtr(h.OldestCheckAgeSec),
			escapeSuiteCell(h.Reason),
		)
	}
}

func writeSuiteCheckIntervalMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch    int
		Service  string
		Action   string
		Interval CheckIntervalRow
	}
	for _, batch := range report.Batches {
		for _, health := range batch.Health {
			for _, interval := range health.CheckIntervals {
				rows = append(rows, struct {
					Batch    int
					Service  string
					Action   string
					Interval CheckIntervalRow
				}{Batch: batch.ActiveCount, Service: health.Service, Action: health.Action, Interval: interval})
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Check Interval Distribution\n\n")
	fmt.Fprintln(b, "| Active | Service | Action | Check Interval Min | Active Sites |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | ---: |")
	for _, row := range rows {
		fmt.Fprintf(b, "| %d | %s | %s | %d | %d |\n",
			row.Batch,
			escapeSuiteCell(row.Service),
			escapeSuiteCell(row.Action),
			row.Interval.CheckIntervalMinutes,
			row.Interval.ActiveSites,
		)
	}
}

func writeSuiteThroughputMarginMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch  int
		Margin ThroughputMargin
	}
	for _, batch := range report.Batches {
		for _, margin := range batch.ThroughputMargins {
			rows = append(rows, struct {
				Batch  int
				Margin ThroughputMargin
			}{Batch: batch.ActiveCount, Margin: margin})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Throughput Margin\n\n")
	fmt.Fprintln(b, "| Active | Service | Status | Active Sites | Freshness Window Min | Required/Min | Recent/Min | Margin/Min | Margin % | Reason |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, row := range rows {
		m := row.Margin
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(m.Service),
			escapeSuiteCell(m.Status),
			formatIntPtr(m.ActiveSites),
			formatMaybePositiveInt(m.FreshnessWindowMinutes),
			formatFloatPtr(m.RequiredChecksPerMinute),
			formatFloatPtr(m.RecentChecksPerMinute),
			formatFloatPtr(m.MarginChecksPerMinute),
			formatFloatPtr(m.MarginPercent),
			escapeSuiteCell(m.Reason),
		)
	}
}

func writeSuiteTargetObserverMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch   int
		Service targetserver.CapacityObserveServiceSummary
	}
	for _, batch := range report.Batches {
		latest := latestTargetObservation(batch.TargetObservations)
		if latest == nil {
			continue
		}
		for _, service := range latest.Services {
			rows = append(rows, struct {
				Batch   int
				Service targetserver.CapacityObserveServiceSummary
			}{Batch: batch.ActiveCount, Service: service})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Target Observer\n\n")
	fmt.Fprintln(b, "| Active | Service | Expected | Observed | Never Seen | Stale | Coverage % | Requests | Req/S | Req/Site Mean | Expected Ratio | P95 Age Sec | Max Age Sec |")
	fmt.Fprintln(b, "| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, row := range rows {
		s := row.Service
		fmt.Fprintf(b, "| %d | %s | %d | %d | %d | %d | %.2f | %d | %.2f | %.2f | %.2f | %.2f | %.2f |\n",
			row.Batch,
			escapeSuiteCell(s.ID),
			s.ExpectedSites,
			s.ObservedSites,
			s.NeverSeenSites,
			s.StaleSites,
			s.CoveragePercent,
			s.TotalRequests,
			s.RequestsPerSecond,
			s.RequestsPerSiteMean,
			s.ExpectedRequestRatio,
			s.LastSeenAgeSecondsP95,
			s.LastSeenAgeSecondsMax,
		)
	}
}

func writeSuiteCapacityReplayMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch  int
		Replay CapacityReplayRun
		Event  CapacityReplayEventResult
	}
	for _, batch := range report.Batches {
		latest := latestCapacityReplay(batch.CapacityReplays)
		if latest == nil {
			continue
		}
		for _, event := range latest.Events {
			rows = append(rows, struct {
				Batch  int
				Replay CapacityReplayRun
				Event  CapacityReplayEventResult
			}{Batch: batch.ActiveCount, Replay: *latest, Event: event})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Capacity Replay\n\n")
	fmt.Fprintln(b, "| Active | Event | Type | Hosts | Activate Failures | Deactivate Failures | Error |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | ---: | ---: | --- |")
	for _, row := range rows {
		fmt.Fprintf(b, "| %d | %s | %s | %d | %d | %d | %s |\n",
			row.Batch,
			escapeSuiteCell(row.Event.ID),
			escapeSuiteCell(capacityReplayEventType(row.Replay.Plan, row.Event.ID)),
			len(row.Event.Hosts),
			row.Event.ActivateFailures,
			row.Event.DeactivateFailures,
			escapeSuiteCell(firstCapacityReplayHostError(row.Event)),
		)
	}
}

func writeSuiteReplayDetectionMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch   int
		Event   ReplayDetectionEvent
		Service ReplayDetectionServiceSummary
	}
	for _, batch := range report.Batches {
		latest := latestReplayDetection(batch.ReplayDetections)
		if latest == nil {
			continue
		}
		for _, event := range latest.Events {
			for _, service := range event.Services {
				rows = append(rows, struct {
					Batch   int
					Event   ReplayDetectionEvent
					Service ReplayDetectionServiceSummary
				}{Batch: batch.ActiveCount, Event: event, Service: service})
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Replay Detection\n\n")
	fmt.Fprintln(b, "| Active | Event | Service | Status | Hosts | Eligible | Down | Recovery | Late Down | Preexisting | Expected Interval | Normal Interval | Next Interval | Interval Mismatches | Down Min/Mean/Max | Recovery Min/Mean/Max | Error |")
	fmt.Fprintln(b, "| ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- | ---: | --- | --- | --- |")
	for _, row := range rows {
		s := row.Service
		fmt.Fprintf(b, "| %d | %s | %s | %s | %d | %d | %d | %d | %d | %d | %s | %s | %s | %d | %s | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(row.Event.ID),
			escapeSuiteCell(s.Service),
			escapeSuiteCell(s.Status),
			s.Hosts,
			s.EligibleHosts,
			s.DownDetected,
			s.RecoveryDetected,
			s.LateDownDetected,
			s.PreexistingDownOverlappedFailure,
			formatSeconds(s.ExpectedCheckIntervalSec),
			escapeSuiteCell(formatIntRange(s.NormalCheckIntervalMinSec, s.NormalCheckIntervalMaxSec)),
			escapeSuiteCell(formatIntRange(s.NextCheckIntervalMinSec, s.NextCheckIntervalMaxSec)),
			s.CheckIntervalMismatchEvents,
			escapeSuiteCell(formatLatencyRange(s.DownLatencyMinSec, s.DownLatencyMeanSec, s.DownLatencyMaxSec)),
			escapeSuiteCell(formatLatencyRange(s.RecoveryLatencyMinSec, s.RecoveryLatencyMeanSec, s.RecoveryLatencyMaxSec)),
			escapeSuiteCell(s.Error),
		)
	}
}

func writeSuiteStreamingTelemetryMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch int
		Host  StreamingTelemetryHostSnapshot
	}
	for _, batch := range report.Batches {
		latest := latestStreamingTelemetry(batch.StreamingTelemetry)
		if latest == nil {
			continue
		}
		for _, host := range latest.Hosts {
			rows = append(rows, struct {
				Batch int
				Host  StreamingTelemetryHostSnapshot
			}{Batch: batch.ActiveCount, Host: host})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Streaming Telemetry\n\n")
	fmt.Fprintln(b, "| Active | Service | Status | Samples | Completed | SPS Avg | SPS Max | Max Lag Ms | Pending Max | Result Depth Max | Failures | Stale Results | Error |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |")
	for _, row := range rows {
		agg := row.Host.Aggregate
		fmt.Fprintf(b, "| %d | %s | %s | %d | %d | %.2f | %d | %d | %d | %d | %d | %d | %s |\n",
			row.Batch,
			escapeSuiteCell(row.Host.Service),
			escapeSuiteCell(row.Host.Status),
			agg.Samples,
			agg.Completed,
			agg.SPSAverage,
			agg.SPSMax,
			agg.MaxLagMSMax,
			agg.PendingMax,
			agg.ResultDepthMax,
			agg.Failures,
			agg.StaleResults,
			escapeSuiteCell(row.Host.Error),
		)
	}
}

func writeSuiteNetworkBucketMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch   int
		Host    NetworkBucketHostSnapshot
		Counter NetworkBucketCounter
	}
	for _, batch := range report.Batches {
		for _, host := range latestNetworkBucketSnapshots(batch.NetworkBuckets) {
			for _, counter := range host.Counters {
				rows = append(rows, struct {
					Batch   int
					Host    NetworkBucketHostSnapshot
					Counter NetworkBucketCounter
				}{Batch: batch.ActiveCount, Host: host, Counter: counter})
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Network Buckets\n\n")
	fmt.Fprintln(b, "| Active | Host | Bucket | Direction | Bytes | Packets |")
	fmt.Fprintln(b, "| ---: | --- | --- | --- | ---: | ---: |")
	for _, row := range rows {
		fmt.Fprintf(b, "| %d | %s | %s | %s | %d | %d |\n",
			row.Batch,
			escapeSuiteCell(row.Host.ID),
			escapeSuiteCell(row.Counter.Bucket),
			escapeSuiteCell(row.Counter.Direction),
			row.Counter.Bytes,
			row.Counter.Packets,
		)
	}
}

func writeSuiteTargetPreflightMarkdown(b *strings.Builder, report SuiteReport) {
	var rows []struct {
		Batch     int
		Preflight TargetPreflight
	}
	for _, batch := range report.Batches {
		for _, preflight := range batch.TargetPreflights {
			rows = append(rows, struct {
				Batch     int
				Preflight TargetPreflight
			}{Batch: batch.ActiveCount, Preflight: preflight})
		}
	}
	if len(rows) == 0 {
		return
	}
	fmt.Fprint(b, "\n## Target Preflight\n\n")
	fmt.Fprintln(b, "| Active | Service | Status | Samples | HTTP | Error |")
	fmt.Fprintln(b, "| ---: | --- | --- | ---: | --- | --- |")
	for _, row := range rows {
		p := row.Preflight
		httpStatus := "checked"
		if p.SkippedHTTP {
			httpStatus = "skipped"
		}
		fmt.Fprintf(b, "| %d | %s | %s | %d | %s | %s |\n",
			row.Batch,
			escapeSuiteCell(p.Service),
			escapeSuiteCell(p.Status),
			p.SampleCount,
			escapeSuiteCell(httpStatus),
			escapeSuiteCell(p.Error),
		)
	}
}

func reportBatch(report SuiteReport, activeCount int) SuiteBatchReport {
	for _, batch := range report.Batches {
		if batch.ActiveCount == activeCount {
			return batch
		}
	}
	return SuiteBatchReport{}
}

func serviceMissedCheckThresholdStatus(batch SuiteBatchReport, service string) string {
	for _, finding := range batch.Thresholds {
		if finding.Name == "missed_check_percent" && finding.Series == service {
			return finding.Status
		}
	}
	return "-"
}

func throughputMarginsFromHealth(health []ServiceHealth) []ThroughputMargin {
	var margins []ThroughputMargin
	for _, h := range health {
		if h.Action != "window-end-verify" {
			continue
		}
		margin := ThroughputMargin{
			Service:     h.Service,
			Status:      "not_measured",
			ActiveSites: h.ActiveSites,
			Reason:      h.Reason,
		}
		if h.ActiveSites == nil || h.FreshnessWindowMinutes <= 0 || h.RecentChecksPerMinute == nil {
			if margin.Reason == "" {
				margin.Reason = "freshness throughput was not measured"
			}
			margins = append(margins, margin)
			continue
		}
		margin.FreshnessWindowMinutes = h.FreshnessWindowMinutes
		required := float64(*h.ActiveSites) / float64(h.FreshnessWindowMinutes)
		recent := *h.RecentChecksPerMinute
		rawMargin := recent - required
		marginPct := 0.0
		if required > 0 {
			marginPct = rawMargin / required * 100
		}
		margin.RequiredChecksPerMinute = &required
		margin.RecentChecksPerMinute = &recent
		margin.MarginChecksPerMinute = &rawMargin
		margin.MarginPercent = &marginPct
		margin.Status = "pass"
		if rawMargin < 0 {
			margin.Status = "fail"
		}
		margins = append(margins, margin)
	}
	return margins
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
		"host_net_rx":                         true,
		"host_net_tx":                         true,
		"host_root_disk_used":                 true,
		"scrape_up":                           true,
		"dockerstats_scrape_success":          true,
		"process_cpu_used":                    true,
		"process_memory_resident":             true,
		"docker_container_cpu_used":           true,
		"docker_container_memory_working_set": true,
		"docker_container_net_rx":             true,
		"docker_container_net_tx":             true,
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
	if m.Error != "" || m.CleanupStatus == "fail" || m.LifecycleStatus == "fail" || m.HealthStatus == "fail" || m.PrometheusStatus == "fail" || m.PrometheusStatus == "preflight_failed" || m.TargetObserverStatus == "fail" || m.TargetObserverStatus == "preflight_failed" || m.CapacityReplayStatus == "fail" || m.CapacityReplayStatus == "preflight_failed" || m.ReplayDetectionStatus == "fail" || m.NetworkBucketStatus == "fail" || m.NetworkBucketStatus == "preflight_failed" || m.StreamingTelemetryStatus == "fail" {
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

func suiteTargetObserverStatus(batch SuiteBatchReport) string {
	if batch.TargetObserverStatus != "" {
		return batch.TargetObserverStatus
	}
	if len(batch.TargetObservations) > 0 {
		return "captured"
	}
	return "-"
}

func suiteCapacityReplayStatus(batch SuiteBatchReport) string {
	if batch.CapacityReplayStatus != "" {
		return batch.CapacityReplayStatus
	}
	if len(batch.CapacityReplays) > 0 {
		return "captured"
	}
	return "-"
}

func suiteReplayDetectionStatus(batch SuiteBatchReport) string {
	if batch.ReplayDetectionStatus != "" {
		return batch.ReplayDetectionStatus
	}
	if len(batch.ReplayDetections) > 0 {
		return "captured"
	}
	return "-"
}

func suiteStreamingTelemetryStatus(batch SuiteBatchReport) string {
	if batch.StreamingTelemetryStatus != "" {
		return batch.StreamingTelemetryStatus
	}
	if len(batch.StreamingTelemetry) > 0 {
		return "captured"
	}
	return "-"
}

func suiteNetworkBucketStatus(batch SuiteBatchReport) string {
	if batch.NetworkBucketStatus != "" {
		return batch.NetworkBucketStatus
	}
	if len(batch.NetworkBuckets) > 0 {
		return "captured"
	}
	return "-"
}

func suiteTargetStatus(preflights []TargetPreflight) string {
	if len(preflights) == 0 {
		return "-"
	}
	status := "pass"
	for _, preflight := range preflights {
		if preflight.Status != "pass" {
			status = preflight.Status
			if status == "" {
				status = "unknown"
			}
			break
		}
	}
	return status
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

func formatMaybePositiveInt(value int) string {
	if value <= 0 {
		return "-"
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
