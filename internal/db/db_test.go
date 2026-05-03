package db

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestMetricTextValueTruncatesToColumnLimit(t *testing.T) {
	long := strings.Repeat("x", derivedMetricTextMaxChars+20)
	got := metricTextValue(long)
	if got == nil {
		t.Fatal("metricTextValue returned nil for non-empty input")
	}
	if utf8.RuneCountInString(*got) != derivedMetricTextMaxChars {
		t.Fatalf("metric text length = %d, want %d", utf8.RuneCountInString(*got), derivedMetricTextMaxChars)
	}
	if !strings.HasSuffix(*got, "...") {
		t.Fatalf("metric text should end with truncation suffix, got %q", *got)
	}
}

func TestMetricTextValueKeepsShortAndEmptyValues(t *testing.T) {
	if got := metricTextValue(""); got != nil {
		t.Fatalf("empty metric text = %q, want nil", *got)
	}
	const short = "rate limited"
	got := metricTextValue(short)
	if got == nil || *got != short {
		t.Fatalf("short metric text = %v, want %q", got, short)
	}
}

func TestCampaignExportTableRejectsUnsupportedTableWithoutQuerying(t *testing.T) {
	var d DB
	if _, err := d.CampaignExportTable(context.Background(), nil, "secrets"); err == nil {
		t.Fatal("CampaignExportTable accepted unsupported table")
	}
}

func TestCampaignExportTableWithoutCampaignsReturnsHeader(t *testing.T) {
	var d DB
	table, err := d.CampaignExportTable(context.Background(), nil, "scenario_runs")
	if err != nil {
		t.Fatalf("CampaignExportTable: %v", err)
	}
	if table.Name != "scenario_runs" || len(table.Header) == 0 || len(table.Rows) != 0 {
		t.Fatalf("table = %+v, want scenario_runs header with no rows", table)
	}
}

func TestFormatExportValue(t *testing.T) {
	ts := time.Date(2026, 5, 1, 10, 0, 0, 123, time.FixedZone("offset", -5*60*60))
	if got := formatExportValue(ts); got != "2026-05-01T15:00:00.000000123Z" {
		t.Fatalf("time export = %q", got)
	}
	if got := formatExportValue([]byte("json")); got != "json" {
		t.Fatalf("bytes export = %q", got)
	}
	if got := formatExportValue(nil); got != "" {
		t.Fatalf("nil export = %q", got)
	}
}
