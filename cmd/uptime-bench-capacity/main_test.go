package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

func TestResolveWindowDefaultsStartFromDuration(t *testing.T) {
	end := "2026-04-30T18:00:00Z"
	start, gotEnd, err := resolveWindow("", end, 15*time.Minute)
	if err != nil {
		t.Fatalf("resolveWindow: %v", err)
	}
	if gotEnd.Format(time.RFC3339) != end {
		t.Fatalf("end = %s, want %s", gotEnd.Format(time.RFC3339), end)
	}
	if got := start.Format(time.RFC3339); got != "2026-04-30T17:45:00Z" {
		t.Fatalf("start = %s", got)
	}
}

func TestParseTimeArgUnixTimestamp(t *testing.T) {
	got, err := parseTimeArg("1777574400")
	if err != nil {
		t.Fatalf("parseTimeArg: %v", err)
	}
	if got.Format(time.RFC3339) != "2026-04-30T18:40:00Z" {
		t.Fatalf("time = %s", got.Format(time.RFC3339))
	}
}

func TestWriteTableIncludesSummary(t *testing.T) {
	report := capacitybench.Report{
		PrometheusURL: "http://prometheus",
		Start:         time.Date(2026, 4, 30, 18, 0, 0, 0, time.UTC),
		End:           time.Date(2026, 4, 30, 18, 15, 0, 0, time.UTC),
		Step:          "15s",
		Summaries: []capacitybench.SeriesSummary{
			{
				Query:   "host_cpu_used",
				Unit:    "percent",
				Labels:  map[string]string{"instance": "jetmon-v1.example.com"},
				Samples: 3,
				Avg:     12.345,
				P95:     20,
				Max:     21,
				Last:    11,
			},
		},
	}
	var buf bytes.Buffer
	writeTable(&buf, report)
	out := buf.String()
	for _, want := range []string{"host_cpu_used", "jetmon-v1.example.com", "12.35"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table output missing %q:\n%s", want, out)
		}
	}
}
