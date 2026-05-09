package jetmoncapacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
	"github.com/Automattic/uptime-bench/internal/reportdir"
	"github.com/Automattic/uptime-bench/internal/targetserver"
)

// RunOptions describes one capacity runner invocation.
type RunOptions struct {
	ConfigPath       string
	Mode             string
	Services         []string
	ActiveCount      int
	DurationOverride time.Duration
	CooldownOverride time.Duration
	BatchSizes       []int
	SuiteStartCount  int
	FullSuite        bool
	SuiteStatePath   string
	OutDir           string
	Description      string
	Apply            bool
	ForceReseed      bool
	PrometheusURL    string
}

// Runner executes guarded Jetmon capacity lifecycle runs.
type Runner struct {
	Executor       SQLExecutor
	Collector      PrometheusCollector
	URLChecker     TargetURLChecker
	ObserverClient TargetObserverClient
	NetworkBuckets NetworkBucketCollector
	Clock          Clock
	Sleeper        Sleeper
}

// SQLExecutor executes rendered SQL against a service DB.
type SQLExecutor interface {
	ExecuteSQL(ctx context.Context, dsn string, sqlText string) (SQLExecutionResult, error)
}

// PrometheusCollector captures one Prometheus window.
type PrometheusCollector interface {
	Collect(ctx context.Context, promURL string, instances []string, start, end time.Time, step, rateWindow time.Duration) (capacitybench.Report, error)
}

// Clock is injected so run windows can be tested without wall-clock sleeps.
type Clock interface {
	Now() time.Time
}

// Sleeper waits for batch windows and cooldowns.
type Sleeper interface {
	Sleep(ctx context.Context, duration time.Duration) error
}

// DefaultSQLExecutor uses the package MySQL executor.
type DefaultSQLExecutor struct{}

// ExecuteSQL executes SQL on a MySQL DSN.
func (DefaultSQLExecutor) ExecuteSQL(ctx context.Context, dsn string, sqlText string) (SQLExecutionResult, error) {
	return ExecuteSQL(ctx, dsn, sqlText)
}

// DefaultPrometheusCollector uses capacitybench's default query set.
type DefaultPrometheusCollector struct{}

// Collect captures a capacity metrics window.
func (DefaultPrometheusCollector) Collect(ctx context.Context, promURL string, instances []string, start, end time.Time, step, rateWindow time.Duration) (capacitybench.Report, error) {
	instanceRegex, err := capacitybench.InstanceRegex(instances)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("prometheus instances: %w", err)
	}
	client := &capacitybench.PrometheusClient{
		BaseURL: promURL,
		Client:  &http.Client{Timeout: 30 * time.Second},
	}
	summaries, err := capacitybench.Collect(ctx, client, capacitybench.DefaultQueries(instanceRegex, rateWindow), start, end, step)
	if err != nil {
		return capacitybench.Report{}, fmt.Errorf("prometheus collect: %w", err)
	}
	return capacitybench.Report{
		PrometheusURL: promURL,
		Start:         start,
		End:           end,
		Step:          step.String(),
		Instances:     instances,
		Summaries:     summaries,
	}, nil
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type realSleeper struct{}

func (realSleeper) Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// RunManifest is the operator-facing artifact for one invocation or batch.
type RunManifest struct {
	ID                    string                                `json:"id"`
	Mode                  string                                `json:"mode"`
	Apply                 bool                                  `json:"apply"`
	ForceReseed           bool                                  `json:"force_reseed,omitempty"`
	ConfigPath            string                                `json:"config_path"`
	OutDir                string                                `json:"out_dir"`
	ActiveCount           int                                   `json:"active_count,omitempty"`
	BatchCount            int                                   `json:"batch_count,omitempty"`
	TotalBatchCount       int                                   `json:"total_batch_count,omitempty"`
	BatchSizes            []int                                 `json:"batch_sizes,omitempty"`
	SuiteStartCount       int                                   `json:"suite_start_count,omitempty"`
	SuiteStartSource      string                                `json:"suite_start_source,omitempty"`
	SuiteStatePath        string                                `json:"suite_state_path,omitempty"`
	BatchDuration         string                                `json:"batch_duration,omitempty"`
	Cooldown              string                                `json:"cooldown,omitempty"`
	EstimatedRuntime      string                                `json:"estimated_runtime,omitempty"`
	PrometheusURL         string                                `json:"prometheus_url,omitempty"`
	Instances             []string                              `json:"instances,omitempty"`
	Target                TargetManifest                        `json:"target,omitempty"`
	CreatedAt             time.Time                             `json:"created_at"`
	WindowStart           *time.Time                            `json:"window_start,omitempty"`
	WindowEnd             *time.Time                            `json:"window_end,omitempty"`
	DeactivatedAt         *time.Time                            `json:"deactivated_at,omitempty"`
	LifecycleStatus       string                                `json:"lifecycle_status,omitempty"`
	HealthStatus          string                                `json:"health_status,omitempty"`
	PrometheusStatus      string                                `json:"prometheus_status,omitempty"`
	PrometheusError       string                                `json:"prometheus_error,omitempty"`
	TargetObserverStatus  string                                `json:"target_observer_status,omitempty"`
	TargetObserverError   string                                `json:"target_observer_error,omitempty"`
	CapacityReplayStatus  string                                `json:"capacity_replay_status,omitempty"`
	CapacityReplayError   string                                `json:"capacity_replay_error,omitempty"`
	ReplayDetectionStatus string                                `json:"replay_detection_status,omitempty"`
	ReplayDetectionError  string                                `json:"replay_detection_error,omitempty"`
	NetworkBucketStatus   string                                `json:"network_bucket_status,omitempty"`
	NetworkBucketError    string                                `json:"network_bucket_error,omitempty"`
	CleanupStatus         string                                `json:"cleanup_status,omitempty"`
	CleanupError          string                                `json:"cleanup_error,omitempty"`
	Services              []ServiceManifest                     `json:"services"`
	Artifacts             []Artifact                            `json:"artifacts"`
	Executions            []ExecutionManifest                   `json:"executions,omitempty"`
	Health                []ServiceHealth                       `json:"health,omitempty"`
	Thresholds            []ThresholdFinding                    `json:"thresholds,omitempty"`
	TargetPreflights      []TargetPreflight                     `json:"target_preflights,omitempty"`
	TargetObservations    []targetserver.CapacityObserveSummary `json:"target_observations,omitempty"`
	CapacityReplays       []CapacityReplayRun                   `json:"capacity_replays,omitempty"`
	ReplayDetections      []ReplayDetectionRun                  `json:"replay_detections,omitempty"`
	NetworkBuckets        []NetworkBucketHostSnapshot           `json:"network_buckets,omitempty"`
	StopRecommended       bool                                  `json:"stop_recommended,omitempty"`
	StopReason            string                                `json:"stop_reason,omitempty"`
	Error                 string                                `json:"error,omitempty"`
	Notes                 []string                              `json:"notes,omitempty"`
}

// SuiteState is the persisted resume hint for subsequent run-suite invocations.
type SuiteState struct {
	ID                     string    `json:"id"`
	ConfigPath             string    `json:"config_path,omitempty"`
	LastCompletedBatch     int       `json:"last_completed_batch"`
	LastCleanBatch         int       `json:"last_clean_batch,omitempty"`
	FirstProblemBatch      int       `json:"first_problem_batch,omitempty"`
	LastCompletedAt        time.Time `json:"last_completed_at"`
	LastRunDir             string    `json:"last_run_dir"`
	LastBatchDir           string    `json:"last_batch_dir"`
	BatchDuration          string    `json:"batch_duration,omitempty"`
	StopRecommended        bool      `json:"stop_recommended,omitempty"`
	StopReason             string    `json:"stop_reason,omitempty"`
	CompletedBatchSequence []int     `json:"completed_batch_sequence,omitempty"`
}

// ServiceManifest describes one service namespace in a manifest.
type ServiceManifest struct {
	ID                   string `json:"id"`
	Schema               string `json:"schema"`
	BlogIDStart          int64  `json:"blog_id_start"`
	BlogIDEnd            int64  `json:"blog_id_end"`
	ReservedCount        int    `json:"reserved_count"`
	URLNumberStart       int64  `json:"url_number_start"`
	BucketMin            int    `json:"bucket_min"`
	BucketMax            int    `json:"bucket_max"`
	CheckIntervalMinutes int    `json:"check_interval_minutes"`
	DSNEnv               string `json:"dsn_env,omitempty"`
	DSNFile              string `json:"dsn_file,omitempty"`
	HasDSN               bool   `json:"has_dsn"`
}

// Artifact records a generated file path.
type Artifact struct {
	Service string `json:"service,omitempty"`
	Action  string `json:"action,omitempty"`
	Path    string `json:"path"`
}

// ExecutionManifest records one SQL execution.
type ExecutionManifest struct {
	Service string             `json:"service"`
	Action  string             `json:"action"`
	Result  SQLExecutionResult `json:"result"`
}

// ServiceHealth is a compact DB-derived health snapshot.
type ServiceHealth struct {
	Service                    string             `json:"service"`
	Action                     string             `json:"action"`
	Status                     string             `json:"status"`
	Reason                     string             `json:"reason,omitempty"`
	BenchmarkSites             *int64             `json:"benchmark_sites,omitempty"`
	ActiveSites                *int64             `json:"active_sites,omitempty"`
	ExpectedActiveSites        *int64             `json:"expected_active_sites,omitempty"`
	StaleActiveSites           *int64             `json:"stale_active_sites,omitempty"`
	MissedCheckPercent         *float64           `json:"missed_check_percent,omitempty"`
	OpenEvents                 *int64             `json:"open_events,omitempty"`
	RecentCheckHistoryRows     *int64             `json:"recent_check_history_rows,omitempty"`
	RecentChecksPerMinute      *float64           `json:"recent_checks_per_minute,omitempty"`
	FreshnessWindowMinutes     int                `json:"freshness_window_minutes,omitempty"`
	FreshnessSamples           *int64             `json:"freshness_samples,omitempty"`
	FreshestCheckAgeSec        *float64           `json:"freshest_check_age_sec,omitempty"`
	AverageCheckAgeSec         *float64           `json:"average_check_age_sec,omitempty"`
	P50CheckAgeSec             *float64           `json:"p50_check_age_sec,omitempty"`
	P95CheckAgeSec             *float64           `json:"p95_check_age_sec,omitempty"`
	P99CheckAgeSec             *float64           `json:"p99_check_age_sec,omitempty"`
	OldestCheckAgeSec          *float64           `json:"oldest_check_age_sec,omitempty"`
	StaleBuckets               []BucketFreshness  `json:"stale_buckets,omitempty"`
	CheckIntervals             []CheckIntervalRow `json:"check_intervals,omitempty"`
	CheckIntervalMismatchSites *int64             `json:"check_interval_mismatch_sites,omitempty"`
	FreshnessMeasured          bool               `json:"freshness_measured"`
}

// BucketFreshness summarizes stale rows in one scheduler bucket.
type BucketFreshness struct {
	BucketNo         int     `json:"bucket_no"`
	ActiveSites      int64   `json:"active_sites"`
	StaleActiveSites int64   `json:"stale_active_sites"`
	StalePercent     float64 `json:"stale_percent"`
}

