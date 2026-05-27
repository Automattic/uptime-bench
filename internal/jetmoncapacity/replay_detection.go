package jetmoncapacity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReplayDetectionRun records service event-history correlation for the
// deterministic downtime/recovery windows applied by capacity replay.
type ReplayDetectionRun struct {
	Status      string                 `json:"status"`
	Error       string                 `json:"error,omitempty"`
	CollectedAt time.Time              `json:"collected_at"`
	Events      []ReplayDetectionEvent `json:"events"`
}

// ReplayDetectionEvent summarizes detection for one replay event.
type ReplayDetectionEvent struct {
	ID              string                          `json:"id"`
	Type            string                          `json:"type"`
	ExpectedOutcome string                          `json:"expected_outcome,omitempty"`
	ActivatedAt     *time.Time                      `json:"activated_at,omitempty"`
	DeactivatedAt   *time.Time                      `json:"deactivated_at,omitempty"`
	Services        []ReplayDetectionServiceSummary `json:"services"`
}

// ReplayDetectionServiceSummary summarizes detection for one service and replay
// event.
type ReplayDetectionServiceSummary struct {
	Service                          string                `json:"service"`
	Status                           string                `json:"status"`
	ExpectedOutcome                  string                `json:"expected_outcome,omitempty"`
	Error                            string                `json:"error,omitempty"`
	Hosts                            int                   `json:"hosts"`
	EligibleHosts                    int                   `json:"eligible_hosts"`
	DownDetected                     int                   `json:"down_detected"`
	RecoveryDetected                 int                   `json:"recovery_detected"`
	LateDownDetected                 int                   `json:"late_down_detected"`
	PreexistingDownOverlappedFailure int                   `json:"preexisting_down_overlapped_failure"`
	MissingDown                      int                   `json:"missing_down"`
	MissingRecovery                  int                   `json:"missing_recovery"`
	ExpectedCheckIntervalSec         int                   `json:"expected_check_interval_sec,omitempty"`
	NormalCheckIntervalMinSec        *int                  `json:"normal_check_interval_min_sec,omitempty"`
	NormalCheckIntervalMaxSec        *int                  `json:"normal_check_interval_max_sec,omitempty"`
	NextCheckIntervalMinSec          *int                  `json:"next_check_interval_min_sec,omitempty"`
	NextCheckIntervalMaxSec          *int                  `json:"next_check_interval_max_sec,omitempty"`
	CheckIntervalMismatchEvents      int                   `json:"check_interval_mismatch_events,omitempty"`
	DownLatencyMinSec                *float64              `json:"down_latency_min_sec,omitempty"`
	DownLatencyMeanSec               *float64              `json:"down_latency_mean_sec,omitempty"`
	DownLatencyMaxSec                *float64              `json:"down_latency_max_sec,omitempty"`
	RecoveryLatencyMinSec            *float64              `json:"recovery_latency_min_sec,omitempty"`
	RecoveryLatencyMeanSec           *float64              `json:"recovery_latency_mean_sec,omitempty"`
	RecoveryLatencyMaxSec            *float64              `json:"recovery_latency_max_sec,omitempty"`
	HostResults                      []ReplayDetectionHost `json:"host_results,omitempty"`
}

// ReplayDetectionHost records per-host detection and raw service evidence.
type ReplayDetectionHost struct {
	Host                             string                    `json:"host"`
	HostNumber                       int64                     `json:"host_number"`
	BlogID                           int64                     `json:"blog_id"`
	DownDetected                     bool                      `json:"down_detected"`
	DownDetectedDuring               bool                      `json:"down_detected_during_failure"`
	PreexistingDownOverlappedFailure bool                      `json:"preexisting_down_overlapped_failure,omitempty"`
	LateDownDetected                 bool                      `json:"late_down_detected,omitempty"`
	DownAt                           *time.Time                `json:"down_at,omitempty"`
	DownLatencySec                   *float64                  `json:"down_latency_sec,omitempty"`
	RecoveryDetected                 bool                      `json:"recovery_detected"`
	RecoveryAt                       *time.Time                `json:"recovery_at,omitempty"`
	RecoveryLatencySec               *float64                  `json:"recovery_latency_sec,omitempty"`
	RawEvents                        []ReplayDetectionRawEvent `json:"raw_events,omitempty"`
}

