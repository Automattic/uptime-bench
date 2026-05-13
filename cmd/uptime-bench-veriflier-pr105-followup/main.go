package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

const (
	defaultAPIBaseURL      = "http://jetmon-service-host-2:8090/api/v1"
	defaultV2Addr          = "10.0.0.173:7803"
	defaultTargetURL       = "http://10.0.0.176"
	defaultTargetHost      = "10.0.0.176"
	defaultTargetControl   = "http://10.0.0.176:9000"
	defaultRequestTimeout  = 5 * time.Second
	defaultMonitorTimeout  = 35 * time.Minute
	defaultOverloadCycles  = 3
	defaultOverloadHold    = 8 * time.Second
	defaultOverloadTimeout = 25 * time.Second
)

type report struct {
	GeneratedAt    time.Time     `json:"generated_at"`
	StartedAt      time.Time     `json:"started_at"`
	FinishedAt     time.Time     `json:"finished_at"`
	UptimeBenchCWD string        `json:"uptime_bench_cwd"`
	APIBaseURL     string        `json:"api_base_url"`
	V2Addr         string        `json:"v2_addr"`
	TargetURL      string        `json:"target_url"`
	TargetControl  string        `json:"target_control_url"`
	Phases         []phaseResult `json:"phases"`
	Summary        summary       `json:"summary"`
}

type summary struct {
	Status   string `json:"status"`
	Passed   int    `json:"passed"`
	Failed   int    `json:"failed"`
	Skipped  int    `json:"skipped"`
	Warnings int    `json:"warnings"`
}

type phaseResult struct {
	Name       string        `json:"name"`
	Status     string        `json:"status"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	Checks     []checkResult `json:"checks,omitempty"`
	Notes      []string      `json:"notes,omitempty"`
	Error      string        `json:"error,omitempty"`
}

type checkResult struct {
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Detail string         `json:"detail,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type v2BatchRequest struct {
	BatchID    string           `json:"batch_id,omitempty"`
	DeadlineMS int64            `json:"deadline_ms,omitempty"`
	Requests   []v2CheckRequest `json:"requests"`
}

type v2CheckRequest struct {
	RequestID           string            `json:"request_id,omitempty"`
	BlogID              int64             `json:"blog_id"`
	URL                 string            `json:"url"`
	TimeoutMS           int64             `json:"timeout_ms,omitempty"`
	Method              string            `json:"method,omitempty"`
	DetectionProfile    string            `json:"detection_profile,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	BodyRules           bodyRules         `json:"body_rules,omitempty"`
	RedirectPolicy      string            `json:"redirect_policy,omitempty"`
	BodyReadMaxBytes    int64             `json:"body_read_max_bytes,omitempty"`
	BodyReadMaxMS       int32             `json:"body_read_max_ms,omitempty"`
	KeywordReadMaxBytes int64             `json:"keyword_read_max_bytes,omitempty"`
	KeywordReadMaxMS    int32             `json:"keyword_read_max_ms,omitempty"`
}

type bodyRules struct {
	Required  []string `json:"required,omitempty"`
	Forbidden []string `json:"forbidden,omitempty"`
}

type v2BatchResponse struct {
	BatchID string          `json:"batch_id,omitempty"`
	Vantage v2Vantage       `json:"vantage"`
	Agent   v2Agent         `json:"agent"`
	Results []v2CheckResult `json:"results"`
}

type v2Vantage struct {
	ID       string `json:"id"`
	Region   string `json:"region,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type v2Agent struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Version  string `json:"version"`
	Protocol string `json:"protocol,omitempty"`
}

type v2Capacity struct {
	MaxConcurrency int `json:"max_concurrency"`
	QueueCapacity  int `json:"queue_capacity"`
	QueueDepth     int `json:"queue_depth"`
	Active         int `json:"active"`
	InFlight       int `json:"in_flight"`
}

type v2Status struct {
	Status    string     `json:"status"`
	Version   string     `json:"version"`
	Protocols []string   `json:"protocols"`
	Vantage   v2Vantage  `json:"vantage"`
	Agent     v2Agent    `json:"agent"`
	Capacity  v2Capacity `json:"capacity"`
}

type v2CheckResult struct {
	RequestID string `json:"request_id"`
	BlogID    int64  `json:"blog_id"`
	URL       string `json:"url"`
	VantageID string `json:"vantage_id"`
	AgentID   string `json:"agent_id"`
	Outcome   string `json:"outcome"`
	Success   bool   `json:"success"`
	HTTPCode  int32  `json:"http_code"`
	ErrorCode int32  `json:"error_code"`
	RTTMs     int64  `json:"rtt_ms"`
}

type apiSiteCreateRequest struct {
	BlogID               int64             `json:"blog_id"`
	MonitorURL           string            `json:"monitor_url"`
	MonitorActive        bool              `json:"monitor_active"`
	BucketNo             int               `json:"bucket_no"`
	CheckKeyword         *string           `json:"check_keyword"`
	ForbiddenKeyword     *string           `json:"forbidden_keyword"`
	ForbiddenKeywords    []string          `json:"forbidden_keywords,omitempty"`
	RedirectPolicy       string            `json:"redirect_policy"`
	RequestMethod        string            `json:"request_method"`
	DetectionProfile     string            `json:"detection_profile"`
	TimeoutSeconds       *int              `json:"timeout_seconds"`
	CustomHeaders        map[string]string `json:"custom_headers,omitempty"`
	AlertCooldownMinutes *int              `json:"alert_cooldown_minutes"`
	CheckInterval        int               `json:"check_interval"`
}

