package jetmoncapacity

import (
	"strings"
	"testing"
)

func TestScanStrictSchemaRuntimeLogIgnoresStreamingErrorCounters(t *testing.T) {
	log := strings.Join([]string{
		`2026/05/13 15:00:00 orchestrator: streaming summary active=24 completed=24 failures=6 error_timeout=1 error_connect=2 error_keyword=3 error_body_read=4 error_tls_expired=0 error_other=0`,
		`2026/05/13 15:00:01 orchestrator: streaming summary active=24 completed=24 failures=0 successes=24 error_timeout=0 error_keyword=0`,
	}, "\n")

	result := ScanStrictSchemaRuntimeLog(log)
	if result.Status != StrictSchemaRuntimeScanClean {
		t.Fatalf("Status = %q, want %q; findings=%+v", result.Status, StrictSchemaRuntimeScanClean, result.Findings)
	}
	if len(result.Findings) != 0 {
		t.Fatalf("Findings = %+v, want none", result.Findings)
	}
	if got := StrictSchemaRuntimeScanSummary(result); got != "Strict schema/runtime scan: clean" {
		t.Fatalf("summary = %q", got)
	}
}

func TestScanStrictSchemaRuntimeLogFindsSchemaAndRuntimeFailures(t *testing.T) {
	log := strings.Join([]string{
		`2026/05/13 15:00:00 orchestrator: streaming summary active=24 error_timeout=1`,
		`SQLSTATE[42S22]: Column not found: 1054 Unknown column 'check_method' in 'field list'`,
		`Error 1146: Table 'jetmon.site_runtime' doesn't exist`,
		`panic: runtime error: invalid memory address or nil pointer dereference`,
		`lookup site-000001.example.test on 127.0.0.53: no such host`,
		`Error 1264: Out of range value for column 'bucket_no' at row 1`,
		`Error 1406: Data too long for column 'monitor_url' at row 1`,
		`Error 1048: Column 'monitor_url' cannot be null`,
		`Cannot add or update a child row: a foreign key constraint fails`,
	}, "\n")

	result := ScanStrictSchemaRuntimeLog(log)
	if result.Status != StrictSchemaRuntimeScanFound {
		t.Fatalf("Status = %q, want %q", result.Status, StrictSchemaRuntimeScanFound)
	}
	if len(result.Findings) != 8 {
		t.Fatalf("Findings = %d, want 8: %+v", len(result.Findings), result.Findings)
	}
	wantPatterns := []string{
		"unknown column",
		"missing table",
		"panic",
		"no such host",
		"out of range value",
		"data too long",
		"cannot be null",
		"foreign key constraint",
	}
	for i, want := range wantPatterns {
		if result.Findings[i].Pattern != want {
			t.Fatalf("finding %d pattern = %q, want %q", i, result.Findings[i].Pattern, want)
		}
	}
}
