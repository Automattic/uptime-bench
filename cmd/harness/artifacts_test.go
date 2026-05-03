package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
)

func TestHarnessArtifactsCopyInputAndWriteRunResult(t *testing.T) {
	root := t.TempDir()
	inputDir := filepath.Join(root, "inputs")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatalf("mkdir input: %v", err)
	}
	scenarioPath := filepath.Join(inputDir, "http-503.toml")
	if err := os.WriteFile(scenarioPath, []byte("id = \"http-503\"\n"), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}

	artifacts := &harnessArtifacts{dir: filepath.Join(root, "reports")}
	if err := artifacts.CopyInput("scenario", scenarioPath); err != nil {
		t.Fatalf("CopyInput: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(root, "reports", "scenarios", "http-503.toml"))
	if err != nil {
		t.Fatalf("read copied scenario: %v", err)
	}
	if string(copied) != "id = \"http-503\"\n" {
		t.Fatalf("copied scenario = %q", string(copied))
	}

	startedAt := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(2 * time.Minute)
	if err := artifacts.WriteRunResult(controllerRunResult{
		Kind:      "scenario",
		ID:        "http-503",
		RunID:     "run-1",
		StartedAt: startedAt,
		EndedAt:   endedAt,
	}); err != nil {
		t.Fatalf("WriteRunResult: %v", err)
	}
	if err := artifacts.WriteRunResult(controllerRunResult{
		Kind:      "scenario",
		ID:        "http-timeout",
		RunID:     "run-2",
		StartedAt: startedAt,
		EndedAt:   endedAt,
		Err:       errForTest("adapter\tfailed"),
	}); err != nil {
		t.Fatalf("WriteRunResult append: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "reports", "run-results.tsv"))
	if err != nil {
		t.Fatalf("read run-results: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("run-results lines = %d, want 3:\n%s", len(lines), string(data))
	}
	if !strings.Contains(lines[1], "\tok\t") {
		t.Fatalf("success row = %q, want ok status", lines[1])
	}
	if !strings.Contains(lines[2], `adapter\tfailed`) {
		t.Fatalf("failure row = %q, want escaped error", lines[2])
	}
}

func TestCollectFleetStatusDedupesMembersAndRecordsErrors(t *testing.T) {
	const token = "secret"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(control.StatusResponse{
			MemberID: "target-01",
			ActiveFailures: []control.FailureSpec{{
				Type: "http_status",
				Host: "bench.example.test",
			}},
		})
	}))
	defer target.Close()

	nameserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer nameserver.Close()

	targetHost, targetPort := serverHostPort(t, target.URL)
	nsHost, nsPort := serverHostPort(t, nameserver.URL)
	fl := &fleet.Config{
		Control: fleet.ControlConfig{Timeout: time.Second},
		Targets: []fleet.Target{
			{ID: "bench-a", Address: targetHost, ControlPort: targetPort},
			{ID: "bench-b", Address: targetHost, ControlPort: targetPort},
		},
		Nameservers: []fleet.Nameserver{
			{ID: "ns-01", Address: nsHost, ControlPort: nsPort},
		},
	}

	snapshot := collectFleetStatus(context.Background(), fl, token, time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC))
	if len(snapshot.Members) != 2 {
		t.Fatalf("members = %#v, want target aggregate plus nameserver", snapshot.Members)
	}
	if snapshot.CleanupStatus != "fail" {
		t.Fatalf("cleanup status = %q, want fail", snapshot.CleanupStatus)
	}
	if snapshot.ActiveFailureCount != 1 {
		t.Fatalf("active failure count = %d, want 1", snapshot.ActiveFailureCount)
	}
	if snapshot.MemberErrorCount != 1 {
		t.Fatalf("member error count = %d, want 1", snapshot.MemberErrorCount)
	}
	targetStatus := snapshot.Members[0]
	if targetStatus.Role != "target" {
		t.Fatalf("first role = %q, want target", targetStatus.Role)
	}
	if strings.Join(targetStatus.ConfigIDs, ",") != "bench-a,bench-b" {
		t.Fatalf("target config IDs = %#v", targetStatus.ConfigIDs)
	}
	if targetStatus.Status == nil || targetStatus.Status.MemberID != "target-01" {
		t.Fatalf("target status = %#v", targetStatus.Status)
	}
	if len(targetStatus.Status.ActiveFailures) != 1 {
		t.Fatalf("active failures = %#v", targetStatus.Status.ActiveFailures)
	}
	if snapshot.Members[1].Error == "" {
		t.Fatalf("nameserver error empty, want status failure")
	}
}

func TestWriteTargetStatusAfterWritesSnapshot(t *testing.T) {
	const token = "secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(control.StatusResponse{MemberID: "target-01"})
	}))
	defer server.Close()

	host, port := serverHostPort(t, server.URL)
	root := t.TempDir()
	tokenPath := filepath.Join(root, "control-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	artifacts := &harnessArtifacts{dir: filepath.Join(root, "reports")}
	if err := artifacts.WriteTargetStatusAfter(&fleet.Config{
		Control: fleet.ControlConfig{Timeout: time.Second, AuthTokenFile: tokenPath},
		Targets: []fleet.Target{
			{ID: "bench-a", Address: host, ControlPort: port},
		},
	}); err != nil {
		t.Fatalf("WriteTargetStatusAfter: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, "reports", "target-status-after.json"))
	if err != nil {
		t.Fatalf("read target-status-after: %v", err)
	}
	var snapshot fleetStatusSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snapshot.Members) != 1 || snapshot.Members[0].Status.MemberID != "target-01" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.CleanupStatus != "pass" {
		t.Fatalf("cleanup status = %q, want pass", snapshot.CleanupStatus)
	}
}

func TestWriteTargetStatusAfterFailsWhenFailuresRemain(t *testing.T) {
	const token = "secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(control.StatusResponse{
			MemberID: "target-01",
			ActiveFailures: []control.FailureSpec{{
				Type: "http_status",
				Host: "bench.example.test",
			}},
		})
	}))
	defer server.Close()

	host, port := serverHostPort(t, server.URL)
	root := t.TempDir()
	tokenPath := filepath.Join(root, "control-token")
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	artifacts := &harnessArtifacts{dir: filepath.Join(root, "reports")}
	err := artifacts.WriteTargetStatusAfter(&fleet.Config{
		Control: fleet.ControlConfig{Timeout: time.Second, AuthTokenFile: tokenPath},
		Targets: []fleet.Target{
			{ID: "bench-a", Address: host, ControlPort: port},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "active_failures=1") {
		t.Fatalf("WriteTargetStatusAfter error = %v, want active failure error", err)
	}

	data, readErr := os.ReadFile(filepath.Join(root, "reports", "target-status-after.json"))
	if readErr != nil {
		t.Fatalf("read target-status-after: %v", readErr)
	}
	var snapshot fleetStatusSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.CleanupStatus != "fail" || snapshot.ActiveFailureCount != 1 {
		t.Fatalf("snapshot = %#v, want failed cleanup with one active failure", snapshot)
	}
}

type errForTest string

func (e errForTest) Error() string { return string(e) }

func serverHostPort(t *testing.T, rawURL string) (string, int) {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	host, portRaw, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}
