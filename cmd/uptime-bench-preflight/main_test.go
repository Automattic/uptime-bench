package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Automattic/uptime-bench/internal/preflight"
)

func TestSplitCSVTrimsAndDropsEmptyParts(t *testing.T) {
	got := splitCSV(" scenarios/http-503.toml, ,scenarios/http-partial.toml,")
	want := []string{"scenarios/http-503.toml", "scenarios/http-partial.toml"}
	if len(got) != len(want) {
		t.Fatalf("splitCSV length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitCSV[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestWriteTableIncludesDiagnosticsOnlyReport(t *testing.T) {
	var buf bytes.Buffer
	err := write(&buf, "table", preflight.Report{
		Diagnostics: []preflight.Diagnostic{{
			Level:   "warn",
			Subject: "scenarios/http-geo-503.toml",
			Message: "region missing probe ranges",
		}},
	})
	if err != nil {
		t.Fatalf("write table: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"level", "subject", "message", "warn", "region missing probe ranges"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output missing %q:\n%s", want, out)
		}
	}
}

func TestWriteRejectsUnknownFormat(t *testing.T) {
	err := write(&bytes.Buffer{}, "yaml", preflight.Report{})
	if err == nil || !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("write error = %v, want unknown format", err)
	}
}
