package campaign

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"
)

// Plan is the deterministic output of Generate. The runner consumes
// this to drive a campaign: walk Schedule in order, look up the
// referenced Design, run one scenario per replay.
//
// Plan is fully derivable from (Campaign, masterSeed). Same inputs →
// same Plan, byte-for-byte.
type Plan struct {
	Designs  []Design
	Schedule []ReplaySlot
}

// Design is one fully-specified scenario shape that gets replayed
// `Replays` times during the campaign. There is exactly one Design
// per cell — parameter randomization happens once at design time
// (e.g. status_code chosen from [503, 502, 504]); replays use the
// same Design.
type Design struct {
	ID      string // stable, e.g. "d-0007"
	Cell    Cell
	Replays int // how many times this design runs in the campaign

	// Targets is the host IDs this design's failures hit. Length
	// matches the host pattern: 1 for "single", 2 for "two_random",
	// len(pool) for "all".
	Targets []string

	FailureType string

	// Duration is the per-replay scenario duration, sampled uniformly
	// from the cell's duration bucket at design time.
	Duration time.Duration

	// Params carries failure-type-specific values picked at design
	// time (e.g. {"status_code": 503, "phase": "ttfb"}).
	Params map[string]any

	// Escalation, when non-nil, means this design tests a multi-stage
	// failure scenario.
	Escalation *EscalationDesign

	// Seed is the per-design seed derived from the master seed; the
	// runner passes it through so any per-replay randomness (e.g. rate
	// sampling inside a failure) stays reproducible.
	Seed int64
}

// ReportLabel returns the failure bucket label reports should use for
// this design. Non-escalating designs keep the plain failure type; a
// multi-stage escalation includes the pattern and stage order so report
// rows do not collapse distinct campaign shapes into an ambiguous
// sorted failure-type set.
func (d Design) ReportLabel() string {
	if d.Escalation == nil || len(d.Escalation.Stages) == 0 {
		return d.FailureType
	}
	types := make([]string, 0, len(d.Escalation.Stages))
	for _, stage := range d.Escalation.Stages {
		types = append(types, stage.FailureType)
	}
	pattern := d.Escalation.Pattern
	if pattern == "" {
		pattern = "escalation"
	}
	return pattern + ":" + strings.Join(types, ">")
}

// HasMixedContentEscalation reports whether a design mixes keyword_injected
// http_body stages with other http_body content stages. The scenario format has
// one scenario-level keyword configuration, so these designs can under-measure
// the non-injected content stage until per-stage keyword config exists.
func (d Design) HasMixedContentEscalation() bool {
	if d.Escalation == nil {
		return false
	}
	hasInjected := false
	hasOtherBodyContent := false
	for _, stage := range d.Escalation.Stages {
		if stage.FailureType != "http_body" {
			continue
		}
		content, _ := stage.Params["content"].(string)
		switch content {
		case "keyword_injected":
			hasInjected = true
		case "":
			// Empty content is malformed for http_body and will fail later.
		default:
			hasOtherBodyContent = true
		}
	}
	return hasInjected && hasOtherBodyContent
}

// Cell uniquely identifies the stratification position a Design samples.
// Two Designs with the same Cell tuple represent oversampling of the
// same statistical bucket — the generator never produces this.
type Cell struct {
	FailureType    string
	DurationBucket string // bucket name from Campaign.DurationBuckets
	HostPattern    string // HostPatternSingle / TwoRandom / All
}

// EscalationDesign describes a multi-stage failure within a single
// scenario. Stages run in order; each Stage carries an Offset relative
// to scenario start and its own Duration.
type EscalationDesign struct {
	Pattern string
	Stages  []EscalationStage
}

// EscalationStage is one segment of an escalating failure. Pattern
// controls how stage windows relate: layered stages overlap,
// replacement stages hand off at the next stage start, and recovery
// stages leave a quiet gap before the next stage.
type EscalationStage struct {
	FailureType string
	Params      map[string]any
	Offset      time.Duration
	Duration    time.Duration
}

// ReplaySlot is one scheduled execution of a Design. The runner walks
// these in Offset order; Offset is relative to campaign start so the
// generator stays clock-free.
type ReplaySlot struct {
	DesignID string
	Index    int           // 0..Replays-1 for this design
	Offset   time.Duration // from campaign start
}

