package jetmoncapacity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultStreamingTelemetryCollector captures Jetmon streaming scheduler
// counters from journald and, when configured, the operator dashboard state.
type DefaultStreamingTelemetryCollector struct{}

// StreamingTelemetryRun is one capacity-window capture of Jetmon streaming
// scheduler telemetry.
type StreamingTelemetryRun struct {
	Status     string                           `json:"status"`
	Error      string                           `json:"error,omitempty"`
	CapturedAt time.Time                        `json:"captured_at"`
	Start      time.Time                        `json:"start"`
	End        time.Time                        `json:"end"`
	Hosts      []StreamingTelemetryHostSnapshot `json:"hosts,omitempty"`
}

// StreamingTelemetryHostSnapshot contains telemetry for one Jetmon service
// host.
type StreamingTelemetryHostSnapshot struct {
	Service        string                    `json:"service"`
	SSHHost        string                    `json:"ssh_host"`
	Unit           string                    `json:"unit"`
	Status         string                    `json:"status"`
	Error          string                    `json:"error,omitempty"`
	DashboardState *StreamingDashboardState  `json:"dashboard_state,omitempty"`
	Samples        []StreamingSummarySample  `json:"samples,omitempty"`
	Aggregate      StreamingSummaryAggregate `json:"aggregate"`
}

