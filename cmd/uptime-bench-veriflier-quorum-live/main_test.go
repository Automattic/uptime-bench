package main

import "testing"

func TestVerifierConfirmedReason(t *testing.T) {
	tests := []struct {
		name        string
		transitions []apiTransition
		wantReason  string
		wantOK      bool
	}{
		{
			name: "explicit verifier confirmed transition",
			transitions: []apiTransition{{
				Reason: "verifier_confirmed",
			}},
			wantReason: "verifier_confirmed",
			wantOK:     true,
		},
		{
			name: "direct open with verifier metadata",
			transitions: []apiTransition{{
				Reason:   "opened",
				Metadata: []byte(`{"verifier_confirmed":3,"verifier_quorum":3}`),
			}},
			wantReason: "opened",
			wantOK:     true,
		},
		{
			name: "unverified transition",
			transitions: []apiTransition{{
				Reason:   "opened",
				Metadata: []byte(`{"verifier_confirmed":0}`),
			}},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotReason, gotOK := verifierConfirmedReason(tt.transitions)
			if gotReason != tt.wantReason || gotOK != tt.wantOK {
				t.Fatalf("verifierConfirmedReason() = %q, %t; want %q, %t", gotReason, gotOK, tt.wantReason, tt.wantOK)
			}
		})
	}
}
