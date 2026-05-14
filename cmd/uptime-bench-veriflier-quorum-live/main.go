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
	"strconv"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
)

const (
	defaultAPIBaseURL      = "http://127.0.0.1:8090/api/v1"
	defaultTargetControl   = "http://10.0.0.176:9000"
	defaultTargetURL       = "http://site-0000001.capacity.internal"
	defaultTargetHost      = "site-0000001.capacity.internal"
	defaultMonitorTimeout  = 10 * time.Minute
	defaultCheckTimeoutSec = 5
)

type report struct {
	GeneratedAt       time.Time          `json:"generated_at"`
	CaseName          string             `json:"case_name"`
	ExpectedOutcome   string             `json:"expected_outcome"`
	StartedAt         time.Time          `json:"started_at"`
	FinishedAt        time.Time          `json:"finished_at"`
	Status            string             `json:"status"`
	APIBaseURL        string             `json:"api_base_url"`
	TargetURL         string             `json:"target_url"`
	TargetControl     string             `json:"target_control"`
	BlogID            int64              `json:"blog_id"`
	SiteURL           string             `json:"site_url"`
	EventID           int64              `json:"event_id,omitempty"`
	ResolutionReason  string             `json:"resolution_reason,omitempty"`
	DecisionReason    string             `json:"decision_reason,omitempty"`
	DecisionMetadata  map[string]any     `json:"decision_metadata,omitempty"`
	VerifierResults   []map[string]any   `json:"verifier_results,omitempty"`
	Checks            []checkResult      `json:"checks"`
	Failures          []string           `json:"failures,omitempty"`
	Cleanup           map[string]string  `json:"cleanup,omitempty"`
	RequiredVantages  []string           `json:"required_vantages,omitempty"`
	ForbiddenVantages []string           `json:"forbidden_vantages,omitempty"`
	Expected          map[string]float64 `json:"expected,omitempty"`
}

type checkResult struct {
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Detail string         `json:"detail"`
	Data   map[string]any `json:"data,omitempty"`
}

type apiSiteCreateRequest struct {
	BlogID               int64             `json:"blog_id"`
	MonitorURL           string            `json:"monitor_url"`
	MonitorActive        bool              `json:"monitor_active"`
	BucketNo             int               `json:"bucket_no"`
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
		apiBaseURL             = flag.String("api-base-url", defaultAPIBaseURL, "Jetmon v2 API base URL")
		apiToken               = flag.String("api-token", "", "Jetmon API token; prefer JETMON_API_TOKEN")
		targetToken            = flag.String("target-token", "", "target control auth token; prefer TARGET_CONTROL_TOKEN")
		targetURL              = flag.String("target-url", defaultTargetURL, "target base URL to check")
		targetHost             = flag.String("target-host", defaultTargetHost, "target host used for failure injection")
		targetControlURL       = flag.String("target-control-url", defaultTargetControl, "target control base URL")
		outDir                 = flag.String("out-dir", "", "report output directory")
		caseName               = flag.String("case", "multi-vantage-quorum", "test case name")
		expectOutcome          = flag.String("expect-outcome", "down", "expected outcome: down or false_alarm")
		expectedQuorum         = flag.Float64("expect-quorum", -1, "expected verifier_quorum value")
		expectedHealthy        = flag.Float64("expect-healthy", -1, "expected verifier_healthy value")
		expectedConfirmed      = flag.Float64("expect-confirmed", -1, "expected verifier_confirmed value")
		expectedDisagreed      = flag.Float64("expect-disagreed", -1, "expected verifier_disagreed value")
		expectedDuplicateVotes = flag.Float64("expect-duplicate-votes", -1, "expected verifier_duplicate_votes value")
		minVerifierResults     = flag.Int("min-verifier-results", 0, "minimum verifier_results entries in decision metadata")
		requireVantagesRaw     = flag.String("require-vantages", "", "comma-separated v2 vantage_ids required in verifier_results")
		forbidVantagesRaw      = flag.String("forbid-vantages", "", "comma-separated v2 vantage_ids forbidden in verifier_results")
		requireLegacy          = flag.Bool("require-legacy-result", false, "require at least one verifier result without v2 vantage_id")
		requireV2              = flag.Bool("require-v2-result", false, "require at least one verifier result with v2 vantage_id")
		timeout                = flag.Duration("timeout", defaultMonitorTimeout, "max wait for monitor lifecycle")
		checkInterval          = flag.Int("check-interval", 1, "temporary site check interval in minutes")
	)
	flag.Parse()

	if *apiToken == "" {
		*apiToken = os.Getenv("JETMON_API_TOKEN")
	}
	if *targetToken == "" {
		*targetToken = os.Getenv("TARGET_CONTROL_TOKEN")
	}
	if *apiToken == "" || *targetToken == "" {
		log.Fatal("api and target control tokens are required via flags or JETMON_API_TOKEN/TARGET_CONTROL_TOKEN")
	}

	started := time.Now().UTC()
	if *outDir == "" {
		*outDir = filepath.Join("reports", started.Format("20060102T150405Z")+"-jetmon-v2-veriflier-quorum-live")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create report dir: %v", err)
	}

	rep := report{
		GeneratedAt:       started,
		StartedAt:         started,
		CaseName:          *caseName,
		ExpectedOutcome:   *expectOutcome,
		Status:            "pass",
		APIBaseURL:        *apiBaseURL,
		TargetURL:         strings.TrimRight(*targetURL, "/"),
		TargetControl:     *targetControlURL,
		RequiredVantages:  splitCSV(*requireVantagesRaw),
		ForbiddenVantages: splitCSV(*forbidVantagesRaw),
		Expected: map[string]float64{
			"verifier_quorum":          *expectedQuorum,
			"verifier_healthy":         *expectedHealthy,
			"verifier_confirmed":       *expectedConfirmed,
			"verifier_disagreed":       *expectedDisagreed,
			"verifier_duplicate_votes": *expectedDuplicateVotes,
		},
		Cleanup: map[string]string{},
	}

	api := apiClient{
		baseURL: strings.TrimRight(*apiBaseURL, "/"),
		token:   *apiToken,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
	target := control.NewClient(strings.TrimRight(*targetControlURL, "/"), *targetToken, &http.Client{Timeout: 30 * time.Second})

	ctx := context.Background()
	if err := runCase(ctx, &rep, api, target, strings.TrimRight(*targetURL, "/"), *targetHost, *timeout, *checkInterval, *minVerifierResults, *requireLegacy, *requireV2); err != nil {
		rep.fail("run-case", err.Error(), nil)
	}
	rep.FinishedAt = time.Now().UTC()
	if err := writeReports(*outDir, rep); err != nil {
		log.Fatalf("write reports: %v", err)
	}
	if rep.Status != "pass" {
		log.Fatalf("quorum live test status=%s report_dir=%s", rep.Status, *outDir)
	}
	log.Printf("quorum live test status=pass report_dir=%s", *outDir)
}

