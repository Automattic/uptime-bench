package campaign

import (
	"fmt"
	"time"

	"github.com/Automattic/uptime-bench/internal/scenario"
)

// ToScenario converts a Design into a runnable scenario.Scenario for
// one specific replay. The caller supplies replay-level context that
// Designs themselves don't carry: the per-replay scenario ID
// (typically encoding the design + replay index for traceability),
// the monitors list (the active adapters at runtime — campaigns fan
// out to all enabled services), and the campaign's per-run timing
// knobs (check frequency + grace period).
//
// Constraint (first iteration): Designs that target multiple hosts
// (host_pattern = "two_random" or "all") are not yet supported by
// the scenario format, which has a single Target field. ToScenario
// returns an error for those cases. The campaign generator already
// produces multi-host Designs; the orchestrator gates on this until
// the scenario format gains multi-host support.
//
// Escalation: layered pattern only (matches the generator's current
// output). Each EscalationStage becomes a [[failures]] block with
// its Offset honoured. The "replacement" pattern — stage 2 ending
// stage 1 — is flagged in the spec as an open scenario-format
// question and is not modelled here.
func (d *Design) ToScenario(scenarioID string, monitors []string, checkFrequency, gracePeriod time.Duration) (*scenario.Scenario, error) {
	if d == nil {
		return nil, fmt.Errorf("campaign: ToScenario: nil design")
	}
	if len(d.Targets) != 1 {
		return nil, fmt.Errorf("campaign: ToScenario: design %s has %d targets (host_pattern=%q); multi-host scenarios are not yet supported by the scenario format",
			d.ID, len(d.Targets), d.Cell.HostPattern)
	}

	seed := d.Seed
	sc := &scenario.Scenario{
		ID:             scenarioID,
		Version:        "1",
		Description:    fmt.Sprintf("auto-generated from campaign design %s (cell %s/%s/%s)", d.ID, d.Cell.FailureType, d.Cell.DurationBucket, d.Cell.HostPattern),
		Target:         d.Targets[0],
		Monitors:       monitors,
		CheckFrequency: checkFrequency,
		GracePeriod:    gracePeriod,
		Duration:       d.Duration,
		Seed:           &seed,
	}

	var translated []translatedFailure
	addFailure := func(failureType string, params map[string]any, offset time.Duration) error {
		f, err := failureFrom(failureType, params, offset)
		if err != nil {
			return err
		}
		sc.Failures = append(sc.Failures, f)
		translated = append(translated, translatedFailure{Failure: f, Params: params})
		return nil
	}

	if d.Escalation == nil {
		if err := addFailure(d.FailureType, d.Params, 0); err != nil {
			return nil, err
		}
	} else {
		for i, stage := range d.Escalation.Stages {
			if err := addFailure(stage.FailureType, stage.Params, stage.Offset); err != nil {
				return nil, fmt.Errorf("campaign: ToScenario: design %s stage %d: %w", d.ID, i, err)
			}
		}
	}
	if err := applyHTTPBodyDefaults(sc, translated); err != nil {
		return nil, fmt.Errorf("campaign: ToScenario: design %s: %w", d.ID, err)
	}

	return sc, nil
}

type translatedFailure struct {
	Failure scenario.Failure
	Params  map[string]any
}

