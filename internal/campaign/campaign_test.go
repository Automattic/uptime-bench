package campaign

import (
	"strings"
	"testing"
	"time"
)

// validHeader is the minimum required preamble for a campaign TOML.
// Tests append additional sections to it.
const validHeader = `
id              = "test-campaign"
description     = "x"
duration        = "24h"
seed            = 42
check_frequency = "60s"

[targets]
pool     = ["bench-a", "bench-b"]
patterns = ["single", "two_random"]

[duration_buckets]
brief  = { min = "30s", max = "2m" }
medium = { min = "2m",  max = "10m" }

[sampling]
samples_per_cell_default = 20

[[failure_types]]
type = "http_status"
status_code_choices = [503, 502]

[[failure_types]]
type = "tcp_refused"

[cooldown]
per_target_minimum = "10m"
`

// TestParse_HappyPath exercises a fully-formed campaign and checks that
// every locked-in field round-trips through the parser.
func TestParse_HappyPath(t *testing.T) {
	body := validHeader + `
[[sampling.high_discrimination]]
failure_types   = ["http_status"]
samples_per_cell = 60

[escalation]
probability       = 0.20
stages_range      = { min = 2, max = 3 }
inter_stage_range = { min = "30s", max = "5m" }

[budget]
pingdom     = { max_runs_per_hour = 10 }
uptimerobot = { max_runs_per_hour = 1 }
jetmon-v1   = {}
`
	c, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if c.ID != "test-campaign" {
		t.Errorf("ID = %q", c.ID)
	}
	if c.Duration != 24*time.Hour {
		t.Errorf("Duration = %v", c.Duration)
	}
	if c.Seed != 42 {
		t.Errorf("Seed = %d", c.Seed)
	}

	if len(c.Targets.Pool) != 2 || c.Targets.Pool[0] != "bench-a" {
		t.Errorf("Targets.Pool = %v", c.Targets.Pool)
	}
	if len(c.Targets.Patterns) != 2 {
		t.Errorf("Targets.Patterns = %v", c.Targets.Patterns)
	}

	if b, ok := c.DurationBuckets["brief"]; !ok || b.Min != 30*time.Second || b.Max != 2*time.Minute {
		t.Errorf("DurationBuckets[brief] = %+v", b)
	}

	if c.Sampling.SamplesPerCellDefault != 20 {
		t.Errorf("SamplesPerCellDefault = %d", c.Sampling.SamplesPerCellDefault)
	}
	if len(c.Sampling.HighDiscrimination) != 1 ||
		c.Sampling.HighDiscrimination[0].SamplesPerCell != 60 ||
		c.Sampling.HighDiscrimination[0].FailureTypes[0] != "http_status" {
		t.Errorf("HighDiscrimination = %+v", c.Sampling.HighDiscrimination)
	}

	if len(c.FailureTypes) != 2 {
		t.Fatalf("len(FailureTypes) = %d, want 2", len(c.FailureTypes))
	}
	if c.FailureTypes[0].Type != "http_status" {
		t.Errorf("FailureTypes[0].Type = %q", c.FailureTypes[0].Type)
	}
	if len(c.FailureTypes[0].StatusCodeChoices) != 2 || c.FailureTypes[0].StatusCodeChoices[0] != 503 {
		t.Errorf("FailureTypes[0].StatusCodeChoices = %v", c.FailureTypes[0].StatusCodeChoices)
	}

	if c.Escalation == nil {
		t.Fatal("Escalation should be non-nil when [escalation] is declared")
	}
	if c.Escalation.Probability != 0.20 {
		t.Errorf("Escalation.Probability = %v", c.Escalation.Probability)
	}
	if c.Escalation.StagesRange.Min != 2 || c.Escalation.StagesRange.Max != 3 {
		t.Errorf("Escalation.StagesRange = %+v", c.Escalation.StagesRange)
	}
	if c.Escalation.InterStageRange.Min != 30*time.Second || c.Escalation.InterStageRange.Max != 5*time.Minute {
		t.Errorf("Escalation.InterStageRange = %+v", c.Escalation.InterStageRange)
	}

	if c.Budget["pingdom"].MaxRunsPerHour != 10 {
		t.Errorf("Budget[pingdom] = %+v", c.Budget["pingdom"])
	}
	if c.Budget["jetmon-v1"].MaxRunsPerHour != 0 {
		t.Errorf("Budget[jetmon-v1] = %+v (want 0 = unlimited)", c.Budget["jetmon-v1"])
	}

	if c.Cooldown.PerTargetMinimum != 10*time.Minute {
		t.Errorf("Cooldown.PerTargetMinimum = %v", c.Cooldown.PerTargetMinimum)
	}
}