func runCase(ctx context.Context, rep *report, api apiClient, target *control.Client, targetURL, targetHost string, timeout time.Duration, checkInterval, minVerifierResults int, requireLegacy, requireV2 bool) error {
	if err := api.get(ctx, "/health", nil); err != nil {
		return fmt.Errorf("api health: %w", err)
	}
	rep.pass("api-health", "API health returned OK", nil)

	blogID := int64(910509000000 + time.Now().UTC().Unix()%1000000)
	path := "/quorum-live-" + safeName(rep.CaseName) + "-" + newID()
	siteURL := targetURL + path
	rep.BlogID = blogID
	rep.SiteURL = siteURL

	cooldown := 0
	checkTimeout := defaultCheckTimeoutSec
	siteReq := apiSiteCreateRequest{
		BlogID:               blogID,
		MonitorURL:           siteURL,
		MonitorActive:        true,
		BucketNo:             0,
		RedirectPolicy:       "follow",
		RequestMethod:        "HEAD",
		DetectionProfile:     "legacy",
		TimeoutSeconds:       &checkTimeout,
		CustomHeaders:        map[string]string{"X-Uptime-Bench-Test": "veriflier-quorum-live"},
		AlertCooldownMinutes: &cooldown,
		CheckInterval:        checkInterval,
	}
	var site apiSiteResponse
	if err := api.post(ctx, "/sites", siteReq, &site, map[string]string{"Idempotency-Key": "quorum-live-create-" + newID()}); err != nil {
		return fmt.Errorf("create temporary site: %w", err)
	}
	rep.pass("create-site", "created temporary Jetmon site", map[string]any{"blog_id": blogID, "url": siteURL})
	defer func() {
		if err := api.delete(context.Background(), fmt.Sprintf("/sites/%d", blogID)); err != nil {
			rep.Cleanup["delete_site"] = err.Error()
			rep.fail("cleanup-delete-site", err.Error(), nil)
		} else {
			rep.Cleanup["delete_site"] = "ok"
		}
	}()

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if s, err := waitForSiteChecked(waitCtx, api, blogID, time.Now().UTC()); err != nil {
		return fmt.Errorf("wait initial healthy check: %w", err)
	} else {
		rep.pass("initial-check", "temporary site entered scheduler and checked healthy", siteData(s))
	}

	runID := "quorum-live-" + safeName(rep.CaseName) + "-" + newID()
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
		return fmt.Errorf("activate target failure: %w", err)
	}
	rep.pass("activate-http-503", "target failure activated", map[string]any{"path": path})
	defer func() {
		if err := target.Deactivate(context.Background(), control.DeactivateRequest{RunID: runID, FailureType: "http_status", Host: targetHost, Path: path}); err != nil {
			rep.Cleanup["deactivate_failure"] = err.Error()
			rep.fail("cleanup-deactivate-failure", err.Error(), nil)
		} else {
			rep.Cleanup["deactivate_failure"] = "ok"
		}
	}()

	var event apiEventResponse
	var decisionReason string
	var err error
	switch rep.ExpectedOutcome {
	case "down":
		event, decisionReason, err = waitForDownDecision(waitCtx, api, blogID)
	case "false_alarm":
		event, decisionReason, err = waitForFalseAlarmDecision(waitCtx, api, blogID)
	default:
		return fmt.Errorf("unsupported expected outcome %q", rep.ExpectedOutcome)
	}
	if err != nil {
		return err
	}
	rep.EventID = event.ID
	rep.DecisionReason = decisionReason
	if event.ResolutionReason != nil {
		rep.ResolutionReason = *event.ResolutionReason
	}
	rep.pass("decision-observed", "observed expected monitor decision", map[string]any{
		"event_id":          event.ID,
		"reason":            decisionReason,
		"state":             event.State,
		"resolution_reason": rep.ResolutionReason,
	})

	decisionMeta := transitionMetadata(event.Transitions, decisionReason)
	if len(decisionMeta) == 0 {
		return fmt.Errorf("decision transition %q had no metadata", decisionReason)
	}
	rep.DecisionMetadata = decisionMeta
	rep.VerifierResults = verifierResults(decisionMeta)
	if err := validateDecision(rep, minVerifierResults, requireLegacy, requireV2); err != nil {
		return err
	}
	rep.pass("decision-metadata", "decision metadata matched expected quorum/verifier evidence", map[string]any{
		"metadata":         decisionMeta,
		"verifier_results": rep.VerifierResults,
	})

	if err := target.Deactivate(ctx, control.DeactivateRequest{RunID: runID, FailureType: "http_status", Host: targetHost, Path: path}); err != nil {
		return fmt.Errorf("deactivate target failure: %w", err)
	}
	rep.Cleanup["deactivate_failure"] = "ok"
	rep.pass("deactivate-http-503", "target failure deactivated", nil)

	if _, err := waitForSiteCondition(waitCtx, api, blogID, 10*time.Second, func(site apiSiteResponse) (bool, error) {
		return site.CurrentState == "Up" && site.CurrentSeverity == 0, nil
	}); err != nil {
		return fmt.Errorf("wait projection up: %w", err)
	}
	rep.pass("projection-state", "site projection returned to Up", nil)
	return nil
}

