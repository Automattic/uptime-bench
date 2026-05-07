// Package scenario parses and validates uptime-bench scenario TOML files.
package scenario

import (
	"fmt"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Scenario is a parsed and validated scenario definition.
type Scenario struct {
	ID             string
	Version        string
	Description    string
	Target         string
	Monitors       []string
	MonitorKind    string
	FreshHostname  bool
	CheckFrequency time.Duration
	GracePeriod    time.Duration
	Duration       time.Duration
	Seed           *int64
	Failures       []Failure

	// Keyword and KeywordCheck configure body-content checks on the
	// monitor side. Populated only when at least one failure is an
	// http_body content type. KeywordCheck takes "present" or "absent":
	//
	//   - "present" — monitor alerts when Keyword is missing from the
	//     response body (the canary case: defacement, ransomware,
	//     malicious_script, spam_links, keyword_missing).
	//   - "absent" — monitor alerts when Keyword is found in the response
	//     body (the injected-bad-keyword case: keyword_injected).
	//
	// Defaults applied during validation: Keyword falls back to the
	// canary string for non-keyword_injected content failures;
	// KeywordCheck falls back to "absent" if any failure is
	// keyword_injected, else "present".
	Keyword      string
	KeywordCheck string

	// ResponseTimeThreshold configures a monitor-side threshold for
	// slow-success scenarios. A monitor should alert when a completed
	// response takes longer than this duration.
	ResponseTimeThreshold time.Duration

	// RequestHeaders configure custom headers the monitor should send.
	// Header-sensitive scenarios use the same map to activate target-side
	// behavior only for probes carrying those headers.
	RequestHeaders map[string]string

	// Maintenance, when non-nil, declares a vendor-side alert-suppression
	// window the harness asks the monitor to honour during this run. The
	// scenario tests whether the monitor correctly silences alerts during
	// the declared window. See docs/inter-run-state-design.md.
	Maintenance *Maintenance
}

// Maintenance is the parsed [maintenance] block from a scenario TOML.
// All offsets are relative to scenario start; the runner converts them
// to absolute timestamps at provision time.
type Maintenance struct {
	// StartOffset is how far after scenario start the window opens.
	// Defaults to 0 if the [maintenance] block is present but the field
	// is omitted.
	StartOffset time.Duration

	// Duration is how long the window stays open. Required when the
	// [maintenance] block is present (must be positive).
	Duration time.Duration
}

// Failure is one failure block from a scenario file.
type Failure struct {
	Type string
	Rate float64
	// Offset delays this failure's activation by the specified duration
	// after the scenario starts.
	Offset time.Duration

	// Duration optionally overrides the scenario's top-level duration for
	// this failure only. A zero value means the failure runs for the
	// scenario duration from its activation moment.
	Duration time.Duration

	// HTTP failure fields
	StatusCode         int
	HeaderName         string
	HeaderValue        string
	Method             string
	Phase              string
	Delay              time.Duration
	TruncateAfterBytes *int
	Variant            string
	ChainLength        int
	Content            string

	// TCP failure fields — no type-specific fields for tcp_refused / tcp_timeout

	// DNS failure fields
	AddedLatency time.Duration
	Mode         string // dns_ns_unavailable: "silent" | "servfail"

	// TLS failure fields
	DaysExpired   int
	DaysRemaining int
	Reason        string

	// Regions, if non-empty, restricts this failure to probes from specific
	// geographic regions. Region names must match keys in probe_ranges in
	// services.toml. The runner expands names to CIDR lists at run time;
	// the failure is applied only to connections from matching source IPs.
	Regions []string
}

// raw mirrors the TOML structure for unmarshalling before validation.
type raw struct {
	ID                    string            `toml:"id"`
	Version               string            `toml:"version"`
	Description           string            `toml:"description"`
	Target                string            `toml:"target"`
	Monitors              []string          `toml:"monitors"`
	MonitorKind           string            `toml:"monitor_kind"`
	FreshHostname         bool              `toml:"fresh_hostname"`
	CheckFrequency        string            `toml:"check_frequency"`
	GracePeriod           string            `toml:"grace_period"`
	Duration              string            `toml:"duration"`
	Seed                  *int64            `toml:"seed"`
	Keyword               string            `toml:"keyword"`
	KeywordCheck          string            `toml:"keyword_check"`
	ResponseTimeThreshold string            `toml:"response_time_threshold"`
	RequestHeaders        map[string]string `toml:"request_headers"`
	Maintenance           *rawMaintenance   `toml:"maintenance"`
	Failures              []rawFailure      `toml:"failures"`
}

type rawMaintenance struct {
	StartOffset string `toml:"start_offset"`
	Duration    string `toml:"duration"`
}

type rawFailure struct {
	Type string  `toml:"type"`
	Rate float64 `toml:"rate"`

	Offset   string `toml:"offset"`
	Duration string `toml:"duration"`

	StatusCode         int      `toml:"status_code"`
	HeaderName         string   `toml:"header_name"`
	HeaderValue        string   `toml:"header_value"`
	Method             string   `toml:"method"`
	Phase              string   `toml:"phase"`
	Delay              string   `toml:"delay"`
	TruncateAfterBytes *int     `toml:"truncate_after_bytes"`
	Variant            string   `toml:"variant"`
	ChainLength        int      `toml:"chain_length"`
	Content            string   `toml:"content"`
	AddedLatency       string   `toml:"added_latency"`
	Mode               string   `toml:"mode"`
	DaysExpired        int      `toml:"days_expired"`
	DaysRemaining      int      `toml:"days_remaining"`
	Reason             string   `toml:"reason"`
	Regions            []string `toml:"regions"`
}

// Parse decodes and validates a scenario from TOML bytes.
func Parse(data []byte) (*Scenario, error) {
	var r raw
	if err := toml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("scenario: parse: %w", err)
	}
	return validate(r)
}

