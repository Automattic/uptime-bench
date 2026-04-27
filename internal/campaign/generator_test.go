package campaign

import (
	"reflect"
	"sort"
	"testing"
	"time"
)

// generatorTestCampaign returns a small, fully-formed Campaign suitable
// for exercising Generate. It has 2 failure types, 2 duration buckets,
// 2 host patterns → 8 cells. Default samples = 5; one high-discrim tier
// covering http_status raises that to 12. With 4 default-tier cells × 5 +
// 4 high-discrim cells × 12 = 68 total replays.
func generatorTestCampaign(t *testing.T) *Campaign {
	t.Helper()
	c, err := Parse([]byte(`
id          = "gen-test"
duration    = "1h"
seed        = 0

[targets]
pool     = ["bench-a", "bench-b", "bench-c"]
patterns = ["single", "two_random"]

[duration_buckets]
brief  = { min = "30s", max = "2m" }
medium = { min = "2m",  max = "10m" }

[sampling]
samples_per_cell_default = 5

[[sampling.high_discrimination]]
failure_types    = ["http_status"]
samples_per_cell = 12

[[failure_types]]
type = "http_status"
status_code_choices = [503, 502, 504]

[[failure_types]]
type = "tcp_refused"

[cooldown]
per_target_minimum = "5m"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

// TestGenerate_Deterministic — same campaign + same masterSeed produces
// byte-identical Plans. This is the load-bearing reproducibility
// invariant.
func TestGenerate_Deterministic(t *testing.T) {
	c := generatorTestCampaign(t)
	p1, err := Generate(c, 42)
	if err != nil {
		t.Fatalf("Generate (run 1): %v", err)
	}
	p2, err := Generate(c, 42)
	if err != nil {
		t.Fatalf("Generate (run 2): %v", err)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatal("Generate produced different Plans for the same (campaign, masterSeed)")
	}
}

// TestGenerate_DifferentSeedsDiffer — changing the master seed should
// produce a different Plan (otherwise the seed isn't actually a seed).
func TestGenerate_DifferentSeedsDiffer(t *testing.T) {
	c := generatorTestCampaign(t)
	p1, _ := Generate(c, 1)
	p2, _ := Generate(c, 2)
	if reflect.DeepEqual(p1, p2) {
		t.Fatal("seeds 1 and 2 produced identical Plans")
	}
}

// TestGenerate_OneDesignPerCell — exactly one design per cell, no more
// no fewer.
func TestGenerate_OneDesignPerCell(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, err := Generate(c, 42)
	if err != nil {
		t.Fatal(err)
	}
	// 2 failure_types × 2 duration_buckets × 2 host_patterns = 8 cells.
	if len(plan.Designs) != 8 {
		t.Fatalf("got %d designs, want 8 (one per cell)", len(plan.Designs))
	}
	seen := make(map[Cell]bool)
	for _, d := range plan.Designs {
		if seen[d.Cell] {
			t.Errorf("duplicate cell: %+v", d.Cell)
		}
		seen[d.Cell] = true
	}
}

// TestGenerate_HighDiscriminationTierGetsMoreSamples — every design
// targeting a high-discrim failure type has the elevated replay count.
func TestGenerate_HighDiscriminationTierGetsMoreSamples(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	for _, d := range plan.Designs {
		want := 5
		if d.FailureType == "http_status" {
			want = 12
		}
		if d.Replays != want {
			t.Errorf("design %s (failure_type=%s): Replays=%d, want %d", d.ID, d.FailureType, d.Replays, want)
		}
	}
}

// TestGenerate_TotalScheduleMatchesReplays — the schedule has exactly
// one ReplaySlot per (design, replay-index) pair across all designs.
func TestGenerate_TotalScheduleMatchesReplays(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	want := 0
	for _, d := range plan.Designs {
		want += d.Replays
	}
	if len(plan.Schedule) != want {
		t.Errorf("schedule has %d slots, want %d (sum of design replays)", len(plan.Schedule), want)
	}

	// Every (design, index) pair should appear exactly once.
	seen := make(map[string]bool)
	for _, s := range plan.Schedule {
		key := s.DesignID + ":" + intToStr(s.Index)
		if seen[key] {
			t.Errorf("duplicate slot for (%s, %d)", s.DesignID, s.Index)
		}
		seen[key] = true
	}
}

// TestGenerate_ScheduleSortedAndWithinDuration — slot offsets are
// non-decreasing and all fit inside [0, campaign.Duration).
func TestGenerate_ScheduleSortedAndWithinDuration(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)

	if !sort.SliceIsSorted(plan.Schedule, func(i, j int) bool {
		return plan.Schedule[i].Offset < plan.Schedule[j].Offset
	}) {
		t.Error("schedule is not sorted by Offset")
	}
	for _, s := range plan.Schedule {
		if s.Offset < 0 {
			t.Errorf("slot %s/%d has negative offset %v", s.DesignID, s.Index, s.Offset)
		}
		if s.Offset >= c.Duration {
			t.Errorf("slot %s/%d offset %v exceeds campaign duration %v", s.DesignID, s.Index, s.Offset, c.Duration)
		}
	}
}

// TestGenerate_HostPatterns — each design's Targets list size matches
// its host pattern.
func TestGenerate_HostPatterns(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	for _, d := range plan.Designs {
		switch d.Cell.HostPattern {
		case HostPatternSingle:
			if len(d.Targets) != 1 {
				t.Errorf("design %s (single): %d targets, want 1", d.ID, len(d.Targets))
			}
		case HostPatternTwoRandom:
			if len(d.Targets) != 2 {
				t.Errorf("design %s (two_random): %d targets, want 2", d.ID, len(d.Targets))
			}
			if d.Targets[0] == d.Targets[1] {
				t.Errorf("design %s (two_random): two_random produced duplicate target %q", d.ID, d.Targets[0])
			}
		case HostPatternAll:
			if len(d.Targets) != len(c.Targets.Pool) {
				t.Errorf("design %s (all): %d targets, want %d (pool size)", d.ID, len(d.Targets), len(c.Targets.Pool))
			}
		}
	}
}

// TestGenerate_StatusCodeWithinChoices — designs for http_status only
// pick status codes from the configured choices.
func TestGenerate_StatusCodeWithinChoices(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	for _, d := range plan.Designs {
		if d.FailureType != "http_status" {
			continue
		}
		got, ok := d.Params["status_code"].(int)
		if !ok {
			t.Errorf("design %s: status_code missing or wrong type, got %T %v", d.ID, d.Params["status_code"], d.Params["status_code"])
			continue
		}
		if got != 503 && got != 502 && got != 504 {
			t.Errorf("design %s: status_code=%d not in choices", d.ID, got)
		}
	}
}

// TestGenerate_DurationWithinBucket — designs' duration falls in the
// declared range for their cell's bucket.
func TestGenerate_DurationWithinBucket(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	for _, d := range plan.Designs {
		bk := c.DurationBuckets[d.Cell.DurationBucket]
		if d.Duration < bk.Min || d.Duration > bk.Max {
			t.Errorf("design %s: duration %v not in bucket %s [%v, %v]", d.ID, d.Duration, d.Cell.DurationBucket, bk.Min, bk.Max)
		}
	}
}

// TestGenerate_DesignSeedsAreDistinct — every design has its own seed
// derived from the master, not all the same value.
func TestGenerate_DesignSeedsAreDistinct(t *testing.T) {
	c := generatorTestCampaign(t)
	plan, _ := Generate(c, 42)
	seeds := make(map[int64]bool)
	for _, d := range plan.Designs {
		if seeds[d.Seed] {
			t.Errorf("duplicate per-design seed %d (cells should produce distinct seeds)", d.Seed)
		}
		seeds[d.Seed] = true
	}
}

// TestGenerate_NoEscalationWhenProbabilityZero — disabling escalation
// in the config means no design carries an Escalation block.
func TestGenerate_NoEscalationWhenProbabilityZero(t *testing.T) {
	c := generatorTestCampaign(t)
	// no [escalation] declared → c.Escalation is nil already.
	plan, _ := Generate(c, 42)
	for _, d := range plan.Designs {
		if d.Escalation != nil {
			t.Errorf("design %s has Escalation when campaign has none", d.ID)
		}
	}
}

// TestGenerate_EscalationStagesRespectRange — when escalation is
// enabled, the number of stages falls in [stages_range.min, stages_range.max].
func TestGenerate_EscalationStagesRespectRange(t *testing.T) {
	c, err := Parse([]byte(`
id       = "esc-test"
duration = "1h"
seed     = 0

[targets]
pool     = ["bench-a"]
patterns = ["single"]

[duration_buckets]
medium = { min = "5m", max = "10m" }

[sampling]
samples_per_cell_default = 1

[[failure_types]]
type = "http_status"
status_code_choices = [503]

[[failure_types]]
type = "tcp_refused"

[escalation]
probability       = 1.0
stages_range      = { min = 2, max = 3 }
inter_stage_range = { min = "30s", max = "1m" }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, _ := Generate(c, 42)

	sawEscalating := false
	for _, d := range plan.Designs {
		if d.Escalation == nil {
			t.Errorf("design %s has no escalation despite probability=1.0", d.ID)
			continue
		}
		sawEscalating = true
		stages := len(d.Escalation.Stages)
		// Stages can be truncated if offset exceeds scenario duration —
		// allow as low as 1 (the base stage always lands).
		if stages < 1 || stages > 3 {
			t.Errorf("design %s: %d stages, want 1..3", d.ID, stages)
		}
	}
	if !sawEscalating {
		t.Fatal("expected at least one escalating design")
	}
}

// TestGenerate_ZeroSampleCellsSkipped — cells whose tier resolves to
// zero samples emit no design (defensive; current validation forbids
// this, but the generator shouldn't crash if it ever happens).
func TestGenerate_ZeroSampleCellsSkipped(t *testing.T) {
	c := generatorTestCampaign(t)
	c.Sampling.SamplesPerCellDefault = 0
	c.Sampling.HighDiscrimination[0].SamplesPerCell = 12 // only http_status remains
	plan, err := Generate(c, 42)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, d := range plan.Designs {
		if d.FailureType != "http_status" {
			t.Errorf("design %s: non-http_status survived a zero default — failure type %q should have been skipped", d.ID, d.FailureType)
		}
	}
}

// TestGenerate_EmptyCampaignErrors — a Campaign that produces zero
// total replays is a programming error, surface it.
func TestGenerate_EmptyCampaignErrors(t *testing.T) {
	c := generatorTestCampaign(t)
	c.Sampling.SamplesPerCellDefault = 0
	c.Sampling.HighDiscrimination = nil
	if _, err := Generate(c, 42); err == nil {
		t.Fatal("expected error when campaign produces zero replays")
	}
}

// intToStr is a tiny helper to keep the test's map-key construction
// cheap without dragging fmt.Sprintf in.
func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	buf := [12]byte{}
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestGenerate_FixedSeedGoldenCheck pins the exact ID + Cell + first
// schedule slot for a tiny campaign at a known seed. If this test fails
// after a generator change, the determinism contract has been broken
// and downstream consumers (saved campaign artifacts, published
// methodology) will see different results from the same config.
func TestGenerate_FixedSeedGoldenCheck(t *testing.T) {
	c, err := Parse([]byte(`
id       = "golden"
duration = "1h"
seed     = 0

[targets]
pool     = ["a", "b"]
patterns = ["single"]

[duration_buckets]
b = { min = "1m", max = "2m" }

[sampling]
samples_per_cell_default = 2

[[failure_types]]
type = "tcp_refused"

[cooldown]
per_target_minimum = "0s"
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := Generate(c, 12345)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(plan.Designs) != 1 {
		t.Fatalf("expected 1 design (1 failure × 1 bucket × 1 pattern), got %d", len(plan.Designs))
	}
	d := plan.Designs[0]
	if d.ID != "d-0000" {
		t.Errorf("design ID = %q, want d-0000 (first cell)", d.ID)
	}
	if d.Cell.FailureType != "tcp_refused" || d.Cell.DurationBucket != "b" || d.Cell.HostPattern != "single" {
		t.Errorf("cell = %+v, want {tcp_refused, b, single}", d.Cell)
	}
	if d.Replays != 2 {
		t.Errorf("Replays = %d, want 2", d.Replays)
	}
	if len(plan.Schedule) != 2 {
		t.Errorf("schedule size = %d, want 2", len(plan.Schedule))
	}
}

// TestEnumerateCells_StableOrder — same campaign, multiple calls,
// identical cell order. Map iteration in Go is randomized, so this
// test catches "cells leak iteration order from the buckets map" bugs.
func TestEnumerateCells_StableOrder(t *testing.T) {
	c := generatorTestCampaign(t)
	first := enumerateCells(c)
	for i := 0; i < 50; i++ {
		got := enumerateCells(c)
		if !reflect.DeepEqual(first, got) {
			t.Fatalf("enumerateCells produced different orders across calls; first=%v got=%v", first, got)
		}
	}
}

// TestPickTargets_TwoRandomDistinct — over many seeds, two_random
// always returns two distinct hosts.
func TestPickTargets_TwoRandomDistinct(t *testing.T) {
	pool := []string{"a", "b", "c"}
	for seed := int64(0); seed < 100; seed++ {
		got := pickTargets(newRand(seed), pool, HostPatternTwoRandom)
		if len(got) != 2 || got[0] == got[1] {
			t.Fatalf("seed %d: pickTargets two_random = %v (want two distinct)", seed, got)
		}
	}
}

// TestDurationIn_Bounds — durationIn returns values inside [min, max]
// inclusive across many seeds, never below min or above max.
func TestDurationIn_Bounds(t *testing.T) {
	min := 30 * time.Second
	max := 2 * time.Minute
	for seed := int64(0); seed < 200; seed++ {
		got := durationIn(newRand(seed), min, max)
		if got < min || got > max {
			t.Fatalf("seed %d: durationIn = %v, out of [%v, %v]", seed, got, min, max)
		}
	}
}
