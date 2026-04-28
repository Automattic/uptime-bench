package campaign

import (
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/scenario"
)

// TestToScenario_HTTPStatusBasic — a non-escalating http_status
// design becomes a scenario with one [[failures]] block carrying the
// chosen status code, the design's seed, and the right target/monitors.
func TestToScenario_HTTPStatusBasic(t *testing.T) {
	d := &Design{
		ID:          "d-0001",
		Cell:        Cell{FailureType: "http_status", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_status",
		Duration:    90 * time.Second,
		Params:      map[string]any{"status_code": 503},
		Seed:        12345,
	}

	sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("ToScenario: %v", err)
	}

	if sc.ID != "c-1-r-0" {
		t.Errorf("ID = %q", sc.ID)
	}
	if sc.Target != "bench-a" {
		t.Errorf("Target = %q", sc.Target)
	}
	if len(sc.Monitors) != 1 || sc.Monitors[0] != "pingdom" {
		t.Errorf("Monitors = %v", sc.Monitors)
	}
	if sc.Duration != 90*time.Second {
		t.Errorf("Duration = %v", sc.Duration)
	}
	if sc.CheckFrequency != time.Minute {
		t.Errorf("CheckFrequency = %v", sc.CheckFrequency)
	}
	if sc.GracePeriod != 30*time.Second {
		t.Errorf("GracePeriod = %v", sc.GracePeriod)
	}
	if sc.Seed == nil || *sc.Seed != 12345 {
		t.Errorf("Seed = %v, want pointer to 12345", sc.Seed)
	}

	if len(sc.Failures) != 1 {
		t.Fatalf("Failures = %d, want 1", len(sc.Failures))
	}
	f := sc.Failures[0]
	if f.Type != "http_status" || f.StatusCode != 503 {
		t.Errorf("Failures[0] = %+v, want type=http_status status_code=503", f)
	}
	if f.Offset != 0 {
		t.Errorf("Failures[0].Offset = %v, want 0 (non-escalating)", f.Offset)
	}
	if f.Rate != 1.0 {
		t.Errorf("Failures[0].Rate = %v, want 1.0 (campaigns always inject every request)", f.Rate)
	}
}

// TestToScenario_HTTPTimeoutParams — http_timeout requires phase +
// delay; the translator pulls them from Params.
func TestToScenario_HTTPTimeoutParams(t *testing.T) {
	d := &Design{
		ID:          "d-0002",
		Cell:        Cell{FailureType: "http_timeout", DurationBucket: "medium", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_timeout",
		Duration:    5 * time.Minute,
		Params: map[string]any{
			"phase": "ttfb",
			"delay": 30 * time.Second,
		},
		Seed: 1,
	}

	sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("ToScenario: %v", err)
	}
	f := sc.Failures[0]
	if f.Phase != "ttfb" {
		t.Errorf("Phase = %q", f.Phase)
	}
	if f.Delay != 30*time.Second {
		t.Errorf("Delay = %v", f.Delay)
	}
}

func TestToScenario_HTTPRedirectParams(t *testing.T) {
	d := &Design{
		ID:          "d-redirect",
		Cell:        Cell{FailureType: "http_redirect", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_redirect",
		Duration:    time.Minute,
		Params: map[string]any{
			"variant":      "chain",
			"chain_length": 3,
		},
		Seed: 7,
	}

	sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("ToScenario: %v", err)
	}
	f := sc.Failures[0]
	if f.Type != "http_redirect" || f.Variant != "chain" || f.ChainLength != 3 {
		t.Fatalf("failure = %+v, want http_redirect chain length 3", f)
	}
}

func TestToScenario_HTTPBodyKeywordDefaults(t *testing.T) {
	cases := []struct {
		name        string
		params      map[string]any
		wantContent string
		wantKeyword string
		wantCheck   string
	}{
		{
			name:        "body content defaults to canary present check",
			params:      map[string]any{"content": "ransomware"},
			wantContent: "ransomware",
			wantKeyword: scenario.CanaryKeyword,
			wantCheck:   "present",
		},
		{
			name:        "keyword injection defaults to absent check",
			params:      map[string]any{"content": "keyword_injected", "keyword": "HACKED"},
			wantContent: "keyword_injected",
			wantKeyword: "HACKED",
			wantCheck:   "absent",
		},
		{
			name:        "explicit keyword check is honored",
			params:      map[string]any{"content": "keyword_missing", "keyword": "Welcome", "keyword_check": "present"},
			wantContent: "keyword_missing",
			wantKeyword: "Welcome",
			wantCheck:   "present",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Design{
				ID:          "d-body",
				Cell:        Cell{FailureType: "http_body", DurationBucket: "brief", HostPattern: HostPatternSingle},
				Targets:     []string{"bench-a"},
				FailureType: "http_body",
				Duration:    time.Minute,
				Params:      tc.params,
				Seed:        7,
			}

			sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
			if err != nil {
				t.Fatalf("ToScenario: %v", err)
			}
			if got := sc.Failures[0].Content; got != tc.wantContent {
				t.Fatalf("Content = %q, want %q", got, tc.wantContent)
			}
			if sc.Keyword != tc.wantKeyword {
				t.Fatalf("Keyword = %q, want %q", sc.Keyword, tc.wantKeyword)
			}
			if sc.KeywordCheck != tc.wantCheck {
				t.Fatalf("KeywordCheck = %q, want %q", sc.KeywordCheck, tc.wantCheck)
			}
		})
	}
}