// TestParse_NoEscalationLeavesItNil — when [escalation] is omitted, the
// campaign's Escalation pointer is nil rather than zero-valued.
func TestParse_NoEscalationLeavesItNil(t *testing.T) {
	c, err := Parse([]byte(validHeader))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Escalation != nil {
		t.Errorf("Escalation should be nil when omitted, got %+v", c.Escalation)
	}
}

// TestParse_RejectsInvalidConfigs covers each validation rule with a
// concrete failing config + the expected error substring.
func TestParse_RejectsInvalidConfigs(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSubstr string
	}{
		{
			name:       "missing id",
			body:       strings.Replace(validHeader, `id              = "test-campaign"`, "", 1),
			wantSubstr: "id is required",
		},
		{
			name:       "zero duration",
			body:       strings.Replace(validHeader, `duration        = "24h"`, `duration = "0s"`, 1),
			wantSubstr: "duration must be positive",
		},
		{
			name:       "empty target pool",
			body:       strings.Replace(validHeader, `pool     = ["bench-a", "bench-b"]`, `pool = []`, 1),
			wantSubstr: "targets.pool",
		},
		{
			name: "unknown host pattern",
			body: strings.Replace(validHeader,
				`patterns = ["single", "two_random"]`,
				`patterns = ["single", "rolling"]`, 1),
			wantSubstr: `unknown pattern "rolling"`,
		},
		{
			name: "two_random pattern with single-host pool",
			body: strings.Replace(strings.Replace(validHeader,
				`pool     = ["bench-a", "bench-b"]`,
				`pool = ["bench-a"]`, 1),
				`patterns = ["single", "two_random"]`,
				`patterns = ["single", "two_random"]`, 1),
			wantSubstr: "two_random",
		},
		{
			name: "duration bucket with max < min",
			body: strings.Replace(validHeader,
				`brief  = { min = "30s", max = "2m" }`,
				`brief  = { min = "5m", max = "1m" }`, 1),
			wantSubstr: "duration_buckets.brief: max",
		},
		{
			name: "zero default sample count",
			body: strings.Replace(validHeader,
				"samples_per_cell_default = 20",
				"samples_per_cell_default = 0", 1),
			wantSubstr: "samples_per_cell_default must be positive",
		},
		{
			name: "high_discrimination references unknown failure type",
			body: validHeader + `
[[sampling.high_discrimination]]
failure_types    = ["http_phantom"]
samples_per_cell = 60
`,
			wantSubstr: `failure_type "http_phantom" is not declared`,
		},
		{
			name: "high_discrimination zero sample count",
			body: validHeader + `
[[sampling.high_discrimination]]
failure_types    = ["http_status"]
samples_per_cell = 0
`,
			wantSubstr: "samples_per_cell must be positive",
		},
		{
			name: "duplicate failure type",
			body: validHeader + `
[[failure_types]]
type = "http_status"
`,
			wantSubstr: "duplicate type",
		},
		{
			name: "invalid status code",
			body: strings.Replace(validHeader,
				"status_code_choices = [503, 502]",
				"status_code_choices = [503, 999]", 1),
			wantSubstr: "invalid code 999",
		},
		{
			name: "escalation probability out of range",
			body: validHeader + `
[escalation]
probability       = 1.5
stages_range      = { min = 2, max = 3 }
inter_stage_range = { min = "30s", max = "5m" }
`,
			wantSubstr: "probability must be in [0, 1]",
		},
		{
			name: "escalation stages_range below 2",
			body: validHeader + `
[escalation]
probability       = 0.2
stages_range      = { min = 1, max = 3 }
inter_stage_range = { min = "30s", max = "5m" }
`,
			wantSubstr: "stages_range.min must be ≥ 2",
		},
		{
			name: "escalation missing inter_stage_range",
			body: validHeader + `
[escalation]
probability  = 0.2
stages_range = { min = 2, max = 3 }
`,
			wantSubstr: "inter_stage_range is required",
		},
		{
			name: "negative budget",
			body: validHeader + `
[budget]
pingdom = { max_runs_per_hour = -1 }
`,
			wantSubstr: "max_runs_per_hour must be ≥ 0",
		},
		{
			name: "negative cooldown",
			body: strings.Replace(validHeader,
				`per_target_minimum = "10m"`,
				`per_target_minimum = "-1m"`, 1),
			wantSubstr: "cooldown.per_target_minimum must be non-negative",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.body))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// TestParse_HostPatternAllOK — the "all" host pattern is valid even on
