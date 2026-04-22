package control

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// Server is the control plane HTTP server run by each fleet member.
// It exposes activate, deactivate, and status endpoints authenticated
// by a shared bearer token.
type Server struct {
	memberID string
	token    string
	registry *FailureRegistry
}

// NewServer creates a control Server for the given fleet member.
func NewServer(memberID, token string, registry *FailureRegistry) *Server {
	return &Server{memberID: memberID, token: token, registry: registry}
}

// Handler returns the http.Handler for the control API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /activate", s.handleActivate)
	mux.HandleFunc("POST /deactivate", s.handleDeactivate)
	mux.HandleFunc("GET /status", s.handleStatus)
	return s.withAuth(mux)
}

func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	var req ActivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if req.Failure.Type == "" {
		http.Error(w, "failure.type is required", http.StatusBadRequest)
		return
	}
	if req.Failure.Duration <= 0 {
		http.Error(w, "failure.duration must be positive", http.StatusBadRequest)
		return
	}
	s.registry.Set(req.Failure, req.Seed)
	log.Printf("control: activated %s host=%q path=%q rate=%.2f run=%s", req.Failure.Type, req.Failure.Host, req.Failure.Path, req.Failure.Rate, req.RunID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeactivate(w http.ResponseWriter, r *http.Request) {
	var req DeactivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.registry.Remove(req.FailureType, req.Host, req.Path)
	log.Printf("control: deactivated %s host=%q path=%q run=%s", req.FailureType, req.Host, req.Path, req.RunID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		MemberID:       s.memberID,
		ActiveFailures: s.registry.Active(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
