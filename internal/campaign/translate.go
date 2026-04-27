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

	if d.Escalation == nil {
		f, err := failureFrom(d.FailureType, d.Params, 0)
		if err != nil {
			return nil, err
		}
		sc.Failures = []scenario.Failure{f}
	} else {
		for i, stage := range d.Escalation.Stages {
			f, err := failureFrom(stage.FailureType, stage.Params, stage.Offset)
			if err != nil {
				return nil, fmt.Errorf("campaign: ToScenario: design %s stage %d: %w", d.ID, i, err)
			}
			sc.Failures = append(sc.Failures, f)
		}
	}

	return sc, nil
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