// a single-host pool (it just means every scenario hits that one host).
// Regression check that the two_random pool-size guard doesn't
// accidentally apply to "all".
func TestParse_HostPatternAllOK(t *testing.T) {
	body := strings.Replace(strings.Replace(validHeader,
		`pool     = ["bench-a", "bench-b"]`,
		`pool = ["bench-a"]`, 1),
		`patterns = ["single", "two_random"]`,
		`patterns = ["single", "all"]`, 1)
	if _, err := Parse([]byte(body)); err != nil {
		t.Errorf("Parse: %v", err)
	}
}

// TestParse_FailureTypeDelayRange — http_timeout's delay_range parses
// correctly into a DurationRange (and rejects max < min).
func TestParse_FailureTypeDelayRange(t *testing.T) {
	body := validHeader + `
[[failure_types]]
type          = "http_timeout"
phase_choices = ["ttfb", "body"]
delay_range   = { min = "5s", max = "60s" }
`
	c, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.FailureTypes) != 3 {
		t.Fatalf("len(FailureTypes) = %d, want 3", len(c.FailureTypes))
	}
	ft := c.FailureTypes[2]
	if ft.Type != "http_timeout" {
		t.Errorf("Type = %q", ft.Type)
	}
	if ft.DelayRange == nil || ft.DelayRange.Min != 5*time.Second || ft.DelayRange.Max != 60*time.Second {
		t.Errorf("DelayRange = %+v", ft.DelayRange)
	}
	if len(ft.PhaseChoices) != 2 || ft.PhaseChoices[0] != "ttfb" {
		t.Errorf("PhaseChoices = %v", ft.PhaseChoices)
	}
}

func TestParse_FailureTypeHTTPChoices(t *testing.T) {
	body := validHeader + `
[[failure_types]]
type            = "http_redirect"
variant_choices = ["loop", "chain"]

[[failure_types]]
type            = "http_body"
content_choices = ["keyword_missing", "keyword_injected", "ransomware"]
keyword_choices = ["uptime-bench-canary", "HACKED"]
`
	c, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.FailureTypes) != 4 {
		t.Fatalf("len(FailureTypes) = %d, want 4", len(c.FailureTypes))
	}
	byType := make(map[string]FailureType, len(c.FailureTypes))
	for _, ft := range c.FailureTypes {
		byType[ft.Type] = ft
	}
	if got := byType["http_redirect"].VariantChoices; len(got) != 2 || got[1] != "chain" {
		t.Fatalf("http_redirect VariantChoices = %v", got)
	}
	if got := byType["http_body"].ContentChoices; len(got) != 3 || got[2] != "ransomware" {
		t.Fatalf("http_body ContentChoices = %v", got)
	}
	if got := byType["http_body"].KeywordChoices; len(got) != 2 || got[1] != "HACKED" {
		t.Fatalf("http_body KeywordChoices = %v", got)
	}
}

