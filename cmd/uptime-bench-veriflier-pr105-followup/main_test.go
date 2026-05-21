package main

import (
	"encoding/json"
	"testing"
)

func TestVersionMatchesCommit(t *testing.T) {
	if !versionMatchesCommit("785e262", "785e262bc7201eb04725bb17d82f39f92c791857") {
		t.Fatal("short status version should match full expected commit")
	}
	if versionMatchesCommit("07ab232", "785e262bc7201eb04725bb17d82f39f92c791857") {
		t.Fatal("different status version should not match expected commit")
	}
}

func TestNotificationExpectedAttempts(t *testing.T) {
	for _, classification := range []string{"disabled_by_test_plan", "enabled_no_relevant_events"} {
		if notificationExpectedAttempts(classification) {
			t.Fatalf("%s should not expect WPCOM attempts", classification)
		}
	}
	for _, classification := range []string{"enabled_expected_attempts_seen", "enabled_expected_attempts_missing"} {
		if !notificationExpectedAttempts(classification) {
			t.Fatalf("%s should expect WPCOM attempts", classification)
		}
	}
}

func TestValidateDirectMatrixResult(t *testing.T) {
	err := validateDirectMatrixResult(directMatrixCase{
		WantSuccess:    true,
		WantHTTPCode:   200,
		AcceptOutcomes: []string{"up"},
		MinRTTMS:       100,
	}, &v2CheckResult{Success: true, HTTPCode: 200, Outcome: "up", RTTMs: 125})
	if err != nil {
		t.Fatalf("valid success result failed: %v", err)
	}

	err = validateDirectMatrixResult(directMatrixCase{
		WantErrorCodeNonZero: true,
		AcceptOutcomes:       []string{"down"},
	}, &v2CheckResult{Success: false, Outcome: "down", ErrorCode: 7})
	if err != nil {
		t.Fatalf("valid down result failed: %v", err)
	}

	err = validateDirectMatrixResult(directMatrixCase{
		WantSuccess:    true,
		WantHTTPCode:   200,
		AcceptOutcomes: []string{"up"},
	}, &v2CheckResult{Success: true, HTTPCode: 500, Outcome: "down"})
	if err == nil {
		t.Fatal("mismatched HTTP code should fail")
	}
}

func TestSanitizeIDPart(t *testing.T) {
	if got := sanitizeIDPart("GET/full redirect policy"); got != "get-full-redirect-policy" {
		t.Fatalf("sanitizeIDPart = %q", got)
	}
	if got := sanitizeIDPart("!!!"); got != "case" {
		t.Fatalf("empty sanitizeIDPart = %q", got)
	}
}

func TestEventDetailDataIncludesLifecycleAndVoteEvidence(t *testing.T) {
	stateSeemsDown := "Seems Down"
	stateDown := "Down"
	meta, err := json.Marshal(map[string]any{
		"verifier_quorum":          2,
		"verifier_min_healthy":     2,
		"verifier_healthy":         3,
		"verifier_confirmed":       2,
		"verifier_disagreed":       1,
		"verifier_duplicate_votes": 1,
		"verifier_results": []map[string]any{
			{"vantage_id": "a", "outcome": "down", "success": false, "http_code": 503},
			{"vantage_id": "b", "outcome": "down", "success": false, "http_code": 503},
			{"vantage_id": "b", "outcome": "down", "success": false, "http_code": 503},
			{"vantage_id": "c", "success": false, "http_code": 0},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := eventDetailData(apiEventResponse{
		apiEventListRecord: apiEventListRecord{ID: 123, State: "Down"},
		Transitions: []apiTransition{
			{Reason: "opened", StateAfter: &stateSeemsDown},
			{Reason: "verifier_confirmed", StateAfter: &stateDown, Metadata: meta},
			{Reason: "verifier_cleared"},
		},
	})
	if got := data["seems_down_openings"]; got != 1 {
		t.Fatalf("seems_down_openings = %v, want 1", got)
	}
	if got := data["verifier_confirmed_down_promotions"]; got != 1 {
		t.Fatalf("verifier_confirmed_down_promotions = %v, want 1", got)
	}
	if got := data["verifier_cleared_recoveries"]; got != 1 {
		t.Fatalf("verifier_cleared_recoveries = %v, want 1", got)
	}
	evidence, ok := data["verifier_vote_evidence"].(map[string]any)
	if !ok {
		t.Fatalf("verifier_vote_evidence missing or wrong type: %#v", data["verifier_vote_evidence"])
	}
	if got := evidence["counted_vantages"]; got != 3 {
		t.Fatalf("counted_vantages = %v, want 3", got)
	}
	if got := evidence["verifier_duplicate_votes"]; got != int64(1) {
		t.Fatalf("verifier_duplicate_votes = %v, want 1", got)
	}
	if got := evidence["missing_outcomes"]; got != 1 {
		t.Fatalf("missing_outcomes = %v, want 1", got)
	}
	if got := evidence["transport_errors"]; got != 1 {
		t.Fatalf("transport_errors = %v, want 1", got)
	}
}