type apiSiteResponse struct {
	ID                 int64                `json:"id"`
	BlogID             int64                `json:"blog_id"`
	MonitorURL         string               `json:"monitor_url"`
	MonitorActive      bool                 `json:"monitor_active"`
	CurrentState       string               `json:"current_state"`
	CurrentSeverity    int                  `json:"current_severity"`
	ActiveEventID      *int64               `json:"active_event_id"`
	LastCheckedAt      *string              `json:"last_checked_at"`
	LastStatusChangeAt *string              `json:"last_status_change_at"`
	ActiveEvents       []apiEventListRecord `json:"active_events,omitempty"`
}

type apiEventListResponse struct {
	Data []apiEventListRecord `json:"data"`
}

type apiEventListRecord struct {
	ID               int64           `json:"id"`
	SiteID           int64           `json:"site_id"`
	CheckType        string          `json:"check_type"`
	Severity         int             `json:"severity"`
	State            string          `json:"state"`
	StartedAt        string          `json:"started_at"`
	EndedAt          *string         `json:"ended_at"`
	ResolutionReason *string         `json:"resolution_reason"`
	Metadata         json.RawMessage `json:"metadata"`
	TransitionCount  int             `json:"transition_count"`
}

type apiEventResponse struct {
	apiEventListRecord
	Transitions []apiTransition `json:"transitions"`
}

type apiTransition struct {
	ID             int64           `json:"id"`
	EventID        int64           `json:"event_id"`
	SeverityBefore *int            `json:"severity_before"`
	SeverityAfter  *int            `json:"severity_after"`
	StateBefore    *string         `json:"state_before"`
	StateAfter     *string         `json:"state_after"`
	Reason         string          `json:"reason"`
	Source         string          `json:"source"`
	Metadata       json.RawMessage `json:"metadata"`
	ChangedAt      string          `json:"changed_at"`
}

type apiClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func main() {
	var (
		apiBaseURL       = flag.String("api-base-url", defaultAPIBaseURL, "Jetmon v2 API base URL")
		apiToken         = flag.String("api-token", "", "Jetmon API token; prefer JETMON_API_TOKEN")
		v2Addr           = flag.String("v2-addr", defaultV2Addr, "Jetmon v2 Veriflier address")
		v2Token          = flag.String("v2-token", "", "Jetmon v2 Veriflier auth token; prefer V2_VERIFLIER_TOKEN")
		targetToken      = flag.String("target-token", "", "target control auth token; prefer TARGET_CONTROL_TOKEN")
		targetURL        = flag.String("target-url", defaultTargetURL, "target base URL to check")
		targetHost       = flag.String("target-host", defaultTargetHost, "target host used for failure injection")
		targetControlURL = flag.String("target-control-url", defaultTargetControl, "target control base URL")
		outDir           = flag.String("out-dir", "", "report output directory")
		monitorTimeout   = flag.Duration("monitor-timeout", defaultMonitorTimeout, "max wait for monitor lifecycle phase")
		overloadCycles   = flag.Int("overload-cycles", defaultOverloadCycles, "number of overload cycles")
		skipMonitor      = flag.Bool("skip-monitor", false, "skip monitor lifecycle phase")
	)
	flag.Parse()

	if *apiToken == "" {
		*apiToken = os.Getenv("JETMON_API_TOKEN")
	}
	if *v2Token == "" {
		*v2Token = os.Getenv("V2_VERIFLIER_TOKEN")
	}
	if *targetToken == "" {
		*targetToken = os.Getenv("TARGET_CONTROL_TOKEN")
	}
	if *apiToken == "" || *v2Token == "" || *targetToken == "" {
		log.Fatal("api, v2 Veriflier, and target control tokens are required via flags or JETMON_API_TOKEN/V2_VERIFLIER_TOKEN/TARGET_CONTROL_TOKEN")
	}

	started := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", started.Format("20060102T150405Z")+"-jetmon-v2-veriflier-pr105-followup")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create report dir: %v", err)
	}

	ctx := context.Background()
	httpClient := &http.Client{Timeout: 30 * time.Second}
	api := apiClient{
		baseURL: strings.TrimRight(*apiBaseURL, "/"),
		token:   *apiToken,
		client:  httpClient,
	}
	target := control.NewClient(strings.TrimRight(*targetControlURL, "/"), *targetToken, httpClient)

	rep := report{
		GeneratedAt:    started,
		StartedAt:      started,
		UptimeBenchCWD: mustGetwd(),
		APIBaseURL:     *apiBaseURL,
		V2Addr:         *v2Addr,
		TargetURL:      strings.TrimRight(*targetURL, "/"),
		TargetControl:  *targetControlURL,
	}

	runPhase := func(name string, fn func(context.Context) phaseResult) {
		log.Printf("phase %s: start", name)
		phase := fn(ctx)
		rep.Phases = append(rep.Phases, phase)
		log.Printf("phase %s: %s", name, phase.Status)
	}

	var status v2Status
	runPhase("preflight", func(ctx context.Context) phaseResult {
		phase, st := runPreflight(ctx, api, *v2Addr, *targetControlURL, *targetToken)
		status = st
		return phase
	})
	runPhase("auth-and-input-security", func(ctx context.Context) phaseResult {
		return runSecurityChecks(ctx, *v2Addr, *v2Token)
	})
	runPhase("staged-check-policy", func(ctx context.Context) phaseResult {
		return runStagedPolicy(ctx, target, *v2Addr, *v2Token, strings.TrimRight(*targetURL, "/"), *targetHost)
	})
	runPhase("sustained-overload-recovery", func(ctx context.Context) phaseResult {
		return runOverload(ctx, target, *v2Addr, *v2Token, status, strings.TrimRight(*targetURL, "/"), *targetHost, *overloadCycles)
	})
	if *skipMonitor {
		rep.Phases = append(rep.Phases, phaseResult{
			Name:       "monitor-lifecycle",
			Status:     "skip",
			StartedAt:  time.Now().UTC(),
			FinishedAt: time.Now().UTC(),
			Notes:      []string{"skipped by flag"},
		})
	} else {
		runPhase("monitor-lifecycle", func(ctx context.Context) phaseResult {
			return runMonitorLifecycle(ctx, api, target, strings.TrimRight(*targetURL, "/"), *targetHost, *monitorTimeout)
		})
	}

	rep.FinishedAt = time.Now().UTC()
	rep.Summary = summarize(rep.Phases)
	if err := writeReports(*outDir, rep); err != nil {
		log.Fatalf("write reports: %v", err)
	}
	if rep.Summary.Status != "pass" {
		log.Fatalf("follow-up test status=%s report_dir=%s", rep.Summary.Status, *outDir)
	}
	log.Printf("follow-up test status=pass report_dir=%s", *outDir)
}