// Generate produces a deterministic Plan for the given Campaign.
//
// "Deterministic" means: with the same Campaign config and the same
// masterSeed, two calls produce byte-identical output. The function
// does no I/O and never reads the wall clock. All randomness flows
// from masterSeed.
//
// Returns an error if the campaign produces zero cells (no failure
// types × duration buckets × host patterns) or zero total replays.
// Validation that the Campaign itself is well-formed is the parser's
// job (see Parse); Generate trusts what it's given.
func Generate(c *Campaign, masterSeed int64) (*Plan, error) {
	if c == nil {
		return nil, fmt.Errorf("campaign: Generate: nil campaign")
	}
	cells := enumerateCells(c)
	if len(cells) == 0 {
		return nil, fmt.Errorf("campaign: Generate: no cells produced (empty failure_types, duration_buckets, or patterns)")
	}

	// Resolve per-cell sample count by walking the high-discrimination
	// tiers. First-match wins, so multiple tiers covering the same
	// failure_type take the count from whichever appears first.
	highDiscrim := make(map[string]int)
	for _, tier := range c.Sampling.HighDiscrimination {
		for _, ft := range tier.FailureTypes {
			if _, already := highDiscrim[ft]; !already {
				highDiscrim[ft] = tier.SamplesPerCell
			}
		}
	}

	plan := &Plan{
		Designs: make([]Design, 0, len(cells)),
	}

	totalReplays := 0
	for i, cell := range cells {
		samples := c.Sampling.SamplesPerCellDefault
		if n, ok := highDiscrim[cell.FailureType]; ok {
			samples = n
		}
		if samples <= 0 {
			continue
		}

		designSeed := deriveSeed(masterSeed, "design", i)
		ftConfig := failureTypeByName(c, cell.FailureType)
		design := generateDesign(c, cell, samples, ftConfig, designSeed, i)
		plan.Designs = append(plan.Designs, design)
		totalReplays += samples
	}

	if totalReplays == 0 {
		return nil, fmt.Errorf("campaign: Generate: every cell resolved to zero samples")
	}

	schedule, err := generateSchedule(plan.Designs, c.Duration, c.Cooldown.PerTargetMinimum, masterSeed)
	if err != nil {
		return nil, err
	}
	plan.Schedule = schedule
	return plan, nil
}

// enumerateCells produces the cross-product of failure types × duration
// buckets × host patterns, in a stable order so the same campaign config
// always produces the same cell list.
//
// Ordering: failure_types in declared order, duration_buckets in
// alphabetical order (Go map iteration is randomized otherwise), host
// patterns in declared order. Stability matters because Cell positions
// feed into per-design seeds.
func enumerateCells(c *Campaign) []Cell {
	bucketNames := make([]string, 0, len(c.DurationBuckets))
	for name := range c.DurationBuckets {
		bucketNames = append(bucketNames, name)
	}
	sort.Strings(bucketNames)

	cells := make([]Cell, 0, len(c.FailureTypes)*len(bucketNames)*len(c.Targets.Patterns))
	for _, ft := range c.FailureTypes {
		for _, b := range bucketNames {
			for _, p := range c.Targets.Patterns {
				cells = append(cells, Cell{
					FailureType:    ft.Type,
					DurationBucket: b,
					HostPattern:    p,
				})
			}
		}
	}
	return cells
}

// generateDesign builds one Design for the given cell, pulling all
// random choices from a per-design PRNG so the design is a pure
// function of (campaign, cell, designIndex, masterSeed).
func generateDesign(c *Campaign, cell Cell, samples int, ftConfig *FailureType, seed int64, idx int) Design {
	r := newRand(seed)

	bucket := c.DurationBuckets[cell.DurationBucket]
	dur := durationIn(r, bucket.Min, bucket.Max)

	targets := pickTargets(r, c.Targets.Pool, cell.HostPattern)

	params := pickFailureParams(r, ftConfig)

	d := Design{
		ID:          fmt.Sprintf("d-%04d", idx),
		Cell:        cell,
		Replays:     samples,
		Targets:     targets,
		FailureType: cell.FailureType,
		Duration:    dur,
		Params:      params,
		Seed:        seed,
	}

	if c.Escalation != nil && r.Float64() < c.Escalation.Probability {
		d.Escalation = pickEscalation(r, c, cell.FailureType, dur)
	}

	return d
}

