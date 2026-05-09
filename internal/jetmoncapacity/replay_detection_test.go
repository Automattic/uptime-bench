package jetmoncapacity

import (
	"math"
	"testing"
	"time"
)

func TestAnalyzeReplayDetectionHostV1StatusTransitions(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 5, 47, 45, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	downAt := activatedAt.Add(75 * time.Second)
	recoveryAt := deactivatedAt.Add(30 * time.Second)
	downStatus := 0
	upStatus := 1

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0000001.example.test",
		HostNumber: 1,
		BlogID:     8000000000000000,
	}, []ReplayDetectionRawEvent{
		{ID: "1", NewStatus: &downStatus, ReportedAt: &downAt},
		{ID: "2", NewStatus: &upStatus, ReportedAt: &recoveryAt},
	}, activatedAt, deactivatedAt)

	if !host.DownDetected || !host.DownDetectedDuring {
		t.Fatalf("down detection = detected:%v during:%v, want true/true", host.DownDetected, host.DownDetectedDuring)
	}
	if !host.RecoveryDetected {
		t.Fatal("recovery detection = false, want true")
	}
	assertFloatPointerNear(t, "down latency", host.DownLatencySec, 75)
	assertFloatPointerNear(t, "recovery latency", host.RecoveryLatencySec, 30)
}

func TestAnalyzeReplayDetectionHostV2EndedEvent(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 5, 47, 45, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	startedAt := activatedAt.Add(12 * time.Second)
	endedAt := deactivatedAt.Add(18 * time.Second)

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0500001.example.test",
		HostNumber: 500001,
		BlogID:     8000001000000000,
	}, []ReplayDetectionRawEvent{{
		ID:        "2619600",
		CheckType: "http",
		State:     "Down",
		StartedAt: &startedAt,
		EndedAt:   &endedAt,
	}}, activatedAt, deactivatedAt)

	if !host.DownDetected || !host.DownDetectedDuring {
		t.Fatalf("down detection = detected:%v during:%v, want true/true", host.DownDetected, host.DownDetectedDuring)
	}
	if !host.RecoveryDetected {
		t.Fatal("recovery detection = false, want true")
	}
	assertFloatPointerNear(t, "down latency", host.DownLatencySec, 12)
	assertFloatPointerNear(t, "recovery latency", host.RecoveryLatencySec, 18)
}

func TestAnalyzeReplayDetectionHostLateDownDoesNotPass(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 5, 47, 45, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	startedAt := deactivatedAt.Add(5 * time.Second)

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0500001.example.test",
		HostNumber: 500001,
		BlogID:     8000001000000000,
	}, []ReplayDetectionRawEvent{{
		ID:        "2619600",
		CheckType: "http",
		State:     "Seems Down",
		StartedAt: &startedAt,
	}}, activatedAt, deactivatedAt)

	if !host.DownDetected {
		t.Fatal("down detection = false, want true for late down")
	}
	if host.DownDetectedDuring {
		t.Fatal("down detected during failure = true, want false for late down")
	}
	if !host.LateDownDetected {
		t.Fatal("late down detected = false, want true")
	}
}

func TestAnalyzeReplayDetectionHostPreexistingDownOverlap(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 8, 46, 31, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	startedAt := activatedAt.Add(-35 * time.Second)
	endedAt := deactivatedAt.Add(45 * time.Second)
	lateStartedAt := deactivatedAt.Add(5 * time.Minute)

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0500882.example.test",
		HostNumber: 500882,
		BlogID:     8000001000000881,
	}, []ReplayDetectionRawEvent{{
		ID:        "1",
		CheckType: "http",
		State:     "Down",
		StartedAt: &startedAt,
		EndedAt:   &endedAt,
	}, {
		ID:        "2",
		CheckType: "http",
		State:     "Seems Down",
		StartedAt: &lateStartedAt,
	}}, activatedAt, deactivatedAt)

	if !host.PreexistingDownOverlappedFailure {
		t.Fatal("preexisting overlap = false, want true")
	}
	if host.DownDetectedDuring {
		t.Fatal("down detected during failure = true, want false for contaminated sample")
	}
	if host.LateDownDetected {
		t.Fatal("late down detected = true, want false when sample is contaminated by preexisting down")
	}
	if !host.RecoveryDetected {
		t.Fatal("recovery detected = false, want true")
	}
}