func runPreflight(ctx context.Context, api apiClient, v2Addr, targetControlURL, targetToken string) (phaseResult, v2Status) {
	phase := newPhase("preflight")
	var status v2Status
	if err := api.get(ctx, "/health", nil); err != nil {
		phase.fail("api-health", err.Error(), nil)
	} else {
		phase.pass("api-health", "API health returned OK", nil)
	}
	if err := api.get(ctx, "/me", nil); err != nil {
		phase.fail("api-me", err.Error(), nil)
	} else {
		phase.pass("api-me", "API token accepted", nil)
	}
	if err := getJSON(ctx, "http://"+v2Addr+"/v2/status", "", &status); err != nil {
		phase.fail("v2-status", err.Error(), nil)
	} else if status.Status != "OK" || status.Vantage.ID == "" || status.Agent.ID == "" || status.Capacity.MaxConcurrency <= 0 {
		phase.fail("v2-status", "missing expected status, identity, or capacity fields", map[string]any{
			"status": status.Status, "vantage_id": status.Vantage.ID, "agent_id": status.Agent.ID,
			"max_concurrency": status.Capacity.MaxConcurrency,
		})
	} else {
		phase.pass("v2-status", "v2 status returned identity and capacity", map[string]any{
			"version": status.Version, "vantage_id": status.Vantage.ID, "agent_id": status.Agent.ID,
			"max_concurrency": status.Capacity.MaxConcurrency, "queue_capacity": status.Capacity.QueueCapacity,
		})
	}
	targetClient := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(targetControlURL, "/")+"/status", nil)
	req.Header.Set("Authorization", "Bearer "+targetToken)
	resp, err := targetClient.Do(req)
	if err != nil {
		phase.fail("target-control-status", err.Error(), nil)
	} else {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			phase.fail("target-control-status", fmt.Sprintf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body))), nil)
		} else if strings.Contains(string(body), `"active_failures":[]`) {
			phase.pass("target-control-status", "target has no active failures", nil)
		} else {
			phase.fail("target-control-status", "target has active failures before run", map[string]any{"body": json.RawMessage(body)})
		}
	}
	return phase.finish(), status
}

func runSecurityChecks(ctx context.Context, v2Addr, token string) phaseResult {
	phase := newPhase("auth-and-input-security")
	url := "http://" + v2Addr + "/v2/check"
	valid := v2BatchRequest{
		BatchID:    "security-" + newID(),
		DeadlineMS: 5000,
		Requests: []v2CheckRequest{{
			RequestID:        "security-" + newID(),
			BlogID:           910405001,
			URL:              defaultTargetURL + "/pr105-security",
			Method:           "HEAD",
			DetectionProfile: "legacy",
			TimeoutMS:        5000,
		}},
	}
	body, _ := json.Marshal(valid)
	expectHTTP(ctx, &phase, "missing-auth", url, "", body, http.StatusUnauthorized)
	expectHTTP(ctx, &phase, "wrong-auth", url, "wrong-token-"+newID(), body, http.StatusUnauthorized)
	expectHTTP(ctx, &phase, "malformed-json", url, token, []byte(`{"requests":[`), http.StatusBadRequest)
	expectHTTP(ctx, &phase, "missing-required-url", url, token, []byte(`{"requests":[{"blog_id":910405002}]}`), http.StatusBadRequest)
	expectHTTPMethod(ctx, &phase, "unexpected-method", http.MethodGet, url, token, nil, http.StatusMethodNotAllowed)
	large := []byte(fmt.Sprintf(
		`{"requests":[{"blog_id":910405003,"url":"%s/pr105-security-large","headers":{"X-Large":"%s"}}]}`,
		defaultTargetURL,
		strings.Repeat("x", 10*1024*1024+1),
	))
	expectHTTP(ctx, &phase, "large-body", url, token, large, http.StatusRequestEntityTooLarge)
	return phase.finish()
}

