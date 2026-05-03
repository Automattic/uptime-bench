package reportdir

import (
	"testing"
	"time"
)

func TestNameUsesTimestampDurationAndSlug(t *testing.T) {
	start := time.Date(2026, 5, 3, 4, 42, 55, 0, time.FixedZone("CDT", -5*60*60))
	got := Name(start, 8*time.Hour+90*time.Second, "V1/V2 Inclusive Overnight")
	want := "20260503T094255Z-8h02m-v1-v2-inclusive-overnight"
	if got != want {
		t.Fatalf("Name = %q, want %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := map[time.Duration]string{
		0:                               "0m",
		500 * time.Millisecond:          "1s",
		45 * time.Second:                "45s",
		15*time.Minute + 20*time.Second: "15m",
		90 * time.Minute:                "1h30m",
		8*time.Hour - 10*time.Second:    "8h",
	}
	for duration, want := range tests {
		if got := FormatDuration(duration); got != want {
			t.Fatalf("FormatDuration(%s) = %q, want %q", duration, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	got := Slug("  Capacity Scout / Jetmon_v2  ")
	want := "capacity-scout-jetmon-v2"
	if got != want {
		t.Fatalf("Slug = %q, want %q", got, want)
	}
	if got := Slug("!"); got != "run" {
		t.Fatalf("empty Slug = %q, want run", got)
	}
}
