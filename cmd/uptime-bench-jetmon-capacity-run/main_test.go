package main

import (
	"strings"
	"testing"
)

func TestParseBatchSizes(t *testing.T) {
	got, err := parseBatchSizes("100, 1000,10000")
	if err != nil {
		t.Fatalf("parseBatchSizes: %v", err)
	}
	want := []int{100, 1000, 10000}
	if len(got) != len(want) {
		t.Fatalf("parseBatchSizes length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseBatchSizes[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestParseBatchSizesRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"10,not-a-number": "not an integer",
		"10,0":            "must be positive",
		"10,-5":           "must be positive",
	}
	for input, want := range tests {
		_, err := parseBatchSizes(input)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("parseBatchSizes(%q) = %v, want %q", input, err, want)
		}
	}
}

func TestParseBatchSizesEmptyMeansUseConfig(t *testing.T) {
	got, err := parseBatchSizes("")
	if err != nil {
		t.Fatalf("parseBatchSizes empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("parseBatchSizes empty = %#v, want empty", got)
	}
}
