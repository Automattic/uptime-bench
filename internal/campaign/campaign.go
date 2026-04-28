// Package campaign parses and validates uptime-bench campaign TOML files.
//
// A campaign is a long-running orchestration mode where the harness
// self-generates randomized scenarios and aggregates the results into
// per-(failure_type, service) statistics. This package only handles
// parsing + validation; the design+schedule generator is in a separate
// step. See ROADMAP.md "Automated randomized testing campaigns" for the
// methodology.
package campaign

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
)

// Campaign is a parsed and validated campaign definition.
type Campaign struct {
	ID          string
	Description string
	Duration    time.Duration
	Seed        int64

	// CheckFrequency and GracePeriod apply to every replay generated
	// from the campaign. Both are campaign-level (not per-cell) so all
	// services are tested at the same probe rate and the same alert
	// settle window — that's what makes per-service detection-latency
	// numbers comparable.
	CheckFrequency time.Duration
	GracePeriod    time.Duration

	Targets         Targets
	DurationBuckets map[string]DurationBucket
	Sampling        Sampling
	FailureTypes    []FailureType
	Escalation      *Escalation
	Budget          map[string]ServiceBudget
	Cooldown        Cooldown
}

// Targets describes the host pool the campaign chooses from and the
// host-pattern dimensions to stratify on.
type Targets struct {
	// Pool is the list of host IDs (matching fleet.toml [[targets.sites]]
	// IDs) the campaign may target.
	Pool []string
	// Patterns is the host-pattern dimension values to stratify on.
	// Each pattern is one of: "single" (one random host), "two_random"
	// (two distinct random hosts), or "all" (every host in the pool).
	// Each declared pattern becomes its own cell in the stratification.
	Patterns []string
}

// HostPattern values for Targets.Patterns.
const (
	HostPatternSingle    = "single"
	HostPatternTwoRandom = "two_random"
	HostPatternAll       = "all"
)

// DurationBucket is one named range that scenarios sample their
// duration from. A campaign declares a few of these (e.g. "brief",
// "medium", "long") and each becomes its own cell in the
// stratification.
type DurationBucket struct {
	Min time.Duration
	Max time.Duration
}

// Sampling controls how many scenario designs each cell receives. The
// default applies to every cell unless that cell's failure type is
// listed in a HighDiscrimination tier.
//
// See ROADMAP.md for the rationale: failure types where service-to-
// service differences are expected to be small (e.g. HTTP 5xx, TLS
// expiration) need more samples to produce defensible percentiles.
type Sampling struct {
	SamplesPerCellDefault int
	HighDiscrimination    []HighDiscrimTier
}

// HighDiscrimTier raises the sample count for a named subset of failure
// types. Multiple tiers may be declared (a "very high" and "moderately
// high" split, say). Every type listed must also appear in
// Campaign.FailureTypes.
type HighDiscrimTier struct {
	FailureTypes   []string
	SamplesPerCell int
}

// FailureType describes one failure type the campaign may sample, plus
// any per-type parameter ranges. Per-type fields are populated only
// when relevant (e.g. StatusCodeChoices applies to http_status only).
type FailureType struct {
	Type string

	// HTTP failure parameters
	StatusCodeChoices []int
	PhaseChoices      []string
	DelayRange        *DurationRange
	ContentChoices    []string

	// KeywordChoices is the pool the generator samples *only* when the
	// chosen content is keyword_injected — i.e. the foreign string the
	// failure injects into the body. For non-injected http_body content
	// (ransomware, defacement, etc.) the scenario keyword falls back to
	// the canary at translate time, since those failures are detected
	// by the canary going missing rather than by a custom keyword.
	KeywordChoices []string

	// TLS failure parameters
	DaysExpiredChoices   []int
	DaysRemainingChoices []int
	VariantChoices       []string
	ReasonChoices        []string

	// Reserved for future failure-type parameters; add fields here as
	// new types come online.
}

// DurationRange is a closed [Min, Max] interval used for randomized
// per-failure parameters that take a duration (e.g. http_timeout
// delay).
type DurationRange struct {
	Min time.Duration
	Max time.Duration
}

// Escalation configures multi-stage scenarios. Optional — the campaign
// has none if [escalation] is omitted.
type Escalation struct {
	Probability     float64       // [0, 1]
	StagesRange     IntRange      // typically {2, 3}
	InterStageRange DurationRange // delay between consecutive stages
	Patterns        []string      // escalation shape choices
}

// Escalation pattern values.
const (
	EscalationPatternLayered     = "layered"
	EscalationPatternReplacement = "replacement"
	EscalationPatternRecovery    = "recovery"
)

