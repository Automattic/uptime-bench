// Package runplan estimates campaign and scenario wall-clock spans from
// deterministic plans. It does not do I/O and intentionally stays separate from
// runner so preflight tools can explain timing without starting provider work.
package runplan

import (
	"sort"
	"time"

	"github.com/Automattic/uptime-bench/internal/campaign"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

// ScenarioRuntime returns the minimum harness-controlled runtime for a single
// scenario after provisioning has completed. Provider API latency, jitter before
// process start, and post-run sleep are outside the scenario definition and are
// not included.
func ScenarioRuntime(sc *scenario.Scenario) time.Duration {
	if sc == nil {
		return 0
	}
	return latestFailureEndOffset(sc.Duration, sc.Failures) + sc.GracePeriod
}

// CampaignEstimate summarizes the generated campaign schedule.
type CampaignEstimate struct {
	Designs              int
	Replays              int
	ConfiguredDuration   time.Duration
	ScheduledSpan        time.Duration
	SerialRuntime        time.Duration
	MaxScenarioRuntime   time.Duration
	MaxStartsInHour      int
	MaxConcurrentSamples int
}

// EstimateCampaign returns timing diagnostics for a generated plan. Scheduled
// span assumes slots could start at their planned offsets; SerialRuntime mirrors
// the current RunCampaign implementation, which walks one scenario at a time.
func EstimateCampaign(c *campaign.Campaign, plan *campaign.Plan) CampaignEstimate {
	if c == nil || plan == nil {
		return CampaignEstimate{}
	}
	byDesign := make(map[string]campaign.Design, len(plan.Designs))
	for _, d := range plan.Designs {
		byDesign[d.ID] = d
	}

	slots := append([]campaign.ReplaySlot(nil), plan.Schedule...)
	sort.Slice(slots, func(i, j int) bool {
		return slots[i].Offset < slots[j].Offset
	})

	var scheduledSpan, serialRuntime, maxScenarioRuntime time.Duration
	intervals := make([]interval, 0, len(slots))
	for _, slot := range slots {
		d, ok := byDesign[slot.DesignID]
		if !ok {
			continue
		}
		runtime := d.Duration + c.GracePeriod
		if runtime > maxScenarioRuntime {
			maxScenarioRuntime = runtime
		}
		if end := slot.Offset + runtime; end > scheduledSpan {
			scheduledSpan = end
		}
		start := slot.Offset
		if serialRuntime > start {
			start = serialRuntime
		}
		serialRuntime = start + runtime
		intervals = append(intervals, interval{start: slot.Offset, end: slot.Offset + runtime})
	}

	return CampaignEstimate{
		Designs:              len(plan.Designs),
		Replays:              len(plan.Schedule),
		ConfiguredDuration:   c.Duration,
		ScheduledSpan:        scheduledSpan,
		SerialRuntime:        serialRuntime,
		MaxScenarioRuntime:   maxScenarioRuntime,
		MaxStartsInHour:      maxStartsInWindow(slots, time.Hour),
		MaxConcurrentSamples: maxConcurrent(intervals),
	}
}

func latestFailureEndOffset(defaultDuration time.Duration, failures []scenario.Failure) time.Duration {
	if len(failures) == 0 {
		return defaultDuration
	}
	var latest time.Duration
	for _, f := range failures {
		dur := defaultDuration
		if f.Duration > 0 {
			dur = f.Duration
		}
		if end := f.Offset + dur; end > latest {
			latest = end
		}
	}
	return latest
}

func maxStartsInWindow(slots []campaign.ReplaySlot, window time.Duration) int {
	if len(slots) == 0 {
		return 0
	}
	offsets := make([]time.Duration, len(slots))
	for i, slot := range slots {
		offsets[i] = slot.Offset
	}
	sort.Slice(offsets, func(i, j int) bool { return offsets[i] < offsets[j] })
	maxCount := 0
	end := 0
	for start, offset := range offsets {
		if end < start {
			end = start
		}
		for end < len(offsets) && offsets[end]-offset < window {
			end++
		}
		if count := end - start; count > maxCount {
			maxCount = count
		}
	}
	return maxCount
}

type interval struct {
	start time.Duration
	end   time.Duration
}

func maxConcurrent(intervals []interval) int {
	type point struct {
		t     time.Duration
		delta int
	}
	points := make([]point, 0, len(intervals)*2)
	for _, iv := range intervals {
		points = append(points, point{t: iv.start, delta: 1}, point{t: iv.end, delta: -1})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].t == points[j].t {
			return points[i].delta < points[j].delta
		}
		return points[i].t < points[j].t
	})
	active, maxActive := 0, 0
	for _, p := range points {
		active += p.delta
		if active > maxActive {
			maxActive = active
		}
	}
	return maxActive
}
