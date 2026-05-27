package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

const (
	defaultV2Addr        = "10.0.0.173:7803"
	defaultTargetURL     = "http://10.0.0.176"
	defaultTargetHost    = "10.0.0.176"
	defaultTargetControl = "http://10.0.0.176:9000"
)

type v2BatchRequest struct {
	BatchID    string           `json:"batch_id,omitempty"`
	DeadlineMS int64            `json:"deadline_ms,omitempty"`
	Requests   []v2CheckRequest `json:"requests"`
}

type v2CheckRequest struct {
	RequestID           string `json:"request_id,omitempty"`
	BlogID              int64  `json:"blog_id"`
	URL                 string `json:"url"`
	TimeoutMS           int64  `json:"timeout_ms,omitempty"`
	Method              string `json:"method,omitempty"`
	DetectionProfile    string `json:"detection_profile,omitempty"`
	RedirectPolicy      string `json:"redirect_policy,omitempty"`
	BodyReadMaxBytes    int64  `json:"body_read_max_bytes,omitempty"`
	BodyReadMaxMS       int32  `json:"body_read_max_ms,omitempty"`
	KeywordReadMaxBytes int64  `json:"keyword_read_max_bytes,omitempty"`
	KeywordReadMaxMS    int32  `json:"keyword_read_max_ms,omitempty"`
}

type v2BatchResponse struct {
	BatchID string          `json:"batch_id,omitempty"`
	Results []v2CheckResult `json:"results"`
}

type v2CheckResult struct {
	RequestID string `json:"request_id"`
	BlogID    int64  `json:"blog_id"`
	URL       string `json:"url"`
	Outcome   string `json:"outcome"`
	Success   bool   `json:"success"`
	HTTPCode  int32  `json:"http_code"`
	ErrorCode int32  `json:"error_code"`
	RTTMs     int64  `json:"rtt_ms"`
}

type report struct {
	GeneratedAt   time.Time       `json:"generated_at"`
	StartedAt     time.Time       `json:"started_at"`
	FinishedAt    time.Time       `json:"finished_at"`
	Status        string          `json:"status"`
	V2Addr        string          `json:"v2_addr"`
	TargetURL     string          `json:"target_url"`
	TargetControl string          `json:"target_control_url"`
	HTTPStatus    int             `json:"http_status"`
	Checks        []checkResult   `json:"checks"`
	Results       []resultSummary `json:"results,omitempty"`
	Notes         []string        `json:"notes,omitempty"`
}