// IntRange is a closed [Min, Max] integer interval.
type IntRange struct {
	Min, Max int
}

// ServiceBudget caps how often a campaign may run scenarios that
// exercise a given service. MaxRunsPerHour = 0 means unlimited (used
// for self-hosted services like Jetmon).
type ServiceBudget struct {
	MaxRunsPerHour int
}

// Cooldown spaces out scenarios on the same target so vendor-side
// alert cooldowns don't bias detection latency.
type Cooldown struct {
	PerTargetMinimum time.Duration
}

// ─── Parsing ────────────────────────────────────────────────────────────────

// Parse decodes and validates a campaign from TOML bytes.
func Parse(data []byte) (*Campaign, error) {
	var r raw
	if err := toml.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("campaign: parse: %w", err)
	}
	return validate(r)
}

// raw mirrors the TOML structure for unmarshalling before validation.
// Duration fields are strings here; validate() parses them.
type raw struct {
	ID             string `toml:"id"`
	Description    string `toml:"description"`
	Duration       string `toml:"duration"`
	Seed           int64  `toml:"seed"`
	CheckFrequency string `toml:"check_frequency"`
	GracePeriod    string `toml:"grace_period"`

	Targets         rawTargets                `toml:"targets"`
	DurationBuckets map[string]rawDurationBkt `toml:"duration_buckets"`
	Sampling        rawSampling               `toml:"sampling"`
	FailureTypes    []rawFailureType          `toml:"failure_types"`
	Escalation      *rawEscalation            `toml:"escalation"`
	Budget          map[string]rawBudget      `toml:"budget"`
	Cooldown        rawCooldown               `toml:"cooldown"`
}

type rawTargets struct {
	Pool     []string `toml:"pool"`
	Patterns []string `toml:"patterns"`
}

type rawDurationBkt struct {
	Min string `toml:"min"`
	Max string `toml:"max"`
}

type rawSampling struct {
	SamplesPerCellDefault int                  `toml:"samples_per_cell_default"`
	HighDiscrimination    []rawHighDiscrimTier `toml:"high_discrimination"`
}

type rawHighDiscrimTier struct {
	FailureTypes   []string `toml:"failure_types"`
	SamplesPerCell int      `toml:"samples_per_cell"`
}

type rawFailureType struct {
	Type                 string          `toml:"type"`
	StatusCodeChoices    []int           `toml:"status_code_choices"`
	PhaseChoices         []string        `toml:"phase_choices"`
	DelayRange           *rawDurationRng `toml:"delay_range"`
	ContentChoices       []string        `toml:"content_choices"`
	KeywordChoices       []string        `toml:"keyword_choices"`
	DaysExpiredChoices   []int           `toml:"days_expired_choices"`
	DaysRemainingChoices []int           `toml:"days_remaining_choices"`
	VariantChoices       []string        `toml:"variant_choices"`
	ReasonChoices        []string        `toml:"reason_choices"`
}

type rawDurationRng struct {
	Min string `toml:"min"`
	Max string `toml:"max"`
}

type rawEscalation struct {
	Probability     float64         `toml:"probability"`
	StagesRange     rawIntRange     `toml:"stages_range"`
	InterStageRange *rawDurationRng `toml:"inter_stage_range"`
	Patterns        []string        `toml:"patterns"`
}

type rawIntRange struct {
	Min int `toml:"min"`
	Max int `toml:"max"`
}

type rawBudget struct {
	MaxRunsPerHour int `toml:"max_runs_per_hour"`
}

type rawCooldown struct {
	PerTargetMinimum string `toml:"per_target_minimum"`
}

// ─── Validation ─────────────────────────────────────────────────────────────