func validate(r raw) (*Scenario, error) {
	if r.ID == "" {
		return nil, fmt.Errorf("scenario: id is required")
	}
	if r.Version == "" {
		return nil, fmt.Errorf("scenario: version is required")
	}
	if r.Target == "" {
		return nil, fmt.Errorf("scenario: target is required")
	}
	if len(r.Monitors) == 0 {
		return nil, fmt.Errorf("scenario: at least one monitor is required")
	}
	if len(r.Failures) == 0 {
		return nil, fmt.Errorf("scenario: at least one [[failures]] block is required")
	}

	checkFreq, err := parseDuration("check_frequency", r.CheckFrequency, true)
	if err != nil {
		return nil, err
	}
	monitorKind, err := validateMonitorKind(r.MonitorKind)
	if err != nil {
		return nil, err
	}
	gracePeriod, err := parseDuration("grace_period", r.GracePeriod, true)
	if err != nil {
		return nil, err
	}
	duration, err := parseDuration("duration", r.Duration, true)
	if err != nil {
		return nil, err
	}
	responseTimeThreshold, err := parseDuration("response_time_threshold", r.ResponseTimeThreshold, false)
	if err != nil {
		return nil, err
	}
	if responseTimeThreshold < 0 {
		return nil, fmt.Errorf("scenario: response_time_threshold must be non-negative")
	}

	s := &Scenario{
		ID:                    r.ID,
		Version:               r.Version,
		Description:           r.Description,
		Target:                r.Target,
		Monitors:              r.Monitors,
		MonitorKind:           monitorKind,
		FreshHostname:         r.FreshHostname,
		CheckFrequency:        checkFreq,
		GracePeriod:           gracePeriod,
		Duration:              duration,
		Seed:                  r.Seed,
		Keyword:               r.Keyword,
		KeywordCheck:          r.KeywordCheck,
		ResponseTimeThreshold: responseTimeThreshold,
		RequestHeaders:        normalizeRequestHeaders(r.RequestHeaders),
	}

	for i, rf := range r.Failures {
		f, err := validateFailure(i, rf)
		if err != nil {
			return nil, err
		}
		s.Failures = append(s.Failures, f)
	}

	if err := applyKeywordDefaults(s); err != nil {
		return nil, err
	}

	if r.Maintenance != nil {
		m, err := validateMaintenance(r.Maintenance)
		if err != nil {
			return nil, err
		}
		s.Maintenance = m
	}

	return s, nil
}

func validateMonitorKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "http", nil
	}
	switch kind {
	case "http", "dns", "tcp", "ssl_certificate", "heartbeat":
		return kind, nil
	default:
		return "", fmt.Errorf("scenario: monitor_kind must be one of: http, dns, tcp, ssl_certificate, heartbeat (got %q)", kind)
	}
}

func normalizeRequestHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validateMaintenance parses and validates the [maintenance] block.
func validateMaintenance(rm *rawMaintenance) (*Maintenance, error) {
	startOffset, err := parseDuration("maintenance.start_offset", rm.StartOffset, false)
	if err != nil {
		return nil, err
	}
	if startOffset < 0 {
		return nil, fmt.Errorf("scenario: maintenance.start_offset must be non-negative")
	}
	dur, err := parseDuration("maintenance.duration", rm.Duration, true)
	if err != nil {
		return nil, err
	}
	if dur <= 0 {
		return nil, fmt.Errorf("scenario: maintenance.duration must be positive")
	}
	return &Maintenance{StartOffset: startOffset, Duration: dur}, nil
}

// CanaryKeyword is the marker string present in healthy responses from
// the target fleet. Content scenarios default to alerting on its absence.
const CanaryKeyword = "uptime-bench-canary"

// applyKeywordDefaults fills in scenario-level Keyword / KeywordCheck for
// content scenarios. See Scenario.Keyword for the rules.
func applyKeywordDefaults(s *Scenario) error {
	hasContentFailure := false
	hasInjected := false
	for _, f := range s.Failures {
		if f.Type != "http_body" {
			continue
		}
		hasContentFailure = true
		if f.Content == "keyword_injected" {
			hasInjected = true
		}
	}
	if !hasContentFailure {
		// No content failure — keyword fields, if set, are irrelevant.
		// Validate them anyway so typos surface early.
		if s.KeywordCheck != "" && s.KeywordCheck != "present" && s.KeywordCheck != "absent" {
			return fmt.Errorf("scenario: keyword_check must be one of: present, absent (got %q)", s.KeywordCheck)
		}
		return nil
	}

	if s.Keyword == "" {
		if hasInjected {
			return fmt.Errorf("scenario: keyword is required at scenario level when any failure is content = keyword_injected (it is the string being injected, which the monitor must check for)")
		}
		s.Keyword = CanaryKeyword
	}

	if s.KeywordCheck == "" {
		if hasInjected {
			s.KeywordCheck = "absent"
		} else {
			s.KeywordCheck = "present"
		}
	}
	if s.KeywordCheck != "present" && s.KeywordCheck != "absent" {
		return fmt.Errorf("scenario: keyword_check must be one of: present, absent (got %q)", s.KeywordCheck)
	}
	return nil
}

