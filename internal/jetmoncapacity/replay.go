package jetmoncapacity

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

// CapacityReplayPlan is the deterministic failure schedule used during a
// capacity window.
type CapacityReplayPlan struct {
	RunID            string                    `json:"run_id"`
	TargetControlURL string                    `json:"target_control_url"`
	ActiveCount      int                       `json:"active_count"`
	WindowDuration   string                    `json:"window_duration"`
	Seed             int64                     `json:"seed"`
	Events           []CapacityReplayEventPlan `json:"events"`
}

// CapacityReplayEventPlan is one replay event after host sampling.
type CapacityReplayEventPlan struct {
	ID          string                       `json:"id"`
	Offset      string                       `json:"offset"`
	Duration    string                       `json:"duration"`
	Type        string                       `json:"type"`
	StatusCode  int                          `json:"status_code,omitempty"`
	Rate        float64                      `json:"rate"`
	Path        string                       `json:"path,omitempty"`
	Method      string                       `json:"method,omitempty"`
	Seed        int64                        `json:"seed"`
	HostNumbers []int64                      `json:"host_numbers"`
	Hosts       []string                     `json:"hosts"`
	Services    []CapacityReplayServiceHosts `json:"services,omitempty"`
}

// CapacityReplayServiceHosts records the sampled hosts for one service. When
// services use distinct generated URL ranges, this keeps the replay plan
// auditable while the flat Hosts list remains easy for the executor to apply.
type CapacityReplayServiceHosts struct {
	Service     string   `json:"service"`
	HostNumbers []int64  `json:"host_numbers"`
	Hosts       []string `json:"hosts"`
}

// CapacityReplayRun records execution outcomes for a replay plan.
type CapacityReplayRun struct {
	Status      string                      `json:"status"`
	Error       string                      `json:"error,omitempty"`
	StartedAt   time.Time                   `json:"started_at"`
	CompletedAt time.Time                   `json:"completed_at,omitempty"`
	Plan        CapacityReplayPlan          `json:"plan"`
	Events      []CapacityReplayEventResult `json:"events"`
}

// CapacityReplayEventResult records activate/deactivate results for one event.
type CapacityReplayEventResult struct {
	ID                 string                     `json:"id"`
	ActivatedAt        *time.Time                 `json:"activated_at,omitempty"`
	DeactivatedAt      *time.Time                 `json:"deactivated_at,omitempty"`
	ActivateFailures   int                        `json:"activate_failures,omitempty"`
	DeactivateFailures int                        `json:"deactivate_failures,omitempty"`
	Hosts              []CapacityReplayHostResult `json:"hosts"`
}

// CapacityReplayHostResult records one target-control operation result.
type CapacityReplayHostResult struct {
	Host            string `json:"host"`
	ActivateError   string `json:"activate_error,omitempty"`
	DeactivateError string `json:"deactivate_error,omitempty"`
}

type capacityReplayHandle struct {
	done <-chan CapacityReplayRun
}

func (r Runner) startCapacityReplay(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, activeCount int, windowDuration time.Duration, m *RunManifest) (*capacityReplayHandle, error) {
	cfg = cfg.Normalize()
	if !cfg.CapacityReplay.Enabled {
		return nil, nil
	}
	timeout, err := cfg.CapacityReplayTimeout()
	if err != nil {
		return nil, err
	}
	token, err := resolveCapacityReplayToken(cfg.CapacityReplay)
	if err != nil {
		return nil, err
	}
	plan, err := buildCapacityReplayPlan(cfg, services, activeCount, windowDuration, capacityObserverRunID(m))
	if err != nil {
		return nil, err
	}
	if err := writeCapacityReplayPlan(dir, plan, m); err != nil {
		return nil, err
	}
	m.CapacityReplayStatus = "running"
	m.CapacityReplayError = ""
	done := make(chan CapacityReplayRun, 1)
	go func() {
		done <- r.executeCapacityReplay(ctx, plan, token, timeout)
	}()
	return &capacityReplayHandle{done: done}, nil
}

