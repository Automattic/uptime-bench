// Package scenario parses and validates uptime-bench scenario TOML files.
package scenario

import (
	"fmt"
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
	CheckFrequency time.Duration
	GracePeriod    time.Duration
	Duration       time.Duration
	Seed           *int64
	Failures       []Failure
}

// Failure is one failure block from a scenario file.
type Failure struct {
	Type string
	Rate float64
	// Offset delays this failure's activation by the specified duration
	// after the scenario starts. The failure runs for the scenario's
	// `duration` from its activation moment.
	Offset time.Duration

	// HTTP failure fields
	StatusCode         int
	Phase              string
	Delay              time.Duration
	TruncateAfterBytes *int
	Variant            string
	ChainLength        int
	Content            string
	Keyword            string

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
	ID             string       `toml:"id"`
	Version        string       `toml:"version"`
	Description    string       `toml:"description"`
	Target         string       `toml:"target"`
	Monitors       []string     `toml:"monitors"`
	CheckFrequency string       `toml:"check_frequency"`
	GracePeriod    string       `toml:"grace_period"`
	Duration       string       `toml:"duration"`
	Seed           *int64       `toml:"seed"`
	Failures       []rawFailure `toml:"failures"`
}

type rawFailure struct {
	Type string  `toml:"type"`
	Rate float64 `toml:"rate"`

	Offset string `toml:"offset"`

	StatusCode         int      `toml:"status_code"`
	Phase              string   `toml:"phase"`
	Delay              string   `toml:"delay"`
	TruncateAfterBytes *int     `toml:"truncate_after_bytes"`
	Variant            string   `toml:"variant"`
	ChainLength        int      `toml:"chain_length"`
	Content            string   `toml:"content"`
	Keyword            string   `toml:"keyword"`
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
	gracePeriod, err := parseDuration("grace_period", r.GracePeriod, true)
	if err != nil {
		return nil, err
	}
	duration, err := parseDuration("duration", r.Duration, true)
	if err != nil {
		return nil, err
	}

	s := &Scenario{
		ID:             r.ID,
		Version:        r.Version,
		Description:    r.Description,
		Target:         r.Target,
		Monitors:       r.Monitors,
		CheckFrequency: checkFreq,
		GracePeriod:    gracePeriod,
		Duration:       duration,
		Seed:           r.Seed,
	}

	for i, rf := range r.Failures {
		f, err := validateFailure(i, rf)
		if err != nil {
			return nil, err
		}
		s.Failures = append(s.Failures, f)
	}

	return s, nil
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

	f := Failure{
		Type:               rf.Type,
		Rate:               rate,
		Offset:             offset,
		StatusCode:         rf.StatusCode,
		Phase:              rf.Phase,
		TruncateAfterBytes: rf.TruncateAfterBytes,
		Variant:            rf.Variant,
		ChainLength:        rf.ChainLength,
		Content:            rf.Content,
		Keyword:            rf.Keyword,
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
	case "http_timeout":
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
	case "http_partial":
		// truncate_after_bytes is optional
	case "http_redirect":
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
		if f.Content == "" {
			return fmt.Errorf("%s: content is required for http_body", ctx)
		}
		switch f.Content {
		case "empty", "error_page", "keyword_missing", "keyword_injected",
			"ransomware", "defacement", "malicious_script", "spam_links":
		default:
			return fmt.Errorf("%s: content must be one of: empty, error_page, keyword_missing, keyword_injected, ransomware, defacement, malicious_script, spam_links", ctx)
		}
		if (f.Content == "keyword_missing" || f.Content == "keyword_injected") && f.Keyword == "" {
			return fmt.Errorf("%s: keyword is required when content = %s", ctx, f.Content)
		}
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
