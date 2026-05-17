package main

import (
	"context"
	"reflect"
	"strings"
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

func TestLockedTokenSemaphoreAvoidsFragmentedAcquireDeadlock(t *testing.T) {
	sem := newLockedTokenSemaphore(3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	releaseA, err := sem.Acquire(ctx, 2)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		releaseB, err := sem.Acquire(ctx, 2)
		if err == nil {
			releaseB()
		}
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("second acquire completed before tokens were released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	releaseA()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second acquire after release: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("second acquire did not complete after release")
	}
}

func TestSummarizeResourcesIncludesNetworkTotalsAndInterfaces(t *testing.T) {
	samples := []resourceSample{
		{
			TimeNS:           int64(1 * time.Second),
			ClockTicks:       100,
			HostTotalJiffies: 1000,
			HostIdleJiffies:  700,
			ProcJiffies:      100,
			RSSBytes:         100 * 1024 * 1024,
			OpenFDs:          10,
			Threads:          4,
			ReadBytes:        1000,
			WriteBytes:       2000,
			NetRXBytes:       1000,
			NetTXBytes:       2000,
			NetCounterSource: "/proc/net/dev",
			NetInterfaces:    []string{"eth1", "eth0"},
			NetExcluded:      []string{"lo"},
		},
		{
			TimeNS:           int64(6 * time.Second),
			ClockTicks:       100,
			HostTotalJiffies: 2000,
			HostIdleJiffies:  1300,
			ProcJiffies:      150,
			RSSBytes:         110 * 1024 * 1024,
			OpenFDs:          12,
			Threads:          4,
			ReadBytes:        1500,
			WriteBytes:       2600,
			NetRXBytes:       2500,
			NetTXBytes:       4300,
			NetCounterSource: "/proc/net/dev",
			NetInterfaces:    []string{"eth0"},
			NetExcluded:      []string{"lo", "docker0"},
		},
		{
			TimeNS:           int64(11 * time.Second),
			ClockTicks:       100,
			HostTotalJiffies: 3000,
			HostIdleJiffies:  1900,
			ProcJiffies:      180,
			RSSBytes:         120 * 1024 * 1024,
			OpenFDs:          14,
			Threads:          5,
			ReadBytes:        1700,
			WriteBytes:       3200,
			NetRXBytes:       4000,
			NetTXBytes:       6000,
			NetCounterSource: "/proc/net/dev",
			NetInterfaces:    []string{"eth1"},
			NetExcluded:      []string{"lo"},
		},
	}

	got := summarizeResources(samples, nil)
	if got.NetCounterSource != "/proc/net/dev" {
		t.Fatalf("NetCounterSource = %q", got.NetCounterSource)
	}
	if !reflect.DeepEqual(got.NetInterfaces, []string{"eth0", "eth1"}) {
		t.Fatalf("NetInterfaces = %#v", got.NetInterfaces)
	}
	if !reflect.DeepEqual(got.NetExcluded, []string{"docker0", "lo"}) {
		t.Fatalf("NetExcluded = %#v", got.NetExcluded)
	}
	if got.HostNetRXBytesTotal != 3000 {
		t.Fatalf("HostNetRXBytesTotal = %v, want 3000", got.HostNetRXBytesTotal)
	}
	if got.HostNetTXBytesTotal != 4000 {
		t.Fatalf("HostNetTXBytesTotal = %v, want 4000", got.HostNetTXBytesTotal)
	}
	if got.HostNetRXBytesPerSecond.Count != 2 {
		t.Fatalf("HostNetRXBytesPerSecond.Count = %d, want 2", got.HostNetRXBytesPerSecond.Count)
	}
}

func TestRenderMarkdownIncludesRealResourceNetworkSummary(t *testing.T) {
	rep := urlOnceReport{
		StartedAt:      time.Date(2026, 5, 17, 1, 2, 3, 0, time.UTC),
		FinishedAt:     time.Date(2026, 5, 17, 1, 7, 3, 0, time.UTC),
		Phase:          "real",
		TargetLocality: "test",
		Notes:          []string{"direct Veriflier calls only"},
		FixtureResults: []modeResult{
			{
				Mode:                   "v2-get-simple_http",
				Endpoint:               "v2",
				URLCount:               10,
				Completed:              10,
				ChecksPerSecond:        2,
				HostNetRXBytesPerCheck: 200,
				HostNetTXBytesPerCheck: 100,
				ResourceSummary: resourceSummary{
					Samples:                 2,
					NetCounterSource:        "/proc/net/dev",
					NetInterfaces:           []string{"eth0"},
					HostNetRXBytesTotal:     2000,
					HostNetTXBytesTotal:     1000,
					HostNetRXBytesPerSecond: statBlock{Avg: 1500},
					HostNetTXBytesPerSecond: statBlock{Avg: 700},
					RSSBytes:                statBlock{Avg: 90 * 1024 * 1024, P95: 95 * 1024 * 1024, Max: 100 * 1024 * 1024},
					OpenFDs:                 statBlock{Avg: 8, P95: 9, Max: 10},
					Threads:                 statBlock{Avg: 4, P95: 4, Max: 4},
				},
			},
		},
		RealResults: []modeResult{
			{
				Mode:                   "v2-get-full",
				Endpoint:               "v2",
				URLCount:               10,
				Completed:              10,
				ChecksPerSecond:        2,
				HostNetRXBytesPerCheck: 123.4,
				HostNetTXBytesPerCheck: 56.7,
				ResourceSummary: resourceSummary{
					Samples:                 3,
					NetCounterSource:        "/proc/net/dev",
					NetInterfaces:           []string{"eth0"},
					HostNetRXBytesTotal:     1234,
					HostNetTXBytesTotal:     567,
					HostNetRXBytesPerSecond: statBlock{Avg: 1000},
					HostNetTXBytesPerSecond: statBlock{Avg: 500},
					RSSBytes:                statBlock{Avg: 100 * 1024 * 1024, P95: 110 * 1024 * 1024, Max: 120 * 1024 * 1024},
					OpenFDs:                 statBlock{Avg: 10, P95: 11, Max: 12},
					Threads:                 statBlock{Avg: 4, P95: 4, Max: 4},
				},
			},
		},
	}

	md := renderMarkdown(rep)
	for _, want := range []string{
		"## Fixture Resource Samples",
		"## Real URL Resource Samples",
		"Net RX/TX total MiB",
		"Net RX/TX per completed check B",
		"/proc/net/dev",
		"`eth0`",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered markdown does not contain %q:\n%s", want, md)
		}
	}
}