func waitForDownDecision(ctx context.Context, api apiClient, blogID int64) (apiEventResponse, string, error) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		events, err := api.listEvents(ctx, blogID, true)
		if err != nil {
			return apiEventResponse{}, "", err
		}
		for _, event := range events {
			if event.State != "Down" {
				continue
			}
			detail, err := api.getEvent(ctx, blogID, event.ID)
			if err != nil {
				return apiEventResponse{}, "", err
			}
			if reason, ok := verifierConfirmedReason(detail.Transitions); ok {
				return detail, reason, nil
			}
		}
		select {
		case <-ctx.Done():
			return apiEventResponse{}, "", fmt.Errorf("timed out waiting for Down decision for blog_id=%d", blogID)
		case <-tick.C:
		}
	}
}

func waitForFalseAlarmDecision(ctx context.Context, api apiClient, blogID int64) (apiEventResponse, string, error) {
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		events, err := api.listEvents(ctx, blogID, false)
		if err != nil {
			return apiEventResponse{}, "", err
		}
		for _, event := range events {
			if event.ResolutionReason == nil || *event.ResolutionReason != "false_alarm" {
				continue
			}
			detail, err := api.getEvent(ctx, blogID, event.ID)
			if err != nil {
				return apiEventResponse{}, "", err
			}
			if hasTransitionReason(detail.Transitions, "false_alarm") {
				return detail, "false_alarm", nil
			}
		}
		select {
		case <-ctx.Done():
			return apiEventResponse{}, "", fmt.Errorf("timed out waiting for false_alarm decision for blog_id=%d", blogID)
		case <-tick.C:
		}
	}
}