func runStagedPolicy(ctx context.Context, target *control.Client, v2Addr, token, targetURL, targetHost string) phaseResult {
	phase := newPhase("staged-check-policy")
	path := "/pr105-policy-" + newID()
	runID := "pr105-policy-" + newID()
	err := target.Activate(ctx, control.ActivateRequest{
		RunID: runID,
		Seed:  910406000,
		Failure: control.FailureSpec{
			Type:     "http_method_status",
			Host:     targetHost,
			Path:     path,
			Duration: 2 * time.Minute,
			Rate:     1,
			Params:   map[string]any{"method": "GET", "status_code": 503},
		},
	})
	if err != nil {
		phase.fail("activate-get-503", err.Error(), nil)
		return phase.finish()
	}
	defer deactivateFailure(target, runID, "http_method_status", targetHost, path)
	time.Sleep(300 * time.Millisecond)

	checkURL := targetURL + path
	head := v2CheckRequest{RequestID: "head-legacy-" + newID(), BlogID: 910406001, URL: checkURL, Method: "HEAD", DetectionProfile: "legacy", TimeoutMS: 5000}
	if res, err := sendV2Check(ctx, v2Addr, token, head); err != nil {
		phase.fail("head-legacy-get-failure", err.Error(), nil)
	} else if !res.Success || res.HTTPCode != 200 || res.Outcome != "up" {
		phase.fail("head-legacy-get-failure", "HEAD legacy should ignore GET-only 503", resultData(res))
	} else {
		phase.pass("head-legacy-get-failure", "HEAD legacy remained up while GET returned 503", resultData(res))
	}

	simple := v2CheckRequest{RequestID: "get-simple-" + newID(), BlogID: 910406002, URL: checkURL, Method: "GET", DetectionProfile: "simple_http", TimeoutMS: 5000}
	if res, err := sendV2Check(ctx, v2Addr, token, simple); err != nil {
		phase.fail("get-simple-http-status", err.Error(), nil)
	} else if res.Success || res.HTTPCode != 503 || res.Outcome != "down" {
		phase.fail("get-simple-http-status", "GET simple_http should detect GET 503", resultData(res))
	} else {
		phase.pass("get-simple-http-status", "GET simple_http detected GET 503", resultData(res))
	}

	keywordPath := "/pr105-policy-keyword-" + newID()
	keywordURL := targetURL + keywordPath
	required := v2CheckRequest{
		RequestID:        "get-full-required-" + newID(),
		BlogID:           910406003,
		URL:              keywordURL,
		Method:           "GET",
		DetectionProfile: "full",
		TimeoutMS:        5000,
		BodyRules:        bodyRules{Required: []string{"definitely-not-present-" + newID()}},
	}
	if res, err := sendV2Check(ctx, v2Addr, token, required); err != nil {
		phase.fail("get-full-required-keyword", err.Error(), nil)
	} else if res.Success || res.ErrorCode == 0 {
		phase.fail("get-full-required-keyword", "GET full should detect missing required keyword", resultData(res))
	} else {
		phase.pass("get-full-required-keyword", "GET full detected missing required keyword", resultData(res))
	}

	simpleWithKeyword := required
	simpleWithKeyword.RequestID = "get-simple-keyword-ignored-" + newID()
	simpleWithKeyword.BlogID = 910406004
	simpleWithKeyword.DetectionProfile = "simple_http"
	if res, err := sendV2Check(ctx, v2Addr, token, simpleWithKeyword); err != nil {
		phase.fail("get-simple-keyword-ignored", err.Error(), nil)
	} else if !res.Success || res.HTTPCode != 200 {
		phase.fail("get-simple-keyword-ignored", "GET simple_http should ignore body rules", resultData(res))
	} else {
		phase.pass("get-simple-keyword-ignored", "GET simple_http ignored body rules", resultData(res))
	}

	forbidden := v2CheckRequest{
		RequestID:        "get-full-forbidden-" + newID(),
		BlogID:           910406005,
		URL:              keywordURL,
		Method:           "GET",
		DetectionProfile: "full",
		TimeoutMS:        5000,
		BodyRules:        bodyRules{Forbidden: []string{"uptime-bench-canary"}},
	}
	if res, err := sendV2Check(ctx, v2Addr, token, forbidden); err != nil {
		phase.fail("get-full-forbidden-keyword", err.Error(), nil)
	} else if res.Success || res.ErrorCode == 0 {
		phase.fail("get-full-forbidden-keyword", "GET full should detect forbidden keyword", resultData(res))
	} else {
		phase.pass("get-full-forbidden-keyword", "GET full detected forbidden keyword", resultData(res))
	}

	redirectPath := "/pr105-policy-redirect-" + newID()
	redirectRunID := "pr105-policy-redirect-" + newID()
	if err := target.Activate(ctx, control.ActivateRequest{
		RunID: redirectRunID,
		Seed:  910406006,
		Failure: control.FailureSpec{
			Type:     "http_redirect",
			Host:     targetHost,
			Path:     redirectPath,
			Duration: 2 * time.Minute,
			Rate:     1,
			Params:   map[string]any{"variant": "loop", "method": "GET"},
		},
	}); err != nil {
		phase.fail("activate-redirect", err.Error(), nil)
	} else {
		defer deactivateFailure(target, redirectRunID, "http_redirect", targetHost, redirectPath)
		time.Sleep(300 * time.Millisecond)
		redirect := v2CheckRequest{
			RequestID:        "get-full-redirect-" + newID(),
			BlogID:           910406007,
			URL:              targetURL + redirectPath,
			Method:           "GET",
			DetectionProfile: "full",
			RedirectPolicy:   "fail",
			TimeoutMS:        5000,
		}
		if res, err := sendV2Check(ctx, v2Addr, token, redirect); err != nil {
			phase.fail("get-full-redirect-policy", err.Error(), nil)
		} else if res.Success || res.ErrorCode == 0 {
			phase.fail("get-full-redirect-policy", "GET full should detect redirect policy failure", resultData(res))
		} else {
			phase.pass("get-full-redirect-policy", "GET full detected redirect policy failure", resultData(res))
		}
	}
	return phase.finish()
}

