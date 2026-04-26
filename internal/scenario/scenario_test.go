package scenario

import (
	"strings"
	"testing"
)

// TestValidateFailureType_DefaultsApplied is a regression test for a bug where
// validateFailureType took the Failure by value and silently discarded every
// default it set. Each subtest parses a scenario that omits a defaultable field
// and asserts the parsed Failure carries the expected default.
func TestValidateFailureType_DefaultsApplied(t *testing.T) {
	const header = `
id              = "x"
version         = "1"
target          = "t"
monitors        = ["m"]
check_frequency = "60s"
grace_period    = "60s"
duration        = "60s"
`

	cases := []struct {
		name      string
		failure   string
		assertion func(t *testing.T, f Failure)
	}{
		{
			name: "http_redirect chain default chain_length=15",
			failure: `
[[failures]]
type    = "http_redirect"
variant = "chain"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.ChainLength != 15 {
					t.Fatalf("ChainLength: got %d, want 15", f.ChainLength)
				}
			},
		},
		{
			name: "dns_ns_unavailable default mode=silent",
			failure: `
[[failures]]
type = "dns_ns_unavailable"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.Mode != "silent" {
					t.Fatalf("Mode: got %q, want %q", f.Mode, "silent")
				}
			},
		},
		{
			name: "tls_expired default days_expired=1",
			failure: `
[[failures]]
type = "tls_expired"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.DaysExpired != 1 {
					t.Fatalf("DaysExpired: got %d, want 1", f.DaysExpired)
				}
			},
		},
		{
			name: "tls_invalid default variant=self_signed",
			failure: `
[[failures]]
type = "tls_invalid"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.Variant != "self_signed" {
					t.Fatalf("Variant: got %q, want %q", f.Variant, "self_signed")
				}
			},
		},
		{
			name: "tls_handshake default reason=version_mismatch",
			failure: `
[[failures]]
type = "tls_handshake"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.Reason != "version_mismatch" {
					t.Fatalf("Reason: got %q, want %q", f.Reason, "version_mismatch")
				}
			},
		},
		{
			name: "tls_deprecated default variant=TLS11",
			failure: `
[[failures]]
type = "tls_deprecated"
`,
			assertion: func(t *testing.T, f Failure) {
				if f.Variant != "TLS11" {
					t.Fatalf("Variant: got %q, want %q", f.Variant, "TLS11")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Parse([]byte(header + tc.failure))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(sc.Failures) != 1 {
				t.Fatalf("expected 1 failure, got %d", len(sc.Failures))
			}
			tc.assertion(t, sc.Failures[0])
		})
	}
}

// TestValidateFailureType_RejectsBadInput covers the existing validation
// errors so the refactor doesn't accidentally weaken them.
func TestValidateFailureType_RejectsBadInput(t *testing.T) {
	const header = `
id              = "x"
version         = "1"
target          = "t"
monitors        = ["m"]
check_frequency = "60s"
grace_period    = "60s"
duration        = "60s"
`

	cases := []struct {
		name       string
		failure    string
		wantSubstr string
	}{
		{"http_status bad code", `
[[failures]]
type        = "http_status"
status_code = 999
`, "status_code must be a valid HTTP status code"},
		{"http_redirect bad variant", `
[[failures]]
type    = "http_redirect"
variant = "bogus"
`, "variant must be one of: loop, chain"},
		{"unknown failure type", `
[[failures]]
type = "made_up"
`, "unknown failure type"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(header + tc.failure))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestKeywordDefaults exercises applyKeywordDefaults: the rules that fill
// in scenario.Keyword and scenario.KeywordCheck for content scenarios.
func TestKeywordDefaults(t *testing.T) {
	const header = `
id              = "x"
version         = "1"
target          = "t"
monitors        = ["m"]
check_frequency = "60s"
grace_period    = "60s"
duration        = "60s"
`

	cases := []struct {
		name      string
		body      string
		wantKw    string
		wantCheck string
	}{
		{
			name: "defacement defaults to canary + present",
			body: `
[[failures]]
type    = "http_body"
content = "defacement"
`,
			wantKw:    CanaryKeyword,
			wantCheck: "present",
		},
		{
			name: "keyword_missing defaults to canary + present",
			body: `
[[failures]]
type    = "http_body"
content = "keyword_missing"
`,
			wantKw:    CanaryKeyword,
			wantCheck: "present",
		},
		{
			name: "explicit keyword overrides default",
			body: `
keyword = "Welcome"

[[failures]]
type    = "http_body"
content = "ransomware"
`,
			wantKw:    "Welcome",
			wantCheck: "present",
		},
		{
			name: "keyword_injected with explicit keyword defaults check to absent",
			body: `
keyword = "HACKED"

[[failures]]
type    = "http_body"
content = "keyword_injected"
`,
			wantKw:    "HACKED",
			wantCheck: "absent",
		},
		{
			name: "non-content scenario leaves keyword untouched",
			body: `
[[failures]]
type        = "http_status"
status_code = 503
`,
			wantKw:    "",
			wantCheck: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := Parse([]byte(header + tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if sc.Keyword != tc.wantKw {
				t.Errorf("Keyword = %q, want %q", sc.Keyword, tc.wantKw)
			}
			if sc.KeywordCheck != tc.wantCheck {
				t.Errorf("KeywordCheck = %q, want %q", sc.KeywordCheck, tc.wantCheck)
			}
		})
	}
}

// TestKeywordValidationErrors covers the cases where the keyword config is
// malformed enough to reject the scenario at parse time.
func TestKeywordValidationErrors(t *testing.T) {
	const header = `
id              = "x"
version         = "1"
target          = "t"
monitors        = ["m"]
check_frequency = "60s"
grace_period    = "60s"
duration        = "60s"
`

	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{
			name: "keyword_injected without explicit keyword is rejected",
			body: `
[[failures]]
type    = "http_body"
content = "keyword_injected"
`,
			wantSubstr: "keyword is required",
		},
		{
			name: "invalid keyword_check value",
			body: `
keyword       = "k"
keyword_check = "maybe"

[[failures]]
type    = "http_body"
content = "defacement"
`,
			wantSubstr: "keyword_check must be one of",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(header + tc.body))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}
