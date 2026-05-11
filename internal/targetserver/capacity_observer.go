package targetserver

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var hostPatternVerbRE = regexp.MustCompile(`%0?\d*d`)

// CapacityObserveResetRequest starts a fresh target-side observation window for
// generated capacity-test hostnames.
type CapacityObserveResetRequest struct {
	RunID                string                   `json:"run_id,omitempty"`
	ActiveCount          int                      `json:"active_count"`
	CheckIntervalSeconds int                      `json:"check_interval_seconds,omitempty"`
	StaleAfterSeconds    int                      `json:"stale_after_seconds,omitempty"`
	Services             []CapacityObserveService `json:"services"`
}

// CapacityObserveService describes one service's generated target namespace.
type CapacityObserveService struct {
	ID                   string `json:"id"`
	HostPattern          string `json:"host_pattern"`
	URLStart             int64  `json:"url_start"`
	Count                int    `json:"count,omitempty"`
	CheckIntervalSeconds int    `json:"check_interval_seconds,omitempty"`
	StaleAfterSeconds    int    `json:"stale_after_seconds,omitempty"`
}

// CapacityObserveSummary is the target-side black-box view of monitor traffic
// during a capacity window.
type CapacityObserveSummary struct {
	Active               bool                            `json:"active"`
	RunID                string                          `json:"run_id,omitempty"`
	StartedAt            time.Time                       `json:"started_at,omitempty"`
	SnapshotAt           time.Time                       `json:"snapshot_at"`
	ElapsedSeconds       float64                         `json:"elapsed_seconds,omitempty"`
	ActiveCount          int                             `json:"active_count,omitempty"`
	CheckIntervalSeconds int                             `json:"check_interval_seconds,omitempty"`
	StaleAfterSeconds    int                             `json:"stale_after_seconds,omitempty"`
	Services             []CapacityObserveServiceSummary `json:"services,omitempty"`
}

// CapacityObserveServiceSummary summarizes observed requests for one service.
type CapacityObserveServiceSummary struct {
	ID                       string            `json:"id"`
	HostPattern              string            `json:"host_pattern"`
	URLStart                 int64             `json:"url_start"`
	ExpectedSites            int               `json:"expected_sites"`
	CheckIntervalSeconds     int               `json:"check_interval_seconds,omitempty"`
	StaleAfterSeconds        int               `json:"stale_after_seconds,omitempty"`
	ObservedSites            int               `json:"observed_sites"`
	NeverSeenSites           int               `json:"never_seen_sites"`
	StaleSites               int               `json:"stale_sites"`
	CoveragePercent          float64           `json:"coverage_percent"`
	TotalRequests            uint64            `json:"total_requests"`
	MethodCounts             map[string]uint64 `json:"method_counts,omitempty"`
	StatusCounts             map[string]uint64 `json:"status_counts,omitempty"`
	StatusHostCounts         map[string]int    `json:"status_host_counts,omitempty"`
	RequestsPerSecond        float64           `json:"requests_per_second,omitempty"`
	RequestsPerSiteMin       float64           `json:"requests_per_site_min,omitempty"`
	RequestsPerSiteMean      float64           `json:"requests_per_site_mean,omitempty"`
	RequestsPerSiteP50       float64           `json:"requests_per_site_p50,omitempty"`
	RequestsPerSiteP95       float64           `json:"requests_per_site_p95,omitempty"`
	RequestsPerSiteP99       float64           `json:"requests_per_site_p99,omitempty"`
	RequestsPerSiteMax       float64           `json:"requests_per_site_max,omitempty"`
	LastSeenAgeSecondsMin    float64           `json:"last_seen_age_seconds_min,omitempty"`
	LastSeenAgeSecondsMean   float64           `json:"last_seen_age_seconds_mean,omitempty"`
	LastSeenAgeSecondsP50    float64           `json:"last_seen_age_seconds_p50,omitempty"`
	LastSeenAgeSecondsP95    float64           `json:"last_seen_age_seconds_p95,omitempty"`
	LastSeenAgeSecondsP99    float64           `json:"last_seen_age_seconds_p99,omitempty"`
	LastSeenAgeSecondsMax    float64           `json:"last_seen_age_seconds_max,omitempty"`
	ExpectedMinChecksPerSite int64             `json:"expected_min_checks_per_site,omitempty"`
	ExpectedMinRequests      int64             `json:"expected_min_requests,omitempty"`
	ExpectedRequestRatio     float64           `json:"expected_request_ratio,omitempty"`
}

// CapacityObserver records generated-host traffic served by the target. It is
// intentionally black-box: monitors under test make normal HTTP requests, and
// the target counts what arrived during the active window.
type CapacityObserver struct {
	mu    sync.RWMutex
	state *capacityObserveState
}

