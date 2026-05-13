package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

const (
	defaultV1Addr          = "10.0.0.172:7801"
	defaultV2Addr          = "10.0.0.173:7803"
	defaultTargetURL       = "http://10.0.0.176/"
	defaultTargetHost      = "10.0.0.176"
	defaultTargetControl   = "http://10.0.0.176:9000"
	defaultCallbackListen  = ":7800"
	defaultRequestTimeout  = 5 * time.Second
	defaultScenarioTimeout = 25 * time.Second
)

type legacyCheckRequest struct {
	BlogID              int64             `json:"BlogID"`
	URL                 string            `json:"URL"`
	Method              string            `json:"Method,omitempty"`
	DetectionProfile    string            `json:"DetectionProfile,omitempty"`
	TimeoutSeconds      int32             `json:"TimeoutSeconds,omitempty"`
	CustomHeaders       map[string]string `json:"CustomHeaders,omitempty"`
	RedirectPolicy      string            `json:"RedirectPolicy,omitempty"`
	RequestID           string            `json:"RequestID,omitempty"`
	BodyReadMaxBytes    int64             `json:"BodyReadMaxBytes,omitempty"`
	BodyReadMaxMS       int32             `json:"BodyReadMaxMS,omitempty"`
	KeywordReadMaxBytes int64             `json:"KeywordReadMaxBytes,omitempty"`
	KeywordReadMaxMS    int32             `json:"KeywordReadMaxMS,omitempty"`
	Keyword             string            `json:"Keyword,omitempty"`
	ForbiddenKeyword    string            `json:"ForbiddenKeyword,omitempty"`
	ForbiddenKeywords   []string          `json:"ForbiddenKeywords,omitempty"`
}

type legacyCheckResponse struct {
	Results []legacyCheckResult `json:"results"`
}

type legacyCheckResult struct {
	BlogID    int64  `json:"BlogID"`
	URL       string `json:"URL"`
	Host      string `json:"Host"`
	Success   bool   `json:"Success"`
	HTTPCode  int32  `json:"HTTPCode"`
	ErrorCode int32  `json:"ErrorCode"`
	RTTMs     int64  `json:"RTTMs"`
	RequestID string `json:"RequestID"`
}

type v2BatchRequest struct {
	BatchID    string           `json:"batch_id,omitempty"`
	DeadlineMS int64            `json:"deadline_ms,omitempty"`
	Requests   []v2CheckRequest `json:"requests"`
}

type v2CheckRequest struct {
	RequestID        string            `json:"request_id,omitempty"`
	BlogID           int64             `json:"blog_id"`
	URL              string            `json:"url"`
	TimeoutMS        int64             `json:"timeout_ms,omitempty"`
	Method           string            `json:"method,omitempty"`
	DetectionProfile string            `json:"detection_profile,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	RedirectPolicy   string            `json:"redirect_policy,omitempty"`
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

type legacyV1Request struct {
	AuthToken string                 `json:"auth_token"`
	Checks    []legacyV1RequestCheck `json:"checks"`
}

type legacyV1RequestCheck struct {
	BlogID     int64  `json:"blog_id"`
	MonitorURL string `json:"monitor_url"`
}

type legacyV1Ack struct {
	Veriflier string `json:"veriflier,omitempty"`
	Status    int    `json:"status"`
	Error     string `json:"error,omitempty"`
}

type legacyV1Callback struct {
	AuthToken string                `json:"auth_token,omitempty"`
	Checks    []legacyV1CallbackRow `json:"checks"`
}

type legacyV1CallbackRow struct {
	BlogID     int64  `json:"blog_id"`
	MonitorURL string `json:"monitor_url"`
	Status     int    `json:"status"`
	Code       int    `json:"code"`
	RTT        int64  `json:"rtt"`
}

type scenarioSpec struct {
	Name        string
	URL         string
	FailureType string
	Params      map[string]any
	Host        string
	Path        string
	Timeout     time.Duration
}

type preflightResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type scenarioResult struct {
	Name               string               `json:"name"`
	URL                string               `json:"url"`
	FailureType        string               `json:"failure_type,omitempty"`
	V1                 *legacyV1CallbackRow `json:"v1,omitempty"`
	V2Legacy           *legacyCheckResult   `json:"v2_legacy,omitempty"`
	V2                 *v2CheckResult       `json:"v2,omitempty"`
	V1Class            string               `json:"v1_class,omitempty"`
	V2LegacyClass      string               `json:"v2_legacy_class,omitempty"`
	V2Class            string               `json:"v2_class,omitempty"`
	DecisionMatch      bool                 `json:"decision_match"`
	HTTPCodeMatch      bool                 `json:"http_code_match"`
	FailureClassMatch  bool                 `json:"failure_class_match"`
	V2ContractMatch    bool                 `json:"v2_contract_match"`
	Compatible         bool                 `json:"compatible"`
	CompatibilityNotes []string             `json:"compatibility_notes,omitempty"`
	Error              string               `json:"error,omitempty"`
}

type overloadResult struct {
	Status         string `json:"status"`
	HTTPStatus     int    `json:"http_status"`
	Outcome        string `json:"outcome,omitempty"`
	Error          string `json:"error,omitempty"`
	RecoveryStatus string `json:"recovery_status,omitempty"`
}

type report struct {
	GeneratedAt    time.Time         `json:"generated_at"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	UptimeBenchCWD string            `json:"uptime_bench_cwd"`
	V1Addr         string            `json:"v1_addr"`
	V2Addr         string            `json:"v2_addr"`
	TargetURL      string            `json:"target_url"`
	TargetControl  string            `json:"target_control_url"`
	CallbackListen string            `json:"callback_listen"`
	Preflight      []preflightResult `json:"preflight"`
	Scenarios      []scenarioResult  `json:"scenarios"`
	Overload       overloadResult    `json:"overload"`
	Summary        reportSummary     `json:"summary"`
}