// pickTargets selects host IDs from the pool matching the host pattern.
// "single" → one random host; "two_random" → two distinct random hosts;
// "all" → every host in declared order.
func pickTargets(r *rand.Rand, pool []string, pattern string) []string {
	switch pattern {
	case HostPatternSingle:
		return []string{pool[r.IntN(len(pool))]}
	case HostPatternTwoRandom:
		// Pick two distinct indices: pick j from [0, n-1], then if
		// j >= i, shift it up by 1 to skip i.
		i := r.IntN(len(pool))
		j := r.IntN(len(pool) - 1)
		if j >= i {
			j++
		}
		return []string{pool[i], pool[j]}
	case HostPatternAll:
		out := make([]string, len(pool))
		copy(out, pool)
		return out
	}
	// Validator forbids unknown patterns; unreachable on well-formed input.
	return nil
}

// pickFailureParams samples per-failure-type parameters from the
// configured choices. Returns map[string]any so values can be plugged
// directly into a scenario.Failure later.
func pickFailureParams(r *rand.Rand, ft *FailureType) map[string]any {
	if ft == nil {
		return map[string]any{}
	}
	p := map[string]any{}
	if len(ft.StatusCodeChoices) > 0 {
		p["status_code"] = ft.StatusCodeChoices[r.IntN(len(ft.StatusCodeChoices))]
	}
	if len(ft.PhaseChoices) > 0 {
		p["phase"] = ft.PhaseChoices[r.IntN(len(ft.PhaseChoices))]
	}
	if ft.DelayRange != nil {
		p["delay"] = durationIn(r, ft.DelayRange.Min, ft.DelayRange.Max)
	}
	content := ""
	if len(ft.ContentChoices) > 0 {
		content = ft.ContentChoices[r.IntN(len(ft.ContentChoices))]
		p["content"] = content
	}
	// keyword applies only to the keyword_injected content variant —
	// it is the foreign string injected into the body, which the
	// monitor must then notice. For other http_body content (ransomware,
	// defacement, …) the scenario keyword stays the canary so that
	// healthy-state present-checks pass; the translator handles that
	// canary fallback when no keyword is set in params.
	if content == "keyword_injected" && len(ft.KeywordChoices) > 0 {
		p["keyword"] = ft.KeywordChoices[r.IntN(len(ft.KeywordChoices))]
	}
	if len(ft.DaysExpiredChoices) > 0 {
		p["days_expired"] = ft.DaysExpiredChoices[r.IntN(len(ft.DaysExpiredChoices))]
	}
	if len(ft.DaysRemainingChoices) > 0 {
		p["days_remaining"] = ft.DaysRemainingChoices[r.IntN(len(ft.DaysRemainingChoices))]
	}
	if len(ft.VariantChoices) > 0 {
		p["variant"] = ft.VariantChoices[r.IntN(len(ft.VariantChoices))]
	}
	if len(ft.ReasonChoices) > 0 {
		p["reason"] = ft.ReasonChoices[r.IntN(len(ft.ReasonChoices))]
	}
	return p
}

// pickEscalation builds a multi-stage escalation. Each stage starts at the
// previous stage's activation plus a sampled inter-stage delay. The selected
// pattern decides how long each stage remains active.
func pickEscalation(r *rand.Rand, c *Campaign, baseFailureType string, scenarioDuration time.Duration) *EscalationDesign {
	stages := c.Escalation.StagesRange.Min
	if c.Escalation.StagesRange.Max > c.Escalation.StagesRange.Min {
		stages += r.IntN(c.Escalation.StagesRange.Max - c.Escalation.StagesRange.Min + 1)
	}

	pattern := EscalationPatternLayered
	if len(c.Escalation.Patterns) > 0 {
		pattern = c.Escalation.Patterns[r.IntN(len(c.Escalation.Patterns))]
	}

	offsets := make([]time.Duration, 0, stages)
	offset := time.Duration(0)
	for i := 0; i < stages; i++ {
		offsets = append(offsets, offset)
		if i < stages-1 {
			offset += durationIn(r, c.Escalation.InterStageRange.Min, c.Escalation.InterStageRange.Max)
			// Don't push past scenario end; truncate the remaining
			// schedule rather than producing a stage that starts after
			// the scenario ends.
			if offset >= scenarioDuration {
				break
			}
		}
	}

	out := &EscalationDesign{
		Pattern: pattern,
		Stages:  make([]EscalationStage, 0, len(offsets)),
	}
	for i, offset := range offsets {
		// Stage 1 reuses the base failure type so the scenario's
		// primary failure is always present from t=0; later stages
		// pick another type from the campaign's failure_types.
		ftName := baseFailureType
		if i > 0 {
			ftName = c.FailureTypes[r.IntN(len(c.FailureTypes))].Type
		}
		ft := failureTypeByName(c, ftName)
		params := pickFailureParams(r, ft)

		out.Stages = append(out.Stages, EscalationStage{
			FailureType: ftName,
			Params:      params,
			Offset:      offset,
			Duration:    escalationStageDuration(pattern, offsets, i, scenarioDuration),
		})
	}
	return out
}

