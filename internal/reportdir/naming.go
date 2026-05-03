// Package reportdir provides stable, sortable report directory names.
package reportdir

import (
	"fmt"
	"strings"
	"time"
)

// TimestampLayout is the UTC timestamp prefix used by report directory names.
const TimestampLayout = "20060102T150405Z"

// Name returns a path-safe report directory name in the canonical
// START_TIMESTAMP-DURATION-DESCRIPTION form.
func Name(start time.Time, duration time.Duration, description string) string {
	return Timestamp(start) + "-" + FormatDuration(duration) + "-" + Slug(description)
}

// Timestamp formats the report start time in UTC for lexical sorting.
func Timestamp(start time.Time) string {
	if start.IsZero() {
		return "unknown-start"
	}
	return start.UTC().Format(TimestampLayout)
}

// FormatDuration returns a compact path-safe duration. Durations of one minute
// or longer are rounded to the nearest minute because exact timestamps are
// preserved inside report metadata.
func FormatDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0m"
	}
	if duration < time.Minute {
		seconds := int(duration.Round(time.Second) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := int(duration.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	hours := minutes / 60
	remainingMinutes := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	if remainingMinutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%02dm", hours, remainingMinutes)
}

// Slug converts a human description into a lowercase ASCII path segment.
func Slug(description string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(description)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == '.' || r == ' ' || r == '/':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "run"
	}
	return slug
}