type reportSummary struct {
	Status            string `json:"status"`
	Scenarios         int    `json:"scenarios"`
	Compatible        int    `json:"compatible"`
	Incompatible      int    `json:"incompatible"`
	PreflightFailures int    `json:"preflight_failures"`
}

type callbackServer struct {
	srv       *http.Server
	listener  net.Listener
	mu        sync.Mutex
	callbacks map[int64]legacyV1CallbackRow
	seen      chan struct{}
}

func main() {
	var (
		v1Addr             = flag.String("v1-addr", defaultV1Addr, "Jetmon v1 Veriflier address")
		v2Addr             = flag.String("v2-addr", defaultV2Addr, "Jetmon v2 Veriflier address")
		v1Token            = flag.String("v1-token", "", "Jetmon v1 Veriflier auth token; prefer V1_VERIFLIER_TOKEN")
		v2Token            = flag.String("v2-token", "", "Jetmon v2 Veriflier auth token; prefer V2_VERIFLIER_TOKEN")
		targetToken        = flag.String("target-token", "", "target control auth token; prefer TARGET_CONTROL_TOKEN")
		targetURL          = flag.String("target-url", defaultTargetURL, "target URL to check")
		targetHost         = flag.String("target-host", defaultTargetHost, "target host used for failure injection")
		targetControlURL   = flag.String("target-control-url", defaultTargetControl, "target control base URL")
		callbackListen     = flag.String("callback-listen", defaultCallbackListen, "TLS callback listen address for v1 results")
		outDir             = flag.String("out-dir", "", "report output directory")
		requestTimeoutFlag = flag.Duration("request-timeout", defaultRequestTimeout, "per-check timeout")
		scenarioTimeout    = flag.Duration("scenario-timeout", defaultScenarioTimeout, "scenario timeout including v1 callback wait")
		skipOverload       = flag.Bool("skip-overload", false, "skip v2 overload smoke")
	)
	flag.Parse()

	if *v1Token == "" {
		*v1Token = os.Getenv("V1_VERIFLIER_TOKEN")
	}
	if *v2Token == "" {
		*v2Token = os.Getenv("V2_VERIFLIER_TOKEN")
	}
	if *targetToken == "" {
		*targetToken = os.Getenv("TARGET_CONTROL_TOKEN")
	}
	if *v1Token == "" || *v2Token == "" || *targetToken == "" {
		log.Fatal("v1, v2, and target control tokens are required via flags or V1_VERIFLIER_TOKEN/V2_VERIFLIER_TOKEN/TARGET_CONTROL_TOKEN")
	}

	started := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", started.Format("20060102T150405Z")+"-jetmon-v2-veriflier-pr105")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create report dir: %v", err)
	}

	cb, err := startCallbackServer(*callbackListen)
	if err != nil {
		log.Fatalf("start v1 callback server: %v", err)
	}
	defer cb.Close()

	httpClient := &http.Client{Timeout: 15 * time.Second}
	targetClient := control.NewClient(strings.TrimRight(*targetControlURL, "/"), *targetToken, httpClient)
	ctx := context.Background()

	rep := report{
		GeneratedAt:    started,
		StartedAt:      started,
		UptimeBenchCWD: mustGetwd(),
		V1Addr:         *v1Addr,
		V2Addr:         *v2Addr,
		TargetURL:      *targetURL,
		TargetControl:  *targetControlURL,
		CallbackListen: *callbackListen,
	}

	rep.Preflight = append(rep.Preflight, preflightV1(ctx, *v1Addr))
	status, pf := preflightV2(ctx, *v2Addr)
	rep.Preflight = append(rep.Preflight, pf...)

	scenarios := buildScenarios(*targetURL, *targetHost, *requestTimeoutFlag)
	for i, sc := range scenarios {
		res := runScenario(ctx, targetClient, cb, *v1Addr, *v1Token, *v2Addr, *v2Token, sc, int64(910105000+i), *scenarioTimeout)
		rep.Scenarios = append(rep.Scenarios, res)
		log.Printf("scenario %s compatible=%t notes=%s", res.Name, res.Compatible, strings.Join(res.CompatibilityNotes, "; "))
		if !res.Compatible && res.Error != "" {
			log.Printf("scenario %s error=%s", res.Name, res.Error)
		}
	}
	if !*skipOverload {
		rep.Overload = runOverloadSmoke(ctx, *v2Addr, *v2Token, status, *targetURL)
	}

	rep.FinishedAt = time.Now().UTC()
	rep.Summary = summarizeReport(rep)
	if err := writeReports(*outDir, rep); err != nil {
		log.Fatalf("write reports: %v", err)
	}
	if rep.Summary.Status != "pass" {
		log.Fatalf("veriflier PR105 test status=%s report_dir=%s", rep.Summary.Status, *outDir)
	}
	log.Printf("veriflier PR105 test status=pass report_dir=%s", *outDir)
}