func (r Runner) finishCapacityReplay(ctx context.Context, dir string, handle *capacityReplayHandle, m *RunManifest) error {
	if handle == nil {
		return nil
	}
	var result CapacityReplayRun
	select {
	case result = <-handle.done:
	case <-ctx.Done():
		m.CapacityReplayStatus = "fail"
		m.CapacityReplayError = ctx.Err().Error()
		return ctx.Err()
	}
	m.CapacityReplays = append(m.CapacityReplays, result)
	if result.Status == "pass" {
		m.CapacityReplayStatus = "pass"
		m.CapacityReplayError = ""
	} else {
		m.CapacityReplayStatus = "fail"
		m.CapacityReplayError = result.Error
	}
	if err := writeCapacityReplayRun(dir, result, m); err != nil {
		return err
	}
	if result.Status != "pass" {
		return fmt.Errorf("capacity replay failed: %s", result.Error)
	}
	return nil
}

func (r Runner) executeCapacityReplay(ctx context.Context, plan CapacityReplayPlan, token string, timeout time.Duration) CapacityReplayRun {
	started := time.Now().UTC()
	run := CapacityReplayRun{
		Status:    "pass",
		StartedAt: started,
		Plan:      plan,
	}
	client := control.NewClient(plan.TargetControlURL, token, &http.Client{Timeout: timeout})
	results := make([]CapacityReplayEventResult, len(plan.Events))
	errs := make([]error, len(plan.Events))
	var wg sync.WaitGroup
	for i, event := range plan.Events {
		wg.Add(1)
		go func(i int, event CapacityReplayEventPlan) {
			defer wg.Done()
			results[i], errs[i] = r.executeCapacityReplayEvent(ctx, client, plan, event, started)
		}(i, event)
	}
	wg.Wait()
	for i, result := range results {
		run.Events = append(run.Events, result)
		if errs[i] != nil && run.Error == "" {
			run.Status = "fail"
			run.Error = errs[i].Error()
		}
		if result.ActivateFailures > 0 || result.DeactivateFailures > 0 {
			run.Status = "fail"
			if run.Error == "" {
				run.Error = "one or more capacity replay target-control operations failed"
			}
		}
	}
	run.CompletedAt = time.Now().UTC()
	return run
}

func (r Runner) executeCapacityReplayEvent(ctx context.Context, client *control.Client, plan CapacityReplayPlan, event CapacityReplayEventPlan, started time.Time) (CapacityReplayEventResult, error) {
	result := CapacityReplayEventResult{ID: event.ID}
	offset, err := time.ParseDuration(event.Offset)
	if err != nil {
		return result, err
	}
	duration, err := time.ParseDuration(event.Duration)
	if err != nil {
		return result, err
	}
	if err := r.Sleeper.Sleep(ctx, time.Until(started.Add(offset))); err != nil {
		return result, err
	}
	activatedAt := time.Now().UTC()
	result.ActivatedAt = &activatedAt
	result.Hosts = make([]CapacityReplayHostResult, 0, len(event.Hosts))
	for _, host := range event.Hosts {
		hostResult := CapacityReplayHostResult{Host: host}
		if err := client.Activate(ctx, control.ActivateRequest{
			RunID: capacityReplayControlRunID(plan.RunID, event.ID),
			Seed:  event.Seed,
			Failure: control.FailureSpec{
				Type:     event.Type,
				Host:     host,
				Path:     event.Path,
				Duration: duration + 30*time.Second,
				Rate:     event.Rate,
				Params:   replayFailureParams(event),
			},
		}); err != nil {
			hostResult.ActivateError = err.Error()
			result.ActivateFailures++
		}
		result.Hosts = append(result.Hosts, hostResult)
	}
	if err := r.Sleeper.Sleep(ctx, duration); err != nil {
		return result, err
	}
	deactivatedAt := time.Now().UTC()
	result.DeactivatedAt = &deactivatedAt
	for i := range result.Hosts {
		host := result.Hosts[i].Host
		if err := client.Deactivate(ctx, control.DeactivateRequest{
			RunID:       capacityReplayControlRunID(plan.RunID, event.ID),
			FailureType: event.Type,
			Host:        host,
			Path:        event.Path,
		}); err != nil {
			result.Hosts[i].DeactivateError = err.Error()
			result.DeactivateFailures++
		}
	}
	return result, nil
}