func validateFailure(i int, rf rawFailure) (Failure, error) {
	ctx := fmt.Sprintf("scenario: failures[%d]", i)

	if rf.Type == "" {
		return Failure{}, fmt.Errorf("%s: type is required", ctx)
	}

	rate := rf.Rate
	if rate == 0 {
		rate = 1.0
	}
	if rate <= 0 || rate > 1.0 {
		return Failure{}, fmt.Errorf("%s: rate must be in (0.0, 1.0], got %v", ctx, rate)
	}

	offset, err := parseDuration("offset", rf.Offset, false)
	if err != nil {
		return Failure{}, fmt.Errorf("%s: %w", ctx, err)
	}
	if offset < 0 {
		return Failure{}, fmt.Errorf("%s: offset must be non-negative", ctx)
	}
	duration := time.Duration(0)
	if rf.Duration != "" {
		duration, err = parseDuration("duration", rf.Duration, true)
		if err != nil {
			return Failure{}, fmt.Errorf("%s: %w", ctx, err)
		}
		if duration <= 0 {
			return Failure{}, fmt.Errorf("%s: duration must be positive", ctx)
		}
	}

	f := Failure{
		Type:               rf.Type,
		Rate:               rate,
		Offset:             offset,
		Duration:           duration,
		StatusCode:         rf.StatusCode,
		Method:             rf.Method,
		Phase:              rf.Phase,
		TruncateAfterBytes: rf.TruncateAfterBytes,
		HeaderName:         strings.TrimSpace(rf.HeaderName),
		HeaderValue:        rf.HeaderValue,
		Variant:            rf.Variant,
		ChainLength:        rf.ChainLength,
		Content:            rf.Content,
		Mode:               rf.Mode,
		DaysExpired:        rf.DaysExpired,
		DaysRemaining:      rf.DaysRemaining,
		Reason:             rf.Reason,
		Regions:            rf.Regions,
	}

	if rf.Delay != "" {
		d, err := parseDuration("delay", rf.Delay, false)
		if err != nil {
			return Failure{}, fmt.Errorf("%s: %w", ctx, err)
		}
		f.Delay = d
	}
	if rf.AddedLatency != "" {
		d, err := parseDuration("added_latency", rf.AddedLatency, false)
		if err != nil {
			return Failure{}, fmt.Errorf("%s: %w", ctx, err)
		}
		f.AddedLatency = d
	}

	if err := validateFailureType(ctx, &f); err != nil {
		return Failure{}, err
	}

	return f, nil
}