func validate(r raw) (*Campaign, error) {
	c := &Campaign{
		ID:          r.ID,
		Description: r.Description,
		Seed:        r.Seed,
	}

	if r.ID == "" {
		return nil, fmt.Errorf("campaign: id is required")
	}

	dur, err := parseDuration("duration", r.Duration, true)
	if err != nil {
		return nil, err
	}
	if dur <= 0 {
		return nil, fmt.Errorf("campaign: duration must be positive")
	}
	c.Duration = dur

	cf, err := parseDuration("check_frequency", r.CheckFrequency, true)
	if err != nil {
		return nil, err
	}
	if cf <= 0 {
		return nil, fmt.Errorf("campaign: check_frequency must be positive")
	}
	c.CheckFrequency = cf

	// grace_period defaults to 3 minutes (matches the scenario library)
	// but is overridable per campaign.
	gp, err := parseDuration("grace_period", r.GracePeriod, false)
	if err != nil {
		return nil, err
	}
	if gp == 0 {
		gp = 3 * time.Minute
	}
	if gp < 0 {
		return nil, fmt.Errorf("campaign: grace_period must be non-negative")
	}
	c.GracePeriod = gp

	if err := validateTargets(r.Targets, &c.Targets); err != nil {
		return nil, err
	}
	if err := validateDurationBuckets(r.DurationBuckets, c); err != nil {
		return nil, err
	}
	if err := validateFailureTypes(r.FailureTypes, c); err != nil {
		return nil, err
	}
	if err := validateSampling(r.Sampling, c); err != nil {
		return nil, err
	}
	if r.Escalation != nil {
		esc, err := validateEscalation(*r.Escalation)
		if err != nil {
			return nil, err
		}
		c.Escalation = esc
	}
	if err := validateBudget(r.Budget, c); err != nil {
		return nil, err
	}
	if err := validateCooldown(r.Cooldown, c); err != nil {
		return nil, err
	}

	return c, nil
}

func validateTargets(rt rawTargets, out *Targets) error {
	if len(rt.Pool) == 0 {
		return fmt.Errorf("campaign: targets.pool must list at least one host")
	}
	if len(rt.Patterns) == 0 {
		return fmt.Errorf("campaign: targets.patterns must list at least one pattern")
	}
	for _, p := range rt.Patterns {
		switch p {
		case HostPatternSingle, HostPatternTwoRandom, HostPatternAll:
		default:
			return fmt.Errorf("campaign: targets.patterns: unknown pattern %q (allowed: %q, %q, %q)",
				p, HostPatternSingle, HostPatternTwoRandom, HostPatternAll)
		}
	}
	if containsString(rt.Patterns, HostPatternTwoRandom) && len(rt.Pool) < 2 {
		return fmt.Errorf("campaign: targets.patterns includes %q but pool has only %d host(s) (need ≥ 2)",
			HostPatternTwoRandom, len(rt.Pool))
	}
	out.Pool = rt.Pool
	out.Patterns = rt.Patterns
	return nil
}

func validateDurationBuckets(rb map[string]rawDurationBkt, c *Campaign) error {
	if len(rb) == 0 {
		return fmt.Errorf("campaign: duration_buckets must declare at least one bucket")
	}
	c.DurationBuckets = make(map[string]DurationBucket, len(rb))
	for name, bk := range rb {
		minD, err := parseDuration(fmt.Sprintf("duration_buckets.%s.min", name), bk.Min, true)
		if err != nil {
			return err
		}
		maxD, err := parseDuration(fmt.Sprintf("duration_buckets.%s.max", name), bk.Max, true)
		if err != nil {
			return err
		}
		if minD <= 0 {
			return fmt.Errorf("campaign: duration_buckets.%s.min must be positive", name)
		}
		if maxD < minD {
			return fmt.Errorf("campaign: duration_buckets.%s: max (%v) must be ≥ min (%v)", name, maxD, minD)
		}
		c.DurationBuckets[name] = DurationBucket{Min: minD, Max: maxD}
	}
	return nil
}