func buildScenarios(targetURL, targetHost string, timeout time.Duration) []scenarioSpec {
	return []scenarioSpec{
		{Name: "healthy-200", URL: targetURL, Timeout: timeout},
		{Name: "http-500", URL: targetURL, FailureType: "http_status", Host: targetHost, Path: "/", Params: map[string]any{"status_code": 500}, Timeout: timeout},
		{Name: "http-403", URL: targetURL, FailureType: "http_status", Host: targetHost, Path: "/", Params: map[string]any{"status_code": 403}, Timeout: timeout},
		{Name: "http-404", URL: targetURL, FailureType: "http_status", Host: targetHost, Path: "/", Params: map[string]any{"status_code": 404}, Timeout: timeout},
		{Name: "head-503", URL: targetURL, FailureType: "http_method_status", Host: targetHost, Path: "/", Params: map[string]any{"method": "HEAD", "status_code": 503}, Timeout: timeout},
		{Name: "get-503-head-ok", URL: targetURL, FailureType: "http_method_status", Host: targetHost, Path: "/", Params: map[string]any{"method": "GET", "status_code": 503}, Timeout: timeout},
		{Name: "head-timeout", URL: targetURL, FailureType: "http_timeout", Host: targetHost, Path: "/", Params: map[string]any{"method": "HEAD", "delay": "8s"}, Timeout: 3 * time.Second},
		{Name: "connection-refused", URL: "http://" + targetHost + ":1/", Timeout: 3 * time.Second},
	}
}