// TestToScenario_LayeredEscalation — an escalation Design with two
// stages produces two [[failures]] blocks, each carrying its own
// offset and duration. Stage 1 has offset=0; stage 2 carries the
// configured offset.
func TestToScenario_LayeredEscalation(t *testing.T) {
	d := &Design{
		ID:          "d-0003",
		Cell:        Cell{FailureType: "http_status", DurationBucket: "long", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_status",
		Duration:    10 * time.Minute,
		Params:      map[string]any{"status_code": 503},
		Seed:        2,
		Escalation: &EscalationDesign{
			Stages: []EscalationStage{
				{FailureType: "http_status", Params: map[string]any{"status_code": 503}, Offset: 0, Duration: 10 * time.Minute},
				{FailureType: "tcp_refused", Params: map[string]any{}, Offset: 2 * time.Minute, Duration: 8 * time.Minute},
			},
		},
	}

	sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("ToScenario: %v", err)
	}
	if len(sc.Failures) != 2 {
		t.Fatalf("Failures = %d, want 2 (one per stage)", len(sc.Failures))
	}
	if sc.Failures[0].Type != "http_status" || sc.Failures[0].Offset != 0 {
		t.Errorf("Failures[0] = %+v, want http_status @ 0", sc.Failures[0])
	}
	if sc.Failures[0].Duration != 10*time.Minute {
		t.Errorf("Failures[0].Duration = %v, want 10m", sc.Failures[0].Duration)
	}
	if sc.Failures[1].Type != "tcp_refused" || sc.Failures[1].Offset != 2*time.Minute {
		t.Errorf("Failures[1] = %+v, want tcp_refused @ 2m", sc.Failures[1])
	}
	if sc.Failures[1].Duration != 8*time.Minute {
		t.Errorf("Failures[1].Duration = %v, want 8m", sc.Failures[1].Duration)
	}
}

// TestToScenario_MultiHostRejected — host_pattern = "two_random" /
// "all" produce designs with multiple Targets; the scenario format has
// only one Target. Translator rejects rather than silently picking the
// first.
func TestToScenario_MultiHostRejected(t *testing.T) {
	d := &Design{
		ID:          "d-0004",
		Cell:        Cell{FailureType: "tcp_refused", DurationBucket: "brief", HostPattern: HostPatternTwoRandom},
		Targets:     []string{"bench-a", "bench-b"},
		FailureType: "tcp_refused",
		Duration:    time.Minute,
		Params:      map[string]any{},
		Seed:        3,
	}

	_, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err == nil {
		t.Fatal("expected error for multi-host design")
	}
	if !strings.Contains(err.Error(), "multi-host") {
		t.Errorf("err = %v, want one mentioning multi-host limitation", err)
	}
}