func buildCapacityReplayPlan(cfg RunConfig, services []ServiceLifecycle, activeCount int, windowDuration time.Duration, runID string) (CapacityReplayPlan, error) {
	cfg = cfg.Normalize()
	plan := CapacityReplayPlan{
		RunID:            runID,
		TargetControlURL: cfg.CapacityReplay.TargetControlURL,
		ActiveCount:      activeCount,
		WindowDuration:   windowDuration.String(),
		Seed:             cfg.CapacityReplay.Seed,
	}
	if plan.RunID == "" {
		plan.RunID = fmt.Sprintf("capacity-replay-%d", time.Now().UTC().Unix())
	}
	for i, event := range cfg.CapacityReplay.Events {
		offset, err := capacityReplayEventOffset(event, i)
		if err != nil {
			return CapacityReplayPlan{}, err
		}
		duration, err := capacityReplayEventDuration(event, i)
		if err != nil {
			return CapacityReplayPlan{}, err
		}
		if offset+duration > windowDuration {
			return CapacityReplayPlan{}, fmt.Errorf("capacity_replay.events[%d] extends beyond batch window: offset %s + duration %s > %s", i, offset, duration, windowDuration)
		}
		eventSeed := event.Seed
		if eventSeed == 0 {
			eventSeed = cfg.CapacityReplay.Seed + int64(i+1)*7919 + int64(activeCount)
		}
		eventPlan := CapacityReplayEventPlan{
			ID:         event.ID,
			Offset:     offset.String(),
			Duration:   duration.String(),
			Type:       event.Type,
			StatusCode: event.StatusCode,
			Rate:       event.Rate,
			Path:       event.Path,
			Method:     event.Method,
			Seed:       eventSeed,
		}
		hostSeen := map[string]bool{}
		if event.HostStart > 0 || len(services) == 0 {
			hostNumbers := capacityReplayHostNumbers(event, activeCount, eventSeed, firstReplayHostStart(cfg))
			eventPlan.HostNumbers = append(eventPlan.HostNumbers, hostNumbers...)
			for _, n := range hostNumbers {
				host := fmt.Sprintf(cfg.Targets.HostPattern, n)
				if !hostSeen[host] {
					eventPlan.Hosts = append(eventPlan.Hosts, host)
					hostSeen[host] = true
				}
			}
			eventPlan.Services = append(eventPlan.Services, capacityReplayServiceHostsForNumbers(services, cfg.Targets.HostPattern, hostNumbers, activeCount)...)
		} else {
			for serviceIndex, service := range services {
				serviceSeed := eventSeed + int64(serviceIndex)*104729
				hostNumbers := capacityReplayHostNumbers(event, activeCount, serviceSeed, service.Config.URLNumberStart)
				serviceHosts := CapacityReplayServiceHosts{
					Service:     service.ID,
					HostNumbers: append([]int64(nil), hostNumbers...),
					Hosts:       make([]string, 0, len(hostNumbers)),
				}
				for _, n := range hostNumbers {
					host := fmt.Sprintf(cfg.Targets.HostPattern, n)
					serviceHosts.Hosts = append(serviceHosts.Hosts, host)
					if !hostSeen[host] {
						eventPlan.HostNumbers = append(eventPlan.HostNumbers, n)
						eventPlan.Hosts = append(eventPlan.Hosts, host)
						hostSeen[host] = true
					}
				}
				eventPlan.Services = append(eventPlan.Services, serviceHosts)
			}
		}
		sort.Slice(eventPlan.HostNumbers, func(i, j int) bool { return eventPlan.HostNumbers[i] < eventPlan.HostNumbers[j] })
		sort.Strings(eventPlan.Hosts)
		plan.Events = append(plan.Events, eventPlan)
	}
	return plan, nil
}