func runScenario(ctx context.Context, targetClient *control.Client, cb *callbackServer, v1Addr, v1Token, v2Addr, v2Token string, sc scenarioSpec, blogID int64, timeout time.Duration) scenarioResult {
	res := scenarioResult{Name: sc.Name, URL: sc.URL, FailureType: sc.FailureType}
	runID := "veriflier-pr105-" + sc.Name + "-" + newID()
	if sc.FailureType != "" {
		req := control.ActivateRequest{
			RunID: runID,
			Seed:  blogID,
			Failure: control.FailureSpec{
				Type:     sc.FailureType,
				Host:     sc.Host,
				Path:     sc.Path,
				Duration: timeout + 10*time.Second,
				Rate:     1,
				Params:   sc.Params,
			},
		}
		if err := targetClient.Activate(ctx, req); err != nil {
			res.Error = "activate failure: " + err.Error()
			return finalizeScenarioResult(res)
		}
		defer func() {
			_ = targetClient.Deactivate(context.Background(), control.DeactivateRequest{
				RunID:       runID,
				FailureType: sc.FailureType,
				Host:        sc.Host,
				Path:        sc.Path,
			})
		}()
		time.Sleep(300 * time.Millisecond)
	}

	cb.Forget(blogID)
	requestID := "pr105-" + sc.Name + "-" + newID()
	legacyReq := legacyCheckRequest{
		BlogID:           blogID,
		URL:              sc.URL,
		Method:           "HEAD",
		DetectionProfile: "legacy",
		TimeoutSeconds:   int32((sc.Timeout + time.Second - 1) / time.Second),
		RequestID:        requestID,
	}
	if err := sendV1LegacyCheck(ctx, v1Addr, v1Token, blogID, sc.URL); err != nil {
		res.Error = "v1 request: " + err.Error()
		return finalizeScenarioResult(res)
	}
	v1Row, err := cb.Wait(blogID, timeout)
	if err != nil {
		res.Error = "v1 callback: " + err.Error()
		return finalizeScenarioResult(res)
	}
	res.V1 = &v1Row

	v2Legacy, err := sendV2LegacyCheck(ctx, v2Addr, v2Token, legacyReq)
	if err != nil {
		res.Error = "v2 legacy check: " + err.Error()
		return finalizeScenarioResult(res)
	}
	res.V2Legacy = v2Legacy

	v2Res, err := sendV2Check(ctx, v2Addr, v2Token, legacyReq)
	if err != nil {
		res.Error = "v2 check: " + err.Error()
		return finalizeScenarioResult(res)
	}
	res.V2 = v2Res
	return finalizeScenarioResult(res)
}

func finalizeScenarioResult(res scenarioResult) scenarioResult {
	if res.V1 == nil || res.V2Legacy == nil || res.V2 == nil {
		res.CompatibilityNotes = append(res.CompatibilityNotes, "missing result")
		return res
	}
	v1Up := res.V1.Status == 1
	v2LegacyUp := res.V2Legacy.Success
	v2Up := res.V2.Success
	res.DecisionMatch = v1Up == v2LegacyUp
	if !res.DecisionMatch {
		res.CompatibilityNotes = append(res.CompatibilityNotes, fmt.Sprintf("v1/v2 legacy decision mismatch: v1_up=%t v2_up=%t", v1Up, v2LegacyUp))
	}
	res.V1Class = classifyV1Result(*res.V1)
	res.V2LegacyClass = classifyV2LegacyResult(*res.V2Legacy)
	res.V2Class = classifyV2Result(*res.V2)
	res.FailureClassMatch = res.V1Class == res.V2LegacyClass
	if !res.FailureClassMatch {
		res.CompatibilityNotes = append(res.CompatibilityNotes, fmt.Sprintf("v1/v2 legacy failure class mismatch: v1=%s v2=%s", res.V1Class, res.V2LegacyClass))
	}
	res.HTTPCodeMatch = true
	if shouldStrictlyCompareHTTPCode(res.V1Class, res.V2LegacyClass, res.V1.Code, res.V2Legacy.HTTPCode) {
		res.HTTPCodeMatch = int32(res.V1.Code) == res.V2Legacy.HTTPCode
		if !res.HTTPCodeMatch {
			res.CompatibilityNotes = append(res.CompatibilityNotes, fmt.Sprintf("v1/v2 legacy http_code mismatch: v1=%d v2=%d", res.V1.Code, res.V2Legacy.HTTPCode))
		}
	} else if res.V1.Code != int(res.V2Legacy.HTTPCode) {
		res.CompatibilityNotes = append(res.CompatibilityNotes, fmt.Sprintf("raw http_code differs within normalized class: v1=%d v2=%d class=%s", res.V1.Code, res.V2Legacy.HTTPCode, res.V1Class))
	} else {
		res.CompatibilityNotes = append(res.CompatibilityNotes, "no-response class compared as down decision only")
	}
	res.V2ContractMatch = v2LegacyUp == v2Up && res.V2LegacyClass == res.V2Class
	if shouldStrictlyCompareHTTPCode(res.V2LegacyClass, res.V2Class, int(res.V2Legacy.HTTPCode), res.V2.HTTPCode) {
		res.V2ContractMatch = res.V2ContractMatch && res.V2Legacy.HTTPCode == res.V2.HTTPCode
	}
	if !res.V2ContractMatch {
		res.CompatibilityNotes = append(res.CompatibilityNotes, fmt.Sprintf("v2 legacy/v2 contract mismatch: legacy_up=%t v2_up=%t legacy_class=%s v2_class=%s legacy_code=%d v2_code=%d", v2LegacyUp, v2Up, res.V2LegacyClass, res.V2Class, res.V2Legacy.HTTPCode, res.V2.HTTPCode))
	}
	if res.V2.RequestID == "" || res.V2.VantageID == "" || res.V2.AgentID == "" || res.V2.Outcome == "" {
		res.V2ContractMatch = false
		res.CompatibilityNotes = append(res.CompatibilityNotes, "v2 contract missing request_id/vantage_id/agent_id/outcome")
	}
	res.Compatible = res.DecisionMatch && res.HTTPCodeMatch && res.FailureClassMatch && res.V2ContractMatch && res.Error == ""
	if len(res.CompatibilityNotes) == 0 {
		res.CompatibilityNotes = append(res.CompatibilityNotes, "compatible")
	}
	return res
}