func validateFailureTypes(rfs []rawFailureType, c *Campaign) error {
	if len(rfs) == 0 {
		return fmt.Errorf("campaign: at least one [[failure_types]] block is required")
	}
	seen := make(map[string]bool, len(rfs))
	c.FailureTypes = make([]FailureType, 0, len(rfs))
	for i, rf := range rfs {
		if rf.Type == "" {
			return fmt.Errorf("campaign: failure_types[%d].type is required", i)
		}
		if seen[rf.Type] {
			return fmt.Errorf("campaign: failure_types[%d]: duplicate type %q", i, rf.Type)
		}
		seen[rf.Type] = true

		ft := FailureType{
			Type:                 rf.Type,
			StatusCodeChoices:    rf.StatusCodeChoices,
			PhaseChoices:         rf.PhaseChoices,
			ContentChoices:       rf.ContentChoices,
			KeywordChoices:       rf.KeywordChoices,
			DaysExpiredChoices:   rf.DaysExpiredChoices,
			DaysRemainingChoices: rf.DaysRemainingChoices,
			VariantChoices:       rf.VariantChoices,
			ReasonChoices:        rf.ReasonChoices,
		}
		for _, code := range rf.StatusCodeChoices {
			if code < 100 || code > 599 {
				return fmt.Errorf("campaign: failure_types[%d] (%s): status_code_choices contains invalid code %d", i, rf.Type, code)
			}
		}
		for _, days := range rf.DaysExpiredChoices {
			if days <= 0 {
				return fmt.Errorf("campaign: failure_types[%d] (%s): days_expired_choices must be positive (got %d)", i, rf.Type, days)
			}
		}
		for _, days := range rf.DaysRemainingChoices {
			if days <= 0 {
				return fmt.Errorf("campaign: failure_types[%d] (%s): days_remaining_choices must be positive (got %d)", i, rf.Type, days)
			}
		}
		for _, variant := range rf.VariantChoices {
			if err := validateVariantChoice(rf.Type, variant); err != nil {
				return fmt.Errorf("campaign: failure_types[%d] (%s): %w", i, rf.Type, err)
			}
		}
		contentNeedsKeyword := false
		for _, content := range rf.ContentChoices {
			if err := validateContentChoice(rf.Type, content); err != nil {
				return fmt.Errorf("campaign: failure_types[%d] (%s): %w", i, rf.Type, err)
			}
			if content == "keyword_injected" {
				contentNeedsKeyword = true
			}
		}
		for _, keyword := range rf.KeywordChoices {
			if keyword == "" {
				return fmt.Errorf("campaign: failure_types[%d] (%s): keyword_choices must not contain empty strings", i, rf.Type)
			}
		}
		if rf.Type == "http_body" && contentNeedsKeyword && len(rf.KeywordChoices) == 0 {
			return fmt.Errorf("campaign: failure_types[%d] (%s): keyword_choices is required when content_choices includes keyword_injected", i, rf.Type)
		}
		for _, reason := range rf.ReasonChoices {
			if err := validateReasonChoice(rf.Type, reason); err != nil {
				return fmt.Errorf("campaign: failure_types[%d] (%s): %w", i, rf.Type, err)
			}
		}
		if rf.DelayRange != nil {
			dr, err := validateDurationRange(
				fmt.Sprintf("failure_types[%d].delay_range", i), *rf.DelayRange)
			if err != nil {
				return err
			}
			ft.DelayRange = dr
		}
		c.FailureTypes = append(c.FailureTypes, ft)
	}
	return nil
}

// The variant/content/reason allowlists below duplicate the switches
// in scenario.validateFailure; campaign needs them up front so user
// TOML errors surface at parse time rather than at translation time.
// Drift between the two is caught by TestVariantContract_* in
// variant_pinning_test.go. If these lists grow past ~5 values per
// type, consider extracting them to a scenario.AllowedVariants
// helper and importing from there.
func validateVariantChoice(failureType, variant string) error {
	switch failureType {
	case "http_redirect":
		switch variant {
		case "loop", "chain":
			return nil
		}
		return fmt.Errorf("variant_choices contains invalid http_redirect variant %q", variant)
	case "tls_invalid":
		switch variant {
		case "self_signed", "hostname_mismatch":
			return nil
		}
		return fmt.Errorf("variant_choices contains invalid tls_invalid variant %q", variant)
	case "tls_deprecated":
		switch variant {
		case "TLS10", "TLS11":
			return nil
		}
		return fmt.Errorf("variant_choices contains invalid tls_deprecated variant %q", variant)
	default:
		return nil
	}
}

func validateContentChoice(failureType, content string) error {
	if failureType != "http_body" {
		return nil
	}
	switch content {
	case "empty", "error_page", "keyword_missing", "keyword_injected",
		"ransomware", "defacement", "malicious_script", "spam_links":
		return nil
	default:
		return fmt.Errorf("content_choices contains invalid http_body content %q", content)
	}
}

func validateReasonChoice(failureType, reason string) error {
	switch failureType {
	case "tls_handshake":
		switch reason {
		case "version_mismatch", "no_common_cipher":
			return nil
		}
		return fmt.Errorf("reason_choices contains invalid tls_handshake reason %q", reason)
	default:
		return nil
	}
}