type capacityObserveState struct {
	runID         string
	startedAt     time.Time
	activeCount   int
	checkInterval time.Duration
	staleAfter    time.Duration
	services      []*capacityObserveServiceState
}

type capacityObserveServiceState struct {
	id            string
	hostPattern   string
	prefix        string
	suffix        string
	urlStart      int64
	count         int
	checkInterval time.Duration
	staleAfter    time.Duration
	requests      []atomic.Uint64
	lastSeen      []atomic.Int64
	methods       [4]atomic.Uint64
	statuses      [1000]atomic.Uint64
	statusHostMu  sync.Mutex
	statusHosts   map[int]map[int]struct{}
	total         atomic.Uint64
}

// NewCapacityObserver returns an inactive target-side capacity observer.
func NewCapacityObserver() *CapacityObserver {
	return &CapacityObserver{}
}

// Reset replaces the active observation window.
func (o *CapacityObserver) Reset(req CapacityObserveResetRequest, now time.Time) (CapacityObserveSummary, error) {
	if o == nil {
		return CapacityObserveSummary{}, fmt.Errorf("capacity observer is nil")
	}
	if req.ActiveCount <= 0 {
		return CapacityObserveSummary{}, fmt.Errorf("active_count must be positive")
	}
	if len(req.Services) == 0 {
		return CapacityObserveSummary{}, fmt.Errorf("at least one service is required")
	}
	checkInterval := time.Minute
	if req.CheckIntervalSeconds > 0 {
		checkInterval = time.Duration(req.CheckIntervalSeconds) * time.Second
	}
	staleAfter := 2 * checkInterval
	if req.StaleAfterSeconds > 0 {
		staleAfter = time.Duration(req.StaleAfterSeconds) * time.Second
	}
	state := &capacityObserveState{
		runID:         strings.TrimSpace(req.RunID),
		startedAt:     now.UTC(),
		activeCount:   req.ActiveCount,
		checkInterval: checkInterval,
		staleAfter:    staleAfter,
		services:      make([]*capacityObserveServiceState, 0, len(req.Services)),
	}
	for _, service := range req.Services {
		compiled, err := newCapacityObserveServiceState(service, req.ActiveCount, checkInterval, staleAfter)
		if err != nil {
			return CapacityObserveSummary{}, err
		}
		state.services = append(state.services, compiled)
	}
	o.mu.Lock()
	o.state = state
	o.mu.Unlock()
	return state.summary(now.UTC()), nil
}

// Summary returns a point-in-time view of the current observation window.
func (o *CapacityObserver) Summary(now time.Time) CapacityObserveSummary {
	if o == nil {
		return CapacityObserveSummary{Active: false, SnapshotAt: now.UTC()}
	}
	o.mu.RLock()
	state := o.state
	o.mu.RUnlock()
	if state == nil {
		return CapacityObserveSummary{Active: false, SnapshotAt: now.UTC()}
	}
	return state.summary(now.UTC())
}

// Record tracks one served request if its Host matches the active capacity
// namespace.
func (o *CapacityObserver) Record(host, method string, now time.Time) {
	o.record(host, method, -1, now)
}

// RecordResponse tracks one served request plus the HTTP response status if its
// Host matches the active capacity namespace.
func (o *CapacityObserver) RecordResponse(host, method string, statusCode int, now time.Time) {
	o.record(host, method, statusCode, now)
}

func (o *CapacityObserver) record(host, method string, statusCode int, now time.Time) {
	if o == nil {
		return
	}
	o.mu.RLock()
	state := o.state
	o.mu.RUnlock()
	if state == nil {
		return
	}
	host = normalizeObserveHost(host)
	if host == "" {
		return
	}
	for _, service := range state.services {
		offset, ok := service.offsetForHost(host)
		if !ok {
			continue
		}
		service.requests[offset].Add(1)
		service.lastSeen[offset].Store(now.UTC().UnixNano())
		service.total.Add(1)
		service.methods[methodBucket(method)].Add(1)
		if statusCode >= 0 && statusCode < len(service.statuses) {
			service.statuses[statusCode].Add(1)
			service.recordStatusHost(statusCode, offset)
		}
	}
}

