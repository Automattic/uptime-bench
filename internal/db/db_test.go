package db

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMetricTextValueTruncatesToColumnLimit(t *testing.T) {
	long := strings.Repeat("x", derivedMetricTextMaxChars+20)
	got := metricTextValue(long)
	if got == nil {
		t.Fatal("metricTextValue returned nil for non-empty input")
	}
	if utf8.RuneCountInString(*got) != derivedMetricTextMaxChars {
		t.Fatalf("metric text length = %d, want %d", utf8.RuneCountInString(*got), derivedMetricTextMaxChars)
	}
	if !strings.HasSuffix(*got, "...") {
		t.Fatalf("metric text should end with truncation suffix, got %q", *got)
	}
}

func TestMetricTextValueKeepsShortAndEmptyValues(t *testing.T) {
	if got := metricTextValue(""); got != nil {
		t.Fatalf("empty metric text = %q, want nil", *got)
	}
	const short = "rate limited"
	got := metricTextValue(short)
	if got == nil || *got != short {
		t.Fatalf("short metric text = %v, want %q", got, short)
	}
}
