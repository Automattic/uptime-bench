package campaign

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/scenario"
)

// variantContract enumerates every (failure_type, kind, value) tuple
// that *both* campaign.validateVariantChoice/validateContentChoice/
// validateReasonChoice and scenario.validateFailure must accept.
//
// This is the single source of truth for the test. Adding a new
// variant means updating: scenario.go, campaign.go, AND this list.
// The test fails loudly if either validator drifts from this list,
// catching the campaign-and-scenario divergence the duplicated
// switch statements otherwise hide.
//
// For deeper coverage — every variant in scenario.go automatically
// flowing through campaign without a manual list update — see the
// note on validateVariantChoice in campaign.go about extracting an
// AllowedVariants helper from scenario.
var variantContract = []struct {
	failureType string
	kind        string // "variant" | "content" | "reason"
	value       string
}{
	{"http_redirect", "variant", "loop"},
	{"http_redirect", "variant", "chain"},

	{"tls_invalid", "variant", "self_signed"},
	{"tls_invalid", "variant", "hostname_mismatch"},

	{"tls_deprecated", "variant", "TLS10"},
	{"tls_deprecated", "variant", "TLS11"},

	{"tls_handshake", "reason", "version_mismatch"},
	{"tls_handshake", "reason", "no_common_cipher"},

	{"http_body", "content", "empty"},
	{"http_body", "content", "error_page"},
	{"http_body", "content", "keyword_missing"},
	{"http_body", "content", "keyword_injected"},
	{"http_body", "content", "ransomware"},
	{"http_body", "content", "defacement"},
	{"http_body", "content", "malicious_script"},
	{"http_body", "content", "spam_links"},
}

func TestVariantContract_CampaignValidatorsAccept(t *testing.T) {
	for _, tc := range variantContract {
		name := fmt.Sprintf("%s/%s/%s", tc.failureType, tc.kind, tc.value)
		t.Run(name, func(t *testing.T) {
			var err error
			switch tc.kind {
			case "variant":
				err = validateVariantChoice(tc.failureType, tc.value)
			case "content":
				err = validateContentChoice(tc.failureType, tc.value)
			case "reason":
				err = validateReasonChoice(tc.failureType, tc.value)
			default:
				t.Fatalf("unknown kind %q in contract entry", tc.kind)
			}
			if err != nil {
				t.Fatalf("campaign rejected (%s, %s, %s): %v", tc.failureType, tc.kind, tc.value, err)
			}
		})
	}
}

func TestVariantContract_ScenarioParseAccepts(t *testing.T) {
	for _, tc := range variantContract {
		name := fmt.Sprintf("%s/%s/%s", tc.failureType, tc.kind, tc.value)
		t.Run(name, func(t *testing.T) {
			toml := scenarioTOMLForContract(tc.failureType, tc.kind, tc.value)
			if _, err := scenario.Parse([]byte(toml)); err != nil {
				t.Fatalf("scenario.Parse rejected (%s, %s, %s): %v\nTOML:\n%s", tc.failureType, tc.kind, tc.value, err, toml)
			}
		})
	}
}

// TestVariantContract_BothRejectKnownBad is a sanity check: a value
// not in the contract should be rejected by both sides. Otherwise the
// pinning test could silently turn into a no-op if validateFailure
// stops checking switch defaults.
func TestVariantContract_BothRejectKnownBad(t *testing.T) {
	bad := []struct {
		failureType string
		kind        string
		value       string
	}{
		{"http_redirect", "variant", "temporary"},
		{"tls_invalid", "variant", "rogue_ca"},
		{"tls_deprecated", "variant", "SSL30"},
		{"tls_handshake", "reason", "cert_required"},
		{"http_body", "content", "traceback"},
	}
	for _, tc := range bad {
		name := fmt.Sprintf("%s/%s/%s", tc.failureType, tc.kind, tc.value)
		t.Run(name+"/campaign", func(t *testing.T) {
			var err error
			switch tc.kind {
			case "variant":
				err = validateVariantChoice(tc.failureType, tc.value)
			case "content":
				err = validateContentChoice(tc.failureType, tc.value)
			case "reason":
				err = validateReasonChoice(tc.failureType, tc.value)
			}
			if err == nil {
				t.Fatalf("campaign accepted unknown value (%s, %s, %s)", tc.failureType, tc.kind, tc.value)
			}
		})
		t.Run(name+"/scenario", func(t *testing.T) {
			toml := scenarioTOMLForContract(tc.failureType, tc.kind, tc.value)
			if _, err := scenario.Parse([]byte(toml)); err == nil {
				t.Fatalf("scenario accepted unknown value (%s, %s, %s)\nTOML:\n%s", tc.failureType, tc.kind, tc.value, toml)
			}
		})
	}
}

// scenarioTOMLForContract builds the minimal scenario TOML that
// exercises one (failure_type, kind, value) tuple. The scenario-level
// keyword is set unconditionally so http_body/keyword_injected passes
// applyKeywordDefaults.
func scenarioTOMLForContract(failureType, kind, value string) string {
	var b strings.Builder
	b.WriteString(`id              = "pin"
version         = "1"
target          = "bench-a"
monitors        = ["pingdom"]
check_frequency = "60s"
grace_period    = "30s"
duration        = "1m"
keyword         = "uptime-bench-canary"

[[failures]]
type = "`)
	b.WriteString(failureType)
	b.WriteString(`"
rate = 1.0
`)
	switch kind {
	case "variant":
		fmt.Fprintf(&b, "variant = %q\n", value)
	case "content":
		fmt.Fprintf(&b, "content = %q\n", value)
	case "reason":
		fmt.Fprintf(&b, "reason = %q\n", value)
	}
	if failureType == "tls_expiring" {
		b.WriteString("days_remaining = 7\n")
	}
	return b.String()
}
