package main

import (
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

func TestActionSuffixIncludesReasonAmbiguityAndError(t *testing.T) {
	got := actionSuffix(adapter.CleanupAction{
		Candidate: adapter.CleanupCandidate{
			Reason:    "outside configured target scope",
			Ambiguous: true,
		},
		Error: "delete failed",
	})

	for _, want := range []string{
		"reason=outside configured target scope",
		"ambiguous=true",
		"error=delete failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("actionSuffix missing %q in %q", want, got)
		}
	}
}

func TestActionSuffixEmptyWhenNoDetails(t *testing.T) {
	if got := actionSuffix(adapter.CleanupAction{}); got != "" {
		t.Fatalf("actionSuffix = %q, want empty", got)
	}
}