func validateDecision(rep *report, minVerifierResults int, requireLegacy, requireV2 bool) error {
	for key, want := range rep.Expected {
		if want < 0 {
			continue
		}
		got, ok := numberValue(rep.DecisionMetadata[key])
		if !ok {
			return fmt.Errorf("metadata %s missing or not numeric", key)
		}
		if got != want {
			return fmt.Errorf("metadata %s=%v want=%v", key, got, want)
		}
	}
	if len(rep.VerifierResults) < minVerifierResults {
		return fmt.Errorf("verifier_results length=%d want >= %d", len(rep.VerifierResults), minVerifierResults)
	}
	seenVantages := map[string]bool{}
	hasLegacy := false
	hasV2 := false
	for _, item := range rep.VerifierResults {
		vantageID, _ := item["vantage_id"].(string)
		if strings.TrimSpace(vantageID) == "" {
			hasLegacy = true
		} else {
			hasV2 = true
			seenVantages[vantageID] = true
		}
	}
	for _, vantageID := range rep.RequiredVantages {
		if !seenVantages[vantageID] {
			return fmt.Errorf("required vantage_id %q absent from verifier_results", vantageID)
		}
	}
	for _, vantageID := range rep.ForbiddenVantages {
		if seenVantages[vantageID] {
			return fmt.Errorf("forbidden vantage_id %q present in verifier_results", vantageID)
		}
	}
	if requireLegacy && !hasLegacy {
		return fmt.Errorf("expected at least one legacy verifier result without vantage_id")
	}
	if requireV2 && !hasV2 {
		return fmt.Errorf("expected at least one v2 verifier result with vantage_id")
	}
	return nil
}

func transitionMetadata(transitions []apiTransition, reason string) map[string]any {
	for _, transition := range transitions {
		if transition.Reason != reason || len(transition.Metadata) == 0 {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(transition.Metadata, &out); err == nil {
			return out
		}
	}
	return nil
}

func verifierResults(meta map[string]any) []map[string]any {
	raw, ok := meta["verifier_results"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if ok {
			out = append(out, m)
		}
	}
	return out
}

func numberValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
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
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (r *report) pass(name, detail string, data map[string]any) {
	r.Checks = append(r.Checks, checkResult{Name: name, Status: "pass", Detail: detail, Data: data})
}

func (r *report) fail(name, detail string, data map[string]any) {
	r.Status = "fail"
	r.Failures = append(r.Failures, fmt.Sprintf("%s: %s", name, detail))
	r.Checks = append(r.Checks, checkResult{Name: name, Status: "fail", Detail: detail, Data: data})
}

func writeReports(dir string, rep report) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Jetmon v2 Live Veriflier Quorum Test\n\n")
	fmt.Fprintf(&b, "- Case: `%s`\n", rep.CaseName)
	fmt.Fprintf(&b, "- Status: `%s`\n", rep.Status)
	fmt.Fprintf(&b, "- Expected outcome: `%s`\n", rep.ExpectedOutcome)
	fmt.Fprintf(&b, "- Blog ID: `%d`\n", rep.BlogID)
	if rep.EventID != 0 {
		fmt.Fprintf(&b, "- Event ID: `%d`\n", rep.EventID)
	}
	if rep.DecisionReason != "" {
		fmt.Fprintf(&b, "- Decision reason: `%s`\n", rep.DecisionReason)
	}
	if len(rep.Failures) > 0 {
		fmt.Fprintf(&b, "\n## Failures\n\n")
		for _, failure := range rep.Failures {
			fmt.Fprintf(&b, "- %s\n", failure)
		}
	}
	fmt.Fprintf(&b, "\n## Checks\n\n")
	for _, check := range rep.Checks {
		fmt.Fprintf(&b, "- `%s`: %s - %s\n", check.Status, check.Name, check.Detail)
	}
	if len(rep.DecisionMetadata) > 0 {
		fmt.Fprintf(&b, "\n## Decision Metadata\n\n```json\n")
		encoded, _ := json.MarshalIndent(rep.DecisionMetadata, "", "  ")
		b.Write(encoded)
		fmt.Fprintf(&b, "\n```\n")
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(b.String()), 0o644)
}

func siteData(site apiSiteResponse) map[string]any {
	out := map[string]any{
		"id":               site.ID,
		"blog_id":          site.BlogID,
		"current_state":    site.CurrentState,
		"current_severity": site.CurrentSeverity,
	}
	if site.LastCheckedAt != nil {
		out["last_checked_at"] = *site.LastCheckedAt
	}
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

func verifierConfirmedReason(transitions []apiTransition) (string, bool) {
	for _, transition := range transitions {
		if transition.Reason == "verifier_confirmed" {
			return transition.Reason, true
		}
		if len(transition.Metadata) == 0 {
			continue
		}
		var meta map[string]any
		if err := json.Unmarshal(transition.Metadata, &meta); err != nil {
			continue
		}
		if confirmed, ok := numberValue(meta["verifier_confirmed"]); ok && confirmed > 0 {
			return transition.Reason, true
		}
	}
	return "", false
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func safeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			continue
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "case"
	}
	return out
}

func newID() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf[:])
}