// TestToScenario_MissingRequiredParam — http_status without
// status_code in Params should error with a clear message rather than
// producing a scenario with status_code=0 (which the validator would
// reject downstream anyway, but earlier feedback is better).
func TestToScenario_MissingRequiredParam(t *testing.T) {
	d := &Design{
		ID:          "d-0005",
		Cell:        Cell{FailureType: "http_status", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_status",
		Duration:    time.Minute,
		Params:      map[string]any{},
		Seed:        4,
	}

	_, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err == nil {
		t.Fatal("expected error for missing status_code param")
	}
	if !strings.Contains(err.Error(), "status_code") {
		t.Errorf("err = %v, want one mentioning status_code", err)
	}
}

func TestToScenario_TLSFailureParams(t *testing.T) {
	cases := []struct {
		name        string
		failureType string
		params      map[string]any
		assert      func(t *testing.T, f scenario.Failure)
	}{
		{
			name:        "tls_expired",
			failureType: "tls_expired",
			params:      map[string]any{"days_expired": 7},
			assert: func(t *testing.T, f scenario.Failure) {
				if f.DaysExpired != 7 {
					t.Fatalf("DaysExpired = %d, want 7", f.DaysExpired)
				}
			},
		},
		{
			name:        "tls_expiring",
			failureType: "tls_expiring",
			params:      map[string]any{"days_remaining": 6},
			assert: func(t *testing.T, f scenario.Failure) {
				if f.DaysRemaining != 6 {
					t.Fatalf("DaysRemaining = %d, want 6", f.DaysRemaining)
				}
			},
		},
		{
			name:        "tls_invalid",
			failureType: "tls_invalid",
			params:      map[string]any{"variant": "hostname_mismatch"},
			assert: func(t *testing.T, f scenario.Failure) {
				if f.Variant != "hostname_mismatch" {
					t.Fatalf("Variant = %q, want hostname_mismatch", f.Variant)
				}
			},
		},
		{
			name:        "tls_handshake",
			failureType: "tls_handshake",
			params:      map[string]any{"reason": "no_common_cipher"},
			assert: func(t *testing.T, f scenario.Failure) {
				if f.Reason != "no_common_cipher" {
					t.Fatalf("Reason = %q, want no_common_cipher", f.Reason)
				}
			},
		},
		{
			name:        "tls_deprecated",
			failureType: "tls_deprecated",
			params:      map[string]any{"variant": "TLS10"},
			assert: func(t *testing.T, f scenario.Failure) {
				if f.Variant != "TLS10" {
					t.Fatalf("Variant = %q, want TLS10", f.Variant)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &Design{
				ID:          "d-tls",
				Cell:        Cell{FailureType: tc.failureType, DurationBucket: "brief", HostPattern: HostPatternSingle},
				Targets:     []string{"bench-a"},
				FailureType: tc.failureType,
				Duration:    time.Minute,
				Params:      tc.params,
				Seed:        5,
			}

			sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
			if err != nil {
				t.Fatalf("ToScenario: %v", err)
			}
			if got := sc.Failures[0].Type; got != tc.failureType {
				t.Fatalf("Failure.Type = %q, want %q", got, tc.failureType)
			}
			tc.assert(t, sc.Failures[0])
		})
	}
}

func TestToScenario_TLSExpiringRequiresDaysRemaining(t *testing.T) {
	d := &Design{
		ID:          "d-tls-missing",
		Cell:        Cell{FailureType: "tls_expiring", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "tls_expiring",
		Duration:    time.Minute,
		Params:      map[string]any{},
		Seed:        5,
	}

	_, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err == nil {
		t.Fatal("expected error for missing days_remaining param")
	}
	if !strings.Contains(err.Error(), "days_remaining") {
		t.Errorf("err = %v, want one mentioning days_remaining", err)
	}
}

func TestToScenario_HTTPBodyKeywordInjectedRequiresKeyword(t *testing.T) {
	d := &Design{
		ID:          "d-body-missing-keyword",
		Cell:        Cell{FailureType: "http_body", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_body",
		Duration:    time.Minute,
		Params:      map[string]any{"content": "keyword_injected"},
		Seed:        5,
	}

	_, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err == nil {
		t.Fatal("expected error for keyword_injected without keyword")
	}
	if !strings.Contains(err.Error(), "keyword is required") {
		t.Errorf("err = %v, want one mentioning required keyword", err)
	}
}

// TestToScenario_UnsupportedFailureType — failure types that haven't
// been wired yet should fail explicitly. Catches "campaign generator
// added support for type X but translator wasn't updated."
func TestToScenario_UnsupportedFailureType(t *testing.T) {
	d := &Design{
		ID:          "d-0006",
		Cell:        Cell{FailureType: "http_cache_stale", DurationBucket: "brief", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_cache_stale",
		Duration:    time.Minute,
		Params:      map[string]any{},
		Seed:        5,
	}

	_, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err == nil {
		t.Fatal("expected error for unsupported failure type")
	}
	if !strings.Contains(err.Error(), "unsupported failure_type") {
		t.Errorf("err = %v, want one mentioning unsupported failure_type", err)
	}
}

// TestToScenario_DurationStringParam — parameters that arrive as
// duration strings (which would happen if a campaign config carried
// them through JSON or TOML in string form) parse correctly.
func TestToScenario_DurationStringParam(t *testing.T) {
	d := &Design{
		ID:          "d-0007",
		Cell:        Cell{FailureType: "http_timeout", DurationBucket: "medium", HostPattern: HostPatternSingle},
		Targets:     []string{"bench-a"},
		FailureType: "http_timeout",
		Duration:    5 * time.Minute,
		Params: map[string]any{
			"phase": "body",
			"delay": "45s", // string form
		},
		Seed: 6,
	}

	sc, err := d.ToScenario("c-1-r-0", []string{"pingdom"}, time.Minute, 30*time.Second)
	if err != nil {
		t.Fatalf("ToScenario: %v", err)
	}
	if sc.Failures[0].Delay != 45*time.Second {
		t.Errorf("Delay = %v, want 45s (parsed from string)", sc.Failures[0].Delay)
	}
}