type checkResult struct {
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Detail string         `json:"detail,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type resultSummary struct {
	RequestID string `json:"request_id"`
	Category  string `json:"category"`
	URL       string `json:"url"`
	Success   bool   `json:"success"`
	Outcome   string `json:"outcome"`
	HTTPCode  int32  `json:"http_code"`
	ErrorCode int32  `json:"error_code"`
	RTTMs     int64  `json:"rtt_ms"`
}

type requestCase struct {
	category    string
	request     v2CheckRequest
	mustPass    bool
	mustFailOwn bool
}

func main() {
	var (
		v2Addr           = flag.String("v2-addr", defaultV2Addr, "Jetmon v2 Veriflier HTTP address")
		v2Token          = flag.String("v2-token", "", "Jetmon v2 Veriflier token; prefer -v2-token-file or V2_VERIFLIER_TOKEN")
		v2TokenFile      = flag.String("v2-token-file", "", "file containing Jetmon v2 Veriflier token")
		targetURL        = flag.String("target-url", defaultTargetURL, "internal target base URL")
		targetHost       = flag.String("target-host", defaultTargetHost, "target Host value for failure activation")
		targetControlURL = flag.String("target-control-url", defaultTargetControl, "target control base URL")
		targetToken      = flag.String("target-token", "", "target control token; prefer -target-token-file or TARGET_CONTROL_TOKEN")
		targetTokenFile  = flag.String("target-token-file", "", "file containing target control token")
		skipFixtures     = flag.Bool("skip-target-fixtures", false, "skip target-control fixture failures and run only safe/public plus suspicious URL cases")
		safeURL          = flag.String("safe-url", "", "healthy public control URL for -skip-target-fixtures mode")
		outDir           = flag.String("out-dir", "", "report output directory")
		timeout          = flag.Duration("request-timeout", 12*time.Second, "overall /v2/check request timeout")
	)
	flag.Parse()

	started := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", started.Format("20060102T150405Z")+"-jetmon-v2-poison-batch")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatalf("create report dir: %v", err)
	}

	token, err := firstSecret(*v2Token, *v2TokenFile, "V2_VERIFLIER_TOKEN", "VERIFLIER_AUTH_TOKEN")
	if err != nil {
		fatalReport(*outDir, started, *v2Addr, *targetURL, *targetControlURL, "missing v2 token: "+err.Error(), nil)
	}
	var targetSecret string
	if !*skipFixtures {
		targetSecret, err = firstSecret(*targetToken, *targetTokenFile, "TARGET_CONTROL_TOKEN")
		if err != nil {
			fatalReport(*outDir, started, *v2Addr, *targetURL, *targetControlURL, "missing target token: "+err.Error(), nil)
		}
	}

	rep := report{
		StartedAt:     started,
		V2Addr:        *v2Addr,
		TargetURL:     strings.TrimRight(*targetURL, "/"),
		TargetControl: *targetControlURL,
		Status:        "pass",
		Notes: []string{
			"Expected contract: a suspicious URL may fail its own result, but must not fail the whole mixed batch or suppress unrelated healthy results.",
			"Redirect-to-internal requires an absolute redirect fixture; this command covers direct internal targets and redirect loops with the current target service.",
			"Unsupported per-request options are included with healthy control URLs to verify they produce per-request failures instead of batch-level rejection.",
		},
	}
	if *skipFixtures {
		rep.Notes = append(rep.Notes, "Target-control fixture cases were skipped; this mode is for public-safe control validation when direct Veriflier target safety rejects internal fixture hostnames.")
	}

	ctx := context.Background()
	var target *control.Client
	if !*skipFixtures {
		target = control.NewClient(*targetControlURL, targetSecret, &http.Client{Timeout: 5 * time.Second})
	}
	var cleanups []control.DeactivateRequest
	finish := func() {
		if target != nil {
			for i := len(cleanups) - 1; i >= 0; i-- {
				_ = target.Deactivate(context.Background(), cleanups[i])
			}
		}
		writeAndExit(*outDir, &rep)
	}
	if !*skipFixtures {
		if status, err := target.Status(ctx); err != nil {
			rep.fail("target-control-status", err.Error(), nil)
			finish()
			return
		} else if len(status.ActiveFailures) != 0 {
			rep.fail("target-control-clean", "target has active failures before poison batch", map[string]any{"active_failures": status.ActiveFailures})
			finish()
			return
		} else {
			rep.pass("target-control-clean", "target has no active failures", nil)
		}
	} else {
		rep.pass("target-control-skip", "target fixtures skipped", nil)
	}

	id := started.Format("20060102T150405")
	redirectPath := "/poison-redirect-loop-" + id
	slowPath := "/poison-slow-" + id
	partialPath := "/poison-partial-" + id
	activations := []control.ActivateRequest{
		{
			RunID: "poison-redirect-" + id,
			Seed:  1,
			Failure: control.FailureSpec{
				Type:     "http_redirect",
				Host:     *targetHost,
				Path:     redirectPath,
				Duration: 5 * time.Minute,
				Rate:     1,
				Params:   map[string]any{"method": "GET", "variant": "loop"},
			},
		},
		{
			RunID: "poison-slow-" + id,
			Seed:  2,
			Failure: control.FailureSpec{
				Type:     "http_timeout",
				Host:     *targetHost,
				Path:     slowPath,
				Duration: 5 * time.Minute,
				Rate:     1,
				Params:   map[string]any{"method": "GET", "delay": "3s"},
			},
		},
		{
			RunID: "poison-partial-" + id,
			Seed:  3,
			Failure: control.FailureSpec{
				Type:     "http_partial",
				Host:     *targetHost,
				Path:     partialPath,
				Duration: 5 * time.Minute,
				Rate:     1,
				Params:   map[string]any{"method": "GET", "truncate_after_bytes": 9},
			},
		},
	}
	if !*skipFixtures {
		for _, activation := range activations {
			if err := target.Activate(ctx, activation); err != nil {
				rep.fail("activate-"+activation.Failure.Type, err.Error(), map[string]any{"path": activation.Failure.Path})
				finish()
				return
			}
			rep.pass("activate-"+activation.Failure.Type, "target failure activated", map[string]any{"path": activation.Failure.Path})
			cleanups = append(cleanups, control.DeactivateRequest{RunID: activation.RunID, FailureType: activation.Failure.Type, Host: activation.Failure.Host, Path: activation.Failure.Path})
		}
	}

	targetBase := strings.TrimRight(*targetURL, "/")
	controlURL := targetBase + "/poison-safe-" + id
	if safe := strings.TrimSpace(*safeURL); safe != "" {
		controlURL = safe
	}
	if *skipFixtures {
		if controlURL == "" {
			rep.fail("safe-url", "-safe-url is required with -skip-target-fixtures", nil)
			finish()
			return
		}
	}
	cases := []requestCase{
		{category: "safe-before", mustPass: true, request: req(920000001, controlURL, "GET", "full", 3000)},
		{category: "localhost", mustFailOwn: true, request: req(920000002, "http://localhost/admin", "GET", "full", 1000)},
		{category: "loopback-ip", mustFailOwn: true, request: req(920000003, "http://127.0.0.1/admin", "GET", "full", 1000)},
		{category: "rfc1918-ip", mustFailOwn: true, request: req(920000004, "http://10.0.0.1/", "GET", "full", 1000)},
		{category: "link-local-ip", mustFailOwn: true, request: req(920000005, "http://169.254.1.1/", "GET", "full", 1000)},
		{category: "metadata-ip", mustFailOwn: true, request: req(920000006, "http://169.254.169.254/latest/meta-data/", "GET", "full", 1000)},
		{category: "malformed-url", mustFailOwn: true, request: req(920000007, "://invalid-url", "GET", "full", 1000)},
		{category: "unsupported-scheme", mustFailOwn: true, request: req(920000008, "ftp://example.com/", "GET", "full", 1000)},
		{category: "dns-failure", mustFailOwn: true, request: req(920000009, "http://poison-does-not-exist-"+id+".capacity.internal/", "GET", "full", 1000)},
		{category: "unsupported-method", mustFailOwn: true, request: req(920000015, controlURL, "BREW", "full", 1000)},
		{category: "unsupported-profile", mustFailOwn: true, request: req(920000016, controlURL, "GET", "unsupported_profile", 1000)},
		{category: "unsupported-redirect-policy", request: withRedirect(req(920000017, controlURL, "GET", "full", 1000), "sideways")},
		{category: "invalid-body-limit", request: withBodyLimit(req(920000018, controlURL, "GET", "full", 1000), -1)},
		{category: "safe-after", mustPass: true, request: req(920000014, controlURL, "GET", "full", 3000)},
	}
	if !*skipFixtures {
		cases = append(cases,
			requestCase{category: "redirect-loop", mustFailOwn: true, request: withRedirect(req(920000010, targetBase+redirectPath, "GET", "full", 3000), "follow")},
			requestCase{category: "slow-response", mustFailOwn: true, request: req(920000011, targetBase+slowPath, "GET", "full", 1000)},
			requestCase{category: "partial-body", mustFailOwn: true, request: req(920000012, targetBase+partialPath, "GET", "full", 3000)},
			requestCase{category: "small-body-limit", request: withBodyLimit(req(920000013, targetBase+"/poison-body-limit-"+id, "GET", "full", 3000), 8)},
		)
	}
	for i := range cases {
		cases[i].request.RequestID = cases[i].category
	}

	batch := v2BatchRequest{BatchID: "poison-" + id, DeadlineMS: int64((*timeout + 2*time.Second) / time.Millisecond)}
	for _, tc := range cases {
		batch.Requests = append(batch.Requests, tc.request)
	}
	code, decoded, body, err := postBatch(ctx, *v2Addr, token, batch, *timeout)
	rep.HTTPStatus = code
	if err != nil {
		rep.fail("mixed-batch-http", err.Error(), map[string]any{"http_status": code, "body": string(body)})
		finish()
		return
	}
	if code != http.StatusOK {
		rep.fail("mixed-batch-http", "mixed batch returned non-200 HTTP status", map[string]any{"http_status": code, "body": string(body)})
		finish()
		return
	}
	rep.pass("mixed-batch-http", "mixed batch returned HTTP 200", map[string]any{"request_count": len(batch.Requests)})

	byID := map[string]v2CheckResult{}
	for _, result := range decoded.Results {
		byID[result.RequestID] = result
	}
	if len(decoded.Results) != len(cases) {
		rep.fail("result-count", fmt.Sprintf("result count=%d want=%d", len(decoded.Results), len(cases)), nil)
	} else {
		rep.pass("result-count", "every request received an individual result", nil)
	}
	for _, tc := range cases {
		result, ok := byID[tc.category]
		if !ok {
			rep.fail("result-"+tc.category, "missing result", map[string]any{"url": tc.request.URL})
			continue
		}
		rep.Results = append(rep.Results, resultSummary{
			RequestID: result.RequestID,
			Category:  tc.category,
			URL:       result.URL,
			Success:   result.Success,
			Outcome:   result.Outcome,
			HTTPCode:  result.HTTPCode,
			ErrorCode: result.ErrorCode,
			RTTMs:     result.RTTMs,
		})
		if tc.mustPass && !result.Success {
			rep.fail("healthy-"+tc.category, "healthy control request failed", map[string]any{"outcome": result.Outcome, "error_code": result.ErrorCode, "http_code": result.HTTPCode})
			continue
		}
		if tc.mustPass {
			rep.pass("healthy-"+tc.category, "healthy control request succeeded", map[string]any{"http_code": result.HTTPCode, "rtt_ms": result.RTTMs})
		}
		if tc.mustFailOwn && result.Success {
			rep.fail("poison-"+tc.category, "poison request unexpectedly succeeded", map[string]any{"outcome": result.Outcome, "http_code": result.HTTPCode, "rtt_ms": result.RTTMs})
			continue
		}
		if tc.mustFailOwn {
			rep.pass("poison-"+tc.category, "poison request failed in its own result", map[string]any{"outcome": result.Outcome, "error_code": result.ErrorCode, "http_code": result.HTTPCode, "rtt_ms": result.RTTMs})
		}
	}
	finish()
}

func req(blogID int64, url, method, profile string, timeoutMS int64) v2CheckRequest {
	return v2CheckRequest{BlogID: blogID, URL: url, Method: method, DetectionProfile: profile, TimeoutMS: timeoutMS}
}

func withRedirect(r v2CheckRequest, policy string) v2CheckRequest {
	r.RedirectPolicy = policy
	return r
}

func withBodyLimit(r v2CheckRequest, n int64) v2CheckRequest {
	r.BodyReadMaxBytes = n
	return r
}

func postBatch(ctx context.Context, addr, token string, batch v2BatchRequest, timeout time.Duration) (int, v2BatchResponse, []byte, error) {
	body, err := json.Marshal(batch)
	if err != nil {
		return 0, v2BatchResponse{}, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v2/check", bytes.NewReader(body))
	if err != nil {
		return 0, v2BatchResponse{}, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, v2BatchResponse{}, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, v2BatchResponse{}, nil, err
	}
	var decoded v2BatchResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(respBody, &decoded); err != nil {
			return resp.StatusCode, v2BatchResponse{}, respBody, err
		}
	}
	return resp.StatusCode, decoded, respBody, nil
}

func firstSecret(explicit, file string, envNames ...string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit), nil
	}
	if file != "" {
		b, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		if s := strings.TrimSpace(string(b)); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("%s is empty", file)
	}
	for _, name := range envNames {
		if s := strings.TrimSpace(os.Getenv(name)); s != "" {
			return s, nil
		}
	}
	return "", fmt.Errorf("set one of %s", strings.Join(envNames, ", "))
}

func (r *report) pass(name, detail string, data map[string]any) {
	r.Checks = append(r.Checks, checkResult{Name: name, Status: "pass", Detail: detail, Data: data})
}

func (r *report) fail(name, detail string, data map[string]any) {
	r.Status = "fail"
	r.Checks = append(r.Checks, checkResult{Name: name, Status: "fail", Detail: detail, Data: data})
}

func writeAndExit(dir string, r *report) {
	r.FinishedAt = time.Now().UTC()
	r.GeneratedAt = r.FinishedAt
	if err := writeReport(dir, *r); err != nil {
		fatalf("write report: %v", err)
	}
	if r.Status != "pass" {
		os.Exit(1)
	}
}

func fatalReport(dir string, started time.Time, v2Addr, targetURL, targetControl, detail string, data map[string]any) {
	rep := report{StartedAt: started, V2Addr: v2Addr, TargetURL: targetURL, TargetControl: targetControl, Status: "fail"}
	rep.fail("preflight", detail, data)
	writeAndExit(dir, &rep)
}

func writeReport(dir string, rep report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Jetmon v2 Poison Batch Isolation\n\n")
	fmt.Fprintf(&md, "Status: `%s`\n\n", rep.Status)
	fmt.Fprintf(&md, "- Started: `%s`\n", rep.StartedAt.Format(time.RFC3339))
	fmt.Fprintf(&md, "- Finished: `%s`\n", rep.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&md, "- v2 Veriflier: `%s`\n", rep.V2Addr)
	fmt.Fprintf(&md, "- Target URL: `%s`\n", rep.TargetURL)
	fmt.Fprintf(&md, "- HTTP status: `%d`\n\n", rep.HTTPStatus)
	for _, note := range rep.Notes {
		fmt.Fprintf(&md, "- %s\n", note)
	}
	if len(rep.Notes) > 0 {
		md.WriteByte('\n')
	}
	fmt.Fprintln(&md, "## Checks")
	md.WriteByte('\n')
	for _, check := range rep.Checks {
		fmt.Fprintf(&md, "- `%s` `%s`: %s\n", check.Status, check.Name, check.Detail)
	}
	if len(rep.Results) > 0 {
		fmt.Fprintln(&md, "\n## Results")
		md.WriteByte('\n')
		fmt.Fprintln(&md, "| Category | Success | Outcome | HTTP | Error | RTT ms |")
		fmt.Fprintln(&md, "|---|---:|---|---:|---:|---:|")
		for _, result := range rep.Results {
			fmt.Fprintf(&md, "| %s | %t | %s | %d | %d | %d |\n", result.Category, result.Success, result.Outcome, result.HTTPCode, result.ErrorCode, result.RTTMs)
		}
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(md.String()), 0o644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