// StreamingDashboardState mirrors the small subset of Jetmon dashboard state
// useful for capacity reports.
type StreamingDashboardState struct {
	WorkerCount     int       `json:"worker_count"`
	ActiveChecks    int       `json:"active_checks"`
	QueueDepth      int       `json:"queue_depth"`
	RetryQueueSize  int       `json:"retry_queue_size"`
	SitesPerSec     int       `json:"sites_per_sec"`
	RoundDurationMS int64     `json:"round_duration_ms"`
	GoSysMemMB      int       `json:"go_sys_mem_mb"`
	RSSMemMB        int       `json:"rss_mem_mb"`
	BucketMin       int       `json:"bucket_min"`
	BucketMax       int       `json:"bucket_max"`
	BucketOwnership string    `json:"bucket_ownership"`
	Hostname        string    `json:"hostname"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// StreamingSummarySample is one parsed "orchestrator: streaming summary" log
// line emitted by Jetmon v2.
type StreamingSummarySample struct {
	Timestamp          time.Time `json:"timestamp"`
	Active             int       `json:"active"`
	RequiredRate       float64   `json:"required_rate_per_second"`
	Selected           int       `json:"selected"`
	Dispatched         int       `json:"dispatched"`
	Completed          int       `json:"completed"`
	SideEffects        int       `json:"side_effects"`
	Pending            int       `json:"pending"`
	ActiveChecks       int       `json:"active_checks"`
	QueueDepth         int       `json:"queue_depth"`
	ResultDepth        int       `json:"result_depth"`
	SideEffectDepth    int       `json:"side_effect_depth"`
	Workers            int       `json:"workers"`
	WorkerTarget       int       `json:"worker_target"`
	SPS                int       `json:"sps"`
	ElapsedMS          int64     `json:"elapsed_ms"`
	MaxLagMS           int64     `json:"max_lag_ms"`
	AvgLatencyMS       int64     `json:"avg_latency_ms"`
	ScaleLatencyMS     int64     `json:"scale_latency_ms"`
	Successes          int       `json:"successes"`
	Failures           int       `json:"failures"`
	FailurePressure    bool      `json:"failure_pressure"`
	ErrorTimeout       int       `json:"error_timeout"`
	ErrorConnect       int       `json:"error_connect"`
	ErrorSSL           int       `json:"error_ssl"`
	ErrorRedirect      int       `json:"error_redirect"`
	ErrorKeyword       int       `json:"error_keyword"`
	ErrorBodyRead      int       `json:"error_body_read"`
	ErrorTLSExpired    int       `json:"error_tls_expired"`
	ErrorTLSDeprecated int       `json:"error_tls_deprecated"`
	ErrorOther         int       `json:"error_other"`
	HistoryRows        int       `json:"history_rows"`
	SSLRows            int       `json:"ssl_rows"`
	StaleResults       int       `json:"stale_results"`
	BackpressureWaits  int       `json:"backpressure_waits"`
	SideEffectWaits    int       `json:"side_effect_waits"`
	ResultPauses       int       `json:"result_pauses"`
	SideEffectPauses   int       `json:"side_effect_pauses"`
	DispatchLimited    int       `json:"dispatch_limited"`
}

// StreamingSummaryAggregate is a compact rollup of streaming summary samples.
type StreamingSummaryAggregate struct {
	Samples             int     `json:"samples"`
	Completed           int     `json:"completed"`
	Selected            int     `json:"selected"`
	Dispatched          int     `json:"dispatched"`
	Successes           int     `json:"successes"`
	Failures            int     `json:"failures"`
	StaleResults        int     `json:"stale_results"`
	HistoryRows         int     `json:"history_rows"`
	SSLRows             int     `json:"ssl_rows"`
	BackpressureWaits   int     `json:"backpressure_waits"`
	SideEffectWaits     int     `json:"side_effect_waits"`
	ResultPauses        int     `json:"result_pauses"`
	SideEffectPauses    int     `json:"side_effect_pauses"`
	DispatchLimited     int     `json:"dispatch_limited"`
	SPSAverage          float64 `json:"sps_average"`
	SPSMax              int     `json:"sps_max"`
	RequiredRateAverage float64 `json:"required_rate_average_per_second"`
	RequiredRateMax     float64 `json:"required_rate_max_per_second"`
	MaxLagMSMax         int64   `json:"max_lag_ms_max"`
	PendingMax          int     `json:"pending_max"`
	QueueDepthMax       int     `json:"queue_depth_max"`
	ResultDepthMax      int     `json:"result_depth_max"`
	SideEffectDepthMax  int     `json:"side_effect_depth_max"`
	WorkerMax           int     `json:"worker_max"`
}

var streamingSummaryTimestampPattern = regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)

func (DefaultStreamingTelemetryCollector) Collect(ctx context.Context, cfg StreamingTelemetryConfig, services []ServiceLifecycle, start, end time.Time) (StreamingTelemetryRun, error) {
	cfg = normalizeStreamingTelemetryConfig(cfg)
	run := StreamingTelemetryRun{
		Status:     "pass",
		CapturedAt: time.Now().UTC(),
		Start:      start.UTC(),
		End:        end.UTC(),
	}
	if !cfg.Enabled {
		run.Status = "disabled"
		return run, nil
	}
	timeout, err := parseDuration("streaming_telemetry.timeout", cfg.Timeout)
	if err != nil {
		run.Status = "fail"
		run.Error = err.Error()
		return run, err
	}
	hosts := selectedStreamingTelemetryHosts(cfg, services)
	var errs []error
	hasPartial := false
	for _, host := range hosts {
		snapshot := StreamingTelemetryHostSnapshot{
			Service: host.Service,
			SSHHost: host.SSHHost,
			Unit:    host.Unit,
			Status:  "pass",
		}
		if host.DashboardURL != "" {
			state, err := collectStreamingDashboardState(ctx, cfg, host, timeout)
			if err != nil {
				snapshot.Status = "fail"
				snapshot.Error = appendReason(snapshot.Error, "dashboard: "+err.Error())
				errs = append(errs, fmt.Errorf("%s dashboard: %w", host.Service, err))
			} else {
				snapshot.DashboardState = state
			}
		}
		samples, err := collectStreamingSummarySamples(ctx, cfg, host, start, end, timeout)
		if err != nil {
			snapshot.Status = "fail"
			snapshot.Error = appendReason(snapshot.Error, "journal: "+err.Error())
			errs = append(errs, fmt.Errorf("%s journal: %w", host.Service, err))
		} else {
			snapshot.Samples = samples
			snapshot.Aggregate = aggregateStreamingSummarySamples(samples)
			if len(samples) == 0 {
				if markMissingStreamingSummarySamples(&snapshot) {
					hasPartial = true
				} else {
					errs = append(errs, fmt.Errorf("%s journal: no streaming summary log lines found in window", host.Service))
				}
			}
		}
		run.Hosts = append(run.Hosts, snapshot)
	}
	if err := joinErrors(errs); err != nil {
		run.Status = "fail"
		run.Error = err.Error()
		return run, err
	}
	if hasPartial {
		run.Status = "partial"
	}
	return run, nil
}

func markMissingStreamingSummarySamples(snapshot *StreamingTelemetryHostSnapshot) bool {
	if snapshot.DashboardState != nil {
		snapshot.Status = "partial"
		snapshot.Error = appendReason(snapshot.Error, "journal: no streaming summary log lines found in window; dashboard state captured")
		return true
	}
	snapshot.Status = "fail"
	snapshot.Error = appendReason(snapshot.Error, "no streaming summary log lines found in window")
	return false
}

func normalizeStreamingTelemetryConfig(cfg StreamingTelemetryConfig) StreamingTelemetryConfig {
	cfg.SSHConfig = strings.TrimSpace(cfg.SSHConfig)
	cfg.Unit = strings.TrimSpace(cfg.Unit)
	if cfg.Unit == "" {
		cfg.Unit = "jetmon2"
	}
	if cfg.Timeout == "" {
		cfg.Timeout = "10s"
	}
	for i := range cfg.Hosts {
		cfg.Hosts[i] = normalizeStreamingTelemetryHost(cfg, cfg.Hosts[i])
	}
	return cfg
}

func selectedStreamingTelemetryHosts(cfg StreamingTelemetryConfig, services []ServiceLifecycle) []StreamingTelemetryHostConfig {
	if len(services) == 0 {
		return append([]StreamingTelemetryHostConfig(nil), cfg.Hosts...)
	}
	selected := map[string]bool{}
	for _, service := range services {
		selected[service.ID] = true
	}
	var hosts []StreamingTelemetryHostConfig
	for _, host := range cfg.Hosts {
		if selected[host.Service] {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func collectStreamingDashboardState(ctx context.Context, cfg StreamingTelemetryConfig, host StreamingTelemetryHostConfig, timeout time.Duration) (*StreamingDashboardState, error) {
	seconds := int(math.Ceil(timeout.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	url := strings.TrimRight(host.DashboardURL, "/") + "/api/state"
	out, err := runStreamingTelemetrySSH(ctx, cfg, host.SSHHost, timeout, []string{"curl", "--max-time", strconv.Itoa(seconds), "-fsS", url})
	if err != nil {
		return nil, err
	}
	var state StreamingDashboardState
	if err := json.Unmarshal(bytes.TrimSpace(out), &state); err != nil {
		return nil, fmt.Errorf("parse dashboard state: %w", err)
	}
	return &state, nil
}

func collectStreamingSummarySamples(ctx context.Context, cfg StreamingTelemetryConfig, host StreamingTelemetryHostConfig, start, end time.Time, timeout time.Duration) ([]StreamingSummarySample, error) {
	out, err := runStreamingTelemetrySSH(ctx, cfg, host.SSHHost, timeout, []string{
		"journalctl",
		"-u", host.Unit,
		"--since=" + start.UTC().Format(time.RFC3339),
		"--until=" + end.UTC().Format(time.RFC3339),
		"--no-pager",
		"-o", "cat",
	})
	if err != nil {
		return nil, err
	}
	var samples []StreamingSummarySample
	for _, line := range strings.Split(string(out), "\n") {
		sample, ok := parseStreamingSummaryLine(line)
		if ok {
			samples = append(samples, sample)
		}
	}
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].Timestamp.Before(samples[j].Timestamp)
	})
	return samples, nil
}

func parseStreamingSummaryLine(line string) (StreamingSummarySample, bool) {
	const marker = "orchestrator: streaming summary "
	idx := strings.Index(line, marker)
	if idx < 0 {
		return StreamingSummarySample{}, false
	}
	sample := StreamingSummarySample{}
	if ts := streamingSummaryTimestampPattern.FindString(line[:idx]); ts != "" {
		if t, err := time.ParseInLocation("2006/01/02 15:04:05", ts, time.UTC); err == nil {
			sample.Timestamp = t.UTC()
		}
	}
	values := map[string]string{}
	for _, field := range strings.Fields(line[idx+len(marker):]) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			continue
		}
		values[key] = value
	}
	sample.Active = intValue(values, "active")
	sample.RequiredRate = rateValue(values, "required_rate")
	sample.Selected = intValue(values, "selected")
	sample.Dispatched = intValue(values, "dispatched")
	sample.Completed = intValue(values, "completed")
	sample.SideEffects = intValue(values, "side_effects")
	sample.Pending = intValue(values, "pending")
	sample.ActiveChecks = intValue(values, "active_checks")
	sample.QueueDepth = intValue(values, "queue_depth")
	sample.ResultDepth = intValue(values, "result_depth")
	sample.SideEffectDepth = intValue(values, "side_effect_depth")
	sample.Workers = intValue(values, "workers")
	sample.WorkerTarget = intValue(values, "worker_target")
	sample.SPS = intValue(values, "sps")
	sample.ElapsedMS = durationMS(values, "elapsed")
	sample.MaxLagMS = durationMS(values, "max_lag")
	sample.AvgLatencyMS = durationMS(values, "avg_latency")
	sample.ScaleLatencyMS = durationMS(values, "scale_latency")
	sample.Successes = intValue(values, "successes")
	sample.Failures = intValue(values, "failures")
	sample.FailurePressure = boolValue(values, "failure_pressure")
	sample.ErrorTimeout = intValue(values, "error_timeout")
	sample.ErrorConnect = intValue(values, "error_connect")
	sample.ErrorSSL = intValue(values, "error_ssl")
	sample.ErrorRedirect = intValue(values, "error_redirect")
	sample.ErrorKeyword = intValue(values, "error_keyword")
	sample.ErrorBodyRead = intValue(values, "error_body_read")
	sample.ErrorTLSExpired = intValue(values, "error_tls_expired")
	sample.ErrorTLSDeprecated = intValue(values, "error_tls_deprecated")
	sample.ErrorOther = intValue(values, "error_other")
	sample.HistoryRows = intValue(values, "history_rows")
	sample.SSLRows = intValue(values, "ssl_rows")
	sample.StaleResults = intValue(values, "stale_results")
	sample.BackpressureWaits = intValue(values, "backpressure_waits")
	sample.SideEffectWaits = intValue(values, "side_effect_waits")
	sample.ResultPauses = intValue(values, "result_pauses")
	sample.SideEffectPauses = intValue(values, "side_effect_pauses")
	sample.DispatchLimited = intValue(values, "dispatch_limited")
	return sample, true
}

func aggregateStreamingSummarySamples(samples []StreamingSummarySample) StreamingSummaryAggregate {
	agg := StreamingSummaryAggregate{Samples: len(samples)}
	if len(samples) == 0 {
		return agg
	}
	var spsTotal float64
	var requiredTotal float64
	for _, sample := range samples {
		agg.Completed += sample.Completed
		agg.Selected += sample.Selected
		agg.Dispatched += sample.Dispatched
		agg.Successes += sample.Successes
		agg.Failures += sample.Failures
		agg.StaleResults += sample.StaleResults
		agg.HistoryRows += sample.HistoryRows
		agg.SSLRows += sample.SSLRows
		agg.BackpressureWaits += sample.BackpressureWaits
		agg.SideEffectWaits += sample.SideEffectWaits
		agg.ResultPauses += sample.ResultPauses
		agg.SideEffectPauses += sample.SideEffectPauses
		agg.DispatchLimited += sample.DispatchLimited
		spsTotal += float64(sample.SPS)
		requiredTotal += sample.RequiredRate
		if sample.SPS > agg.SPSMax {
			agg.SPSMax = sample.SPS
		}
		if sample.RequiredRate > agg.RequiredRateMax {
			agg.RequiredRateMax = sample.RequiredRate
		}
		if sample.MaxLagMS > agg.MaxLagMSMax {
			agg.MaxLagMSMax = sample.MaxLagMS
		}
		if sample.Pending > agg.PendingMax {
			agg.PendingMax = sample.Pending
		}
		if sample.QueueDepth > agg.QueueDepthMax {
			agg.QueueDepthMax = sample.QueueDepth
		}
		if sample.ResultDepth > agg.ResultDepthMax {
			agg.ResultDepthMax = sample.ResultDepth
		}
		if sample.SideEffectDepth > agg.SideEffectDepthMax {
			agg.SideEffectDepthMax = sample.SideEffectDepth
		}
		if sample.Workers > agg.WorkerMax {
			agg.WorkerMax = sample.Workers
		}
	}
	agg.SPSAverage = spsTotal / float64(len(samples))
	agg.RequiredRateAverage = requiredTotal / float64(len(samples))
	return agg
}

func intValue(values map[string]string, key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(values[key]))
	return n
}

func rateValue(values map[string]string, key string) float64 {
	raw := strings.TrimSuffix(strings.TrimSpace(values[key]), "/s")
	n, _ := strconv.ParseFloat(raw, 64)
	return n
}

func boolValue(values map[string]string, key string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(values[key]))
	return v
}

func durationMS(values map[string]string, key string) int64 {
	d, err := time.ParseDuration(strings.TrimSpace(values[key]))
	if err != nil {
		return 0
	}
	return d.Milliseconds()
}

func runStreamingTelemetrySSH(ctx context.Context, cfg StreamingTelemetryConfig, sshHost string, timeout time.Duration, remoteArgs []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{}
	if cfg.SSHConfig != "" {
		args = append(args, "-F", cfg.SSHConfig)
	}
	args = append(args, sshHost)
	args = append(args, remoteArgs...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("%v: %w: %s", append([]string{"ssh"}, args...), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (r Runner) collectStreamingTelemetry(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, start, end time.Time, m *RunManifest) error {
	if !cfg.StreamingTelemetry.Enabled {
		return nil
	}
	run, err := r.StreamingTelemetry.Collect(ctx, cfg.StreamingTelemetry, services, start, end)
	m.StreamingTelemetry = append(m.StreamingTelemetry, run)
	if writeErr := writeStreamingTelemetryArtifact(dir, "streaming-telemetry-window.json", run, m); writeErr != nil {
		return writeErr
	}
	if err != nil {
		m.StreamingTelemetryStatus = "fail"
		m.StreamingTelemetryError = err.Error()
		return err
	}
	m.StreamingTelemetryStatus = run.Status
	return nil
}

func writeStreamingTelemetryArtifact(dir, name string, run StreamingTelemetryRun, m *RunManifest) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal streaming telemetry: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".json"), Path: path})
	return nil
}

func latestStreamingTelemetry(runs []StreamingTelemetryRun) *StreamingTelemetryRun {
	for i := len(runs) - 1; i >= 0; i-- {
		return &runs[i]
	}
	return nil
}
