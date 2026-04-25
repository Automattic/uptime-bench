package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captured records the request shape a Client sent so tests can assert on
// auth headers, body content, etc.
type captured struct {
	method      string
	path        string
	authHeader  string
	contentType string
	body        []byte
}

// fakeServer returns an httptest.Server that captures every request into c
// and responds with the given status code and optional body.
func fakeServer(c *captured, status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.method = r.Method
		c.path = r.URL.Path
		c.authHeader = r.Header.Get("Authorization")
		c.contentType = r.Header.Get("Content-Type")
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		c.body = append(c.body[:0], buf[:n]...)
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
}

// TestActivate_RequestShape verifies the wire format: POST, /activate,
// bearer token, JSON content-type, and the request fields all reach the
// server intact.
func TestActivate_RequestShape(t *testing.T) {
	var c captured
	srv := fakeServer(&c, http.StatusNoContent, "")
	defer srv.Close()

	client := NewClient(srv.URL, "tok-xyz", srv.Client())
	req := ActivateRequest{
		RunID: "run-1",
		Seed:  42,
		Failure: FailureSpec{
			Type:     "http_status",
			Host:     "example.com",
			Duration: 5 * time.Second,
			Rate:     0.5,
			Params:   map[string]any{"status_code": 503},
		},
	}
	if err := client.Activate(context.Background(), req); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/activate" {
		t.Errorf("path = %q, want /activate", c.path)
	}
	if c.authHeader != "Bearer tok-xyz" {
		t.Errorf("auth = %q, want %q", c.authHeader, "Bearer tok-xyz")
	}
	if c.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", c.contentType)
	}
	var got ActivateRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v\nbody=%q", err, c.body)
	}
	if got.RunID != "run-1" || got.Seed != 42 {
		t.Fatalf("decoded body = %+v", got)
	}
	if got.Failure.Type != "http_status" || got.Failure.Host != "example.com" {
		t.Fatalf("decoded failure = %+v", got.Failure)
	}
}

// TestActivate_RejectsNon204 — the server responding with anything other
// than 204 No Content is treated as an error so misconfigurations surface
// immediately rather than silently desynchronizing harness and target.
func TestActivate_RejectsNon204(t *testing.T) {
	cases := []int{
		http.StatusOK,                  // 200 — wrong, the server should return 204
		http.StatusBadRequest,          // 400 — typical validation rejection
		http.StatusUnauthorized,        // 401 — wrong token
		http.StatusInternalServerError, // 500
	}
	for _, code := range cases {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var c captured
			srv := fakeServer(&c, code, "nope")
			defer srv.Close()
			client := NewClient(srv.URL, "tok", srv.Client())
			err := client.Activate(context.Background(), ActivateRequest{})
			if err == nil {
				t.Fatalf("Activate returned nil for status %d", code)
			}
			if !strings.Contains(err.Error(), "/activate") {
				t.Fatalf("error %q should mention path", err)
			}
		})
	}
}

func TestActivate_NetworkError(t *testing.T) {
	// Point at a closed server so Do() fails.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()

	client := NewClient(srv.URL, "tok", srv.Client())
	err := client.Activate(context.Background(), ActivateRequest{})
	if err == nil {
		t.Fatal("Activate should error when server is unreachable")
	}
	if !strings.Contains(err.Error(), "/activate") {
		t.Fatalf("error %q should mention path", err)
	}
}

func TestDeactivate_RequestShape(t *testing.T) {
	var c captured
	srv := fakeServer(&c, http.StatusNoContent, "")
	defer srv.Close()

	client := NewClient(srv.URL, "tok", srv.Client())
	req := DeactivateRequest{RunID: "run-1", FailureType: "http_status", Host: "example.com"}
	if err := client.Deactivate(context.Background(), req); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	if c.method != http.MethodPost {
		t.Errorf("method = %q, want POST", c.method)
	}
	if c.path != "/deactivate" {
		t.Errorf("path = %q, want /deactivate", c.path)
	}

	var got DeactivateRequest
	if err := json.Unmarshal(c.body, &got); err != nil {
		t.Fatalf("body unmarshal: %v", err)
	}
	if got != req {
		t.Fatalf("decoded body = %+v, want %+v", got, req)
	}
}

func TestStatus_DecodesResponse(t *testing.T) {
	var c captured
	body := `{"member_id":"target-01","active_failures":[{"type":"http_status","host":"x","duration":5000000000,"rate":1}]}`
	srv := fakeServer(&c, http.StatusOK, body)
	defer srv.Close()

	client := NewClient(srv.URL, "tok", srv.Client())
	resp, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if c.method != http.MethodGet {
		t.Errorf("method = %q, want GET", c.method)
	}
	if c.path != "/status" {
		t.Errorf("path = %q, want /status", c.path)
	}
	if resp.MemberID != "target-01" {
		t.Errorf("MemberID = %q, want target-01", resp.MemberID)
	}
	if len(resp.ActiveFailures) != 1 || resp.ActiveFailures[0].Type != "http_status" {
		t.Errorf("ActiveFailures = %+v", resp.ActiveFailures)
	}
}

func TestStatus_RejectsNon200(t *testing.T) {
	var c captured
	srv := fakeServer(&c, http.StatusUnauthorized, `{"error":"bad token"}`)
	defer srv.Close()
	client := NewClient(srv.URL, "tok", srv.Client())
	if _, err := client.Status(context.Background()); err == nil {
		t.Fatal("Status should error on 401")
	}
}

// TestNewClient_DefaultsHTTPClient — passing nil for the http.Client must
// fall back to http.DefaultClient instead of panicking.
func TestNewClient_DefaultsHTTPClient(t *testing.T) {
	c := NewClient("http://example.com", "tok", nil)
	if c.http != http.DefaultClient {
		t.Fatal("nil http.Client should fall back to http.DefaultClient")
	}
}

// TestActivate_HonorsContextCancel — the request must abort if the caller's
// context is cancelled, not block on a slow server.
func TestActivate_HonorsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok", srv.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := client.Activate(ctx, ActivateRequest{})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Activate should error when ctx is cancelled")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Activate took %v after a 50ms ctx timeout — context not honored", elapsed)
	}
}
