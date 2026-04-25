package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAuth_RejectsBadTokens locks down the request gating so the constant-time
// token comparison can't accidentally regress to "always allow" or "always deny".
// The fix uses crypto/subtle.ConstantTimeCompare; the behavioral surface is the
// same as before, but every flavor of bad header must still be rejected.
func TestAuth_RejectsBadTokens(t *testing.T) {
	const token = "good-token-abcdefg"
	srv := NewServer("member-1", token, NewRegistry())
	h := srv.Handler()

	cases := []struct {
		name       string
		auth       string
		wantStatus int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic " + token, http.StatusUnauthorized},
		{"bearer wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"bearer empty token", "Bearer ", http.StatusUnauthorized},
		// Length-prefix attack: starting with the right bytes used to leak
		// information via early-exit comparison; constant-time compare also
		// rejects any wrong-length token regardless of common prefix.
		{"bearer prefix-only", "Bearer good", http.StatusUnauthorized},
		{"bearer with extra suffix", "Bearer " + token + "x", http.StatusUnauthorized},
		// Valid credential reaches the inner mux which doesn't have a route
		// for "/", so we expect 404 — proof that auth let it through.
		{"bearer correct token", "Bearer " + token, http.StatusNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%q)", w.Code, tc.wantStatus, strings.TrimSpace(w.Body.String()))
			}
		})
	}
}

func TestAuth_StatusEndpointWorks(t *testing.T) {
	const token = "good"
	srv := NewServer("member-1", token, NewRegistry())
	h := srv.Handler()

	r := httptest.NewRequest(http.MethodGet, "/status", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
}
