package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseInsertValuesLine(t *testing.T) {
	line := "INSERT INTO `jetpack_monitor_sites` VALUES " +
		"(1,100,1,'https://example.com/a,b',1,1,'2026-05-16 00:00:00',5)," +
		"(2,101,2,'http://example.net/it\\'s-ok',0,1,'2026-05-16 00:00:00',5)," +
		"(3,102,3,'https://example.org/doubled''quote',1,2,'2026-05-16 00:00:00',5);"

	var got [][]string
	err := parseInsertValuesLine(line, func(fields []string) error {
		got = append(got, append([]string(nil), fields...))
		return nil
	})
	if err != nil {
		t.Fatalf("parseInsertValuesLine returned error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("row count = %d, want 3", len(got))
	}
	if got[0][3] != "https://example.com/a,b" {
		t.Fatalf("first URL = %q", got[0][3])
	}
	if got[1][3] != "http://example.net/it's-ok" {
		t.Fatalf("escaped quote URL = %q", got[1][3])
	}
	if got[2][3] != "https://example.org/doubled'quote" {
		t.Fatalf("doubled quote URL = %q", got[2][3])
	}
}

func TestParseInsertValuesLineDetectsUnterminatedRows(t *testing.T) {
	err := parseInsertValuesLine("INSERT INTO jetpack_monitor_sites VALUES (1,2,3,'unterminated", func(fields []string) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected unterminated row error")
	}
}

func TestDryRunPlanRequestCounts(t *testing.T) {
	modes := defaultModes()
	plan := buildDryRunPlan(17, modes, 1, 40, 25, 8*time.Second)
	if plan.ExpectedTotalRequests != 68 {
		t.Fatalf("ExpectedTotalRequests = %d, want 68", plan.ExpectedTotalRequests)
	}
	want := map[string]int{
		"v1-legacy":          17,
		"v2-head-legacy":     17,
		"v2-get-simple_http": 17,
		"v2-get-full":        17,
	}
	if !reflect.DeepEqual(plan.ExpectedRequestsByMode, want) {
		t.Fatalf("ExpectedRequestsByMode = %#v, want %#v", plan.ExpectedRequestsByMode, want)
	}
}

func TestMakeHostAwareBatchesAvoidsDuplicateHostsWithinBatch(t *testing.T) {
	checks := []urlCheck{
		{SyntheticBlogID: 1, Host: "a.example"},
		{SyntheticBlogID: 2, Host: "b.example"},
		{SyntheticBlogID: 3, Host: "a.example"},
		{SyntheticBlogID: 4, Host: "c.example"},
	}
	batches := makeHostAwareBatches(checks, 3)
	if len(batches) != 2 {
		t.Fatalf("batch count = %d, want 2", len(batches))
	}
	for _, batch := range batches {
		seen := map[string]bool{}
		for _, check := range batch {
			if seen[check.Host] {
				t.Fatalf("duplicate host %q in batch %#v", check.Host, batch)
			}
			seen[check.Host] = true
		}
	}
}

func TestMakeHostAwareBatchesHonorsBatchSize(t *testing.T) {
	checks := []urlCheck{
		{SyntheticBlogID: 1, Host: "a.example"},
		{SyntheticBlogID: 2, Host: "b.example"},
		{SyntheticBlogID: 3, Host: "c.example"},
	}
	batches := makeHostAwareBatches(checks, 2)
	if got := []int{len(batches[0]), len(batches[1])}; !reflect.DeepEqual(got, []int{2, 1}) {
		t.Fatalf("batch sizes = %#v, want [2 1]", got)
	}
}