func capacityReplayServiceHostsForNumbers(services []ServiceLifecycle, hostPattern string, hostNumbers []int64, activeCount int) []CapacityReplayServiceHosts {
	if len(services) == 0 || len(hostNumbers) == 0 {
		return nil
	}
	out := make([]CapacityReplayServiceHosts, 0, len(services))
	for _, service := range services {
		start := service.Config.URLNumberStart
		end := start + int64(activeCount) - 1
		serviceHosts := CapacityReplayServiceHosts{
			Service: service.ID,
		}
		for _, n := range hostNumbers {
			if n < start || n > end {
				continue
			}
			serviceHosts.HostNumbers = append(serviceHosts.HostNumbers, n)
			serviceHosts.Hosts = append(serviceHosts.Hosts, fmt.Sprintf(hostPattern, n))
		}
		if len(serviceHosts.HostNumbers) > 0 {
			out = append(out, serviceHosts)
		}
	}
	return out
}

func firstReplayHostStart(cfg RunConfig) int64 {
	if cfg.Targets.URLStart != 0 {
		return cfg.Targets.URLStart
	}
	return defaultURLNumberStart
}

func capacityReplayHostNumbers(event CapacityReplayEvent, activeCount int, seed int64, defaultHostStart int64) []int64 {
	if activeCount <= 0 {
		return nil
	}
	hostStart := event.HostStart
	if hostStart == 0 {
		hostStart = defaultHostStart
	}
	hostCount := event.HostCount
	if hostCount <= 0 || hostCount > activeCount {
		hostCount = activeCount
	}
	sampleCount := event.SampleCount
	if sampleCount <= 0 {
		sampleCount = hostCount
		if sampleCount > 25 {
			sampleCount = 25
		}
	}
	if sampleCount > hostCount {
		sampleCount = hostCount
	}
	rng := rand.New(rand.NewSource(seed))
	perm := rng.Perm(hostCount)
	numbers := make([]int64, 0, sampleCount)
	for _, offset := range perm[:sampleCount] {
		numbers = append(numbers, hostStart+int64(offset))
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	return numbers
}

func replayFailureParams(event CapacityReplayEventPlan) map[string]any {
	params := map[string]any{}
	if event.StatusCode > 0 {
		params["status_code"] = event.StatusCode
	}
	if strings.TrimSpace(event.Method) != "" {
		params["method"] = event.Method
	}
	return params
}

func capacityReplayControlRunID(runID, eventID string) string {
	runID = strings.TrimSpace(runID)
	eventID = strings.TrimSpace(eventID)
	if runID == "" {
		runID = "capacity-replay"
	}
	if eventID == "" {
		return runID
	}
	return runID + "-" + eventID
}

func resolveCapacityReplayToken(cfg CapacityReplayConfig) (string, error) {
	if cfg.TokenFile != "" {
		data, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read capacity_replay.token_file %s: %w", cfg.TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("capacity_replay.token_file %s is empty", cfg.TokenFile)
		}
		return token, nil
	}
	envName := cfg.TokenEnv
	if envName == "" {
		envName = "CONTROL_TOKEN"
	}
	token := strings.TrimSpace(os.Getenv(envName))
	if token == "" {
		return "", fmt.Errorf("capacity replay token is required: configure capacity_replay.token_file or set %s", envName)
	}
	return token, nil
}

func writeCapacityReplayPlan(dir string, plan CapacityReplayPlan, m *RunManifest) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal capacity replay plan: %w", err)
	}
	path := filepath.Join(dir, "capacity-replay-plan.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: "capacity-replay-plan", Path: path})
	return nil
}

func writeCapacityReplayRun(dir string, run CapacityReplayRun, m *RunManifest) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal capacity replay run: %w", err)
	}
	path := filepath.Join(dir, "capacity-replay-run.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: "capacity-replay-run", Path: path})
	return nil
}