// CheckIntervalRow describes active-row check interval distribution.
type CheckIntervalRow struct {
	CheckIntervalMinutes int   `json:"check_interval_minutes"`
	ActiveSites          int64 `json:"active_sites"`
}

// ThresholdFinding records one pass/fail/not-measured threshold check.
type ThresholdFinding struct {
	Name   string  `json:"name"`
	Status string  `json:"status"`
	Series string  `json:"series,omitempty"`
	Value  float64 `json:"value,omitempty"`
	Limit  float64 `json:"limit,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

// Run executes one capacity runner invocation.
func (r Runner) Run(ctx context.Context, opts RunOptions) (RunManifest, error) {
	r = r.withDefaults()
	if opts.ConfigPath == "" {
		opts.ConfigPath = "configs/capacity/jetmon.example.toml"
	}
	cfg, err := LoadRunConfig(opts.ConfigPath)
	if err != nil {
		return RunManifest{}, fmt.Errorf("load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return RunManifest{}, fmt.Errorf("config: %w", err)
	}
	services, err := cfg.ServiceLifecycles(opts.Services)
	if err != nil {
		return RunManifest{}, fmt.Errorf("services: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "" {
		mode = "plan"
	}
	if !ValidRunMode(mode) {
		return RunManifest{}, fmt.Errorf("unsupported mode %q", opts.Mode)
	}
	if opts.Apply && mode == "plan" {
		return RunManifest{}, fmt.Errorf("-apply is not valid with -mode=plan")
	}
	duration, err := cfg.BatchDuration()
	if err != nil {
		return RunManifest{}, err
	}
	if opts.DurationOverride > 0 {
		duration = opts.DurationOverride
	}
	cooldown, err := cfg.CooldownDuration()
	if err != nil {
		return RunManifest{}, err
	}
	if opts.CooldownOverride > 0 {
		cooldown = opts.CooldownOverride
	}
	batchSizes := append([]int(nil), cfg.Batches.Sizes...)
	if len(opts.BatchSizes) > 0 {
		batchSizes = append([]int(nil), opts.BatchSizes...)
	}
	if err := validateBatchSizes(batchSizes, cfg.Targets.Count); err != nil {
		return RunManifest{}, err
	}
	activeCount := opts.ActiveCount
	if activeCount == 0 && len(batchSizes) > 0 {
		activeCount = batchSizes[0]
	}
	if mode == "activate" || mode == "run-batch" {
		if activeCount <= 0 {
			return RunManifest{}, fmt.Errorf("active count must be positive")
		}
		if activeCount > cfg.Targets.Count {
			return RunManifest{}, fmt.Errorf("active count %d exceeds target count %d", activeCount, cfg.Targets.Count)
		}
	}

	outDir := opts.OutDir
	if outDir == "" {
		outDir = filepath.Join("reports", reportdir.Name(r.Clock.Now().UTC(), plannedReportDuration(mode, duration, cooldown, len(batchSizes)), capacityReportDescription(cfg.ID, mode, activeCount, opts.Description)))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return RunManifest{}, fmt.Errorf("create out dir: %w", err)
	}

	suiteStatePath := opts.SuiteStatePath
	if suiteStatePath == "" {
		suiteStatePath = defaultSuiteStatePath(outDir, cfg.ID)
	}
	suiteBatches := append([]int(nil), batchSizes...)
	suiteStartCount := 0
	suiteStartSource := ""
	var suiteSelectionNotes []string
	if mode == "run-suite" {
		selection, err := selectSuiteBatches(batchSizes, cfg.ID, suiteStatePath, opts.FullSuite, opts.SuiteStartCount)
		if err != nil {
			return RunManifest{}, err
		}
		suiteBatches = selection.Sizes
		suiteStartCount = selection.StartCount
		suiteStartSource = selection.Source
		suiteSelectionNotes = selection.Notes
	}

	manifest := RunManifest{
		ID:            cfg.ID,
		Mode:          mode,
		Apply:         opts.Apply,
		ForceReseed:   opts.ForceReseed,
		ConfigPath:    opts.ConfigPath,
		OutDir:        outDir,
		BatchDuration: duration.String(),
		Cooldown:      cooldown.String(),
		PrometheusURL: firstNonEmpty(opts.PrometheusURL, cfg.PrometheusURL),
		Instances:     cfg.Instances,
		Target:        targetManifest(cfg),
		CreatedAt:     r.Clock.Now().UTC(),
		Services:      ServiceSummaries(services),
	}
	if mode == "activate" || mode == "run-batch" {
		manifest.ActiveCount = activeCount
	}
	if mode == "run-suite" {
		manifest.BatchCount = len(suiteBatches)
		manifest.TotalBatchCount = len(batchSizes)
		manifest.BatchSizes = suiteBatches
		manifest.SuiteStartCount = suiteStartCount
		manifest.SuiteStartSource = suiteStartSource
		manifest.SuiteStatePath = suiteStatePath
		manifest.EstimatedRuntime = estimateSuiteRuntime(len(suiteBatches), duration, cooldown).String()
		manifest.Notes = append(manifest.Notes, suiteSelectionNotes...)
	}
	if !opts.Apply {
		manifest.Notes = append(manifest.Notes, "dry-run only: no live Jetmon databases were modified")
	}

	switch mode {
	case "plan":
		err = r.writeAllPlans(ctx, outDir, services, batchSizes, &manifest)
	case "seed":
		err = r.applyAction(ctx, outDir, services, OperationSeed, "seed", 0, opts.Apply, opts.ForceReseed, &manifest)
	case "activate":
		_, err = r.activateServices(ctx, outDir, services, activeCount, opts.Apply, &manifest)
	case "deactivate":
		err = r.applyAction(ctx, outDir, services, OperationDeactivate, "deactivate", 0, opts.Apply, false, &manifest)
	case "verify":
		err = r.verifyServices(ctx, outDir, services, "verify", opts.Apply, -1, &manifest)
	case "run-batch":
		err = r.runBatch(ctx, outDir, services, cfg, activeCount, duration, opts.Apply, manifest.PrometheusURL, &manifest)
	case "run-suite":
		err = r.runSuite(ctx, outDir, services, cfg, suiteBatches, duration, cooldown, opts.Apply, opts.ForceReseed, manifest.PrometheusURL, suiteStatePath, &manifest)
	}
	if err != nil {
		manifest.Error = err.Error()
	}
	if summaryErr := WriteSummary(outDir, manifest); summaryErr != nil {
		err = errors.Join(err, fmt.Errorf("write summary: %w", summaryErr))
	} else {
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Action: "summary", Path: filepath.Join(outDir, "summary.txt")})
	}
	if writeErr := WriteManifest(outDir, manifest); writeErr != nil {
		err = errors.Join(err, fmt.Errorf("write manifest: %w", writeErr))
	}
	return manifest, err
}

func (r Runner) withDefaults() Runner {
	if r.Executor == nil {
		r.Executor = DefaultSQLExecutor{}
	}
	if r.Collector == nil {
		r.Collector = DefaultPrometheusCollector{}
	}
	if r.URLChecker == nil {
		r.URLChecker = defaultTargetURLChecker{}
	}
	if r.ObserverClient == nil {
		r.ObserverClient = DefaultTargetObserverClient{}
	}
	if r.NetworkBuckets == nil {
		r.NetworkBuckets = DefaultNetworkBucketCollector{}
	}
	if r.Clock == nil {
		r.Clock = realClock{}
	}
	if r.Sleeper == nil {
		r.Sleeper = realSleeper{}
	}
	return r
}

type suiteSelection struct {
	Sizes      []int
	StartCount int
	Source     string
	Notes      []string
}

func selectSuiteBatches(sizes []int, id, statePath string, fullSuite bool, explicitStart int) (suiteSelection, error) {
	if len(sizes) == 0 {
		return suiteSelection{}, fmt.Errorf("run-suite requires at least one batch size")
	}
	if fullSuite {
		if explicitStart > 0 {
			return suiteSelection{}, fmt.Errorf("-full-suite and -suite-start-count cannot be used together")
		}
		return suiteSelection{
			Sizes:      append([]int(nil), sizes...),
			StartCount: sizes[0],
			Source:     "full_suite",
			Notes:      []string{"full suite requested: prior suite state ignored"},
		}, nil
	}

	start := explicitStart
	source := "first_configured"
	var notes []string
	if start > 0 {
		source = "explicit"
	} else {
		state, err := readSuiteState(statePath)
		if err != nil {
			return suiteSelection{}, err
		}
		if state != nil {
			resumeBatch := state.LastCleanBatch
			resumeKind := "last clean"
			if resumeBatch <= 0 {
				resumeBatch = state.LastCompletedBatch
				resumeKind = "last completed"
			}
			if state.ID == id && resumeBatch > 0 {
				start = resumeBatch
				source = "state"
				notes = append(notes, fmt.Sprintf("resuming run-suite from %s batch %d recorded in %s", resumeKind, start, statePath))
				if state.FirstProblemBatch > 0 {
					notes = append(notes, fmt.Sprintf("prior suite first problem batch was %d: %s", state.FirstProblemBatch, state.StopReason))
				}
			} else if state.ID != "" && state.ID != id {
				notes = append(notes, fmt.Sprintf("suite state %s belongs to %q; starting from first configured batch for %q", statePath, state.ID, id))
			}
		}
	}
	if start <= 0 {
		start = sizes[0]
	}

	selected, actualStart := batchSizesFrom(sizes, start)
	if len(selected) == 0 {
		return suiteSelection{}, fmt.Errorf("no batch sizes selected")
	}
	if actualStart != start {
		notes = append(notes, fmt.Sprintf("requested suite start %d is not configured; starting at next available batch %d", start, actualStart))
	}
	return suiteSelection{
		Sizes:      selected,
		StartCount: actualStart,
		Source:     source,
		Notes:      notes,
	}, nil
}

func batchSizesFrom(sizes []int, start int) ([]int, int) {
	if len(sizes) == 0 {
		return nil, 0
	}
	for i, size := range sizes {
		if size >= start {
			return append([]int(nil), sizes[i:]...), size
		}
	}
	last := sizes[len(sizes)-1]
	return []int{last}, last
}

func readSuiteState(path string) (*SuiteState, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read suite state %s: %w", path, err)
	}
	var state SuiteState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parse suite state %s: %w", path, err)
	}
	return &state, nil
}

func writeSuiteState(path string, state SuiteState) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func defaultSuiteStatePath(outDir, id string) string {
	return filepath.Join(filepath.Dir(outDir), safeName(id)+"-suite-state.json")
}

func validateBatchSizes(sizes []int, targetCount int) error {
	for i, size := range sizes {
		if size <= 0 {
			return fmt.Errorf("batch sizes must be positive")
		}
		if i > 0 && size <= sizes[i-1] {
			return fmt.Errorf("batch sizes must be strictly increasing")
		}
		if targetCount > 0 && size > targetCount {
			return fmt.Errorf("batch size %d exceeds target count %d", size, targetCount)
		}
	}
	return nil
}

func (r Runner) writeAllPlans(ctx context.Context, dir string, services []ServiceLifecycle, sizes []int, m *RunManifest) error {
	if err := r.applyAction(ctx, dir, services, OperationSeed, "seed", 0, false, false, m); err != nil {
		return err
	}
	for _, size := range sizes {
		if err := r.applyAction(ctx, dir, services, OperationActivate, "activate", size, false, false, m); err != nil {
			return err
		}
	}
	if err := r.applyAction(ctx, dir, services, OperationDeactivate, "deactivate", 0, false, false, m); err != nil {
		return err
	}
	return r.verifyServices(ctx, dir, services, "verify", false, 0, m)
}

func (r Runner) runBatch(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, activeCount int, duration time.Duration, apply bool, promURL string, m *RunManifest) (err error) {
	var activated []ServiceLifecycle
	defer func() {
		if !apply || len(activated) == 0 {
			return
		}
		cleanupCtx, cancel := cleanupContext()
		defer cancel()
		cleanupErr := r.applyAction(cleanupCtx, dir, activated, OperationDeactivate, "cleanup-deactivate", 0, true, false, m)
		if cleanupErr != nil {
			m.CleanupStatus = "fail"
			m.CleanupError = cleanupErr.Error()
			err = errors.Join(err, fmt.Errorf("cleanup failed: %w", cleanupErr))
		} else {
			m.CleanupStatus = "pass"
		}
	}()

	if apply {
		if err := r.preflightPrometheus(ctx, cfg, promURL, m); err != nil {
			return err
		}
	}

	m.LifecycleStatus = "running"
	if apply {
		activated, err = r.activateServices(ctx, dir, services, activeCount, true, m)
	} else {
		err = r.activateServicesDryRun(ctx, dir, services, activeCount, m)
	}
	if err != nil {
		m.LifecycleStatus = "fail"
		return err
	}
	if !apply {
		if err := r.applyAction(ctx, dir, services, OperationDeactivate, "deactivate", 0, false, false, m); err != nil {
			m.LifecycleStatus = "fail"
			return err
		}
		m.LifecycleStatus = "pass"
		return r.verifyServices(ctx, dir, services, "verify", false, activeCount, m)
	}
	if err := r.preflightActivatedTargets(ctx, services, cfg, activeCount, m); err != nil {
		m.LifecycleStatus = "fail"
		return err
	}
	if err := r.resetTargetObserver(ctx, dir, services, cfg, activeCount, m); err != nil {
		m.LifecycleStatus = "fail"
		return err
	}
	if err := r.resetNetworkBuckets(ctx, dir, services, cfg, m); err != nil {
		m.LifecycleStatus = "fail"
		return err
	}

	start := r.Clock.Now().UTC()
	m.WindowStart = &start
	replayHandle, err := r.startCapacityReplay(ctx, dir, services, cfg, activeCount, duration, m)
	if err != nil {
		m.CapacityReplayStatus = "fail"
		m.CapacityReplayError = err.Error()
		m.LifecycleStatus = "fail"
		return err
	}
	if err := WriteManifest(dir, *m); err != nil {
		m.LifecycleStatus = "fail"
		return fmt.Errorf("write activation manifest: %w", err)
	}
	if err := r.Sleeper.Sleep(ctx, duration); err != nil {
		m.LifecycleStatus = "fail"
		return fmt.Errorf("batch window interrupted: %w", err)
	}
	end := r.Clock.Now().UTC()
	m.WindowEnd = &end
	if err := r.finishCapacityReplay(ctx, dir, replayHandle, m); err != nil {
		m.CapacityReplayStatus = "fail"
		m.CapacityReplayError = err.Error()
		m.Notes = append(m.Notes, "Capacity replay failed: "+err.Error())
	}
	if err := r.collectReplayDetections(ctx, dir, services, cfg, m); err != nil {
		m.ReplayDetectionStatus = "fail"
		m.ReplayDetectionError = err.Error()
		m.Notes = append(m.Notes, "Replay detection correlation failed: "+err.Error())
	}
	if err := r.snapshotTargetObserver(ctx, dir, cfg, m); err != nil {
		m.TargetObserverStatus = "fail"
		m.TargetObserverError = err.Error()
		m.Notes = append(m.Notes, "Target observer snapshot failed: "+err.Error())
	}
	if err := r.snapshotNetworkBuckets(ctx, dir, services, cfg, m); err != nil {
		m.NetworkBucketStatus = "fail"
		m.NetworkBucketError = err.Error()
		m.Notes = append(m.Notes, "Network bucket snapshot failed: "+err.Error())
	}

	if err := r.verifyServices(ctx, dir, services, "window-end-verify", true, activeCount, m); err != nil {
		m.LifecycleStatus = "fail"
		return err
	}
	if err := r.applyAction(ctx, dir, services, OperationDeactivate, "deactivate", 0, true, false, m); err != nil {
		m.CleanupStatus = "fail"
		m.CleanupError = err.Error()
		return err
	}
	activated = nil
	deactivatedAt := r.Clock.Now().UTC()
	m.DeactivatedAt = &deactivatedAt
	m.CleanupStatus = "deactivated"

	if err := r.collectPrometheus(ctx, dir, cfg, promURL, start, end, m); err != nil {
		m.PrometheusStatus = "fail"
		m.PrometheusError = err.Error()
		m.Notes = append(m.Notes, "Prometheus capture failed: "+err.Error())
	}
	if err := r.verifyServices(ctx, dir, services, "post-deactivate-verify", true, 0, m); err != nil {
		m.CleanupStatus = "fail"
		m.CleanupError = err.Error()
		return err
	}
	m.CleanupStatus = "pass"
	m.Thresholds = append(m.Thresholds, EvaluateThresholds(m.Health, m.PrometheusURL, cfg.StopThreshold, m.loadPrometheusReport(dir))...)
	setHealthStatus(m)
	setStopRecommendation(m)
	m.LifecycleStatus = "pass"
	return nil
}

func (r Runner) runSuite(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, sizes []int, duration, cooldown time.Duration, apply, forceReseed bool, promURL string, suiteStatePath string, m *RunManifest) error {
	if apply {
		if err := r.preflightPrometheus(ctx, cfg, promURL, m); err != nil {
			return err
		}
		if err := r.collectBaseline(ctx, dir, cfg, promURL, m); err != nil {
			return err
		}
	}
	var completed []int
	var children []RunManifest
	lastCleanBatch := 0
	firstProblemBatch := 0
	if apply && len(sizes) > 0 {
		state, err := readSuiteState(suiteStatePath)
		if err != nil {
			return err
		}
		if state != nil && state.ID == cfg.ID && state.LastCleanBatch > 0 && state.LastCleanBatch < sizes[0] {
			lastCleanBatch = state.LastCleanBatch
			m.Notes = append(m.Notes, fmt.Sprintf("preserving prior last clean batch %d from %s until a higher batch passes", lastCleanBatch, suiteStatePath))
		}
	}
	writeRollup := func() error {
		if len(children) == 0 {
			return nil
		}
		if err := writeSuiteReport(dir, *m, children); err != nil {
			return err
		}
		recordArtifactOnce(m, Artifact{Action: "capacity-report", Path: filepath.Join(dir, "capacity.md")})
		recordArtifactOnce(m, Artifact{Action: "capacity-json", Path: filepath.Join(dir, "capacity.json")})
		return nil
	}
	for i, size := range sizes {
		batchDir := filepath.Join(dir, fmt.Sprintf("batch-%07d", size))
		if err := os.MkdirAll(batchDir, 0o755); err != nil {
			return fmt.Errorf("create batch dir %s: %w", batchDir, err)
		}
		child := RunManifest{
			ID:            cfg.ID,
			Mode:          "run-batch",
			Apply:         apply,
			ForceReseed:   forceReseed,
			ConfigPath:    m.ConfigPath,
			OutDir:        batchDir,
			ActiveCount:   size,
			BatchDuration: duration.String(),
			Cooldown:      cooldown.String(),
			PrometheusURL: promURL,
			Instances:     cfg.Instances,
			Target:        targetManifest(cfg),
			CreatedAt:     r.Clock.Now().UTC(),
			Services:      m.Services,
		}
		if !apply {
			child.Notes = append(child.Notes, "dry-run only: no live Jetmon databases were modified")
		}
		if err := r.runBatch(ctx, batchDir, services, cfg, size, duration, apply, promURL, &child); err != nil {
			child.Error = err.Error()
			if WriteSummary(batchDir, child) == nil {
				child.Artifacts = append(child.Artifacts, Artifact{Action: "summary", Path: filepath.Join(batchDir, "summary.txt")})
			}
			_ = WriteManifest(batchDir, child)
			children = append(children, child)
			if reportErr := writeRollup(); reportErr != nil {
				return errors.Join(fmt.Errorf("batch %d: %w", size, err), fmt.Errorf("write suite report: %w", reportErr))
			}
			return fmt.Errorf("batch %d: %w", size, err)
		}
		if err := WriteSummary(batchDir, child); err != nil {
			return fmt.Errorf("write batch %d summary: %w", size, err)
		}
		child.Artifacts = append(child.Artifacts, Artifact{Action: "summary", Path: filepath.Join(batchDir, "summary.txt")})
		if err := WriteManifest(batchDir, child); err != nil {
			return fmt.Errorf("write batch %d manifest: %w", size, err)
		}
		children = append(children, child)
		m.Artifacts = append(m.Artifacts, Artifact{
			Action: fmt.Sprintf("batch-%d", size),
			Path:   filepath.Join(batchDir, "run.json"),
		})
		completed = append(completed, size)
		if suiteBatchStatus(child) == "pass" {
			lastCleanBatch = size
		} else if firstProblemBatch == 0 {
			firstProblemBatch = size
		}
		if apply {
			if err := writeSuiteState(suiteStatePath, SuiteState{
				ID:                     cfg.ID,
				ConfigPath:             m.ConfigPath,
				LastCompletedBatch:     size,
				LastCleanBatch:         lastCleanBatch,
				FirstProblemBatch:      firstProblemBatch,
				LastCompletedAt:        r.Clock.Now().UTC(),
				LastRunDir:             dir,
				LastBatchDir:           batchDir,
				BatchDuration:          duration.String(),
				StopRecommended:        child.StopRecommended,
				StopReason:             child.StopReason,
				CompletedBatchSequence: append([]int(nil), completed...),
			}); err != nil {
				return fmt.Errorf("write suite state after batch %d: %w", size, err)
			}
			m.Artifacts = append(m.Artifacts, Artifact{Action: "suite-state", Path: suiteStatePath})
		}
		if child.StopRecommended {
			m.StopRecommended = true
			m.StopReason = fmt.Sprintf("stopped after batch %d: %s", size, child.StopReason)
			m.Notes = append(m.Notes, m.StopReason)
			break
		}
		if apply && i < len(sizes)-1 {
			if err := r.Sleeper.Sleep(ctx, cooldown); err != nil {
				return fmt.Errorf("cooldown after batch %d interrupted: %w", size, err)
			}
		}
	}
	return writeRollup()
}

func (r Runner) activateServicesDryRun(ctx context.Context, dir string, services []ServiceLifecycle, activeCount int, m *RunManifest) error {
	return r.applyAction(ctx, dir, services, OperationActivate, "activate", activeCount, false, false, m)
}

func (r Runner) activateServices(ctx context.Context, dir string, services []ServiceLifecycle, activeCount int, apply bool, m *RunManifest) ([]ServiceLifecycle, error) {
	var cleanupCandidates []ServiceLifecycle
	for _, service := range services {
		if apply {
			cleanupCandidates = append(cleanupCandidates, service)
		}
		if err := r.applyAction(ctx, dir, []ServiceLifecycle{service}, OperationActivate, "activate", activeCount, apply, false, m); err != nil {
			if apply {
				cleanupCtx, cancel := cleanupContext()
				_ = r.applyAction(cleanupCtx, dir, cleanupCandidates, OperationDeactivate, "partial-activation-cleanup", 0, true, false, m)
				cancel()
			}
			return nil, err
		}
		if apply {
			if err := r.verifyActiveCount(ctx, service, activeCount, m); err != nil {
				cleanupCtx, cancel := cleanupContext()
				_ = r.applyAction(cleanupCtx, dir, cleanupCandidates, OperationDeactivate, "partial-activation-cleanup", 0, true, false, m)
				cancel()
				return nil, err
			}
			if err := r.verifyActiveCheckInterval(ctx, service, activeCount, m); err != nil {
				cleanupCtx, cancel := cleanupContext()
				_ = r.applyAction(cleanupCtx, dir, cleanupCandidates, OperationDeactivate, "partial-activation-cleanup", 0, true, false, m)
				cancel()
				return nil, err
			}
		}
	}
	return cleanupCandidates, nil
}

func (r Runner) applyAction(ctx context.Context, dir string, services []ServiceLifecycle, op Operation, label string, activeCount int, apply bool, forceReseed bool, m *RunManifest) error {
	for _, service := range services {
		if apply && op == OperationSeed {
			if err := r.assertSeedSafe(ctx, service, forceReseed, m); err != nil {
				return err
			}
		}
		plan := Plan{Action: op, Config: service.Config}
		if op == OperationActivate {
			plan.ActiveCount = activeCount
		}
		sqlText, err := RenderSQL(plan)
		if err != nil {
			return fmt.Errorf("render %s %s: %w", service.ID, label, err)
		}
		path := filepath.Join(dir, sqlFilename(service.ID, label, activeCount))
		if err := os.WriteFile(path, []byte(sqlText), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		m.Artifacts = append(m.Artifacts, Artifact{Service: service.ID, Action: label, Path: path})
		if apply {
			result, err := r.execServiceSQL(ctx, service, sqlText)
			if err != nil {
				return fmt.Errorf("apply %s %s: %w", service.ID, label, err)
			}
			m.Executions = append(m.Executions, ExecutionManifest{Service: service.ID, Action: label, Result: result})
		}
	}
	return nil
}

func (r Runner) verifyServices(ctx context.Context, dir string, services []ServiceLifecycle, label string, apply bool, expectedActive int, m *RunManifest) error {
	for _, service := range services {
		sqlText, err := RenderSQL(Plan{Action: OperationVerify, Config: service.Config})
		if err != nil {
			return fmt.Errorf("render %s %s: %w", service.ID, label, err)
		}
		path := filepath.Join(dir, sqlFilename(service.ID, Operation(label), 0))
		if err := os.WriteFile(path, []byte(sqlText), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		m.Artifacts = append(m.Artifacts, Artifact{Service: service.ID, Action: label, Path: path})
		if !apply {
			continue
		}
		result, err := r.execServiceSQL(ctx, service, sqlText)
		if err != nil {
			return fmt.Errorf("apply %s %s: %w", service.ID, label, err)
		}
		m.Executions = append(m.Executions, ExecutionManifest{Service: service.ID, Action: label, Result: result})
		m.Health = append(m.Health, serviceHealthFromVerify(service, label, result, expectedActive))
	}
	return nil
}

func (r Runner) verifyActiveCount(ctx context.Context, service ServiceLifecycle, expected int, m *RunManifest) error {
	sqlText, err := RenderActiveCountSQL(service.Config)
	if err != nil {
		return fmt.Errorf("render %s active-count verification: %w", service.ID, err)
	}
	result, err := r.execServiceSQL(ctx, service, sqlText)
	if err != nil {
		return fmt.Errorf("verify %s active count: %w", service.ID, err)
	}
	m.Executions = append(m.Executions, ExecutionManifest{Service: service.ID, Action: "active-count-verify", Result: result})
	got, ok := firstInt64(result, "active_sites")
	if !ok {
		return fmt.Errorf("%s active-count verification returned no active_sites", service.ID)
	}
	want := int64(expected)
	status := "pass"
	reason := ""
	if got != want {
		status = "fail"
		reason = fmt.Sprintf("active_sites=%d, want %d", got, want)
	}
	m.Health = append(m.Health, ServiceHealth{
		Service:             service.ID,
		Action:              "active-count-verify",
		Status:              status,
		Reason:              reason,
		ActiveSites:         &got,
		ExpectedActiveSites: &want,
		FreshnessMeasured:   false,
	})
	if got != want {
		return fmt.Errorf("%s active count = %d, want %d", service.ID, got, want)
	}
	return nil
}

func (r Runner) verifyActiveCheckInterval(ctx context.Context, service ServiceLifecycle, expectedActive int, m *RunManifest) error {
	sqlText, err := RenderActiveCheckIntervalSQL(service.Config)
	if err != nil {
		return fmt.Errorf("render %s active check-interval verification: %w", service.ID, err)
	}
	result, err := r.execServiceSQL(ctx, service, sqlText)
	if err != nil {
		return fmt.Errorf("verify %s active check interval: %w", service.ID, err)
	}
	m.Executions = append(m.Executions, ExecutionManifest{Service: service.ID, Action: "active-check-interval-verify", Result: result})
	rows := checkIntervalRows(result)
	expectedInterval := service.Config.CheckIntervalMinutes
	var total, mismatched int64
	for _, row := range rows {
		total += row.ActiveSites
		if row.CheckIntervalMinutes != expectedInterval {
			mismatched += row.ActiveSites
		}
	}
	status := "pass"
	reason := ""
	if total != int64(expectedActive) {
		status = "fail"
		reason = fmt.Sprintf("active interval rows=%d, want %d", total, expectedActive)
	} else if mismatched > 0 {
		status = "fail"
		reason = fmt.Sprintf("%d active sites have check_interval different from %dm", mismatched, expectedInterval)
	}
	expected := int64(expectedActive)
	health := ServiceHealth{
		Service:                    service.ID,
		Action:                     "active-check-interval-verify",
		Status:                     status,
		Reason:                     reason,
		ActiveSites:                &total,
		ExpectedActiveSites:        &expected,
		CheckIntervals:             rows,
		CheckIntervalMismatchSites: &mismatched,
		FreshnessMeasured:          false,
	}
	m.Health = append(m.Health, health)
	if status != "pass" {
		return fmt.Errorf("%s active check interval verification failed: %s", service.ID, reason)
	}
	return nil
}

func (r Runner) assertSeedSafe(ctx context.Context, service ServiceLifecycle, force bool, m *RunManifest) error {
	sqlText, err := RenderSeedSafetySQL(service.Config)
	if err != nil {
		return fmt.Errorf("render %s seed preflight: %w", service.ID, err)
	}
	result, err := r.execServiceSQL(ctx, service, sqlText)
	if err != nil {
		return fmt.Errorf("seed preflight %s: %w", service.ID, err)
	}
	m.Executions = append(m.Executions, ExecutionManifest{Service: service.ID, Action: "seed-preflight", Result: result})
	total, _ := firstInt64(result, "total_rows")
	matching, _ := firstInt64(result, "matching_url_rows")
	if total == 0 {
		return nil
	}
	if matching != total {
		return fmt.Errorf("%s seed preflight found %d rows in reserved range, but only %d match the generated URL namespace", service.ID, total, matching)
	}
	if !force {
		return fmt.Errorf("%s seed preflight found %d existing benchmark-looking rows; rerun with -force-reseed to delete and recreate them", service.ID, total)
	}
	return nil
}

func (r Runner) execServiceSQL(ctx context.Context, service ServiceLifecycle, sqlText string) (SQLExecutionResult, error) {
	if !service.HasDSN {
		var hints []string
		if service.DSNEnv != "" {
			hints = append(hints, "set "+service.DSNEnv)
		}
		if service.DSNFile != "" {
			hints = append(hints, "create dsn_file "+service.DSNFile)
		}
		if len(hints) == 0 {
			hints = append(hints, "configure dsn_env or dsn_file")
		}
		return SQLExecutionResult{}, fmt.Errorf("%s requires a DSN; %s", service.ID, strings.Join(hints, " or "))
	}
	return r.Executor.ExecuteSQL(ctx, service.DSN, sqlText)
}

func (r Runner) collectBaseline(ctx context.Context, dir string, cfg RunConfig, promURL string, m *RunManifest) error {
	duration, err := cfg.BaselineDuration()
	if err != nil {
		return err
	}
	end := r.Clock.Now().UTC()
	start := end.Add(-duration)
	return r.collectPrometheusTo(ctx, filepath.Join(dir, "prometheus-baseline.json"), cfg, promURL, start, end, m)
}

func (r Runner) collectPrometheus(ctx context.Context, dir string, cfg RunConfig, promURL string, start, end time.Time, m *RunManifest) error {
	return r.collectPrometheusTo(ctx, filepath.Join(dir, "prometheus-window.json"), cfg, promURL, start, end, m)
}

func (r Runner) resetTargetObserver(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, activeCount int, m *RunManifest) error {
	cfg = cfg.Normalize()
	if !cfg.TargetObserver.Enabled {
		return nil
	}
	timeout, err := cfg.TargetObserverTimeout()
	if err != nil {
		m.TargetObserverStatus = "preflight_failed"
		m.TargetObserverError = err.Error()
		return err
	}
	token, err := resolveTargetObserverToken(cfg.TargetObserver)
	if err != nil {
		m.TargetObserverStatus = "preflight_failed"
		m.TargetObserverError = err.Error()
		return err
	}
	checkInterval, err := cfg.CheckIntervalDuration()
	if err != nil {
		m.TargetObserverStatus = "preflight_failed"
		m.TargetObserverError = err.Error()
		return err
	}
	staleAfter, err := cfg.TargetObserverStaleAfter()
	if err != nil {
		m.TargetObserverStatus = "preflight_failed"
		m.TargetObserverError = err.Error()
		return err
	}
	req := targetserver.CapacityObserveResetRequest{
		RunID:                capacityObserverRunID(m),
		ActiveCount:          activeCount,
		CheckIntervalSeconds: int(checkInterval / time.Second),
		Services:             make([]targetserver.CapacityObserveService, 0, len(services)),
	}
	if staleAfter > 0 {
		req.StaleAfterSeconds = int(staleAfter / time.Second)
	}
	for _, service := range services {
		serviceStaleAfterSeconds := service.Config.CheckIntervalMinutes * 2 * 60
		if staleAfter > 0 {
			serviceStaleAfterSeconds = int(staleAfter / time.Second)
		}
		req.Services = append(req.Services, targetserver.CapacityObserveService{
			ID:                   service.ID,
			HostPattern:          cfg.Targets.HostPattern,
			URLStart:             service.Config.URLNumberStart,
			Count:                activeCount,
			CheckIntervalSeconds: service.Config.CheckIntervalMinutes * 60,
			StaleAfterSeconds:    serviceStaleAfterSeconds,
		})
	}
	summary, err := r.ObserverClient.Reset(ctx, cfg.TargetObserver.TargetControlURL, token, req, timeout)
	if err != nil {
		m.TargetObserverStatus = "preflight_failed"
		m.TargetObserverError = err.Error()
		return fmt.Errorf("target observer reset: %w", err)
	}
	m.TargetObservations = append(m.TargetObservations, summary)
	m.TargetObserverStatus = "running"
	m.TargetObserverError = ""
	return writeTargetObserverArtifact(dir, "target-observer-reset.json", summary, m)
}

func (r Runner) snapshotTargetObserver(ctx context.Context, dir string, cfg RunConfig, m *RunManifest) error {
	cfg = cfg.Normalize()
	if !cfg.TargetObserver.Enabled {
		return nil
	}
	timeout, err := cfg.TargetObserverTimeout()
	if err != nil {
		return err
	}
	token, err := resolveTargetObserverToken(cfg.TargetObserver)
	if err != nil {
		return err
	}
	summary, err := r.ObserverClient.Summary(ctx, cfg.TargetObserver.TargetControlURL, token, timeout)
	if err != nil {
		return fmt.Errorf("target observer summary: %w", err)
	}
	m.TargetObservations = append(m.TargetObservations, summary)
	findings := EvaluateTargetObserverThresholds(summary, cfg.TargetObserver)
	m.Thresholds = append(m.Thresholds, findings...)
	if finding := firstFailedThreshold(findings); finding != nil {
		m.TargetObserverStatus = "fail"
		m.TargetObserverError = formatThresholdFailure(*finding)
		m.Notes = append(m.Notes, "Target observer threshold failed: "+m.TargetObserverError)
	} else {
		m.TargetObserverStatus = "pass"
		m.TargetObserverError = ""
	}
	return writeTargetObserverArtifact(dir, "target-observer-window.json", summary, m)
}

// EvaluateTargetObserverThresholds converts target-side black-box observation
// gaps into normal capacity threshold findings. Defaults are intentionally
// strict: any never-seen or stale generated target means the batch was not
// clean. Set the corresponding max value to -1 to disable a check.
func EvaluateTargetObserverThresholds(summary targetserver.CapacityObserveSummary, cfg TargetObserverConfig) []ThresholdFinding {
	if !cfg.Enabled {
		return nil
	}
	var findings []ThresholdFinding
	for _, service := range summary.Services {
		series := service.ID
		if series == "" {
			series = service.HostPattern
		}
		if cfg.MaxNeverSeenSites >= 0 {
			status := "pass"
			reason := ""
			if service.NeverSeenSites > cfg.MaxNeverSeenSites {
				status = "fail"
				reason = fmt.Sprintf("%d target sites were never observed; limit is %d", service.NeverSeenSites, cfg.MaxNeverSeenSites)
			}
			findings = append(findings, ThresholdFinding{
				Name:   "target_observer_never_seen_sites",
				Status: status,
				Series: series,
				Value:  float64(service.NeverSeenSites),
				Limit:  float64(cfg.MaxNeverSeenSites),
				Reason: reason,
			})
		}
		if cfg.MaxStaleSites >= 0 {
			status := "pass"
			reason := ""
			if service.StaleSites > cfg.MaxStaleSites {
				status = "fail"
				reason = fmt.Sprintf("%d target sites were stale or never observed; limit is %d", service.StaleSites, cfg.MaxStaleSites)
			}
			findings = append(findings, ThresholdFinding{
				Name:   "target_observer_stale_sites",
				Status: status,
				Series: series,
				Value:  float64(service.StaleSites),
				Limit:  float64(cfg.MaxStaleSites),
				Reason: reason,
			})
		}
		if cfg.MinExpectedRequestRatio > 0 {
			status := "pass"
			reason := ""
			if service.ExpectedRequestRatio < cfg.MinExpectedRequestRatio {
				status = "fail"
				reason = fmt.Sprintf("expected request ratio %.4f is below %.4f", service.ExpectedRequestRatio, cfg.MinExpectedRequestRatio)
			}
			findings = append(findings, ThresholdFinding{
				Name:   "target_observer_expected_request_ratio",
				Status: status,
				Series: series,
				Value:  service.ExpectedRequestRatio,
				Limit:  cfg.MinExpectedRequestRatio,
				Reason: reason,
			})
		}
		if cfg.MaxExpectedRequestRatio > 0 {
			status := "pass"
			reason := ""
			if service.ExpectedRequestRatio > cfg.MaxExpectedRequestRatio {
				status = "fail"
				reason = fmt.Sprintf("expected request ratio %.4f is above %.4f", service.ExpectedRequestRatio, cfg.MaxExpectedRequestRatio)
			}
			findings = append(findings, ThresholdFinding{
				Name:   "target_observer_expected_request_ratio_max",
				Status: status,
				Series: series,
				Value:  service.ExpectedRequestRatio,
				Limit:  cfg.MaxExpectedRequestRatio,
				Reason: reason,
			})
		}
	}
	return findings
}

func firstFailedThreshold(findings []ThresholdFinding) *ThresholdFinding {
	for i := range findings {
		if findings[i].Status == "fail" {
			return &findings[i]
		}
	}
	return nil
}

func formatThresholdFailure(f ThresholdFinding) string {
	if f.Reason != "" {
		if f.Series != "" {
			return fmt.Sprintf("%s for %s: %s", f.Name, f.Series, f.Reason)
		}
		return fmt.Sprintf("%s: %s", f.Name, f.Reason)
	}
	if f.Series != "" {
		return fmt.Sprintf("%s exceeded threshold for %s", f.Name, f.Series)
	}
	return f.Name + " exceeded threshold"
}

func writeTargetObserverArtifact(dir, name string, summary targetserver.CapacityObserveSummary, m *RunManifest) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal target observer summary: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".json"), Path: path})
	return nil
}

func (r Runner) resetNetworkBuckets(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, m *RunManifest) error {
	cfg = cfg.Normalize()
	if !cfg.NetworkBuckets.Enabled {
		return nil
	}
	snapshots, err := r.NetworkBuckets.Reset(ctx, cfg.NetworkBuckets, services)
	if len(snapshots) > 0 {
		m.NetworkBuckets = append(m.NetworkBuckets, snapshots...)
		if writeErr := writeNetworkBucketArtifact(dir, "network-buckets-reset.json", snapshots, m); writeErr != nil && err == nil {
			err = writeErr
		}
	}
	if err != nil {
		m.NetworkBucketStatus = "preflight_failed"
		m.NetworkBucketError = err.Error()
		return fmt.Errorf("network bucket reset: %w", err)
	}
	m.NetworkBucketStatus = "running"
	m.NetworkBucketError = ""
	return nil
}

func (r Runner) snapshotNetworkBuckets(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, m *RunManifest) error {
	cfg = cfg.Normalize()
	if !cfg.NetworkBuckets.Enabled {
		return nil
	}
	snapshots, err := r.NetworkBuckets.Snapshot(ctx, cfg.NetworkBuckets, services)
	if len(snapshots) > 0 {
		m.NetworkBuckets = append(m.NetworkBuckets, snapshots...)
		if writeErr := writeNetworkBucketArtifact(dir, "network-buckets-window.json", snapshots, m); writeErr != nil && err == nil {
			err = writeErr
		}
	}
	if err != nil {
		m.NetworkBucketStatus = "fail"
		m.NetworkBucketError = err.Error()
		return fmt.Errorf("network bucket snapshot: %w", err)
	}
	m.NetworkBucketStatus = "pass"
	m.NetworkBucketError = ""
	return nil
}

func capacityObserverRunID(m *RunManifest) string {
	if m == nil {
		return ""
	}
	if m.OutDir != "" {
		return filepath.Base(m.OutDir)
	}
	return m.ID
}

func (r Runner) preflightPrometheus(ctx context.Context, cfg RunConfig, promURL string, m *RunManifest) error {
	promURL = strings.TrimSpace(promURL)
	if promURL == "" {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = "prometheus_url is required for live capacity windows"
		return fmt.Errorf("prometheus preflight: prometheus_url is required for live capacity windows")
	}
	if len(cfg.Instances) == 0 {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = "instances are required for live capacity windows"
		return fmt.Errorf("prometheus preflight: instances are required for live capacity windows")
	}
	if refs := placeholderPrometheusReferences(promURL, cfg.Instances); len(refs) > 0 {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = "example Prometheus configuration in live apply run: " + strings.Join(refs, ", ")
		return fmt.Errorf("prometheus preflight: refusing live apply run with example Prometheus configuration: %s", strings.Join(refs, ", "))
	}
	step, err := cfg.StepDuration()
	if err != nil {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = err.Error()
		return fmt.Errorf("prometheus preflight step: %w", err)
	}
	rateWindow, err := cfg.RateWindowDuration()
	if err != nil {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = err.Error()
		return fmt.Errorf("prometheus preflight rate window: %w", err)
	}
	end := r.Clock.Now().UTC()
	window := rateWindow + 2*step
	if window < time.Minute {
		window = time.Minute
	}
	report, err := r.Collector.Collect(ctx, promURL, cfg.Instances, end.Add(-window), end, step, rateWindow)
	if err != nil {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = err.Error()
		return fmt.Errorf("prometheus preflight: %w", err)
	}
	missing := missingScrapeUpInstances(report, cfg.Instances)
	if len(missing) > 0 {
		m.PrometheusStatus = "preflight_failed"
		m.PrometheusError = "missing scrape_up series for instances: " + strings.Join(missing, ", ")
		return fmt.Errorf("prometheus preflight: missing scrape_up series for instances: %s", strings.Join(missing, ", "))
	}
	m.PrometheusStatus = "preflight_pass"
	return nil
}

func (r Runner) collectPrometheusTo(ctx context.Context, path string, cfg RunConfig, promURL string, start, end time.Time, m *RunManifest) error {
	if strings.TrimSpace(promURL) == "" {
		m.PrometheusStatus = "skipped"
		m.Notes = append(m.Notes, "Prometheus capture skipped: prometheus_url is empty")
		return nil
	}
	if len(cfg.Instances) == 0 {
		m.PrometheusStatus = "skipped"
		m.Notes = append(m.Notes, "Prometheus capture skipped: instances is empty")
		return nil
	}
	step, err := cfg.StepDuration()
	if err != nil {
		return fmt.Errorf("prometheus step: %w", err)
	}
	rateWindow, err := cfg.RateWindowDuration()
	if err != nil {
		return fmt.Errorf("prometheus rate window: %w", err)
	}
	report, err := r.Collector.Collect(ctx, promURL, cfg.Instances, start, end, step, rateWindow)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal prometheus report: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write prometheus report: %w", err)
	}
	m.Artifacts = append(m.Artifacts, Artifact{Action: strings.TrimSuffix(filepath.Base(path), ".json"), Path: path})
	m.PrometheusStatus = "pass"
	m.PrometheusError = ""
	return nil
}

func (m RunManifest) loadPrometheusReport(dir string) *capacitybench.Report {
	data, err := os.ReadFile(filepath.Join(dir, "prometheus-window.json"))
	if err != nil {
		return nil
	}
	var report capacitybench.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil
	}
	return &report
}

// WriteManifest writes a run manifest.
func WriteManifest(dir string, m RunManifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "run.json"), append(data, '\n'), 0o644)
}

// WriteSummary writes a human-readable operator summary.
func WriteSummary(dir string, m RunManifest) error {
	var b strings.Builder
	fmt.Fprintln(&b, "Jetmon Capacity Run")
	fmt.Fprintf(&b, "ID: %s\n", m.ID)
	fmt.Fprintf(&b, "Mode: %s\n", m.Mode)
	fmt.Fprintf(&b, "Apply: %t\n", m.Apply)
	if m.ActiveCount > 0 {
		fmt.Fprintf(&b, "Active Count: %d\n", m.ActiveCount)
	}
	if m.BatchCount > 0 {
		fmt.Fprintf(&b, "Batch Count: %d\n", m.BatchCount)
	}
	if m.TotalBatchCount > 0 && m.TotalBatchCount != m.BatchCount {
		fmt.Fprintf(&b, "Total Configured Batches: %d\n", m.TotalBatchCount)
	}
	if len(m.BatchSizes) > 0 {
		fmt.Fprintf(&b, "Batch Sizes: %s\n", joinInts(m.BatchSizes))
	}
	if m.SuiteStartCount > 0 {
		fmt.Fprintf(&b, "Suite Start Count: %d\n", m.SuiteStartCount)
	}
	if m.SuiteStartSource != "" {
		fmt.Fprintf(&b, "Suite Start Source: %s\n", m.SuiteStartSource)
	}
	if m.SuiteStatePath != "" {
		fmt.Fprintf(&b, "Suite State Path: %s\n", m.SuiteStatePath)
	}
	if m.EstimatedRuntime != "" {
		fmt.Fprintf(&b, "Estimated Runtime: %s\n", m.EstimatedRuntime)
	}
	if m.WindowStart != nil || m.WindowEnd != nil {
		fmt.Fprintf(&b, "Window: %s to %s\n", formatMaybeTime(m.WindowStart), formatMaybeTime(m.WindowEnd))
	}
	if m.DeactivatedAt != nil {
		fmt.Fprintf(&b, "Deactivated At: %s\n", m.DeactivatedAt.Format(time.RFC3339))
	}
	if m.LifecycleStatus != "" {
		fmt.Fprintf(&b, "Lifecycle Status: %s\n", m.LifecycleStatus)
	}
	if m.HealthStatus != "" {
		fmt.Fprintf(&b, "Health Status: %s\n", m.HealthStatus)
	}
	if m.PrometheusStatus != "" {
		fmt.Fprintf(&b, "Prometheus Status: %s\n", m.PrometheusStatus)
	}
	if m.PrometheusError != "" {
		fmt.Fprintf(&b, "Prometheus Error: %s\n", m.PrometheusError)
	}
	if m.TargetObserverStatus != "" {
		fmt.Fprintf(&b, "Target Observer Status: %s\n", m.TargetObserverStatus)
	}
	if m.TargetObserverError != "" {
		fmt.Fprintf(&b, "Target Observer Error: %s\n", m.TargetObserverError)
	}
	if m.CapacityReplayStatus != "" {
		fmt.Fprintf(&b, "Capacity Replay Status: %s\n", m.CapacityReplayStatus)
	}
	if m.CapacityReplayError != "" {
		fmt.Fprintf(&b, "Capacity Replay Error: %s\n", m.CapacityReplayError)
	}
	if m.ReplayDetectionStatus != "" {
		fmt.Fprintf(&b, "Replay Detection Status: %s\n", m.ReplayDetectionStatus)
	}
	if m.ReplayDetectionError != "" {
		fmt.Fprintf(&b, "Replay Detection Error: %s\n", m.ReplayDetectionError)
	}
	if m.NetworkBucketStatus != "" {
		fmt.Fprintf(&b, "Network Bucket Status: %s\n", m.NetworkBucketStatus)
	}
	if m.NetworkBucketError != "" {
		fmt.Fprintf(&b, "Network Bucket Error: %s\n", m.NetworkBucketError)
	}
	if m.Target.HostPattern != "" || m.Target.URLPattern != "" {
		fmt.Fprintf(&b, "Target Host Pattern: %s\n", m.Target.HostPattern)
		fmt.Fprintf(&b, "Target URL Pattern: %s\n", m.Target.URLPattern)
	}
	if m.CleanupStatus != "" {
		fmt.Fprintf(&b, "Cleanup Status: %s\n", m.CleanupStatus)
	}
	if m.CleanupError != "" {
		fmt.Fprintf(&b, "Cleanup Error: %s\n", m.CleanupError)
	}
	if m.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", m.Error)
	}
	if m.StopRecommended {
		fmt.Fprintln(&b, "Stop Recommended: true")
		fmt.Fprintf(&b, "Stop Reason: %s\n", m.StopReason)
	} else {
		fmt.Fprintln(&b, "Stop Recommended: false")
	}
	if len(m.Notes) > 0 {
		fmt.Fprintln(&b, "\nNotes:")
		for _, note := range m.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}
	if len(m.Health) > 0 {
		fmt.Fprintln(&b, "\nService Health:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tACTION\tSTATUS\tACTIVE\tEXPECTED\tSTALE\tMISSED_CHECK_%\tRECENT_ROWS\tRECENT/MIN\tP95_AGE_SEC\tOLDEST_AGE_SEC\tREASON")
		for _, h := range m.Health {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				h.Service,
				h.Action,
				h.Status,
				formatIntPtr(h.ActiveSites),
				formatIntPtr(h.ExpectedActiveSites),
				formatIntPtr(h.StaleActiveSites),
				formatFloatPtr(h.MissedCheckPercent),
				formatIntPtr(h.RecentCheckHistoryRows),
				formatFloatPtr(h.RecentChecksPerMinute),
				formatFloatPtr(h.P95CheckAgeSec),
				formatFloatPtr(h.OldestCheckAgeSec),
				h.Reason,
			)
		}
		_ = tw.Flush()
	}
	if rows := healthCheckIntervalRows(m.Health); len(rows) > 0 {
		fmt.Fprintln(&b, "\nCheck Interval Distribution:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tACTION\tCHECK_INTERVAL_MIN\tACTIVE")
		for _, row := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", row.Service, row.Action, row.CheckIntervalMinutes, row.ActiveSites)
		}
		_ = tw.Flush()
	}
	if latest := latestTargetObservation(m.TargetObservations); latest != nil && len(latest.Services) > 0 {
		fmt.Fprintln(&b, "\nTarget Observer:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tEXPECTED\tOBSERVED\tNEVER_SEEN\tSTALE\tCOVERAGE_%\tREQUESTS\tREQ/S\tREQ/SITE_MEAN\tEXPECTED_RATIO\tP95_AGE_SEC\tMAX_AGE_SEC")
		for _, service := range latest.Services {
			fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%.2f\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\n",
				service.ID,
				service.ExpectedSites,
				service.ObservedSites,
				service.NeverSeenSites,
				service.StaleSites,
				service.CoveragePercent,
				service.TotalRequests,
				service.RequestsPerSecond,
				service.RequestsPerSiteMean,
				service.ExpectedRequestRatio,
				service.LastSeenAgeSecondsP95,
				service.LastSeenAgeSecondsMax,
			)
		}
		_ = tw.Flush()
	}
	if latestReplay := latestCapacityReplay(m.CapacityReplays); latestReplay != nil {
		fmt.Fprintln(&b, "\nCapacity Replay:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "EVENT\tTYPE\tHOSTS\tACTIVATE_ERRORS\tDEACTIVATE_ERRORS\tERROR")
		for _, event := range latestReplay.Events {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%s\n",
				event.ID,
				capacityReplayEventType(latestReplay.Plan, event.ID),
				len(event.Hosts),
				event.ActivateFailures,
				event.DeactivateFailures,
				firstCapacityReplayHostError(event),
			)
		}
		_ = tw.Flush()
	}
	if latestDetection := latestReplayDetection(m.ReplayDetections); latestDetection != nil {
		fmt.Fprintln(&b, "\nReplay Detection:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "EVENT\tSERVICE\tSTATUS\tHOSTS\tELIGIBLE\tDOWN\tRECOVERY\tLATE_DOWN\tPREEXISTING\tEXPECTED_INTERVAL\tNORMAL_INTERVAL\tNEXT_INTERVAL\tINTERVAL_MISMATCHES\tDOWN_MIN\tDOWN_MEAN\tDOWN_MAX\tRECOVERY_MIN\tRECOVERY_MEAN\tRECOVERY_MAX\tERROR")
		for _, event := range latestDetection.Events {
			for _, service := range event.Services {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					event.ID,
					service.Service,
					service.Status,
					service.Hosts,
					service.EligibleHosts,
					service.DownDetected,
					service.RecoveryDetected,
					service.LateDownDetected,
					service.PreexistingDownOverlappedFailure,
					formatSeconds(service.ExpectedCheckIntervalSec),
					formatIntRange(service.NormalCheckIntervalMinSec, service.NormalCheckIntervalMaxSec),
					formatIntRange(service.NextCheckIntervalMinSec, service.NextCheckIntervalMaxSec),
					service.CheckIntervalMismatchEvents,
					formatFloatPtr(service.DownLatencyMinSec),
					formatFloatPtr(service.DownLatencyMeanSec),
					formatFloatPtr(service.DownLatencyMaxSec),
					formatFloatPtr(service.RecoveryLatencyMinSec),
					formatFloatPtr(service.RecoveryLatencyMeanSec),
					formatFloatPtr(service.RecoveryLatencyMaxSec),
					service.Error,
				)
			}
		}
		_ = tw.Flush()
	}
	if snapshots := latestNetworkBucketSnapshots(m.NetworkBuckets); len(snapshots) > 0 {
		fmt.Fprintln(&b, "\nNetwork Buckets:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tBUCKET\tDIRECTION\tBYTES\tPACKETS")
		for _, snapshot := range snapshots {
			for _, counter := range snapshot.Counters {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n",
					snapshot.ID,
					counter.Bucket,
					counter.Direction,
					counter.Bytes,
					counter.Packets,
				)
			}
		}
		_ = tw.Flush()
	}
	if len(m.TargetPreflights) > 0 {
		fmt.Fprintln(&b, "\nTarget Preflight:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tSTATUS\tSAMPLES\tSKIP_HTTP\tEXPECTED_STATUS\tERROR")
		for _, p := range m.TargetPreflights {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%t\t%s\t%s\n",
				p.Service,
				p.Status,
				p.SampleCount,
				p.SkippedHTTP,
				expectedStatusText(p.ExpectedStatus),
				p.Error,
			)
		}
		_ = tw.Flush()
		for _, p := range m.TargetPreflights {
			for _, sample := range p.Samples {
				fmt.Fprintf(&b, "- %s blog_id=%d bucket=%d url=%s pattern_match=%t\n",
					p.Service, sample.BlogID, sample.BucketNo, sample.URL, sample.PatternMatch)
				for _, check := range sample.Checks {
					fmt.Fprintf(&b, "  check source=%s dns_ok=%t http_ok=%t http_status=%d error=%s\n",
						check.Source, check.DNSOK, check.HTTPOK, check.HTTPStatus, check.Error)
				}
			}
		}
	}
	if bucketRows := summaryBucketRows(m.Health, 20); len(bucketRows) > 0 {
		fmt.Fprintln(&b, "\nBucket Staleness:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "SERVICE\tACTION\tBUCKET\tACTIVE\tSTALE\tSTALE_%")
		for _, row := range bucketRows {
			fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%.2f\n",
				row.Service,
				row.Action,
				row.Bucket.BucketNo,
				row.Bucket.ActiveSites,
				row.Bucket.StaleActiveSites,
				row.Bucket.StalePercent,
			)
		}
		_ = tw.Flush()
		if omitted := countBucketRows(m.Health) - len(bucketRows); omitted > 0 {
			fmt.Fprintf(&b, "... %d additional bucket rows omitted from summary; see run.json for the full list.\n", omitted)
		}
	}
	if len(m.Thresholds) > 0 {
		fmt.Fprintln(&b, "\nThresholds:")
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSTATUS\tSERIES\tVALUE\tLIMIT\tREASON")
		for _, finding := range m.Thresholds {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				finding.Name,
				finding.Status,
				finding.Series,
				formatThresholdValue(finding.Value, finding.Status),
				formatThresholdValue(finding.Limit, ""),
				finding.Reason,
			)
		}
		_ = tw.Flush()
	}
	if len(m.Artifacts) > 0 {
		fmt.Fprintln(&b, "\nArtifacts:")
		for _, artifact := range m.Artifacts {
			label := artifact.Action
			if artifact.Service != "" {
				label = artifact.Service + " " + label
			}
			fmt.Fprintf(&b, "- %s: %s\n", strings.TrimSpace(label), artifact.Path)
		}
	}
	return os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(b.String()), 0o644)
}

// ServiceSummaries returns manifest summaries for services.
func ServiceSummaries(services []ServiceLifecycle) []ServiceManifest {
	out := make([]ServiceManifest, 0, len(services))
	for _, svc := range services {
		c := svc.Config
		out = append(out, ServiceManifest{
			ID:                   svc.ID,
			Schema:               c.Schema,
			BlogIDStart:          c.BlogIDStart,
			BlogIDEnd:            c.BlogIDEnd(),
			ReservedCount:        c.Count,
			URLNumberStart:       c.URLNumberStart,
			BucketMin:            c.BucketMin,
			BucketMax:            c.BucketMax,
			CheckIntervalMinutes: c.CheckIntervalMinutes,
			DSNEnv:               svc.DSNEnv,
			DSNFile:              svc.DSNFile,
			HasDSN:               svc.HasDSN,
		})
	}
	return out
}

// ValidRunMode reports whether mode is supported by Runner.
func ValidRunMode(mode string) bool {
	switch mode {
	case "plan", "seed", "activate", "deactivate", "verify", "run-batch", "run-suite":
		return true
	default:
		return false
	}
}

func serviceHealthFromVerify(service ServiceLifecycle, action string, result SQLExecutionResult, expectedActive int) ServiceHealth {
	health := ServiceHealth{
		Service:           service.ID,
		Action:            action,
		Status:            "pass",
		FreshnessMeasured: service.Config.Schema == SchemaV2,
	}
	if benchmarkSites, ok := firstInt64(result, "benchmark_sites"); ok {
		health.BenchmarkSites = &benchmarkSites
	}
	if activeSites, ok := firstInt64(result, "active_sites"); ok {
		health.ActiveSites = &activeSites
	}
	if expectedActive >= 0 {
		expected := int64(expectedActive)
		health.ExpectedActiveSites = &expected
		if health.ActiveSites != nil && *health.ActiveSites != expected {
			health.Status = "fail"
			health.Reason = fmt.Sprintf("active_sites=%d, want %d", *health.ActiveSites, expected)
		}
	}
	health.CheckIntervals = checkIntervalRows(result)
	if len(health.CheckIntervals) > 0 {
		expectedInterval := service.Config.CheckIntervalMinutes
		var mismatched int64
		for _, row := range health.CheckIntervals {
			if row.CheckIntervalMinutes != expectedInterval {
				mismatched += row.ActiveSites
			}
		}
		health.CheckIntervalMismatchSites = &mismatched
		if mismatched > 0 {
			health.Status = "fail"
			health.Reason = appendReason(health.Reason, fmt.Sprintf("%d active sites have check_interval different from %dm", mismatched, expectedInterval))
		}
	}
	if service.Config.Schema != SchemaV2 {
		health.FreshnessMeasured = false
		health.Reason = appendReason(health.Reason, "freshness and missed checks are not measured for v1 by DB verify")
		return health
	}
	if stale, ok := firstInt64(result, "stale_active_sites"); ok {
		health.StaleActiveSites = &stale
		if health.ActiveSites != nil && *health.ActiveSites > 0 {
			missed := float64(stale) / float64(*health.ActiveSites) * 100
			health.MissedCheckPercent = &missed
		}
	}
	if openEvents, ok := firstInt64(result, "open_events"); ok {
		health.OpenEvents = &openEvents
	}
	if history, ok := firstInt64(result, "recent_check_history_rows"); ok {
		health.RecentCheckHistoryRows = &history
		if defaultFreshSinceMinutes > 0 {
			recentPerMinute := float64(history) / float64(defaultFreshSinceMinutes)
			health.RecentChecksPerMinute = &recentPerMinute
			health.FreshnessWindowMinutes = defaultFreshSinceMinutes
		}
	}
	if samples, ok := firstInt64(result, "freshness_samples"); ok {
		health.FreshnessSamples = &samples
	}
	if freshest, ok := firstFloat64(result, "freshest_check_age_sec"); ok {
		health.FreshestCheckAgeSec = &freshest
	}
	if average, ok := firstFloat64(result, "average_check_age_sec"); ok {
		health.AverageCheckAgeSec = &average
	}
	if p50, ok := firstFloat64(result, "p50_check_age_sec"); ok {
		health.P50CheckAgeSec = &p50
	}
	if p95, ok := firstFloat64(result, "p95_check_age_sec"); ok {
		health.P95CheckAgeSec = &p95
	}
	if p99, ok := firstFloat64(result, "p99_check_age_sec"); ok {
		health.P99CheckAgeSec = &p99
	}
	if oldest, ok := firstFloat64(result, "oldest_check_age_sec"); ok {
		health.OldestCheckAgeSec = &oldest
	}
	health.StaleBuckets = bucketFreshnessRows(result)
	return health
}

// EvaluateThresholds evaluates configured stop thresholds.
func EvaluateThresholds(health []ServiceHealth, promURL string, thresholds StopThresholds, report *capacitybench.Report) []ThresholdFinding {
	var out []ThresholdFinding
	if report != nil {
		out = append(out, highThresholdFindings(report, "host_cpu_used", thresholds.HostCPUPercent)...)
		out = append(out, highThresholdFindings(report, "host_memory_used", thresholds.HostMemoryPercent)...)
		out = append(out, highThresholdFindings(report, "host_root_disk_used", thresholds.RootDiskPercent)...)
		out = append(out, lowThresholdFindings(report, "scrape_up", thresholds.ScrapeUpMin)...)
		out = append(out, lowThresholdFindings(report, "dockerstats_scrape_success", thresholds.DockerstatsScrapeSuccessMin)...)
	} else if promURL != "" {
		out = append(out, ThresholdFinding{Name: "prometheus", Status: "not_measured", Reason: "prometheus report was not available"})
	}
	if thresholds.MissedCheckPercent > 0 {
		measured := false
		for _, h := range health {
			if h.Action != "window-end-verify" {
				continue
			}
			if h.MissedCheckPercent == nil {
				out = append(out, ThresholdFinding{
					Name:   "missed_check_percent",
					Status: "not_measured",
					Series: h.Service,
					Limit:  thresholds.MissedCheckPercent,
					Reason: h.Reason,
				})
				continue
			}
			measured = true
			status := "pass"
			if *h.MissedCheckPercent > thresholds.MissedCheckPercent {
				status = "fail"
			}
			out = append(out, ThresholdFinding{
				Name:   "missed_check_percent",
				Status: status,
				Series: h.Service,
				Value:  *h.MissedCheckPercent,
				Limit:  thresholds.MissedCheckPercent,
			})
		}
		if !measured && len(health) == 0 {
			out = append(out, ThresholdFinding{Name: "missed_check_percent", Status: "not_measured", Limit: thresholds.MissedCheckPercent, Reason: "no service health was captured"})
		}
	}
	return out
}

func highThresholdFindings(report *capacitybench.Report, query string, limit float64) []ThresholdFinding {
	if limit <= 0 {
		return nil
	}
	var out []ThresholdFinding
	for _, s := range report.Summaries {
		if s.Query != query {
			continue
		}
		status := "pass"
		if s.Max > limit {
			status = "fail"
		}
		out = append(out, ThresholdFinding{
			Name:   query,
			Status: status,
			Series: capacitybench.SeriesLabel(s.Labels),
			Value:  s.Max,
			Limit:  limit,
		})
	}
	if len(out) == 0 {
		out = append(out, ThresholdFinding{Name: query, Status: "not_measured", Limit: limit, Reason: "query returned no series"})
	}
	return out
}

func lowThresholdFindings(report *capacitybench.Report, query string, limit float64) []ThresholdFinding {
	if limit <= 0 {
		return nil
	}
	var out []ThresholdFinding
	for _, s := range report.Summaries {
		if s.Query != query {
			continue
		}
		status := "pass"
		if s.Min < limit {
			status = "fail"
		}
		out = append(out, ThresholdFinding{
			Name:   query,
			Status: status,
			Series: capacitybench.SeriesLabel(s.Labels),
			Value:  s.Min,
			Limit:  limit,
		})
	}
	if len(out) == 0 {
		out = append(out, ThresholdFinding{Name: query, Status: "not_measured", Limit: limit, Reason: "query returned no series"})
	}
	return out
}

func setStopRecommendation(m *RunManifest) {
	for _, finding := range m.Thresholds {
		if finding.Status == "fail" {
			m.StopRecommended = true
			m.StopReason = formatThresholdFailure(finding)
			return
		}
	}
	if m.ReplayDetectionStatus == "fail" {
		m.StopRecommended = true
		m.StopReason = firstNonEmpty(m.ReplayDetectionError, "replay detection failed")
		return
	}
}

func setHealthStatus(m *RunManifest) {
	if len(m.Health) == 0 {
		m.HealthStatus = "not_measured"
		return
	}
	m.HealthStatus = "pass"
	for _, h := range m.Health {
		if h.Status == "fail" {
			m.HealthStatus = "fail"
			return
		}
	}
	for _, finding := range m.Thresholds {
		if finding.Name == "missed_check_percent" && finding.Status == "fail" {
			m.HealthStatus = "fail"
			return
		}
	}
}

func placeholderPrometheusReferences(promURL string, instances []string) []string {
	var refs []string
	if parsed, err := url.Parse(promURL); err == nil {
		if isPlaceholderHost(parsed.Hostname()) {
			refs = append(refs, promURL)
		}
	} else if strings.Contains(strings.ToLower(promURL), "example.") {
		refs = append(refs, promURL)
	}
	for _, instance := range instances {
		if isPlaceholderHost(instance) {
			refs = append(refs, instance)
		}
	}
	return refs
}

func isPlaceholderHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "example.com" ||
		host == "example.net" ||
		host == "example.org" ||
		strings.HasSuffix(host, ".example.com") ||
		strings.HasSuffix(host, ".example.net") ||
		strings.HasSuffix(host, ".example.org")
}

func missingScrapeUpInstances(report capacitybench.Report, instances []string) []string {
	present := make(map[string]bool, len(instances))
	for _, summary := range report.Summaries {
		if summary.Query != "scrape_up" || summary.Samples == 0 || summary.Last < 1 {
			continue
		}
		present[summary.Labels["instance"]] = true
	}
	var missing []string
	for _, instance := range instances {
		if !present[instance] {
			missing = append(missing, instance)
		}
	}
	return missing
}

func firstInt64(result SQLExecutionResult, column string) (int64, bool) {
	for _, stmt := range result.Statements {
		for i, col := range stmt.Columns {
			if col != column {
				continue
			}
			if len(stmt.Rows) == 0 || i >= len(stmt.Rows[0]) {
				return 0, false
			}
			value := stmt.Rows[0][i]
			if value == "NULL" || value == "" {
				return 0, true
			}
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return 0, false
			}
			return parsed, true
		}
	}
	return 0, false
}

func firstFloat64(result SQLExecutionResult, column string) (float64, bool) {
	for _, stmt := range result.Statements {
		for i, col := range stmt.Columns {
			if col != column {
				continue
			}
			if len(stmt.Rows) == 0 || i >= len(stmt.Rows[0]) {
				return 0, false
			}
			value := stmt.Rows[0][i]
			if value == "NULL" || value == "" {
				return 0, false
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, false
			}
			return parsed, true
		}
	}
	return 0, false
}

func bucketFreshnessRows(result SQLExecutionResult) []BucketFreshness {
	stmt, ok := statementWithColumns(result, "bucket_no", "active_sites", "stale_active_sites")
	if !ok {
		return nil
	}
	bucketIdx := columnIndex(stmt.Columns, "bucket_no")
	activeIdx := columnIndex(stmt.Columns, "active_sites")
	staleIdx := columnIndex(stmt.Columns, "stale_active_sites")
	var rows []BucketFreshness
	for _, row := range stmt.Rows {
		if bucketIdx >= len(row) || activeIdx >= len(row) || staleIdx >= len(row) {
			continue
		}
		bucketNo, err := strconv.Atoi(row[bucketIdx])
		if err != nil {
			continue
		}
		active, err := strconv.ParseInt(row[activeIdx], 10, 64)
		if err != nil {
			continue
		}
		stale, err := strconv.ParseInt(row[staleIdx], 10, 64)
		if err != nil {
			continue
		}
		var stalePercent float64
		if active > 0 {
			stalePercent = float64(stale) / float64(active) * 100
		}
		rows = append(rows, BucketFreshness{
			BucketNo:         bucketNo,
			ActiveSites:      active,
			StaleActiveSites: stale,
			StalePercent:     stalePercent,
		})
	}
	return rows
}

func checkIntervalRows(result SQLExecutionResult) []CheckIntervalRow {
	stmt, ok := statementWithColumns(result, "check_interval", "active_sites")
	if !ok {
		return nil
	}
	intervalIdx := columnIndex(stmt.Columns, "check_interval")
	activeIdx := columnIndex(stmt.Columns, "active_sites")
	var rows []CheckIntervalRow
	for _, row := range stmt.Rows {
		if intervalIdx >= len(row) || activeIdx >= len(row) {
			continue
		}
		interval, err := strconv.Atoi(row[intervalIdx])
		if err != nil {
			continue
		}
		active, err := strconv.ParseInt(row[activeIdx], 10, 64)
		if err != nil {
			continue
		}
		rows = append(rows, CheckIntervalRow{
			CheckIntervalMinutes: interval,
			ActiveSites:          active,
		})
	}
	return rows
}

func statementWithColumns(result SQLExecutionResult, columns ...string) (SQLStatementResult, bool) {
	for _, stmt := range result.Statements {
		matches := true
		for _, column := range columns {
			if columnIndex(stmt.Columns, column) == -1 {
				matches = false
				break
			}
		}
		if matches {
			return stmt, true
		}
	}
	return SQLStatementResult{}, false
}

func columnIndex(columns []string, want string) int {
	for i, column := range columns {
		if strings.EqualFold(column, want) {
			return i
		}
	}
	return -1
}

type summaryBucketRow struct {
	Service string
	Action  string
	Bucket  BucketFreshness
}

func summaryBucketRows(health []ServiceHealth, limit int) []summaryBucketRow {
	var rows []summaryBucketRow
	for _, h := range health {
		for _, bucket := range h.StaleBuckets {
			rows = append(rows, summaryBucketRow{
				Service: h.Service,
				Action:  h.Action,
				Bucket:  bucket,
			})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Bucket.StalePercent == rows[j].Bucket.StalePercent {
			return rows[i].Bucket.StaleActiveSites > rows[j].Bucket.StaleActiveSites
		}
		return rows[i].Bucket.StalePercent > rows[j].Bucket.StalePercent
	})
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func countBucketRows(health []ServiceHealth) int {
	var count int
	for _, h := range health {
		count += len(h.StaleBuckets)
	}
	return count
}

type healthCheckIntervalRow struct {
	Service              string
	Action               string
	CheckIntervalMinutes int
	ActiveSites          int64
}

func healthCheckIntervalRows(health []ServiceHealth) []healthCheckIntervalRow {
	var rows []healthCheckIntervalRow
	for _, h := range health {
		for _, interval := range h.CheckIntervals {
			rows = append(rows, healthCheckIntervalRow{
				Service:              h.Service,
				Action:               h.Action,
				CheckIntervalMinutes: interval.CheckIntervalMinutes,
				ActiveSites:          interval.ActiveSites,
			})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Service != rows[j].Service {
			return rows[i].Service < rows[j].Service
		}
		if rows[i].Action != rows[j].Action {
			return rows[i].Action < rows[j].Action
		}
		return rows[i].CheckIntervalMinutes < rows[j].CheckIntervalMinutes
	})
	return rows
}

func latestTargetObservation(observations []targetserver.CapacityObserveSummary) *targetserver.CapacityObserveSummary {
	for i := len(observations) - 1; i >= 0; i-- {
		if len(observations[i].Services) == 0 {
			continue
		}
		return &observations[i]
	}
	return nil
}

func latestCapacityReplay(replays []CapacityReplayRun) *CapacityReplayRun {
	for i := len(replays) - 1; i >= 0; i-- {
		if len(replays[i].Events) == 0 {
			continue
		}
		return &replays[i]
	}
	return nil
}

func latestReplayDetection(detections []ReplayDetectionRun) *ReplayDetectionRun {
	for i := len(detections) - 1; i >= 0; i-- {
		if len(detections[i].Events) == 0 {
			continue
		}
		return &detections[i]
	}
	return nil
}

func latestNetworkBucketSnapshots(snapshots []NetworkBucketHostSnapshot) []NetworkBucketHostSnapshot {
	var out []NetworkBucketHostSnapshot
	for _, snapshot := range snapshots {
		if len(snapshot.Counters) == 0 {
			continue
		}
		out = append(out, snapshot)
	}
	return out
}

func capacityReplayEventType(plan CapacityReplayPlan, id string) string {
	for _, event := range plan.Events {
		if event.ID == id {
			return event.Type
		}
	}
	return ""
}

func firstCapacityReplayHostError(event CapacityReplayEventResult) string {
	for _, host := range event.Hosts {
		if host.ActivateError != "" {
			return host.ActivateError
		}
		if host.DeactivateError != "" {
			return host.DeactivateError
		}
	}
	return ""
}

func appendReason(existing, extra string) string {
	if existing == "" {
		return extra
	}
	if extra == "" {
		return existing
	}
	return existing + "; " + extra
}

func formatMaybeTime(t *time.Time) string {
	if t == nil {
		return "not recorded"
	}
	return t.Format(time.RFC3339)
}

func joinInts(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func formatIntPtr(value *int64) string {
	if value == nil {
		return "-"
	}
	return strconv.FormatInt(*value, 10)
}

func formatFloatPtr(value *float64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprintf("%.2f", *value)
}

func formatSeconds(value int) string {
	if value <= 0 {
		return "-"
	}
	return strconv.Itoa(value) + "s"
}

func formatIntRange(min, max *int) string {
	if min == nil && max == nil {
		return "-"
	}
	if min == nil {
		return strconv.Itoa(*max)
	}
	if max == nil || *min == *max {
		return strconv.Itoa(*min)
	}
	return fmt.Sprintf("%d-%d", *min, *max)
}

func formatLatencyRange(min, mean, max *float64) string {
	if min == nil && mean == nil && max == nil {
		return "-"
	}
	return fmt.Sprintf("%s/%s/%s", formatFloatPtr(min), formatFloatPtr(mean), formatFloatPtr(max))
}

func formatThresholdValue(value float64, status string) string {
	if status == "not_measured" && value == 0 {
		return "-"
	}
	return fmt.Sprintf("%.2f", value)
}

func estimateSuiteRuntime(batchCount int, duration, cooldown time.Duration) time.Duration {
	if batchCount <= 0 {
		return 0
	}
	total := time.Duration(batchCount) * duration
	if batchCount > 1 {
		total += time.Duration(batchCount-1) * cooldown
	}
	return total
}

func plannedReportDuration(mode string, duration, cooldown time.Duration, batchCount int) time.Duration {
	switch mode {
	case "run-batch":
		return duration
	case "run-suite":
		return estimateSuiteRuntime(batchCount, duration, cooldown)
	default:
		return 0
	}
}

func capacityReportDescription(id, mode string, activeCount int, override string) string {
	if strings.TrimSpace(override) != "" {
		return override
	}
	description := strings.TrimSpace(id)
	if description == "" {
		description = "jetmon-capacity"
	}
	if mode != "" && mode != "run-suite" {
		description += "-" + mode
	}
	if activeCount > 0 && (mode == "activate" || mode == "run-batch" || mode == "verify") {
		description += fmt.Sprintf("-%d-sites", activeCount)
	}
	return description
}

func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Minute)
}

func sqlFilename(service string, action any, activeCount int) string {
	actionText := fmt.Sprint(action)
	if actionText == string(OperationActivate) && activeCount > 0 {
		return fmt.Sprintf("%s-%s-%d.sql", service, actionText, activeCount)
	}
	return fmt.Sprintf("%s-%s.sql", service, actionText)
}

func safeName(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" {
		return "capacity"
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