func runOverload(ctx context.Context, target *control.Client, v2Addr, token string, status v2Status, targetURL, targetHost string, cycles int) phaseResult {
	phase := newPhase("sustained-overload-recovery")
	if status.Capacity.MaxConcurrency <= 0 || status.Capacity.QueueCapacity <= 0 {
		phase.skip("overload", "capacity unavailable", nil)
		return phase.finish()
	}
	if cycles <= 0 {
		phase.skip("overload", "overload cycles disabled", nil)
		return phase.finish()
	}
	path := "/pr105-overload-" + newID()
	runID := "pr105-overload-" + newID()
	if err := target.Activate(ctx, control.ActivateRequest{
		RunID: runID,
		Seed:  910407000,
		Failure: control.FailureSpec{
			Type:     "http_timeout",
			Host:     targetHost,
			Path:     path,
			Duration: time.Duration(cycles)*defaultOverloadTimeout + time.Minute,
			Rate:     1,
			Params:   map[string]any{"method": "HEAD", "delay": defaultOverloadHold.String()},
		},
	}); err != nil {
		phase.fail("activate-overload-target", err.Error(), nil)
		return phase.finish()
	}
	defer deactivateFailure(target, runID, "http_timeout", targetHost, path)
	time.Sleep(300 * time.Millisecond)

	total := status.Capacity.MaxConcurrency + status.Capacity.QueueCapacity + 200
	if total < 10 {
		total = 10
	}
	checkURL := targetURL + path
	for cycle := 1; cycle <= cycles; cycle++ {
		var ok200, overloaded503, other atomic.Int64
		start := time.Now()
		var wg sync.WaitGroup
		sem := make(chan struct{}, total)
		for i := 0; i < total; i++ {
			wg.Add(1)
			sem <- struct{}{}
			go func(i int) {
				defer wg.Done()
				defer func() { <-sem }()
				req := v2BatchRequest{
					BatchID:    fmt.Sprintf("overload-c%d-%d-%s", cycle, i, newID()),
					DeadlineMS: 15000,
					Requests: []v2CheckRequest{{
						RequestID:        fmt.Sprintf("overload-c%d-%d-%s", cycle, i, newID()),
						BlogID:           int64(910407000 + cycle*10000 + i),
						URL:              checkURL,
						Method:           "HEAD",
						DetectionProfile: "legacy",
						TimeoutMS:        10000,
					}},
				}
				code, outcome, err := sendV2RawBatch(ctx, v2Addr, token, req, 20*time.Second)
				if err == nil && code == http.StatusOK {
					ok200.Add(1)
					return
				}
				if code == http.StatusServiceUnavailable && outcome == "agent_overloaded" {
					overloaded503.Add(1)
					return
				}
				other.Add(1)
			}(i)
		}
		wg.Wait()
		elapsed := time.Since(start)
		data := map[string]any{
			"cycle":           cycle,
			"requests":        total,
			"http_200":        ok200.Load(),
			"overloaded_503":  overloaded503.Load(),
			"other":           other.Load(),
			"elapsed_seconds": elapsed.Seconds(),
		}
		if overloaded503.Load() == 0 {
			phase.fail(fmt.Sprintf("overload-cycle-%d", cycle), "no agent_overloaded responses observed", data)
		} else {
			phase.pass(fmt.Sprintf("overload-cycle-%d", cycle), "observed bounded executor overload responses", data)
		}
	}
	healthy := v2CheckRequest{
		RequestID:        "overload-recovery-" + newID(),
		BlogID:           910499999,
		URL:              targetURL + "/pr105-overload-recovery-" + newID(),
		Method:           "HEAD",
		DetectionProfile: "legacy",
		TimeoutMS:        5000,
	}
	if res, err := sendV2Check(ctx, v2Addr, token, healthy); err != nil {
		phase.fail("post-overload-recovery", err.Error(), nil)
	} else if !res.Success || res.HTTPCode != 200 {
		phase.fail("post-overload-recovery", "healthy check failed after overload", resultData(res))
	} else {
		phase.pass("post-overload-recovery", "healthy check succeeded after overload", resultData(res))
	}
	return phase.finish()
}

