package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench-certmint/internal/library"
	"github.com/Automattic/uptime-bench-certmint/internal/manifest"
)

func TestRunPlanOutputsDueOrders(t *testing.T) {
	configPath := writeCommandConfig(t, t.TempDir())

	out, err := captureStdout(t, func() error {
		return run(context.Background(), []string{"plan", "-config", configPath})
	})
	if err != nil {
		t.Fatalf("run plan error = %v", err)
	}

	var got []struct {
		Environment string   `json:"environment"`
		Domain      string   `json:"domain"`
		Profile     string   `json:"profile"`
		SlotDate    string   `json:"slot_date"`
		Slot        int      `json:"slot"`
		CertName    string   `json:"cert_name"`
		Identifiers []string `json:"identifiers"`
		Command     string   `json:"command"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("plan output is not JSON: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("plan returned %d orders, want 1: %+v", len(got), got)
	}
	today := time.Now().UTC().Format("20060102")
	if got[0].Domain != "bench.example.com" || got[0].Profile != "classic" || got[0].SlotDate != today || got[0].Slot != 0 {
		t.Fatalf("planned order = %+v", got[0])
	}
	if got[0].Environment != "staging" {
		t.Fatalf("Environment = %q, want staging", got[0].Environment)
	}
	if !strings.HasPrefix(got[0].CertName, "ub-certmint-staging-") {
		t.Fatalf("CertName = %q, want staging prefix", got[0].CertName)
	}
	if got[0].Identifiers[1] != "cert-"+today+"-00-classic.unique.bench.example.com" {
		t.Fatalf("unique SAN = %q", got[0].Identifiers[1])
	}
	if !strings.Contains(got[0].Command, `"--staging"`) || !strings.Contains(got[0].Command, `"-d" "bench.example.com"`) {
		t.Fatalf("command missing expected certbot args: %s", got[0].Command)
	}
}

func TestRunOnceDryRunPrintsCommandsAndDoesNotWriteManifest(t *testing.T) {
	tmp := t.TempDir()
	configPath := writeCommandConfig(t, tmp)

	out, err := captureStdout(t, func() error {
		return run(context.Background(), []string{"once", "-config", configPath, "-dry-run"})
	})
	if err != nil {
		t.Fatalf("run once -dry-run error = %v", err)
	}
	if !strings.Contains(out, `"certbot" "certonly"`) || !strings.Contains(out, `"--staging"`) {
		t.Fatalf("dry-run output missing expected certbot command: %s", out)
	}
	if _, err := os.Stat(filepath.Join(tmp, "library", "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run manifest stat error = %v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "library", "staging", "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run staging manifest stat error = %v, want not exist", err)
	}
}

func TestRunInspectSortsEntriesByNotAfter(t *testing.T) {
	libraryDir := t.TempDir()
	late := manifest.Entry{
		ID:       "late",
		NotAfter: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	early := manifest.Entry{
		ID:       "early",
		NotAfter: time.Date(2026, 4, 28, 0, 0, 0, 0, time.UTC),
	}
	if err := manifest.Save(library.ManifestPath(libraryDir), manifest.Manifest{Entries: []manifest.Entry{late, early}}); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return run(context.Background(), []string{"inspect", "-library", libraryDir})
	})
	if err != nil {
		t.Fatalf("run inspect error = %v", err)
	}

	var got manifest.Manifest
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("inspect output is not JSON: %v\n%s", err, out)
	}
	if len(got.Entries) != 2 || got.Entries[0].ID != "early" || got.Entries[1].ID != "late" {
		t.Fatalf("inspect entries = %+v, want sorted by NotAfter", got.Entries)
	}
}

func writeCommandConfig(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	data, err := json.Marshal(map[string]any{
		"library_dir":   filepath.Join(dir, "library"),
		"state_dir":     filepath.Join(dir, "state"),
		"poll_interval": "15m",
		"certbot": map[string]any{
			"binary":             "certbot",
			"email":              "ops@example.com",
			"agree_tos":          true,
			"staging":            true,
			"authenticator_args": []string{"--manual"},
		},
		"domains": []map[string]any{
			{
				"name":                "bench.example.com",
				"identifiers":         []string{"bench.example.com"},
				"unique_san_template": "cert-{date}-{slot}-{profile}.unique.bench.example.com",
				"profiles": []map[string]any{
					{"name": "classic", "per_day": 1},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	fnErr := fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(reader)
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if fnErr != nil {
		return string(data), fnErr
	}
	return string(data), readErr
}