func escalationStageDuration(pattern string, offsets []time.Duration, i int, scenarioDuration time.Duration) time.Duration {
	remaining := scenarioDuration - offsets[i]
	switch pattern {
	case EscalationPatternReplacement:
		if i+1 < len(offsets) {
			return offsets[i+1] - offsets[i]
		}
		return remaining
	case EscalationPatternRecovery:
		if i+1 < len(offsets) {
			return halfPositive(offsets[i+1] - offsets[i])
		}
		return halfPositive(remaining)
	default:
		return remaining
	}
}

func halfPositive(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	return d / 2
}

// replayPair is the generator's internal representation of one requested
// replay before it has a campaign offset.
type replayPair struct {
	design int
	replay int
}

// generateSchedule lays out replays across the campaign duration. It first
// tries the original uniform jittered grid. If that would violate the
// campaign's per-target cooldown, it falls back to a cooldown-aware layout
// that keeps every target's planned starts at least cooldown apart.
func generateSchedule(designs []Design, campaignDuration, cooldown time.Duration, masterSeed int64) ([]ReplaySlot, error) {
	total := 0
	for _, d := range designs {
		total += d.Replays
	}
	if total == 0 {
		return nil, nil
	}

	r := newRand(deriveSeed(masterSeed, "schedule", 0))

	// Build a flat list of (design index, replay index) pairs and shuffle
	// it. Each pair gets one slot in the time grid.
	pairs := make([]replayPair, 0, total)
	for di, d := range designs {
		for ri := 0; ri < d.Replays; ri++ {
			pairs = append(pairs, replayPair{design: di, replay: ri})
		}
	}
	// Fisher-Yates shuffle for deterministic randomization.
	for i := len(pairs) - 1; i > 0; i-- {
		j := r.IntN(i + 1)
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}

	slots := generateUniformSchedule(designs, pairs, campaignDuration, r)
	if cooldown <= 0 || scheduleRespectsTargetCooldown(slots, designsByID(designs), cooldown) {
		return slots, nil
	}
	return generateCooldownSchedule(designs, pairs, campaignDuration, cooldown)
}

func generateUniformSchedule(designs []Design, pairs []replayPair, campaignDuration time.Duration, r *rand.Rand) []ReplaySlot {
	total := len(pairs)
	slot := campaignDuration / time.Duration(total)
	slots := make([]ReplaySlot, len(pairs))
	for i, p := range pairs {
		base := time.Duration(i) * slot
		// Jitter within the slot: ±25% of slot width, clamped to keep
		// the offset within [0, campaignDuration).
		jitter := time.Duration(int64(float64(slot) * (r.Float64()*0.5 - 0.25)))
		offset := base + jitter
		if offset < 0 {
			offset = 0
		}
		if offset >= campaignDuration {
			offset = campaignDuration - time.Millisecond
		}
		slots[i] = ReplaySlot{
			DesignID: designs[p.design].ID,
			Index:    p.replay,
			Offset:   offset,
		}
	}
	// Sort by Offset so the runner can walk the schedule in order.
	sort.Slice(slots, func(i, j int) bool {
		return slots[i].Offset < slots[j].Offset
	})
	return slots
}