func newCapacityObserveServiceState(service CapacityObserveService, activeCount int, defaultCheckInterval, defaultStaleAfter time.Duration) (*capacityObserveServiceState, error) {
	id := strings.TrimSpace(service.ID)
	if id == "" {
		return nil, fmt.Errorf("service id is required")
	}
	hostPattern := strings.TrimSpace(service.HostPattern)
	if hostPattern == "" {
		return nil, fmt.Errorf("%s host_pattern is required", id)
	}
	prefix, suffix, err := splitHostPattern(hostPattern)
	if err != nil {
		return nil, fmt.Errorf("%s host_pattern: %w", id, err)
	}
	urlStart := service.URLStart
	if urlStart == 0 {
		urlStart = 1
	}
	count := service.Count
	if count == 0 || count > activeCount {
		count = activeCount
	}
	if count <= 0 {
		return nil, fmt.Errorf("%s count must be positive", id)
	}
	checkInterval := defaultCheckInterval
	if service.CheckIntervalSeconds > 0 {
		checkInterval = time.Duration(service.CheckIntervalSeconds) * time.Second
	}
	staleAfter := defaultStaleAfter
	if service.StaleAfterSeconds > 0 {
		staleAfter = time.Duration(service.StaleAfterSeconds) * time.Second
	}
	return &capacityObserveServiceState{
		id:            id,
		hostPattern:   hostPattern,
		prefix:        normalizeObservePatternPart(prefix),
		suffix:        normalizeObservePatternPart(suffix),
		urlStart:      urlStart,
		count:         count,
		checkInterval: checkInterval,
		staleAfter:    staleAfter,
		requests:      make([]atomic.Uint64, count),
		lastSeen:      make([]atomic.Int64, count),
		statusHosts:   make(map[int]map[int]struct{}),
	}, nil
}

func splitHostPattern(pattern string) (string, string, error) {
	loc := hostPatternVerbRE.FindStringIndex(pattern)
	if loc == nil {
		return "", "", fmt.Errorf("must contain one integer format verb such as %%07d")
	}
	if hostPatternVerbRE.FindStringIndex(pattern[loc[1]:]) != nil {
		return "", "", fmt.Errorf("must contain only one integer format verb")
	}
	return pattern[:loc[0]], pattern[loc[1]:], nil
}

func (s *capacityObserveServiceState) offsetForHost(host string) (int, bool) {
	if !strings.HasPrefix(host, s.prefix) || !strings.HasSuffix(host, s.suffix) {
		return 0, false
	}
	midEnd := len(host) - len(s.suffix)
	if midEnd < len(s.prefix) {
		return 0, false
	}
	numberText := host[len(s.prefix):midEnd]
	if numberText == "" {
		return 0, false
	}
	number, err := strconv.ParseInt(numberText, 10, 64)
	if err != nil {
		return 0, false
	}
	offset := number - s.urlStart
	if offset < 0 || offset >= int64(s.count) {
		return 0, false
	}
	return int(offset), true
}