func TestParse_RejectsInvalidHTTPChoices(t *testing.T) {
	cases := []struct {
		name       string
		block      string
		wantSubstr string
	}{
		{
			name: "invalid redirect variant",
			block: `
[[failure_types]]
type            = "http_redirect"
variant_choices = ["temporary"]
`,
			wantSubstr: "invalid http_redirect variant",
		},
		{
			name: "invalid body content",
			block: `
[[failure_types]]
type            = "http_body"
content_choices = ["traceback"]
`,
			wantSubstr: "invalid http_body content",
		},
		{
			name: "keyword injected requires keywords",
			block: `
[[failure_types]]
type            = "http_body"
content_choices = ["keyword_injected"]
`,
			wantSubstr: "keyword_choices is required",
		},
		{
			name: "empty keyword choice",
			block: `
[[failure_types]]
type            = "http_body"
content_choices = ["keyword_missing"]
keyword_choices = [""]
`,
			wantSubstr: "keyword_choices",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(validHeader + tc.block))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestParse_FailureTypeTLSChoices(t *testing.T) {
	body := validHeader + `
[[failure_types]]
type                 = "tls_expired"
days_expired_choices = [1, 7, 30]

[[failure_types]]
type                   = "tls_expiring"
days_remaining_choices = [6, 13, 29]

[[failure_types]]
type            = "tls_invalid"
variant_choices = ["self_signed", "hostname_mismatch"]

[[failure_types]]
type           = "tls_handshake"
reason_choices = ["version_mismatch", "no_common_cipher"]

[[failure_types]]
type            = "tls_deprecated"
variant_choices = ["TLS10", "TLS11"]
`
	c, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(c.FailureTypes) != 7 {
		t.Fatalf("len(FailureTypes) = %d, want 7", len(c.FailureTypes))
	}
	byType := make(map[string]FailureType, len(c.FailureTypes))
	for _, ft := range c.FailureTypes {
		byType[ft.Type] = ft
	}
	if got := byType["tls_expired"].DaysExpiredChoices; len(got) != 3 || got[2] != 30 {
		t.Fatalf("tls_expired DaysExpiredChoices = %v", got)
	}
	if got := byType["tls_expiring"].DaysRemainingChoices; len(got) != 3 || got[0] != 6 {
		t.Fatalf("tls_expiring DaysRemainingChoices = %v", got)
	}
	if got := byType["tls_invalid"].VariantChoices; len(got) != 2 || got[1] != "hostname_mismatch" {
		t.Fatalf("tls_invalid VariantChoices = %v", got)
	}
	if got := byType["tls_handshake"].ReasonChoices; len(got) != 2 || got[1] != "no_common_cipher" {
		t.Fatalf("tls_handshake ReasonChoices = %v", got)
	}
	if got := byType["tls_deprecated"].VariantChoices; len(got) != 2 || got[0] != "TLS10" {
		t.Fatalf("tls_deprecated VariantChoices = %v", got)
	}
}

func TestParse_RejectsInvalidTLSChoices(t *testing.T) {
	cases := []struct {
		name       string
		block      string
		wantSubstr string
	}{
		{
			name: "non-positive days expired",
			block: `
[[failure_types]]
type                 = "tls_expired"
days_expired_choices = [0]
`,
			wantSubstr: "days_expired_choices",
		},
		{
			name: "non-positive days remaining",
			block: `
[[failure_types]]
type                   = "tls_expiring"
days_remaining_choices = [-1]
`,
			wantSubstr: "days_remaining_choices",
		},
		{
			name: "invalid invalid-cert variant",
			block: `
[[failure_types]]
type            = "tls_invalid"
variant_choices = ["wrong-host"]
`,
			wantSubstr: "invalid tls_invalid variant",
		},
		{
			name: "invalid deprecated variant",
			block: `
[[failure_types]]
type            = "tls_deprecated"
variant_choices = ["TLS13"]
`,
			wantSubstr: "invalid tls_deprecated variant",
		},
		{
			name: "invalid handshake reason",
			block: `
[[failure_types]]
type           = "tls_handshake"
reason_choices = ["cert_required"]
`,
			wantSubstr: "reason_choices",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(validHeader + tc.block))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}