func preflightV1(ctx context.Context, addr string) preflightResult {
	raw, err := rawTLSExchange(ctx, addr, "GET", "/get/status", nil, 5*time.Second)
	if err != nil {
		return preflightResult{Name: "v1 status", Status: "fail", Detail: err.Error()}
	}
	if strings.TrimSpace(string(raw)) == "OK" {
		return preflightResult{Name: "v1 status", Status: "pass", Detail: "legacy TLS /get/status returned raw OK"}
	}
	statusCode, body, err := parseRawHTTPResponse(raw)
	if err != nil {
		return preflightResult{Name: "v1 status", Status: "fail", Detail: err.Error()}
	}
	if statusCode == http.StatusOK && strings.TrimSpace(string(body)) == "OK" {
		return preflightResult{Name: "v1 status", Status: "pass", Detail: "legacy TLS /get/status returned OK"}
	}
	return preflightResult{Name: "v1 status", Status: "fail", Detail: fmt.Sprintf("status=%d body=%q", statusCode, strings.TrimSpace(string(body)))}
}

func classifyV1Result(row legacyV1CallbackRow) string {
	if row.Status == 1 {
		return "up"
	}
	switch row.Code {
	case 0:
		return "probe_error"
	case 504:
		return "timeout"
	default:
		return "http_response_down"
	}
}

func classifyV2LegacyResult(row legacyCheckResult) string {
	if row.Success {
		return "up"
	}
	if row.HTTPCode > 0 {
		return "http_response_down"
	}
	switch row.ErrorCode {
	case 1:
		return "timeout"
	case 2:
		return "probe_error"
	default:
		return "unknown_down"
	}
}

func classifyV2Result(row v2CheckResult) string {
	if row.Success {
		return "up"
	}
	switch row.Outcome {
	case "timeout":
		return "timeout"
	case "probe_error":
		return "probe_error"
	case "down":
		return "http_response_down"
	default:
		return row.Outcome
	}
}

func shouldStrictlyCompareHTTPCode(leftClass, rightClass string, leftCode int, rightCode int32) bool {
	if leftCode == 0 && rightCode == 0 {
		return false
	}
	if leftClass != rightClass {
		return true
	}
	return leftClass == "up" || leftClass == "http_response_down"
}

