package capacitybench

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteMarkdownIncludesAnalysisAndRawSummary(t *testing.T) {
	report := Report{
		PrometheusURL: "http://prometheus.example.com:9090",
		Start:         time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		End:           time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC),
		Step:          "15s",
		Instances:     []string{"jetmon-v1.example.com", "jetmon-v2.example.com"},
		Summaries: []SeriesSummary{
			{
				Query:   "scrape_up",
				Unit:    "state",
				Labels:  map[string]string{"instance": "jetmon-v1.example.com", "job": "node"},
				Samples: 3,
				Min:     1,
				Avg:     1,
				P50:     1,
				P95:     1,
				Max:     1,
				Last:    1,
			},
			{
				Query:   "host_cpu_used",
				Unit:    "percent",
				Labels:  map[string]string{"instance": "jetmon-v2.example.com"},
				Samples: 3,
				Min:     10,
				Avg:     40,
				P50:     50,
				P95:     91,
				Max:     92,
				Last:    35,
			},
		},
	}

	var buf bytes.Buffer
	if err := WriteMarkdown(&buf, report); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"# Capacity Report",
		"## Analysis",
		"host_cpu_used",
		"max 92.00 reached or exceeded 85%",
		"## Raw Window Summary",
		"jetmon-v1.example.com/job=node",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("markdown output missing %q:\n%s", want, out)
		}
	}
}

func TestAnalyzeFlagsUnhealthyScrape(t *testing.T) {
	report := Report{
		Summaries: []SeriesSummary{
			{
				Query:  "dockerstats_scrape_success",
				Unit:   "state",
				Labels: map[string]string{"instance": "jetmon-v1.example.com"},
				Min:    0,
			},
		},
	}
	findings := Analyze(report)
	if len(findings) != 1 {
		t.Fatalf("findings = %d, want 1", len(findings))
	}
	if findings[0].Status != "fail" {
		t.Fatalf("status = %q, want fail", findings[0].Status)
	}
}

func TestFormatValueHandlesOpsPerSecond(t *testing.T) {
	if got := FormatValue("ops_per_second", 12.345); got != "12.35/s" {
		t.Fatalf("FormatValue = %q, want 12.35/s", got)
	}
}