// validateFailureType checks per-type required fields and applies per-type
// defaults. Takes *Failure so default writes (e.g. ChainLength = 15) are
// observable to the caller.
func validateFailureType(ctx string, f *Failure) error {
	switch f.Type {
	case "http_status":
		if f.StatusCode < 100 || f.StatusCode > 599 {
			return fmt.Errorf("%s: status_code must be a valid HTTP status code (100-599)", ctx)
		}
	case "http_method_status":
		if f.StatusCode < 100 || f.StatusCode > 599 {
			return fmt.Errorf("%s: status_code must be a valid HTTP status code (100-599)", ctx)
		}
		if err := validateMethod(ctx, f.Method, true); err != nil {
			return err
		}
		if len(f.Regions) > 0 {
			return fmt.Errorf("%s: regions are not supported for http_method_status", ctx)
		}
	case "http_timeout":
		if err := validateMethod(ctx, f.Method, false); err != nil {
			return err
		}
		if f.Phase == "" {
			return fmt.Errorf("%s: phase is required for http_timeout", ctx)
		}
		switch f.Phase {
		case "ttfb", "body", "total":
		default:
			return fmt.Errorf("%s: phase must be one of: ttfb, body, total", ctx)
		}
		if f.Delay == 0 {
			return fmt.Errorf("%s: delay is required for http_timeout", ctx)
		}
	case "http_latency":
		if err := validateMethod(ctx, f.Method, false); err != nil {
			return err
		}
		if f.Delay == 0 {
			return fmt.Errorf("%s: delay is required for http_latency", ctx)
		}
	case "http_header_status":
		if f.StatusCode < 100 || f.StatusCode > 599 {
			return fmt.Errorf("%s: status_code must be a valid HTTP status code (100-599)", ctx)
		}
		if f.HeaderName == "" {
			return fmt.Errorf("%s: header_name is required for http_header_status", ctx)
		}
	case "http_partial":
		if err := validateMethod(ctx, f.Method, false); err != nil {
			return err
		}
		// truncate_after_bytes is optional
	case "http_redirect":
		if err := validateMethod(ctx, f.Method, false); err != nil {
			return err
		}
		if f.Variant == "" {
			return fmt.Errorf("%s: variant is required for http_redirect", ctx)
		}
		switch f.Variant {
		case "loop", "chain":
		default:
			return fmt.Errorf("%s: variant must be one of: loop, chain", ctx)
		}
		if f.Variant == "chain" && f.ChainLength == 0 {
			f.ChainLength = 15
		}
	case "http_body":
		if err := validateMethod(ctx, f.Method, false); err != nil {
			return err
		}
		if f.Content == "" {
			return fmt.Errorf("%s: content is required for http_body", ctx)
		}
		switch f.Content {
		case "empty", "error_page", "keyword_missing", "keyword_injected",
			"ransomware", "defacement", "malicious_script", "spam_links":
		default:
			return fmt.Errorf("%s: content must be one of: empty, error_page, keyword_missing, keyword_injected, ransomware, defacement, malicious_script, spam_links", ctx)
		}
		// Keyword is configured at scenario level (see Scenario.Keyword and
		// applyKeywordDefaults), not per-failure. The post-validation
		// keyword-defaults pass enforces that scenarios containing
		// keyword_injected supply an explicit keyword.
	case "tcp_refused", "tcp_timeout":
		// no type-specific fields
	case "dns_nxdomain", "dns_servfail", "dns_timeout", "dns_cname_nxdomain":
		// no type-specific fields
	case "dns_latency":
		if f.AddedLatency == 0 {
			return fmt.Errorf("%s: added_latency is required for dns_latency", ctx)
		}
	case "dns_ns_unavailable":
		if f.Mode == "" {
			f.Mode = "silent"
		}
		switch f.Mode {
		case "silent", "servfail":
		default:
			return fmt.Errorf("%s: mode must be one of: silent, servfail", ctx)
		}
	case "tls_expired":
		if f.DaysExpired == 0 {
			f.DaysExpired = 1
		}
	case "tls_expiring":
		if f.DaysRemaining <= 0 {
			return fmt.Errorf("%s: days_remaining must be a positive integer for tls_expiring", ctx)
		}
	case "tls_invalid":
		if f.Variant == "" {
			f.Variant = "self_signed"
		}
		switch f.Variant {
		case "self_signed", "hostname_mismatch":
		default:
			return fmt.Errorf("%s: variant must be one of: self_signed, hostname_mismatch", ctx)
		}
	case "tls_handshake":
		if f.Reason == "" {
			f.Reason = "version_mismatch"
		}
		switch f.Reason {
		case "version_mismatch", "no_common_cipher":
		default:
			return fmt.Errorf("%s: reason must be one of: version_mismatch, no_common_cipher", ctx)
		}
	case "tls_deprecated":
		if f.Variant == "" {
			f.Variant = "TLS11"
		}
		switch f.Variant {
		case "TLS10", "TLS11":
		default:
			return fmt.Errorf("%s: variant must be one of: TLS10, TLS11", ctx)
		}
	default:
		return fmt.Errorf("%s: unknown failure type %q", ctx, f.Type)
	}
	return nil
}

func validateMethod(ctx, method string, required bool) error {
	if method == "" {
		if required {
			return fmt.Errorf("%s: method must be one of: GET, HEAD", ctx)
		}
		return nil
	}
	switch method {
	case "GET", "HEAD":
		return nil
	default:
		return fmt.Errorf("%s: method must be one of: GET, HEAD", ctx)
	}
}

func parseDuration(field, s string, required bool) (time.Duration, error) {
	if s == "" {
		if required {
			return 0, fmt.Errorf("scenario: %s is required", field)
		}
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("scenario: %s: invalid duration %q: %w", field, s, err)
	}
	return d, nil
}
