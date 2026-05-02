package jetmoncapacity

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteSeedSQLV2BatchesRows(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationSeed,
		Config: Config{
			Schema:               SchemaV2,
			BlogIDStart:          8_000_001_000_000_000,
			Count:                3,
			URLPattern:           "http://site-%07d.load.example.test/",
			URLNumberStart:       1,
			BucketMin:            7,
			BucketMax:            8,
			CheckIntervalMinutes: 1,
			BatchSize:            2,
		},
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "INSERT INTO jetmon_event_transitions")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_sites WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000002;")
	assertContains(t, sql, "(8000001000000000, 7, 'http://site-0000001.load.example.test/'")
	assertContains(t, sql, "(8000001000000001, 8, 'http://site-0000002.load.example.test/'")
	assertContains(t, sql, "(8000001000000002, 7, 'http://site-0000003.load.example.test/'")
	if got := strings.Count(sql, "INSERT INTO jetpack_monitor_sites"); got != 2 {
		t.Fatalf("insert batches = %d, want 2", got)
	}
}

func TestWriteSeedSQLV1UsesBaseColumns(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationSeed,
		Config: Config{
			Schema:               SchemaV1,
			BlogIDStart:          8_000_000_000_000_000,
			Count:                1,
			URLPattern:           "https://site-%03d.example.test/check",
			URLNumberStart:       9,
			BucketMin:            0,
			BucketMax:            0,
			CheckIntervalMinutes: 5,
			BatchSize:            100,
		},
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "(blog_id, bucket_no, monitor_url, monitor_active, site_status, last_status_change, check_interval)")
	assertContains(t, sql, "(8000000000000000, 0, 'https://site-009.example.test/check', 0, 1, UTC_TIMESTAMP(), 5);")
	assertNotContains(t, sql, "jetmon_events")
	assertNotContains(t, sql, "last_checked_at")
}

func TestActivateSQLRequiresActiveCountInsideRange(t *testing.T) {
	plan := Plan{
		Action: OperationActivate,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 10,
			Count:       5,
			URLPattern:  "http://site-%d.example.test/",
		},
		ActiveCount: 6,
	}
	var out bytes.Buffer
	if err := WriteSQL(&out, plan); err == nil {
		t.Fatal("WriteSQL succeeded, want active-count validation error")
	}
}

func TestWriteActivateSQL(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationActivate,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 100,
			Count:       10,
			URLPattern:  "http://site-%d.example.test/",
		},
		ActiveCount: 3,
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "WHERE blog_id BETWEEN 100 AND 109;")
	assertContains(t, sql, "WHERE blog_id BETWEEN 100 AND 102;")
	assertContains(t, sql, "monitor_active = 1")
}

func TestValidateRejectsBadURLPattern(t *testing.T) {
	plan := Plan{
		Action: OperationSeed,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 100,
			Count:       1,
			URLPattern:  "http://example.test/no-placeholder",
		},
	}
	var out bytes.Buffer
	if err := WriteSQL(&out, plan); err == nil {
		t.Fatal("WriteSQL succeeded, want URL pattern validation error")
	}
}

func assertContains(t *testing.T, value, needle string) {
	t.Helper()
	if !strings.Contains(value, needle) {
		t.Fatalf("value does not contain %q:\n%s", needle, value)
	}
}

func assertNotContains(t *testing.T, value, needle string) {
	t.Helper()
	if strings.Contains(value, needle) {
		t.Fatalf("value contains %q:\n%s", needle, value)
	}
}