func runMonitorLifecycle(ctx context.Context, api apiClient, target *control.Client, targetURL, targetHost string, timeout time.Duration) phaseResult {
	phase := newPhase("monitor-lifecycle")
	blogID := int64(910408000000 + time.Now().UTC().Unix()%1000000)
	path := "/pr105-lifecycle-" + newID()
	siteURL := targetURL + path
	cooldown := 0
	checkTimeout := 5
	siteReq := apiSiteCreateRequest{
		BlogID:               blogID,
		MonitorURL:           siteURL,
		MonitorActive:        true,
		BucketNo:             0,
		RedirectPolicy:       "follow",
		RequestMethod:        "HEAD",
		DetectionProfile:     "legacy",
		TimeoutSeconds:       &checkTimeout,
		CustomHeaders:        map[string]string{"X-Uptime-Bench-Test": "veriflier-pr105-followup"},
		AlertCooldownMinutes: &cooldown,
		CheckInterval:        1,
	}
	var site apiSiteResponse
	if err := api.post(ctx, "/sites", siteReq, &site, map[string]string{"Idempotency-Key": "pr105-followup-create-" + newID()}); err != nil {
		phase.fail("create-site", err.Error(), map[string]any{"blog_id": blogID, "url": siteURL})
		return phase.finish()
	}
	phase.pass("create-site", "created temporary Jetmon site", map[string]any{"blog_id": blogID, "url": siteURL})
	defer func() {
		if err := api.delete(context.Background(), fmt.Sprintf("/sites/%d", blogID)); err != nil {
			log.Printf("cleanup site %d: %v", blogID, err)
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s, err := waitForSiteChecked(waitCtx, api, blogID, time.Now().UTC()); err != nil {
		phase.fail("initial-check", err.Error(), nil)
		return phase.finish()
	} else {
		phase.pass("initial-check", "temporary site entered scheduler and checked healthy", siteData(s))
	}

	runID := "pr105-lifecycle-" + newID()
	activatedAt := time.Now().UTC()
	if err := target.Activate(ctx, control.ActivateRequest{
		RunID: runID,
		Seed:  blogID,
		Failure: control.FailureSpec{
			Type:     "http_status",
			Host:     targetHost,
			Path:     path,
			Duration: timeout + time.Minute,
			Rate:     1,
			Params:   map[string]any{"status_code": 503},
		},
	}); err != nil {
		phase.fail("activate-http-503", err.Error(), nil)
		return phase.finish()
	}
	defer deactivateFailure(target, runID, "http_status", targetHost, path)
	phase.pass("activate-http-503", "target failure activated", map[string]any{"path": path, "activated_at": activatedAt.Format(time.RFC3339)})

	downEvent, err := waitForEventState(waitCtx, api, blogID, "Down")
	if err != nil {
		phase.fail("wait-down", err.Error(), nil)
		return phase.finish()
	}
	downDetail, err := api.getEvent(ctx, blogID, downEvent.ID)
	if err != nil {
		phase.fail("down-event-detail", err.Error(), eventData(downEvent))
		return phase.finish()
	}
	if !hasTransitionReason(downDetail.Transitions, "verifier_confirmed") {
		phase.fail("verifier-confirmed-transition", "Down event did not include verifier_confirmed transition", eventDetailData(downDetail))
	} else if !transitionMetadataContains(downDetail.Transitions, "verifier_confirmed", "vantage_id") {
		phase.fail("verifier-v2-evidence", "verifier_confirmed metadata did not include v2 vantage evidence", eventDetailData(downDetail))
	} else {
		phase.pass("verifier-confirmed-transition", "same event promoted to Down with v2 Veriflier evidence", eventDetailData(downDetail))
	}

	if err := target.Deactivate(ctx, control.DeactivateRequest{RunID: runID, FailureType: "http_status", Host: targetHost, Path: path}); err != nil {
		phase.fail("deactivate-http-503", err.Error(), nil)
		return phase.finish()
	}
	phase.pass("deactivate-http-503", "target failure deactivated", nil)

	closed, err := waitForEventClosed(waitCtx, api, blogID, downEvent.ID)
	if err != nil {
		phase.fail("wait-resolved", err.Error(), eventData(downEvent))
		return phase.finish()
	}
	if closed.ResolutionReason == nil || *closed.ResolutionReason == "" {
		phase.fail("resolution-reason", "closed event is missing resolution_reason", eventDetailData(closed))
	} else if !hasTransitionReason(closed.Transitions, "verifier_cleared") && !hasTransitionReason(closed.Transitions, "probe_cleared") {
		phase.fail("recovery-transition", "closed event missing verifier_cleared/probe_cleared transition", eventDetailData(closed))
	} else {
		phase.pass("recovery-transition", "same event resolved after target recovery", eventDetailData(closed))
	}

	var projection apiSiteResponse
	if err := api.get(ctx, fmt.Sprintf("/sites/%d", blogID), &projection); err != nil {
		phase.fail("projection-state", err.Error(), nil)
	} else if projection.CurrentState != "Up" || projection.CurrentSeverity != 0 {
		phase.fail("projection-state", "site projection did not return to Up", siteData(projection))
	} else {
		phase.pass("projection-state", "site projection returned to Up", siteData(projection))
	}
	return phase.finish()
}

func waitForSiteChecked(ctx context.Context, api apiClient, blogID int64, after time.Time) (apiSiteResponse, error) {
	return waitForSiteCondition(ctx, api, blogID, 10*time.Second, func(site apiSiteResponse) (bool, error) {
		if site.LastCheckedAt == nil || *site.LastCheckedAt == "" {
			return false, nil
		}
		checked, err := time.Parse(time.RFC3339Nano, *site.LastCheckedAt)
		if err != nil {
			return false, nil
		}
		return checked.After(after) && site.CurrentState == "Up", nil
	})
}

func waitForEventState(ctx context.Context, api apiClient, blogID int64, state string) (apiEventListRecord, error) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		events, err := api.listEvents(ctx, blogID, true)
		if err != nil {
			return apiEventListRecord{}, err
		}
		for _, event := range events {
			if event.State == state {
				return event, nil
			}
		}
		select {
		case <-ctx.Done():
			return apiEventListRecord{}, fmt.Errorf("timed out waiting for active event state %q", state)
		case <-tick.C:
		}
	}
}