func (s *capacityObserveState) summary(now time.Time) CapacityObserveSummary {
	now = now.UTC()
	elapsed := now.Sub(s.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	out := CapacityObserveSummary{
		Active:               true,
		RunID:                s.runID,
		StartedAt:            s.startedAt,
		SnapshotAt:           now,
		ElapsedSeconds:       elapsed.Seconds(),
		ActiveCount:          s.activeCount,
		CheckIntervalSeconds: int(s.checkInterval / time.Second),
		StaleAfterSeconds:    int(s.staleAfter / time.Second),
		Services:             make([]CapacityObserveServiceSummary, 0, len(s.services)),
	}
	for _, service := range s.services {
		out.Services = append(out.Services, service.summary(now, elapsed))
	}
	return out
}

func (s *capacityObserveServiceState) summary(now time.Time, elapsed time.Duration) CapacityObserveServiceSummary {
	requestValues := make([]float64, 0, s.count)
	ageValues := make([]float64, 0, s.count)
	var observed, stale int
	total := s.total.Load()
	for i := 0; i < s.count; i++ {
		requests := s.requests[i].Load()
		requestValues = append(requestValues, float64(requests))
		lastSeen := s.lastSeen[i].Load()
		if lastSeen == 0 {
			stale++
			continue
		}
		observed++
		age := now.Sub(time.Unix(0, lastSeen).UTC())
		if age < 0 {
			age = 0
		}
		if s.staleAfter > 0 && age > s.staleAfter {
			stale++
		}
		ageValues = append(ageValues, age.Seconds())
	}
	sort.Float64s(requestValues)
	sort.Float64s(ageValues)

	expectedMinChecks := int64(0)
	if s.checkInterval > 0 && elapsed > 0 {
		expectedMinChecks = int64(math.Floor(elapsed.Seconds() / s.checkInterval.Seconds()))
	}
	expectedMinRequests := expectedMinChecks * int64(s.count)
	var expectedRequestRatio float64
	if expectedMinRequests > 0 {
		expectedRequestRatio = float64(total) / float64(expectedMinRequests)
	}

	summary := CapacityObserveServiceSummary{
		ID:                       s.id,
		HostPattern:              s.hostPattern,
		URLStart:                 s.urlStart,
		ExpectedSites:            s.count,
		CheckIntervalSeconds:     int(s.checkInterval / time.Second),
		StaleAfterSeconds:        int(s.staleAfter / time.Second),
		ObservedSites:            observed,
		NeverSeenSites:           s.count - observed,
		StaleSites:               stale,
		TotalRequests:            total,
		MethodCounts:             s.methodCounts(),
		StatusCounts:             s.statusCounts(),
		StatusHostCounts:         s.statusHostCounts(),
		ExpectedMinChecksPerSite: expectedMinChecks,
		ExpectedMinRequests:      expectedMinRequests,
		ExpectedRequestRatio:     expectedRequestRatio,
	}
	if s.count > 0 {
		summary.CoveragePercent = float64(observed) / float64(s.count) * 100
		summary.RequestsPerSiteMean = float64(total) / float64(s.count)
	}
	if elapsed > 0 {
		summary.RequestsPerSecond = float64(total) / elapsed.Seconds()
	}
	if len(requestValues) > 0 {
		summary.RequestsPerSiteMin = requestValues[0]
		summary.RequestsPerSiteP50 = percentileSorted(requestValues, 50)
		summary.RequestsPerSiteP95 = percentileSorted(requestValues, 95)
		summary.RequestsPerSiteP99 = percentileSorted(requestValues, 99)
		summary.RequestsPerSiteMax = requestValues[len(requestValues)-1]
	}
	if len(ageValues) > 0 {
		summary.LastSeenAgeSecondsMin = ageValues[0]
		summary.LastSeenAgeSecondsMean = mean(ageValues)
		summary.LastSeenAgeSecondsP50 = percentileSorted(ageValues, 50)
		summary.LastSeenAgeSecondsP95 = percentileSorted(ageValues, 95)
		summary.LastSeenAgeSecondsP99 = percentileSorted(ageValues, 99)
		summary.LastSeenAgeSecondsMax = ageValues[len(ageValues)-1]
	}
	return summary
}

func (s *capacityObserveServiceState) methodCounts() map[string]uint64 {
	counts := map[string]uint64{
		"GET":   s.methods[0].Load(),
		"HEAD":  s.methods[1].Load(),
		"POST":  s.methods[2].Load(),
		"OTHER": s.methods[3].Load(),
	}
	for method, count := range counts {
		if count == 0 {
			delete(counts, method)
		}
	}
	return counts
}

func (s *capacityObserveServiceState) statusCounts() map[string]uint64 {
	counts := make(map[string]uint64)
	for code := range s.statuses {
		count := s.statuses[code].Load()
		if count == 0 {
			continue
		}
		counts[strconv.Itoa(code)] = count
	}
	return counts
}

func (s *capacityObserveServiceState) recordStatusHost(statusCode, offset int) {
	if statusCode < 400 && statusCode != 0 {
		return
	}
	s.statusHostMu.Lock()
	defer s.statusHostMu.Unlock()
	hosts := s.statusHosts[statusCode]
	if hosts == nil {
		hosts = make(map[int]struct{})
		s.statusHosts[statusCode] = hosts
	}
	hosts[offset] = struct{}{}
}

func (s *capacityObserveServiceState) statusHostCounts() map[string]int {
	s.statusHostMu.Lock()
	defer s.statusHostMu.Unlock()
	counts := make(map[string]int, len(s.statusHosts))
	for code, hosts := range s.statusHosts {
		if len(hosts) == 0 {
			continue
		}
		counts[strconv.Itoa(code)] = len(hosts)
	}
	return counts
}

func methodBucket(method string) int {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "GET":
		return 0
	case "HEAD":
		return 1
	case "POST":
		return 2
	default:
		return 3
	}
}

func percentileSorted(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}
	if percentile <= 0 {
		return values[0]
	}
	if percentile >= 100 {
		return values[len(values)-1]
	}
	position := percentile / 100 * float64(len(values)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return values[lower]
	}
	weight := position - float64(lower)
	return values[lower]*(1-weight) + values[upper]*weight
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func normalizeObserveHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(strings.ToLower(host), ".")
}

func normalizeObservePatternPart(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// RegisterCapacityObserverHandlers wires target-side capacity observation
// endpoints onto the authenticated control mux.
func RegisterCapacityObserverHandlers(mux *http.ServeMux, observer *CapacityObserver) {
	mux.HandleFunc("POST /capacity/observe/reset", func(w http.ResponseWriter, r *http.Request) {
		var req CapacityObserveResetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		summary, err := observer.Reset(req, time.Now().UTC())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeCapacityObserveJSON(w, summary)
	})
	mux.HandleFunc("GET /capacity/observe/summary", func(w http.ResponseWriter, r *http.Request) {
		writeCapacityObserveJSON(w, observer.Summary(time.Now().UTC()))
	})
}

func writeCapacityObserveJSON(w http.ResponseWriter, summary CapacityObserveSummary) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(summary)
}
