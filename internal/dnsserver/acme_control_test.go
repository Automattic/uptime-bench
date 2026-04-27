package dnsserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/control"
)

// newACMETestServer wires the same handler graph cmd/dns/main.go
// builds (control routes + ACME routes under one auth wrap) and
// returns the test server plus the underlying TXT store so assertions
// can inspect post-state directly.
func newACMETestServer(t *testing.T, token string) (*httptest.Server, *TXTStore) {
	t.Helper()
	registry := control.NewRegistry()
	store := NewTXTStore()

	controlSrv := control.NewServer("dns-test", token, registry)
	mux := http.NewServeMux()
	controlSrv.RegisterRoutes(mux)
	RegisterACMEHandlers(mux, store)
	srv := httptest.NewServer(control.AuthMiddleware(token)(mux))
	t.Cleanup(srv.Close)
	return srv, store
}

func TestACMEControl_PutInstallsValueIntoStore(t *testing.T) {
	srv, store := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)

	err := client.Put(context.Background(), ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "validation-token",
		TTL:   30,
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	values, ttl, ok := store.Lookup("_acme-challenge.bench.example.com")
	if !ok {
		t.Fatal("store has no entry after Put")
	}
	if !slices.Equal(values, []string{"validation-token"}) {
		t.Fatalf("values = %v", values)
	}
	if ttl != 30 {
		t.Fatalf("ttl = %d, want 30", ttl)
	}
}

func TestACMEControl_PutDefaultsTTLWhenZero(t *testing.T) {
	srv, store := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)

	err := client.Put(context.Background(), ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "validation-token",
	})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, ttl, _ := store.Lookup("_acme-challenge.bench.example.com")
	if ttl == 0 {
		t.Fatal("ttl = 0 — server should default to a non-zero TTL when client omits it")
	}
}

func TestACMEControl_DeletePreservesSiblingValues(t *testing.T) {
	srv, store := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)
	ctx := context.Background()

	if err := client.Put(ctx, ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "apex",
		TTL:   30,
	}); err != nil {
		t.Fatalf("Put apex: %v", err)
	}
	if err := client.Put(ctx, ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "wildcard",
		TTL:   30,
	}); err != nil {
		t.Fatalf("Put wildcard: %v", err)
	}
	if err := client.Delete(ctx, ACMETXTDeleteRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "apex",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	values, _, ok := store.Lookup("_acme-challenge.bench.example.com")
	if !ok {
		t.Fatal("entry should still exist after deleting one of two values")
	}
	if !slices.Equal(values, []string{"wildcard"}) {
		t.Fatalf("values = %v, want [wildcard]", values)
	}
}

func TestACMEControl_DeleteUnknownIsNoOp(t *testing.T) {
	srv, _ := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)

	err := client.Delete(context.Background(), ACMETXTDeleteRequest{
		Name:  "_acme-challenge.never-added.example.com",
		Value: "missing",
	})
	if err != nil {
		t.Fatalf("Delete should be idempotent for unknown values: %v", err)
	}
}

func TestACMEControl_RejectsUnauthenticated(t *testing.T) {
	srv, _ := newACMETestServer(t, "secret")

	cases := []struct {
		name   string
		method string
	}{
		{"put without token", http.MethodPut},
		{"delete without token", http.MethodDelete},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL+"/acme/txt", strings.NewReader(`{"name":"_acme-challenge.bench.example.com","value":"v"}`))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", resp.StatusCode)
			}
		})
	}
}

func TestACMEControl_RejectsWrongToken(t *testing.T) {
	srv, _ := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "wrong-token", nil)

	err := client.Put(context.Background(), ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "validation-token",
		TTL:   30,
	})
	if err == nil {
		t.Fatal("Put should fail with wrong token")
	}
}

// TestACMEControl_RejectsNonChallengeName scopes the bypass: even an
// authenticated client cannot install a TXT record at an arbitrary
// name through this endpoint. A stolen token is bounded to ACME
// challenge namespaces, not full DNS spoofing.
func TestACMEControl_RejectsNonChallengeName(t *testing.T) {
	srv, store := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)

	err := client.Put(context.Background(), ACMETXTPutRequest{
		Name:  "bench.example.com",
		Value: "spoofed",
		TTL:   30,
	})
	if err == nil {
		t.Fatal("Put should reject names without _acme-challenge. prefix")
	}
	if _, _, ok := store.Lookup("bench.example.com"); ok {
		t.Fatal("rejected request should not have written to the store")
	}
}

func TestACMEControl_RejectsMissingFields(t *testing.T) {
	srv, _ := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)

	cases := []struct {
		name string
		req  ACMETXTPutRequest
	}{
		{"missing name", ACMETXTPutRequest{Value: "v", TTL: 30}},
		{"missing value", ACMETXTPutRequest{Name: "_acme-challenge.bench.example.com", TTL: 30}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := client.Put(context.Background(), tc.req)
			if err == nil {
				t.Fatal("Put should reject incomplete request")
			}
		})
	}
}

// TestACMEControl_PutThenDNSQuery is the round-trip integration
// promised in docs/certmint-dns01-handoff.md: install via the control
// API, query via DNS, confirm the response carries the value, delete,
// confirm it goes away. This exercises the full path without needing
// real UDP/TCP sockets.
func TestACMEControl_PutThenDNSQuery(t *testing.T) {
	srv, store := newACMETestServer(t, "secret")
	client := NewACMEClient(srv.URL, "secret", nil)
	ctx := context.Background()

	if err := client.Put(ctx, ACMETXTPutRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "round-trip-value",
		TTL:   30,
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	resp, _ := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), testZones(nil), store)
	got := parseTXTAnswers(t, resp)
	if len(got) != 1 || got[0] != "round-trip-value" {
		t.Fatalf("DNS round-trip values = %v, want [round-trip-value]", got)
	}

	if err := client.Delete(ctx, ACMETXTDeleteRequest{
		Name:  "_acme-challenge.bench.example.com",
		Value: "round-trip-value",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	resp2, _ := BuildResponse(buildQuery("_acme-challenge.bench.example.com", 16), control.NewRegistry(), testZones(nil), store)
	if rcode := responseRCODE(resp2); rcode != 3 {
		t.Fatalf("after Delete, RCODE = %d, want 3 (NXDOMAIN)", rcode)
	}
}

// TestACMEControl_RegisterIsNoOpForNilStore — guards against a future
// caller that conditionally enables ACME support. Passing nil should
// not register handlers (and shouldn't panic).
func TestACMEControl_RegisterIsNoOpForNilStore(t *testing.T) {
	mux := http.NewServeMux()
	RegisterACMEHandlers(mux, nil)

	req := httptest.NewRequest(http.MethodPut, "/acme/txt", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected route to be unregistered, got status %d", rec.Code)
	}
}