// failureFrom builds a scenario.Failure from the (failureType, params,
// offset) triple a Design or EscalationStage carries. Each failure
// type pulls only the params it cares about; unknown params are
// ignored (the campaign generator only sets ones a downstream consumer
// would use, but the validator on scenario.Scenario gives the final
// say).
func failureFrom(failureType string, params map[string]any, offset time.Duration) (scenario.Failure, error) {
	f := scenario.Failure{
		Type:   failureType,
		Rate:   1.0, // campaigns always inject 100% of requests; rate-mixing happens at the campaign level via cell stratification, not within a single scenario
		Offset: offset,
	}

	switch failureType {
	case "http_status":
		code, err := paramInt(params, "status_code")
		if err != nil {
			return f, fmt.Errorf("http_status: %w", err)
		}
		f.StatusCode = code

	case "http_timeout":
		phase, err := paramString(params, "phase")
		if err != nil {
			return f, fmt.Errorf("http_timeout: %w", err)
		}
		f.Phase = phase
		delay, err := paramDuration(params, "delay")
		if err != nil {
			return f, fmt.Errorf("http_timeout: %w", err)
		}
		f.Delay = delay

	case "http_partial":
		// truncate_after_bytes is optional in scenarios; leave nil if
		// the campaign config doesn't specify it. Future enhancement:
		// add truncate_after_bytes to FailureType.

	case "http_redirect":
		variant, err := paramString(params, "variant")
		if err != nil {
			return f, fmt.Errorf("http_redirect: %w", err)
		}
		f.Variant = variant
		if chainLength, ok, err := optionalParamInt(params, "chain_length"); err != nil {
			return f, fmt.Errorf("http_redirect: %w", err)
		} else if ok {
			f.ChainLength = chainLength
		}

	case "http_body":
		content, err := paramString(params, "content")
		if err != nil {
			return f, fmt.Errorf("http_body: %w", err)
		}
		f.Content = content

	case "tcp_refused", "tcp_timeout":
		// no type-specific params

	case "dns_nxdomain", "dns_servfail", "dns_timeout", "dns_cname_nxdomain":
		// no type-specific params

	case "dns_latency":
		latency, err := paramDuration(params, "added_latency")
		if err != nil {
			return f, fmt.Errorf("dns_latency: %w", err)
		}
		f.AddedLatency = latency

	case "dns_ns_unavailable":
		mode, err := paramString(params, "mode")
		if err != nil {
			// mode has a default in the scenario validator; allow
			// campaigns to omit it.
			mode = ""
		}
		f.Mode = mode

	case "tls_expired":
		if days, ok, err := optionalParamInt(params, "days_expired"); err != nil {
			return f, fmt.Errorf("tls_expired: %w", err)
		} else if ok {
			f.DaysExpired = days
		}

	case "tls_expiring":
		days, err := paramInt(params, "days_remaining")
		if err != nil {
			return f, fmt.Errorf("tls_expiring: %w", err)
		}
		f.DaysRemaining = days

	case "tls_invalid":
		if variant, ok, err := optionalParamString(params, "variant"); err != nil {
			return f, fmt.Errorf("tls_invalid: %w", err)
		} else if ok {
			f.Variant = variant
		}

	case "tls_handshake":
		if reason, ok, err := optionalParamString(params, "reason"); err != nil {
			return f, fmt.Errorf("tls_handshake: %w", err)
		} else if ok {
			f.Reason = reason
		}

	case "tls_deprecated":
		if variant, ok, err := optionalParamString(params, "variant"); err != nil {
			return f, fmt.Errorf("tls_deprecated: %w", err)
		} else if ok {
			f.Variant = variant
		}

	default:
		return f, fmt.Errorf("unsupported failure_type %q", failureType)
	}

	return f, nil
}