func preflightV2(ctx context.Context, addr string) (v2Status, []preflightResult) {
	var out []preflightResult
	client := &http.Client{Timeout: 5 * time.Second}
	statusURL := "http://" + addr + "/status"
	resp, err := client.Get(statusURL)
	if err != nil {
		out = append(out, preflightResult{Name: "v2 legacy status", Status: "fail", Detail: err.Error()})
	} else {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"status":"OK"`) {
			out = append(out, preflightResult{Name: "v2 legacy status", Status: "pass", Detail: "legacy /status returned OK"})
		} else {
			out = append(out, preflightResult{Name: "v2 legacy status", Status: "fail", Detail: fmt.Sprintf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))})
		}
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v2/status", nil)
	resp, err = client.Do(req)
	if err != nil {
		out = append(out, preflightResult{Name: "v2 status", Status: "fail", Detail: err.Error()})
		return v2Status{}, out
	}
	defer resp.Body.Close()
	var status v2Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		out = append(out, preflightResult{Name: "v2 status", Status: "fail", Detail: err.Error()})
		return v2Status{}, out
	}
	if resp.StatusCode != http.StatusOK || status.Status != "OK" || status.Vantage.ID == "" || status.Agent.ID == "" || status.Capacity.MaxConcurrency <= 0 {
		out = append(out, preflightResult{Name: "v2 status", Status: "fail", Detail: fmt.Sprintf("status=%d vantage=%q agent=%q capacity=%+v", resp.StatusCode, status.Vantage.ID, status.Agent.ID, status.Capacity)})
		return status, out
	}
	out = append(out, preflightResult{Name: "v2 status", Status: "pass", Detail: fmt.Sprintf("version=%s vantage=%s agent=%s capacity=%d+%d", status.Version, status.Vantage.ID, status.Agent.ID, status.Capacity.MaxConcurrency, status.Capacity.QueueCapacity)})
	return status, out
}

func sendV1LegacyCheck(ctx context.Context, addr, token string, blogID int64, targetURL string) error {
	body, err := json.Marshal(legacyV1Request{
		AuthToken: token,
		Checks: []legacyV1RequestCheck{{
			BlogID:     blogID,
			MonitorURL: targetURL,
		}},
	})
	if err != nil {
		return err
	}
	statusCode, responseBody, err := rawTLSLegacyRequest(ctx, addr, "POST", "/get/host-status", body, 10*time.Second)
	if err != nil {
		return err
	}
	var ack legacyV1Ack
	if err := json.Unmarshal(responseBody, &ack); err != nil {
		return fmt.Errorf("decode ack: %w", err)
	}
	if statusCode != http.StatusOK || ack.Status != 1 {
		return fmt.Errorf("ack status=%d body_status=%d error=%q", statusCode, ack.Status, ack.Error)
	}
	return nil
}

func rawTLSLegacyRequest(ctx context.Context, addr, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	data, err := rawTLSExchange(ctx, addr, method, path, body, timeout)
	if err != nil {
		return 0, nil, err
	}
	status, payload, parseErr := parseRawHTTPResponse(data)
	if parseErr != nil {
		return 0, nil, parseErr
	}
	return status, payload, nil
}

func rawTLSExchange(ctx context.Context, addr, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		method, path, addr, len(body))
	if _, err := conn.Write(append([]byte(req), body...)); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(conn)
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return data, nil
}

func parseRawHTTPResponse(data []byte) (int, []byte, error) {
	head, body, ok := bytes.Cut(data, []byte("\r\n\r\n"))
	if !ok {
		return 0, nil, fmt.Errorf("response missing header terminator: %q", truncateBytes(data, 200))
	}
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 {
		return 0, nil, fmt.Errorf("empty response headers")
	}
	var status int
	if _, err := fmt.Sscanf(lines[0], "HTTP/1.1 %d", &status); err != nil {
		return 0, nil, fmt.Errorf("parse response status %q: %w", lines[0], err)
	}
	return status, bytes.TrimSpace(body), nil
}

func truncateBytes(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[:n]) + "..."
}

func sendV2LegacyCheck(ctx context.Context, addr, token string, site legacyCheckRequest) (*legacyCheckResult, error) {
	body, err := json.Marshal(map[string]any{"sites": []legacyCheckRequest{site}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded legacyCheckResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Results) != 1 {
		return nil, fmt.Errorf("result count=%d, want 1", len(decoded.Results))
	}
	return &decoded.Results[0], nil
}

func sendV2Check(ctx context.Context, addr, token string, site legacyCheckRequest) (*v2CheckResult, error) {
	reqID := site.RequestID
	if reqID == "" {
		reqID = newID()
	}
	v2Req := v2BatchRequest{
		BatchID:    "pr105-" + newID(),
		DeadlineMS: int64(site.TimeoutSeconds+5) * 1000,
		Requests: []v2CheckRequest{{
			RequestID:        reqID,
			BlogID:           site.BlogID,
			URL:              site.URL,
			TimeoutMS:        int64(site.TimeoutSeconds) * 1000,
			Method:           site.Method,
			DetectionProfile: site.DetectionProfile,
			Headers:          site.CustomHeaders,
			RedirectPolicy:   site.RedirectPolicy,
		}},
	}
	body, err := json.Marshal(v2Req)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v2/check", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("status=%d body=%q", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded v2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Results) != 1 {
		return nil, fmt.Errorf("result count=%d, want 1", len(decoded.Results))
	}
	return &decoded.Results[0], nil
}

func runOverloadSmoke(ctx context.Context, addr, token string, status v2Status, targetURL string) overloadResult {
	total := status.Capacity.MaxConcurrency + status.Capacity.QueueCapacity + 1
	if total <= 1 {
		return overloadResult{Status: "skip", Error: "capacity unavailable"}
	}
	reqs := make([]v2CheckRequest, total)
	for i := range reqs {
		reqs[i] = v2CheckRequest{
			RequestID:        fmt.Sprintf("overload-%d-%s", i, newID()),
			BlogID:           int64(910205000 + i),
			URL:              targetURL,
			Method:           "HEAD",
			DetectionProfile: "legacy",
			TimeoutMS:        5000,
		}
	}
	body, err := json.Marshal(v2BatchRequest{BatchID: "overload-" + newID(), DeadlineMS: 5000, Requests: reqs})
	if err != nil {
		return overloadResult{Status: "fail", Error: err.Error()}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v2/check", bytes.NewReader(body))
	if err != nil {
		return overloadResult{Status: "fail", Error: err.Error()}
	}
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return overloadResult{Status: "fail", Error: err.Error()}
	}
	defer resp.Body.Close()
	var decoded map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	result := overloadResult{HTTPStatus: resp.StatusCode, Outcome: decoded["outcome"]}
	if resp.StatusCode == http.StatusServiceUnavailable && decoded["outcome"] == "agent_overloaded" {
		result.Status = "pass"
	} else {
		result.Status = "fail"
		result.Error = fmt.Sprintf("expected 503 agent_overloaded, got status=%d outcome=%q", resp.StatusCode, decoded["outcome"])
	}
	healthy := legacyCheckRequest{BlogID: 910299999, URL: targetURL, Method: "HEAD", DetectionProfile: "legacy", TimeoutSeconds: 5, RequestID: "recovery-" + newID()}
	if _, err := sendV2Check(ctx, addr, token, healthy); err != nil {
		result.Status = "fail"
		result.RecoveryStatus = "fail: " + err.Error()
	} else {
		result.RecoveryStatus = "pass"
	}
	return result
}

func startCallbackServer(addr string) (*callbackServer, error) {
	cert, err := selfSignedCert()
	if err != nil {
		return nil, err
	}
	cb := &callbackServer{
		callbacks: map[int64]legacyV1CallbackRow{},
		seen:      make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/put/host-status", cb.handleStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return nil, err
	}
	cb.listener = ln
	cb.srv = &http.Server{Handler: mux}
	go func() {
		if err := cb.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("v1 callback server: %v", err)
		}
	}()
	return cb, nil
}

func (c *callbackServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var payload legacyV1Callback
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	for _, row := range payload.Checks {
		c.callbacks[row.BlogID] = row
	}
	c.mu.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"response":1}`))
}