func TestAnalyzeReplayDetectionHostClassifiesPreexistingV2Down(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 5, 47, 45, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	startedAt := activatedAt.Add(-30 * time.Second)
	endedAt := deactivatedAt.Add(10 * time.Second)

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0500001.example.test",
		HostNumber: 500001,
		BlogID:     8000001000000000,
	}, []ReplayDetectionRawEvent{{
		ID:        "2619600",
		CheckType: "http",
		State:     "Down",
		StartedAt: &startedAt,
		EndedAt:   &endedAt,
	}}, activatedAt, deactivatedAt)

	if !host.PreexistingDownOverlappedFailure {
		t.Fatal("preexisting down = false, want true")
	}
	if host.DownDetectedDuring {
		t.Fatal("down detected during failure = true, want false for contaminated sample")
	}
	if !host.RecoveryDetected {
		t.Fatal("recovery detection = false, want true")
	}
}

func TestAnalyzeReplayDetectionHostClassifiesPreexistingV1Down(t *testing.T) {
	activatedAt := time.Date(2026, 5, 9, 5, 47, 45, 0, time.UTC)
	deactivatedAt := activatedAt.Add(7 * time.Minute)
	downAt := activatedAt.Add(-30 * time.Second)
	recoveryAt := deactivatedAt.Add(30 * time.Second)
	downStatus := 0
	upStatus := 1

	host := analyzeReplayDetectionHost(replayDetectionHostSpec{
		Host:       "site-0000001.example.test",
		HostNumber: 1,
		BlogID:     8000000000000000,
	}, []ReplayDetectionRawEvent{
		{ID: "1", NewStatus: &downStatus, ReportedAt: &downAt},
		{ID: "2", NewStatus: &upStatus, ReportedAt: &recoveryAt},
	}, activatedAt, deactivatedAt)

	if !host.PreexistingDownOverlappedFailure {
		t.Fatal("preexisting down = false, want true")
	}
	if host.DownDetectedDuring {
		t.Fatal("down detected during failure = true, want false for contaminated sample")
	}
	if !host.RecoveryDetected {
		t.Fatal("recovery detection = false, want true")
	}
}

func TestSummarizeReplayDetectionIntervals(t *testing.T) {
	hosts := []ReplayDetectionHost{{
		RawEvents: []ReplayDetectionRawEvent{{
			Metadata: map[string]any{
				"observation": map[string]any{
					"normal_check_interval_seconds": 60.0,
					"next_check_interval_seconds":   "60",
				},
			},
		}, {
			Metadata: map[string]any{
				"observation": map[string]any{
					"normal_check_interval_seconds": 300.0,
					"next_check_interval_seconds":   300.0,
				},
			},
		}},
	}}

	got := summarizeReplayDetectionIntervals(hosts, 300)
	if got.normalMin == nil || *got.normalMin != 60 {
		t.Fatalf("normal min = %v, want 60", got.normalMin)
	}
	if got.normalMax == nil || *got.normalMax != 300 {
		t.Fatalf("normal max = %v, want 300", got.normalMax)
	}
	if got.nextMin == nil || *got.nextMin != 60 {
		t.Fatalf("next min = %v, want 60", got.nextMin)
	}
	if got.nextMax == nil || *got.nextMax != 300 {
		t.Fatalf("next max = %v, want 300", got.nextMax)
	}
	if got.mismatchEvents != 1 {
		t.Fatalf("mismatchEvents = %d, want 1", got.mismatchEvents)
	}
}

func TestSummarizeReplayDetectionIntervalsAllowsFailureRetryInterval(t *testing.T) {
	hosts := []ReplayDetectionHost{{
		RawEvents: []ReplayDetectionRawEvent{{
			Metadata: map[string]any{
				"observation": map[string]any{
					"normal_check_interval_seconds": 300.0,
					"next_check_interval_seconds":   60.0,
				},
			},
		}},
	}}

	got := summarizeReplayDetectionIntervals(hosts, 300)
	if got.mismatchEvents != 0 {
		t.Fatalf("mismatchEvents = %d, want 0 for normal=300 next=60 retry metadata", got.mismatchEvents)
	}
	if got.nextMin == nil || *got.nextMin != 60 {
		t.Fatalf("next min = %v, want 60", got.nextMin)
	}
}

func assertFloatPointerNear(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %.2f", name, want)
	}
	if math.Abs(*got-want) > 0.001 {
		t.Fatalf("%s = %.6f, want %.6f", name, *got, want)
	}
}