func waitForEventClosed(ctx context.Context, api apiClient, blogID, eventID int64) (apiEventResponse, error) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		event, err := api.getEvent(ctx, blogID, eventID)
		if err != nil {
			return apiEventResponse{}, err
		}
		if event.EndedAt != nil {
			return event, nil
		}
		select {
		case <-ctx.Done():
			return apiEventResponse{}, fmt.Errorf("timed out waiting for event %d to close", eventID)
		case <-tick.C:
		}
	}
}

func waitForSiteCondition(ctx context.Context, api apiClient, blogID int64, interval time.Duration, pred func(apiSiteResponse) (bool, error)) (apiSiteResponse, error) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		var site apiSiteResponse
		if err := api.get(ctx, fmt.Sprintf("/sites/%d", blogID), &site); err != nil {
			return apiSiteResponse{}, err
		}
		ok, err := pred(site)
		if err != nil {
			return apiSiteResponse{}, err
		}
		if ok {
			return site, nil
		}
		select {
		case <-ctx.Done():
			return apiSiteResponse{}, fmt.Errorf("timed out waiting for site %d condition", blogID)
		case <-tick.C:
		}
	}
}

func sendV2Check(ctx context.Context, addr, token string, site v2CheckRequest) (*v2CheckResult, error) {
	req := v2BatchRequest{
		BatchID:    "pr105-followup-" + newID(),
		DeadlineMS: site.TimeoutMS + 5000,
		Requests:   []v2CheckRequest{site},
	}
	var decoded v2BatchResponse
	code, outcome, err := sendV2Batch(ctx, addr, token, req, 15*time.Second, &decoded)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("status=%d outcome=%q", code, outcome)
	}
	if len(decoded.Results) != 1 {
		return nil, fmt.Errorf("result count=%d, want 1", len(decoded.Results))
	}
	return &decoded.Results[0], nil
}

func sendV2RawBatch(ctx context.Context, addr, token string, batch v2BatchRequest, timeout time.Duration) (int, string, error) {
	return sendV2Batch(ctx, addr, token, batch, timeout, nil)
}

func sendV2Batch(ctx context.Context, addr, token string, batch v2BatchRequest, timeout time.Duration, out *v2BatchResponse) (int, string, error) {
	body, err := json.Marshal(batch)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v2/check", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var decoded map[string]string
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&decoded)
		return resp.StatusCode, decoded["outcome"], nil
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, "", err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, "", nil
}

func expectHTTP(ctx context.Context, phase *phaseBuilder, name, url, token string, body []byte, want int) {
	expectHTTPMethod(ctx, phase, name, http.MethodPost, url, token, body, want)
}

func expectHTTPMethod(ctx context.Context, phase *phaseBuilder, name, method, url, token string, body []byte, want int) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		phase.fail(name, err.Error(), nil)
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		phase.fail(name, err.Error(), nil)
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		phase.fail(name, fmt.Sprintf("status=%d want=%d", resp.StatusCode, want), nil)
		return
	}
	phase.pass(name, fmt.Sprintf("returned HTTP %d", want), nil)
}

func (a apiClient) get(ctx context.Context, path string, out any) error {
	return a.do(ctx, http.MethodGet, path, nil, out, nil)
}

func (a apiClient) post(ctx context.Context, path string, body any, out any, headers map[string]string) error {
	return a.do(ctx, http.MethodPost, path, body, out, headers)
}

func (a apiClient) delete(ctx context.Context, path string) error {
	return a.do(ctx, http.MethodDelete, path, nil, nil, nil)
}

func (a apiClient) listEvents(ctx context.Context, blogID int64, active bool) ([]apiEventListRecord, error) {
	var out apiEventListResponse
	if err := a.get(ctx, fmt.Sprintf("/sites/%d/events?active=%t&limit=20", blogID, active), &out); err != nil {
		return nil, err
	}
	return out.Data, nil
}

func (a apiClient) getEvent(ctx context.Context, blogID, eventID int64) (apiEventResponse, error) {
	var out apiEventResponse
	err := a.get(ctx, fmt.Sprintf("/sites/%d/events/%d", blogID, eventID), &out)
	return out, err
}

func (a apiClient) do(ctx context.Context, method, path string, body any, out any, headers map[string]string) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return nil
}