func generateCooldownSchedule(designs []Design, pairs []replayPair, campaignDuration, cooldown time.Duration) ([]ReplaySlot, error) {
	targetReplayCounts := make(map[string]int)
	for _, p := range pairs {
		for _, target := range designs[p.design].Targets {
			targetReplayCounts[target]++
		}
	}
	for target, count := range targetReplayCounts {
		requiredSpan := time.Duration(count-1) * cooldown
		if requiredSpan >= campaignDuration {
			return nil, fmt.Errorf("campaign: schedule infeasible: target %q has %d replays requiring %v spacing, which does not fit within %v",
				target, count, cooldown, campaignDuration)
		}
	}

	targetPositions := make(map[string]int, len(targetReplayCounts))
	slots := make([]ReplaySlot, len(pairs))
	var latest time.Duration
	for i, p := range pairs {
		targets := designs[p.design].Targets
		var offset time.Duration
		for _, target := range targets {
			candidate := time.Duration(targetPositions[target]) * cooldown
			if candidate > offset {
				offset = candidate
			}
		}
		for _, target := range targets {
			targetPositions[target]++
		}
		if offset > latest {
			latest = offset
		}
		slots[i] = ReplaySlot{
			DesignID: designs[p.design].ID,
			Index:    p.replay,
			Offset:   offset,
		}
	}

	if latest > 0 {
		scaleTo := campaignDuration - time.Nanosecond
		if scaleTo > latest {
			scale := float64(scaleTo) / float64(latest)
			for i := range slots {
				slots[i].Offset = time.Duration(float64(slots[i].Offset) * scale)
			}
		}
	}

	sort.SliceStable(slots, func(i, j int) bool {
		return slots[i].Offset < slots[j].Offset
	})
	if !scheduleRespectsTargetCooldown(slots, designsByID(designs), cooldown) {
		return nil, fmt.Errorf("campaign: internal error: generated schedule violates per-target cooldown %v", cooldown)
	}
	return slots, nil
}

func scheduleRespectsTargetCooldown(slots []ReplaySlot, designs map[string]Design, cooldown time.Duration) bool {
	lastByTarget := make(map[string]time.Duration)
	seenByTarget := make(map[string]bool)
	for _, slot := range slots {
		design, ok := designs[slot.DesignID]
		if !ok {
			continue
		}
		for _, target := range design.Targets {
			if seenByTarget[target] && slot.Offset-lastByTarget[target] < cooldown {
				return false
			}
			lastByTarget[target] = slot.Offset
			seenByTarget[target] = true
		}
	}
	return true
}

func designsByID(designs []Design) map[string]Design {
	out := make(map[string]Design, len(designs))
	for _, d := range designs {
		out[d.ID] = d
	}
	return out
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// failureTypeByName returns the FailureType config for the named type,
// or nil if not found. Validator guarantees existence for declared
// types; nil is the caller's signal to use empty params.
func failureTypeByName(c *Campaign, name string) *FailureType {
	for i := range c.FailureTypes {
		if c.FailureTypes[i].Type == name {
			return &c.FailureTypes[i]
		}
	}
	return nil
}

// durationIn returns a uniformly-random duration in [min, max].
func durationIn(r *rand.Rand, min, max time.Duration) time.Duration {
	if max <= min {
		return min
	}
	span := int64(max - min)
	return min + time.Duration(r.Int64N(span+1))
}

// deriveSeed produces a stable per-component seed from the master seed
// and a label. Using SHA-256 instead of XOR keeps independent
// per-component PRNGs from correlating when label/index pairs collide
// across components (e.g. ("design", 0) vs ("schedule", 0)).
func deriveSeed(master int64, label string, index int) int64 {
	var buf [16]byte
	binary.LittleEndian.PutUint64(buf[0:8], uint64(master))
	binary.LittleEndian.PutUint64(buf[8:16], uint64(index))
	h := sha256.New()
	_, _ = h.Write([]byte(label))
	_, _ = h.Write(buf[:])
	sum := h.Sum(nil)
	return int64(binary.LittleEndian.Uint64(sum[:8]))
}

// newRand creates a math/rand/v2 PCG generator from an int64 seed.
// math/rand/v2 expects two uint64s; we hash the seed to a 256-bit
// digest and use the first two uint64 halves so adjacent seeds don't
// produce correlated streams.
func newRand(seed int64) *rand.Rand {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", seed)))
	a := binary.LittleEndian.Uint64(h[0:8])
	b := binary.LittleEndian.Uint64(h[8:16])
	return rand.New(rand.NewPCG(a, b))
}
