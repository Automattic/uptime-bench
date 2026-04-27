package campaign

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"sort"
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
	Stages []EscalationStage
}

// EscalationStage is one segment of an escalating failure. The
// generator currently models the "layered" pattern (stage 2 starts
// while stage 1 is still active). The "replacement" pattern (stage 2
// ends stage 1) is flagged in the spec as an open scenario-format
// question and is not implemented yet — see ROADMAP.md.
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

	plan.Schedule = generateSchedule(plan.Designs, c.Duration, masterSeed)
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
	return p
}

// pickEscalation builds a multi-stage escalation. Currently models the
// "layered" pattern only — each stage starts at the previous stage's
// activation + a sampled inter-stage delay, and runs for the rest of
// the scenario. The "replacement" pattern (stage 2 ends stage 1) needs
// scenario-format changes flagged in the spec; not implemented yet.
func pickEscalation(r *rand.Rand, c *Campaign, baseFailureType string, scenarioDuration time.Duration) *EscalationDesign {
	stages := c.Escalation.StagesRange.Min
	if c.Escalation.StagesRange.Max > c.Escalation.StagesRange.Min {
		stages += r.IntN(c.Escalation.StagesRange.Max - c.Escalation.StagesRange.Min + 1)
	}

	out := &EscalationDesign{Stages: make([]EscalationStage, 0, stages)}
	offset := time.Duration(0)
	for i := 0; i < stages; i++ {
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
			// Layered: each stage runs from its offset to scenario end.
			Duration: scenarioDuration - offset,
		})

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
	return out
}

// generateSchedule lays out replays evenly across the campaign duration.
// Distribution is uniform with deterministic jitter; per-target cooldown
// and hour-of-day spread are runner-level concerns (the runner can skip
// or defer slots that violate constraints, since cooldown enforcement
// at scheduling time would require knowing the per-replay scenario
// runtime, which depends on adapter behaviour the generator can't see).
func generateSchedule(designs []Design, campaignDuration time.Duration, masterSeed int64) []ReplaySlot {
	total := 0
	for _, d := range designs {
		total += d.Replays
	}
	if total == 0 {
		return nil
	}

	r := newRand(deriveSeed(masterSeed, "schedule", 0))

	// Build a flat list of (design index, replay index) pairs and shuffle
	// it. Each pair gets one slot in the time grid.
	type pair struct{ design, replay int }
	pairs := make([]pair, 0, total)
	for di, d := range designs {
		for ri := 0; ri < d.Replays; ri++ {
			pairs = append(pairs, pair{design: di, replay: ri})
		}
	}
	// Fisher-Yates shuffle for deterministic randomization.
	for i := len(pairs) - 1; i > 0; i-- {
		j := r.IntN(i + 1)
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}

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