func (c *callbackServer) Wait(blogID int64, timeout time.Duration) (legacyV1CallbackRow, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if row, ok := c.Get(blogID); ok {
			return row, nil
		}
		select {
		case <-c.seen:
		case <-tick.C:
		case <-deadline.C:
			return legacyV1CallbackRow{}, fmt.Errorf("timed out waiting for blog_id=%d", blogID)
		}
	}
}

func (c *callbackServer) Get(blogID int64) (legacyV1CallbackRow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.callbacks[blogID]
	return row, ok
}

func (c *callbackServer) Forget(blogID int64) {
	c.mu.Lock()
	delete(c.callbacks, blogID)
	c.mu.Unlock()
}

func (c *callbackServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func selfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "uptime-bench-veriflier-pr105"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return tls.X509KeyPair(certPEM, keyPEM)
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
	fmt.Fprintf(&b, "# Jetmon v2 Veriflier PR105 Test\n\n")
	fmt.Fprintf(&b, "Status: `%s`\n\n", rep.Summary.Status)
	fmt.Fprintf(&b, "- Started: `%s`\n", rep.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Finished: `%s`\n", rep.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- v1 Veriflier: `%s`\n", rep.V1Addr)
	fmt.Fprintf(&b, "- v2 Veriflier: `%s`\n", rep.V2Addr)
	fmt.Fprintf(&b, "- Target URL: `%s`\n\n", rep.TargetURL)

	fmt.Fprintf(&b, "## Preflight\n\n")
	fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---:|---|\n")
	for _, p := range rep.Preflight {
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", p.Name, p.Status, escapePipe(p.Detail))
	}

	fmt.Fprintf(&b, "\n## Compatibility Matrix\n\n")
	fmt.Fprintf(&b, "| Scenario | Compatible | v1 status/code/class | v2 legacy success/code/error/class | v2 outcome/code/class | Notes |\n|---|---:|---:|---:|---:|---|\n")
	for _, s := range rep.Scenarios {
		v1 := "missing"
		if s.V1 != nil {
			v1 = fmt.Sprintf("%d/%d/%s", s.V1.Status, s.V1.Code, s.V1Class)
		}
		v2Legacy := "missing"
		if s.V2Legacy != nil {
			v2Legacy = fmt.Sprintf("%t/%d/%d/%s", s.V2Legacy.Success, s.V2Legacy.HTTPCode, s.V2Legacy.ErrorCode, s.V2LegacyClass)
		}
		v2 := "missing"
		if s.V2 != nil {
			v2 = fmt.Sprintf("%s/%d/%s", s.V2.Outcome, s.V2.HTTPCode, s.V2Class)
		}
		notes := strings.Join(s.CompatibilityNotes, "; ")
		if s.Error != "" {
			notes = strings.TrimSpace(notes + "; error=" + s.Error)
		}
		fmt.Fprintf(&b, "| %s | `%t` | %s | %s | %s | %s |\n", s.Name, s.Compatible, v1, v2Legacy, v2, escapePipe(notes))
	}

	fmt.Fprintf(&b, "\n## Overload Smoke\n\n")
	fmt.Fprintf(&b, "- Status: `%s`\n", rep.Overload.Status)
	fmt.Fprintf(&b, "- HTTP status: `%d`\n", rep.Overload.HTTPStatus)
	if rep.Overload.Outcome != "" {
		fmt.Fprintf(&b, "- Outcome: `%s`\n", rep.Overload.Outcome)
	}
	if rep.Overload.RecoveryStatus != "" {
		fmt.Fprintf(&b, "- Recovery: `%s`\n", rep.Overload.RecoveryStatus)
	}
	if rep.Overload.Error != "" {
		fmt.Fprintf(&b, "- Error: `%s`\n", rep.Overload.Error)
	}
	return b.String()
}

func summarizeReport(rep report) reportSummary {
	s := reportSummary{Scenarios: len(rep.Scenarios)}
	for _, p := range rep.Preflight {
		if p.Status != "pass" {
			s.PreflightFailures++
		}
	}
	for _, sc := range rep.Scenarios {
		if sc.Compatible {
			s.Compatible++
		} else {
			s.Incompatible++
		}
	}
	s.Status = "pass"
	if s.PreflightFailures > 0 || s.Incompatible > 0 || (rep.Overload.Status != "" && rep.Overload.Status != "pass" && rep.Overload.Status != "skip") {
		s.Status = "fail"
	}
	return s
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

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