func getJSON(ctx context.Context, url, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func deactivateFailure(target *control.Client, runID, failureType, host, path string) {
	_ = target.Deactivate(context.Background(), control.DeactivateRequest{
		RunID:       runID,
		FailureType: failureType,
		Host:        host,
		Path:        path,
	})
}

type phaseBuilder struct {
	phaseResult
}

func newPhase(name string) phaseBuilder {
	return phaseBuilder{phaseResult: phaseResult{Name: name, Status: "pass", StartedAt: time.Now().UTC()}}
}

func (p *phaseBuilder) pass(name, detail string, data map[string]any) {
	p.Checks = append(p.Checks, checkResult{Name: name, Status: "pass", Detail: detail, Data: data})
}

func (p *phaseBuilder) fail(name, detail string, data map[string]any) {
	p.Status = "fail"
	p.Checks = append(p.Checks, checkResult{Name: name, Status: "fail", Detail: detail, Data: data})
}

func (p *phaseBuilder) skip(name, detail string, data map[string]any) {
	if p.Status == "pass" {
		p.Status = "skip"
	}
	p.Checks = append(p.Checks, checkResult{Name: name, Status: "skip", Detail: detail, Data: data})
}

func (p *phaseBuilder) finish() phaseResult {
	p.FinishedAt = time.Now().UTC()
	return p.phaseResult
}

func summarize(phases []phaseResult) summary {
	out := summary{Status: "pass"}
	for _, phase := range phases {
		switch phase.Status {
		case "pass":
			out.Passed++
		case "skip":
			out.Skipped++
			if out.Status == "pass" {
				out.Status = "warn"
			}
		default:
			out.Failed++
			out.Status = "fail"
		}
		for _, check := range phase.Checks {
			if check.Status == "skip" {
				out.Warnings++
			}
		}
	}
	return out
}

func writeReports(dir string, rep report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(renderMarkdown(rep)), 0o644)
}

func renderMarkdown(rep report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jetmon v2 Veriflier PR105 Follow-Up Test\n\n")
	fmt.Fprintf(&b, "Status: `%s`\n\n", rep.Summary.Status)
	fmt.Fprintf(&b, "- Started: `%s`\n", rep.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Finished: `%s`\n", rep.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- API: `%s`\n", rep.APIBaseURL)
	fmt.Fprintf(&b, "- v2 Veriflier: `%s`\n", rep.V2Addr)
	fmt.Fprintf(&b, "- Target URL: `%s`\n\n", rep.TargetURL)
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "| Passed | Failed | Skipped | Warnings |\n|---:|---:|---:|---:|\n")
	fmt.Fprintf(&b, "| %d | %d | %d | %d |\n\n", rep.Summary.Passed, rep.Summary.Failed, rep.Summary.Skipped, rep.Summary.Warnings)
	for _, phase := range rep.Phases {
		fmt.Fprintf(&b, "## %s\n\n", phase.Name)
		fmt.Fprintf(&b, "Status: `%s`\n\n", phase.Status)
		if phase.Error != "" {
			fmt.Fprintf(&b, "- Error: `%s`\n", escapePipe(phase.Error))
		}
		for _, note := range phase.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
		if len(phase.Checks) == 0 {
			fmt.Fprintf(&b, "\n")
			continue
		}
		fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---:|---|\n")
		for _, check := range phase.Checks {
			detail := check.Detail
			if len(check.Data) > 0 {
				detail = strings.TrimSpace(detail + " " + compactData(check.Data))
			}
			fmt.Fprintf(&b, "| %s | `%s` | %s |\n", check.Name, check.Status, escapePipe(detail))
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

func compactData(data map[string]any) string {
	if len(data) == 0 {
		return ""
	}
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := data[key]
		switch v := value.(type) {
		case json.RawMessage:
			parts = append(parts, fmt.Sprintf("%s=%s", key, string(v)))
		default:
			parts = append(parts, fmt.Sprintf("%s=%v", key, value))
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func resultData(res *v2CheckResult) map[string]any {
	if res == nil {
		return nil
	}
	return map[string]any{
		"success":    res.Success,
		"outcome":    res.Outcome,
		"http_code":  res.HTTPCode,
		"error_code": res.ErrorCode,
		"vantage_id": res.VantageID,
		"agent_id":   res.AgentID,
		"rtt_ms":     res.RTTMs,
	}
}

func siteData(site apiSiteResponse) map[string]any {
	out := map[string]any{
		"blog_id":          site.BlogID,
		"current_state":    site.CurrentState,
		"current_severity": site.CurrentSeverity,
		"monitor_active":   site.MonitorActive,
	}
	if site.ActiveEventID != nil {
		out["active_event_id"] = *site.ActiveEventID
	}
	if site.LastCheckedAt != nil {
		out["last_checked_at"] = *site.LastCheckedAt
	}
	return out
}

func eventData(event apiEventListRecord) map[string]any {
	out := map[string]any{
		"event_id":         event.ID,
		"state":            event.State,
		"severity":         event.Severity,
		"transition_count": event.TransitionCount,
		"started_at":       event.StartedAt,
	}
	if event.EndedAt != nil {
		out["ended_at"] = *event.EndedAt
	}
	if event.ResolutionReason != nil {
		out["resolution_reason"] = *event.ResolutionReason
	}
	return out
}

func eventDetailData(event apiEventResponse) map[string]any {
	out := eventData(event.apiEventListRecord)
	reasons := make([]string, 0, len(event.Transitions))
	for _, transition := range event.Transitions {
		reasons = append(reasons, transition.Reason)
	}
	out["transition_reasons"] = strings.Join(reasons, ",")
	return out
}

func hasTransitionReason(transitions []apiTransition, reason string) bool {
	for _, transition := range transitions {
		if transition.Reason == reason {
			return true
		}
	}
	return false
}

func transitionMetadataContains(transitions []apiTransition, reason, needle string) bool {
	for _, transition := range transitions {
		if transition.Reason != reason {
			continue
		}
		if bytes.Contains(transition.Metadata, []byte(needle)) {
			return true
		}
	}
	return false
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