// ReplayDetectionRawEvent is a compact cross-version event-history row.
type ReplayDetectionRawEvent struct {
	ID               string         `json:"id,omitempty"`
	EventType        string         `json:"event_type,omitempty"`
	Source           string         `json:"source,omitempty"`
	CheckType        string         `json:"check_type,omitempty"`
	State            string         `json:"state,omitempty"`
	OldStatus        *int           `json:"old_status,omitempty"`
	NewStatus        *int           `json:"new_status,omitempty"`
	HTTPCode         *int           `json:"http_code,omitempty"`
	StartedAt        *time.Time     `json:"started_at,omitempty"`
	EndedAt          *time.Time     `json:"ended_at,omitempty"`
	ReportedAt       *time.Time     `json:"reported_at,omitempty"`
	ResolutionReason string         `json:"resolution_reason,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type replayDetectionHostSpec struct {
	Host       string
	HostNumber int64
	BlogID     int64
}

func (r Runner) collectReplayDetections(ctx context.Context, dir string, services []ServiceLifecycle, cfg RunConfig, m *RunManifest) error {
	cfg = cfg.Normalize()
	if !cfg.ReplayDetection.Enabled {
		return nil
	}
	replay := latestCapacityReplay(m.CapacityReplays)
	if replay == nil {
		return nil
	}
	timeout, err := cfg.ReplayDetectionTimeout()
	if err != nil {
		return err
	}
	padding, err := cfg.ReplayDetectionWindowPadding()
	if err != nil {
		return err
	}
	m.ReplayDetectionStatus = "running"
	m.ReplayDetectionError = ""
	result := ReplayDetectionRun{
		Status:      "pass",
		CollectedAt: time.Now().UTC(),
	}
	serviceByID := make(map[string]ServiceLifecycle, len(services))
	for _, service := range services {
		serviceByID[service.ID] = service
	}
	for _, eventResult := range replay.Events {
		planEvent, ok := replayPlanEvent(replay.Plan, eventResult.ID)
		if !ok {
			continue
		}
		eventSummary := ReplayDetectionEvent{
			ID:              eventResult.ID,
			Type:            planEvent.Type,
			ExpectedOutcome: planEvent.ExpectedOutcome,
			ActivatedAt:     eventResult.ActivatedAt,
			DeactivatedAt:   eventResult.DeactivatedAt,
		}
		for _, serviceHosts := range planEvent.Services {
			service, ok := serviceByID[serviceHosts.Service]
			if !ok {
				continue
			}
			specs := replayDetectionHostSpecs(service, serviceHosts, cfg.Targets.HostPattern)
			serviceSummary, err := r.collectReplayDetectionService(ctx, service, cfg, timeout, padding, eventResult, planEvent.ExpectedOutcome, specs, result.CollectedAt)
			if err != nil {
				serviceSummary.Service = service.ID
				serviceSummary.Status = "fail"
				serviceSummary.Error = err.Error()
				serviceSummary.Hosts = len(specs)
			}
			if serviceSummary.Status != "pass" {
				result.Status = "fail"
			}
			eventSummary.Services = append(eventSummary.Services, serviceSummary)
		}
		result.Events = append(result.Events, eventSummary)
	}
	if result.Status != "pass" && result.Error == "" {
		result.Error = "one or more replayed downtime/recovery events were not recorded accurately"
	}
	m.ReplayDetections = append(m.ReplayDetections, result)
	if result.Status == "pass" {
		m.ReplayDetectionStatus = "pass"
		m.ReplayDetectionError = ""
	} else {
		m.ReplayDetectionStatus = "fail"
		m.ReplayDetectionError = result.Error
	}
	if err := writeReplayDetectionRun(dir, result, m); err != nil {
		return err
	}
	if result.Status != "pass" {
		return fmt.Errorf("replay detection failed: %s", result.Error)
	}
	return nil
}

func (r Runner) collectReplayDetectionService(ctx context.Context, service ServiceLifecycle, cfg RunConfig, timeout, padding time.Duration, event CapacityReplayEventResult, expectedOutcome string, specs []replayDetectionHostSpec, collectedAt time.Time) (ReplayDetectionServiceSummary, error) {
	expectedOutcome = strings.ToLower(strings.TrimSpace(expectedOutcome))
	if expectedOutcome == "" {
		expectedOutcome = "down_recovery"
	}
	summary := ReplayDetectionServiceSummary{
		Service:                  service.ID,
		Status:                   "pass",
		ExpectedOutcome:          expectedOutcome,
		Hosts:                    len(specs),
		ExpectedCheckIntervalSec: service.Config.CheckIntervalMinutes * 60,
	}
	if event.ActivatedAt == nil || event.DeactivatedAt == nil {
		summary.Status = "fail"
		summary.Error = "replay event did not record activation and deactivation timestamps"
		return summary, nil
	}
	if len(specs) == 0 {
		summary.Status = "fail"
		summary.Error = "no replay hosts were mapped to service blog IDs"
		return summary, nil
	}
	var eventsByBlogID map[int64][]ReplayDetectionRawEvent
	var err error
	switch service.Config.Schema {
	case SchemaV1:
		eventsByBlogID, err = collectV1ReplayEvents(ctx, cfg, timeout, padding, event, specs, collectedAt)
	case SchemaV2:
		eventsByBlogID, err = r.collectV2ReplayEvents(ctx, service, padding, event, specs, collectedAt)
	default:
		err = fmt.Errorf("unsupported replay detection schema %q", service.Config.Schema)
	}
	if err != nil {
		summary.Status = "fail"
		summary.Error = err.Error()
		return summary, nil
	}
	var downLatencies []float64
	var recoveryLatencies []float64
	for _, spec := range specs {
		host := analyzeReplayDetectionHost(spec, eventsByBlogID[spec.BlogID], *event.ActivatedAt, *event.DeactivatedAt)
		if expectedOutcome == "up" {
			summary.EligibleHosts++
			if host.PreexistingDownOverlappedFailure || host.DownDetectedDuring {
				summary.DownDetected++
			}
			if host.LateDownDetected {
				summary.LateDownDetected++
			}
			if host.RecoveryDetected {
				summary.RecoveryDetected++
			}
			summary.HostResults = append(summary.HostResults, host)
			continue
		}
		if host.PreexistingDownOverlappedFailure {
			summary.PreexistingDownOverlappedFailure++
		} else {
			summary.EligibleHosts++
		}
		if host.PreexistingDownOverlappedFailure {
			// A host that was already down when the scripted failure began is
			// not a clean sample for in-window replay detection. Keep it visible
			// as contamination instead of counting it as a missed injected event.
		} else if host.DownDetectedDuring {
			summary.DownDetected++
			if host.DownLatencySec != nil {
				downLatencies = append(downLatencies, *host.DownLatencySec)
			}
		} else {
			summary.MissingDown++
			if host.LateDownDetected {
				summary.LateDownDetected++
			}
		}
		if host.RecoveryDetected {
			summary.RecoveryDetected++
			if host.RecoveryLatencySec != nil {
				recoveryLatencies = append(recoveryLatencies, *host.RecoveryLatencySec)
			}
		} else {
			summary.MissingRecovery++
		}
		summary.HostResults = append(summary.HostResults, host)
	}
	summary.DownLatencyMinSec, summary.DownLatencyMeanSec, summary.DownLatencyMaxSec = latencyStats(downLatencies)
	summary.RecoveryLatencyMinSec, summary.RecoveryLatencyMeanSec, summary.RecoveryLatencyMaxSec = latencyStats(recoveryLatencies)
	intervals := summarizeReplayDetectionIntervals(summary.HostResults, summary.ExpectedCheckIntervalSec)
	summary.NormalCheckIntervalMinSec = intervals.normalMin
	summary.NormalCheckIntervalMaxSec = intervals.normalMax
	summary.NextCheckIntervalMinSec = intervals.nextMin
	summary.NextCheckIntervalMaxSec = intervals.nextMax
	summary.CheckIntervalMismatchEvents = intervals.mismatchEvents
	if expectedOutcome == "up" {
		if summary.DownDetected > 0 || summary.LateDownDetected > 0 || summary.RecoveryDetected > 0 {
			summary.Status = "fail"
			var parts []string
			if summary.DownDetected > 0 {
				parts = append(parts, fmt.Sprintf("%d hosts had unexpected down detection", summary.DownDetected))
			}
			if summary.LateDownDetected > 0 {
				parts = append(parts, fmt.Sprintf("%d hosts had unexpected late down detection", summary.LateDownDetected))
			}
			if summary.RecoveryDetected > 0 {
				parts = append(parts, fmt.Sprintf("%d hosts had unexpected recovery events", summary.RecoveryDetected))
			}
			summary.Error = strings.Join(parts, "; ")
		}
	} else if summary.MissingDown > 0 || summary.MissingRecovery > 0 || summary.LateDownDetected > 0 || summary.PreexistingDownOverlappedFailure > 0 {
		summary.Status = "fail"
		var parts []string
		if summary.PreexistingDownOverlappedFailure > 0 {
			parts = append(parts, fmt.Sprintf("%d hosts had pre-existing down events overlapping the replay window", summary.PreexistingDownOverlappedFailure))
		}
		if summary.MissingDown > 0 {
			parts = append(parts, fmt.Sprintf("%d hosts missing in-window down detection", summary.MissingDown))
		}
		if summary.LateDownDetected > 0 {
			parts = append(parts, fmt.Sprintf("%d hosts had only late down detection", summary.LateDownDetected))
		}
		if summary.MissingRecovery > 0 {
			parts = append(parts, fmt.Sprintf("%d hosts missing recovery detection", summary.MissingRecovery))
		}
		summary.Error = strings.Join(parts, "; ")
	}
	if cfg.ReplayDetection.FailOnCheckIntervalMismatch && summary.CheckIntervalMismatchEvents > 0 {
		summary.Status = "fail"
		reason := fmt.Sprintf("%d events reported check interval metadata different from expected %ds", summary.CheckIntervalMismatchEvents, summary.ExpectedCheckIntervalSec)
		if summary.Error != "" {
			summary.Error += "; " + reason
		} else {
			summary.Error = reason
		}
	}
	return summary, nil
}

type replayDetectionIntervalSummary struct {
	normalMin      *int
	normalMax      *int
	nextMin        *int
	nextMax        *int
	mismatchEvents int
}

func summarizeReplayDetectionIntervals(hosts []ReplayDetectionHost, expectedSec int) replayDetectionIntervalSummary {
	var summary replayDetectionIntervalSummary
	for _, host := range hosts {
		for _, event := range host.RawEvents {
			normal, hasNormal := replayMetadataObservationInterval(event.Metadata, "normal_check_interval_seconds")
			next, hasNext := replayMetadataObservationInterval(event.Metadata, "next_check_interval_seconds")
			if hasNormal {
				updateIntRange(&summary.normalMin, &summary.normalMax, normal)
			}
			if hasNext {
				updateIntRange(&summary.nextMin, &summary.nextMax, next)
			}
			if expectedSec > 0 && hasNormal && normal != expectedSec {
				summary.mismatchEvents++
			}
		}
	}
	return summary
}

func replayMetadataObservationInterval(metadata map[string]any, key string) (int, bool) {
	if len(metadata) == 0 {
		return 0, false
	}
	observation, ok := metadata["observation"].(map[string]any)
	if !ok {
		return 0, false
	}
	return metadataInt(observation[key])
}

func metadataInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		if err == nil {
			return int(n), true
		}
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func updateIntRange(min, max **int, value int) {
	if *min == nil || value < **min {
		v := value
		*min = &v
	}
	if *max == nil || value > **max {
		v := value
		*max = &v
	}
}

func replayPlanEvent(plan CapacityReplayPlan, id string) (CapacityReplayEventPlan, bool) {
	for _, event := range plan.Events {
		if event.ID == id {
			return event, true
		}
	}
	return CapacityReplayEventPlan{}, false
}

func replayDetectionHostSpecs(service ServiceLifecycle, serviceHosts CapacityReplayServiceHosts, hostPattern string) []replayDetectionHostSpec {
	n := len(serviceHosts.HostNumbers)
	if len(serviceHosts.Hosts) < n {
		n = len(serviceHosts.Hosts)
	}
	specs := make([]replayDetectionHostSpec, 0, n)
	for i := 0; i < n; i++ {
		hostNumber := serviceHosts.HostNumbers[i]
		offset := hostNumber - service.Config.URLNumberStart
		if offset < 0 || offset >= int64(service.Config.Count) {
			continue
		}
		host := serviceHosts.Hosts[i]
		if host == "" && hostPattern != "" {
			host = fmt.Sprintf(hostPattern, hostNumber)
		}
		specs = append(specs, replayDetectionHostSpec{
			Host:       host,
			HostNumber: hostNumber,
			BlogID:     service.Config.BlogIDStart + offset,
		})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].BlogID < specs[j].BlogID })
	return specs
}

type v1BridgeEventResponse struct {
	ID        int64   `json:"id"`
	BlogID    int64   `json:"blog_id"`
	EventType string  `json:"event_type"`
	Source    string  `json:"source"`
	HTTPCode  *int    `json:"http_code"`
	OldStatus *int    `json:"old_status"`
	NewStatus *int    `json:"new_status"`
	Detail    *string `json:"detail"`
	CreatedAt string  `json:"created_at"`
}

func collectV1ReplayEvents(ctx context.Context, cfg RunConfig, timeout, padding time.Duration, event CapacityReplayEventResult, specs []replayDetectionHostSpec, collectedAt time.Time) (map[int64][]ReplayDetectionRawEvent, error) {
	bridgeURL := strings.TrimRight(strings.TrimSpace(cfg.ReplayDetection.V1BridgeURL), "/")
	if bridgeURL == "" {
		return nil, fmt.Errorf("replay_detection.v1_bridge_url is required for jetmon-v1 detection")
	}
	token, err := resolveReplayDetectionV1Token(cfg.ReplayDetection)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: timeout}
	out := make(map[int64][]ReplayDetectionRawEvent, len(specs))
	since := event.ActivatedAt.Add(-padding).UTC()
	until := collectedAt.Add(padding).UTC()
	for _, spec := range specs {
		q := url.Values{}
		q.Set("blog_id", strconv.FormatInt(spec.BlogID, 10))
		q.Set("since", since.Format(time.RFC3339Nano))
		q.Set("until", until.Format(time.RFC3339Nano))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, bridgeURL+"/events?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("jetmon-v1 bridge /events blog_id=%d: %w", spec.BlogID, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("jetmon-v1 bridge /events blog_id=%d read: %w", spec.BlogID, readErr)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("jetmon-v1 bridge /events blog_id=%d status %d: %s", spec.BlogID, resp.StatusCode, truncateString(string(body), 200))
		}
		var events []v1BridgeEventResponse
		if err := json.Unmarshal(body, &events); err != nil {
			return nil, fmt.Errorf("jetmon-v1 bridge /events blog_id=%d decode: %w", spec.BlogID, err)
		}
		for _, ev := range events {
			raw := ReplayDetectionRawEvent{
				ID:        strconv.FormatInt(ev.ID, 10),
				EventType: ev.EventType,
				Source:    ev.Source,
				OldStatus: ev.OldStatus,
				NewStatus: ev.NewStatus,
				HTTPCode:  ev.HTTPCode,
			}
			if ev.Detail != nil {
				raw.Metadata = map[string]any{"detail": *ev.Detail}
			}
			if t, ok := parseReplayDetectionTime(ev.CreatedAt); ok {
				raw.ReportedAt = &t
			}
			out[spec.BlogID] = append(out[spec.BlogID], raw)
		}
	}
	return out, nil
}

func (r Runner) collectV2ReplayEvents(ctx context.Context, service ServiceLifecycle, padding time.Duration, event CapacityReplayEventResult, specs []replayDetectionHostSpec, collectedAt time.Time) (map[int64][]ReplayDetectionRawEvent, error) {
	var ids []string
	for _, spec := range specs {
		ids = append(ids, strconv.FormatInt(spec.BlogID, 10))
	}
	since := event.ActivatedAt.Add(-padding).UTC()
	until := collectedAt.Add(padding).UTC()
	sqlText := fmt.Sprintf(`SELECT
  CAST(id AS CHAR) AS event_id,
  blog_id,
  check_type,
  state,
  DATE_FORMAT(started_at, '%%Y-%%m-%%dT%%H:%%i:%%s.%%fZ') AS started_at,
  CASE WHEN ended_at IS NULL THEN NULL ELSE DATE_FORMAT(ended_at, '%%Y-%%m-%%dT%%H:%%i:%%s.%%fZ') END AS ended_at,
  COALESCE(resolution_reason, '') AS resolution_reason,
  CAST(metadata AS CHAR) AS metadata
FROM %s
WHERE blog_id IN (%s)
  AND started_at < %s
  AND (ended_at IS NULL OR ended_at >= %s)
ORDER BY blog_id ASC, started_at ASC, id ASC;
`, v2TableEvents, strings.Join(ids, ", "), sqlString(mysqlTimeLiteral(until)), sqlString(mysqlTimeLiteral(since)))
	result, err := r.execServiceSQL(ctx, service, sqlText)
	if err != nil {
		return nil, err
	}
	stmt, ok := statementWithColumns(result, "event_id", "blog_id", "check_type", "state", "started_at")
	if !ok {
		return map[int64][]ReplayDetectionRawEvent{}, nil
	}
	out := map[int64][]ReplayDetectionRawEvent{}
	for _, row := range stmt.Rows {
		value := func(column string) string {
			idx := columnIndex(stmt.Columns, column)
			if idx == -1 || idx >= len(row) {
				return ""
			}
			return row[idx]
		}
		blogID, err := strconv.ParseInt(value("blog_id"), 10, 64)
		if err != nil {
			continue
		}
		raw := ReplayDetectionRawEvent{
			ID:               value("event_id"),
			CheckType:        value("check_type"),
			State:            value("state"),
			ResolutionReason: value("resolution_reason"),
		}
		if t, ok := parseReplayDetectionTime(value("started_at")); ok {
			raw.StartedAt = &t
		}
		if t, ok := parseReplayDetectionTime(value("ended_at")); ok {
			raw.EndedAt = &t
		}
		if meta := strings.TrimSpace(value("metadata")); meta != "" && meta != "NULL" {
			var decoded map[string]any
			if err := json.Unmarshal([]byte(meta), &decoded); err == nil {
				raw.Metadata = decoded
			}
		}
		out[blogID] = append(out[blogID], raw)
	}
	return out, nil
}

func analyzeReplayDetectionHost(spec replayDetectionHostSpec, events []ReplayDetectionRawEvent, activatedAt, deactivatedAt time.Time) ReplayDetectionHost {
	sort.SliceStable(events, func(i, j int) bool {
		return replayRawEventTime(events[i]).Before(replayRawEventTime(events[j]))
	})
	host := ReplayDetectionHost{
		Host:       spec.Host,
		HostNumber: spec.HostNumber,
		BlogID:     spec.BlogID,
		RawEvents:  events,
	}
	for i := range events {
		if replayRawEventOverlapsActivation(events, i, activatedAt) {
			host.PreexistingDownOverlappedFailure = true
			break
		}
	}
	if !host.PreexistingDownOverlappedFailure {
		for _, ev := range events {
			if !replayRawEventDown(ev) {
				continue
			}
			t := replayRawEventTime(ev)
			if t.IsZero() || t.Before(activatedAt) {
				continue
			}
			if host.DownAt == nil {
				detectedAt := t
				host.DownAt = &detectedAt
				latency := detectedAt.Sub(activatedAt).Seconds()
				host.DownLatencySec = &latency
				host.DownDetected = true
				if !detectedAt.After(deactivatedAt) {
					host.DownDetectedDuring = true
				} else {
					host.LateDownDetected = true
				}
			}
		}
	}
	for _, ev := range events {
		if !replayRawEventRecovery(ev) {
			continue
		}
		t := replayRawEventRecoveryTime(ev)
		if t.IsZero() || t.Before(deactivatedAt) {
			continue
		}
		recoveredAt := t
		host.RecoveryAt = &recoveredAt
		latency := recoveredAt.Sub(deactivatedAt).Seconds()
		host.RecoveryLatencySec = &latency
		host.RecoveryDetected = true
		break
	}
	return host
}

func replayRawEventOverlapsActivation(events []ReplayDetectionRawEvent, index int, activatedAt time.Time) bool {
	if index < 0 || index >= len(events) {
		return false
	}
	ev := events[index]
	if !replayRawEventDown(ev) {
		return false
	}
	startedAt := replayRawEventTime(ev)
	if startedAt.IsZero() || !startedAt.Before(activatedAt) {
		return false
	}
	if ev.NewStatus == nil {
		recoveredAt := replayRawEventRecoveryTime(ev)
		if !recoveredAt.IsZero() {
			return recoveredAt.After(activatedAt)
		}
	}
	for _, candidate := range events[index+1:] {
		if !replayRawEventRecovery(candidate) {
			continue
		}
		candidateTime := replayRawEventRecoveryTime(candidate)
		if candidateTime.IsZero() || candidateTime.Before(startedAt) {
			continue
		}
		return candidateTime.After(activatedAt)
	}
	return true
}

func replayRawEventDown(ev ReplayDetectionRawEvent) bool {
	if ev.NewStatus != nil {
		return *ev.NewStatus == 0 || *ev.NewStatus == 2
	}
	state := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(ev.State), " ", "_"))
	return state == "seems_down" || state == "down" || state == "degraded"
}

func replayRawEventRecovery(ev ReplayDetectionRawEvent) bool {
	if ev.NewStatus != nil {
		return *ev.NewStatus == 1
	}
	return ev.EndedAt != nil
}

func replayRawEventTime(ev ReplayDetectionRawEvent) time.Time {
	if ev.ReportedAt != nil {
		return ev.ReportedAt.UTC()
	}
	if ev.StartedAt != nil {
		return ev.StartedAt.UTC()
	}
	return time.Time{}
}

func replayRawEventRecoveryTime(ev ReplayDetectionRawEvent) time.Time {
	if ev.NewStatus != nil {
		return replayRawEventTime(ev)
	}
	if ev.EndedAt != nil {
		return ev.EndedAt.UTC()
	}
	return time.Time{}
}

func latencyStats(values []float64) (*float64, *float64, *float64) {
	if len(values) == 0 {
		return nil, nil, nil
	}
	min := values[0]
	max := values[0]
	sum := 0.0
	for _, value := range values {
		if value < min {
			min = value
		}
		if value > max {
			max = value
		}
		sum += value
	}
	mean := sum / float64(len(values))
	return &min, &mean, &max
}

func resolveReplayDetectionV1Token(cfg ReplayDetectionConfig) (string, error) {
	if cfg.V1TokenFile != "" {
		data, err := os.ReadFile(cfg.V1TokenFile)
		if err != nil {
			return "", fmt.Errorf("read replay_detection.v1_token_file %s: %w", cfg.V1TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("replay_detection.v1_token_file %s is empty", cfg.V1TokenFile)
		}
		return token, nil
	}
	envName := cfg.V1TokenEnv
	if envName == "" {
		envName = "JETMON_BRIDGE_TOKEN"
	}
	token := strings.TrimSpace(os.Getenv(envName))
	if token == "" {
		return "", fmt.Errorf("jetmon-v1 replay detection token is required: configure replay_detection.v1_token_file or set %s", envName)
	}
	return token, nil
}

func parseReplayDetectionTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "NULL" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, raw)
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func mysqlTimeLiteral(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.000000")
}

func truncateString(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "..."
}

func writeReplayDetectionRun(dir string, run ReplayDetectionRun, m *RunManifest) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal replay detection run: %w", err)
	}
	path := filepath.Join(dir, "capacity-replay-detection.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: "capacity-replay-detection", Path: path})
	return nil
}