// applyHTTPBodyDefaults collapses the http_body stages of a translated
// design down to the single Keyword + KeywordCheck pair the scenario
// format carries at scenario level.
//
// Limitation: the scenario format has only one keyword/check per run.
// An escalation that mixes a keyword_injected stage with a non-injected
// http_body stage (ransomware, defacement, keyword_missing, …) collapses
// to KeywordCheck="absent" with the injected keyword, which silences
// the canary-missing signal the non-injected stage was meant to
// measure. The generator can still produce these mixed escalations
// (pickEscalation lets stage 1+ pick any failure type), so this is
// real undermeasurement on those designs, not a theoretical concern.
//
// Per-stage Keyword/KeywordCheck would require moving the fields onto
// scenario.Failure and reworking adapters — most adapters configure
// once at Provision and can't switch keyword mid-run. Tracked under
// "Per-stage keyword config" in ROADMAP.md; instrument prevalence in
// real campaign runs first to decide whether a schema change is
// warranted.
func applyHTTPBodyDefaults(sc *scenario.Scenario, translated []translatedFailure) error {
	hasBody := false
	hasInjected := false
	keyword := ""
	keywordSet := false
	keywordCheck := ""
	keywordCheckSet := false

	for _, tf := range translated {
		if tf.Failure.Type != "http_body" {
			continue
		}
		hasBody = true
		if tf.Failure.Content == "keyword_injected" {
			hasInjected = true
		}
		if kw, ok, err := optionalParamString(tf.Params, "keyword"); err != nil {
			return fmt.Errorf("http_body: %w", err)
		} else if ok {
			if kw == "" {
				return fmt.Errorf("http_body: keyword must not be empty")
			}
			if keywordSet && keyword != kw {
				return fmt.Errorf("http_body: conflicting keyword params %q and %q", keyword, kw)
			}
			keyword = kw
			keywordSet = true
		}
		if check, ok, err := optionalParamString(tf.Params, "keyword_check"); err != nil {
			return fmt.Errorf("http_body: %w", err)
		} else if ok {
			switch check {
			case "present", "absent":
			default:
				return fmt.Errorf("http_body: keyword_check must be one of: present, absent (got %q)", check)
			}
			if keywordCheckSet && keywordCheck != check {
				return fmt.Errorf("http_body: conflicting keyword_check params %q and %q", keywordCheck, check)
			}
			keywordCheck = check
			keywordCheckSet = true
		}
	}

	if !hasBody {
		return nil
	}
	if !keywordSet {
		if hasInjected {
			return fmt.Errorf("http_body: keyword is required when content is keyword_injected")
		}
		keyword = scenario.CanaryKeyword
	}
	if !keywordCheckSet {
		if hasInjected {
			keywordCheck = "absent"
		} else {
			keywordCheck = "present"
		}
	}
	sc.Keyword = keyword
	sc.KeywordCheck = keywordCheck
	return nil
}

// paramInt extracts an int from the params map. Generator-side params
// from pickFailureParams are always concrete int (e.g. status_code
// chosen from []int), so type-assertion to int succeeds; a future
// extension that loaded params from JSON would see float64 instead and
// would need the conversion path commented below.
func paramInt(params map[string]any, key string) (int, error) {
	v, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing param %q", key)
	}
	switch x := v.(type) {
	case int:
		return x, nil
	case int64:
		return int(x), nil
	case float64:
		// JSON-decoded path; round to int.
		return int(x), nil
	default:
		return 0, fmt.Errorf("param %q has type %T, want int", key, v)
	}
}

func optionalParamInt(params map[string]any, key string) (int, bool, error) {
	if _, ok := params[key]; !ok {
		return 0, false, nil
	}
	v, err := paramInt(params, key)
	return v, true, err
}

func paramString(params map[string]any, key string) (string, error) {
	v, ok := params[key]
	if !ok {
		return "", fmt.Errorf("missing param %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("param %q has type %T, want string", key, v)
	}
	return s, nil
}

func optionalParamString(params map[string]any, key string) (string, bool, error) {
	if _, ok := params[key]; !ok {
		return "", false, nil
	}
	v, err := paramString(params, key)
	return v, true, err
}

func paramDuration(params map[string]any, key string) (time.Duration, error) {
	v, ok := params[key]
	if !ok {
		return 0, fmt.Errorf("missing param %q", key)
	}
	switch x := v.(type) {
	case time.Duration:
		return x, nil
	case int64:
		return time.Duration(x), nil
	case string:
		d, err := time.ParseDuration(x)
		if err != nil {
			return 0, fmt.Errorf("param %q: invalid duration %q: %w", key, x, err)
		}
		return d, nil
	default:
		return 0, fmt.Errorf("param %q has type %T, want time.Duration or string", key, v)
	}
}