func validateSampling(rs rawSampling, c *Campaign) error {
	if rs.SamplesPerCellDefault <= 0 {
		return fmt.Errorf("campaign: sampling.samples_per_cell_default must be positive")
	}
	c.Sampling.SamplesPerCellDefault = rs.SamplesPerCellDefault

	declared := make(map[string]bool, len(c.FailureTypes))
	for _, ft := range c.FailureTypes {
		declared[ft.Type] = true
	}

	c.Sampling.HighDiscrimination = make([]HighDiscrimTier, 0, len(rs.HighDiscrimination))
	for i, t := range rs.HighDiscrimination {
		if t.SamplesPerCell <= 0 {
			return fmt.Errorf("campaign: sampling.high_discrimination[%d].samples_per_cell must be positive", i)
		}
		if len(t.FailureTypes) == 0 {
			return fmt.Errorf("campaign: sampling.high_discrimination[%d].failure_types must list at least one type", i)
		}
		for _, name := range t.FailureTypes {
			if !declared[name] {
				return fmt.Errorf("campaign: sampling.high_discrimination[%d]: failure_type %q is not declared in [[failure_types]]", i, name)
			}
		}
		c.Sampling.HighDiscrimination = append(c.Sampling.HighDiscrimination, HighDiscrimTier{
			FailureTypes:   t.FailureTypes,
			SamplesPerCell: t.SamplesPerCell,
		})
	}
	return nil
}

func validateEscalation(re rawEscalation) (*Escalation, error) {
	if re.Probability < 0 || re.Probability > 1 {
		return nil, fmt.Errorf("campaign: escalation.probability must be in [0, 1] (got %v)", re.Probability)
	}
	if re.StagesRange.Min < 2 {
		return nil, fmt.Errorf("campaign: escalation.stages_range.min must be ≥ 2 (got %d)", re.StagesRange.Min)
	}
	if re.StagesRange.Max < re.StagesRange.Min {
		return nil, fmt.Errorf("campaign: escalation.stages_range: max (%d) must be ≥ min (%d)", re.StagesRange.Max, re.StagesRange.Min)
	}
	if re.InterStageRange == nil {
		return nil, fmt.Errorf("campaign: escalation.inter_stage_range is required when [escalation] is declared")
	}
	dr, err := validateDurationRange("escalation.inter_stage_range", *re.InterStageRange)
	if err != nil {
		return nil, err
	}
	patterns := re.Patterns
	if len(patterns) == 0 {
		patterns = []string{EscalationPatternLayered}
	}
	seen := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		switch p {
		case EscalationPatternLayered, EscalationPatternReplacement, EscalationPatternRecovery:
		default:
			return nil, fmt.Errorf("campaign: escalation.patterns contains unknown pattern %q (allowed: %q, %q, %q)",
				p, EscalationPatternLayered, EscalationPatternReplacement, EscalationPatternRecovery)
		}
		if seen[p] {
			return nil, fmt.Errorf("campaign: escalation.patterns contains duplicate pattern %q", p)
		}
		seen[p] = true
	}
	return &Escalation{
		Probability:     re.Probability,
		StagesRange:     IntRange{Min: re.StagesRange.Min, Max: re.StagesRange.Max},
		InterStageRange: *dr,
		Patterns:        patterns,
	}, nil
}

func validateBudget(rb map[string]rawBudget, c *Campaign) error {
	c.Budget = make(map[string]ServiceBudget, len(rb))
	for svc, b := range rb {
		if b.MaxRunsPerHour < 0 {
			return fmt.Errorf("campaign: budget.%s.max_runs_per_hour must be ≥ 0 (0 = unlimited)", svc)
		}
		c.Budget[svc] = ServiceBudget{MaxRunsPerHour: b.MaxRunsPerHour}
	}
	return nil
}

func validateCooldown(rc rawCooldown, c *Campaign) error {
	cd, err := parseDuration("cooldown.per_target_minimum", rc.PerTargetMinimum, false)
	if err != nil {
		return err
	}
	if cd < 0 {
		return fmt.Errorf("campaign: cooldown.per_target_minimum must be non-negative")
	}
	c.Cooldown.PerTargetMinimum = cd
	return nil
}

func validateDurationRange(field string, rd rawDurationRng) (*DurationRange, error) {
	minD, err := parseDuration(field+".min", rd.Min, true)
	if err != nil {
		return nil, err
	}
	maxD, err := parseDuration(field+".max", rd.Max, true)
	if err != nil {
		return nil, err
	}
	if minD <= 0 {
		return nil, fmt.Errorf("campaign: %s.min must be positive", field)
	}
	if maxD < minD {
		return nil, fmt.Errorf("campaign: %s: max (%v) must be ≥ min (%v)", field, maxD, minD)
	}
	return &DurationRange{Min: minD, Max: maxD}, nil
}

// parseDuration parses a TOML duration string. If required is true,
// empty input is an error; otherwise empty returns (0, nil). Mirrors
// the helper in internal/scenario.
func parseDuration(field, s string, required bool) (time.Duration, error) {
	if s == "" {
		if required {
			return 0, fmt.Errorf("campaign: %s is required", field)
		}
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("campaign: %s: invalid duration %q: %w", field, s, err)
	}
	return d, nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
