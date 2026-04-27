package dnsserver

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// ACMETXTPutRequest is the body for PUT /acme/txt. Adding a value that
// already exists is a no-op so certmint hooks may safely retry; TTL is
// last-write-wins.
type ACMETXTPutRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	TTL   uint32 `json:"ttl"`
}

// ACMETXTDeleteRequest is the body for DELETE /acme/txt. Only the
// matching (name, value) pair is removed; sibling values for the same
// name stay in place.
type ACMETXTDeleteRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// RegisterACMEHandlers adds the PUT /acme/txt and DELETE /acme/txt
// endpoints that certmint's certbot manual-auth/manual-cleanup hooks
// call to install and tear down DNS-01 challenge records. The handlers
// write directly to store; the caller is responsible for wrapping mux
// in control.AuthMiddleware so only authenticated clients can mutate.
func RegisterACMEHandlers(mux *http.ServeMux, store *TXTStore) {
	if store == nil {
		return
	}
	mux.HandleFunc("PUT /acme/txt", handleACMETXTPut(store))
	mux.HandleFunc("DELETE /acme/txt", handleACMETXTDelete(store))
}

func handleACMETXTPut(store *TXTStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ACMETXTPutRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := validateACMETXTName(req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Value == "" {
			http.Error(w, "value is required", http.StatusBadRequest)
			return
		}
		ttl := req.TTL
		if ttl == 0 {
			ttl = 30
		}
		store.Add(req.Name, req.Value, ttl)
		log.Printf("control: acme txt put name=%q ttl=%d", req.Name, ttl)
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleACMETXTDelete(store *TXTStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ACMETXTDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := validateACMETXTName(req.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Value == "" {
			http.Error(w, "value is required", http.StatusBadRequest)
			return
		}
		store.Remove(req.Name, req.Value)
		log.Printf("control: acme txt delete name=%q", req.Name)
		w.WriteHeader(http.StatusNoContent)
	}
}

// validateACMETXTName guards the bypass scope: only names under the
// _acme-challenge. prefix may be mutated through this endpoint, so a
// stolen control token cannot be used to spoof arbitrary TXT records
// in the DNS server.
func validateACMETXTName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	normalized := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if !strings.HasPrefix(normalized, acmeChallengePrefix) {
		return fmt.Errorf("name must start with %q", acmeChallengePrefix)
	}
	return nil
}
