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
	assertContains(t, sql, "INSERT INTO jetpack_monitor_event_transitions")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_site_runtime WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000002")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_site_check_config WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000002")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_sites WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000002;")
	assertContains(t, sql, "(8000001000000000, 7, 'http://site-0000001.load.example.test/'")
	assertContains(t, sql, "(8000001000000001, 8, 'http://site-0000002.load.example.test/'")
	assertContains(t, sql, "(8000001000000002, 7, 'http://site-0000003.load.example.test/'")
	assertNotContains(t, sql, "jetmon_events")
	assertNotContains(t, sql, "jetmon_site_runtime")
	assertNotContains(t, sql, "jetmon_site_check_config")
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
			Schema:               SchemaV2,
			BlogIDStart:          100,
			Count:                10,
			URLPattern:           "http://site-%d.example.test/",
			CheckIntervalMinutes: 5,
		},
		ActiveCount: 3,
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "WHERE blog_id BETWEEN 100 AND 109")
	assertContains(t, sql, "WHERE blog_id BETWEEN 100 AND 102")
	assertContains(t, sql, "monitor_active = 1")
	assertContains(t, sql, "check_interval = 5")
	assertContains(t, sql, "last_checked_at = NULL,\n       next_check_at = NULL,\n       last_alert_sent_at = NULL")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_site_runtime WHERE blog_id BETWEEN 100 AND 109")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_site_check_config WHERE blog_id BETWEEN 100 AND 102")
}

func TestWriteActivateSQLV2AppliesExplicitCheckPolicy(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationActivate,
		Config: Config{
			Schema:               SchemaV2,
			BlogIDStart:          100,
			Count:                10,
			URLPattern:           "http://site-%d.example.test/",
			CheckIntervalMinutes: 5,
			BatchSize:            2,
			RequestMethod:        "get",
			DetectionProfile:     "FULL",
		},
		ActiveCount: 3,
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "INSERT INTO jetpack_monitor_site_check_config")
	assertContains(t, sql, "(blog_id, request_method, detection_profile)")
	assertContains(t, sql, "(100, ''GET'', ''full''),")
	assertContains(t, sql, "(101, ''GET'', ''full'')")
	assertContains(t, sql, "(102, ''GET'', ''full'')")
	assertContains(t, sql, "ON DUPLICATE KEY UPDATE request_method = VALUES(request_method), detection_profile = VALUES(detection_profile)")
	if got := strings.Count(sql, "INSERT INTO jetpack_monitor_site_check_config"); got != 2 {
		t.Fatalf("check config insert batches = %d, want 2", got)
	}
}

func TestRenderActiveCheckIntervalSQL(t *testing.T) {
	sql, err := RenderActiveCheckIntervalSQL(Config{
		Schema:      SchemaV2,
		BlogIDStart: 100,
		Count:       10,
		URLPattern:  "http://site-%d.example.test/",
	})
	if err != nil {
		t.Fatalf("RenderActiveCheckIntervalSQL: %v", err)
	}
	assertContains(t, sql, "SELECT\n  check_interval,\n  COUNT(*) AS active_sites")
	assertContains(t, sql, "GROUP BY check_interval")
}

func TestWriteDeactivateSQLV2ResetsSchedulerState(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationDeactivate,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 100,
			Count:       10,
			URLPattern:  "http://site-%d.example.test/",
		},
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "last_checked_at = NULL,\n       next_check_at = NULL,\n       last_alert_sent_at = NULL")
	assertContains(t, sql, "DELETE FROM jetpack_monitor_site_runtime WHERE blog_id BETWEEN 100 AND 109")
	assertContains(t, sql, "-- Deactivate every benchmark-owned site row. Do this before closing events.")
	assertContains(t, sql, "DO SLEEP(5);")
	assertContains(t, sql, "DO SLEEP(2);")
	if got := strings.Count(sql, "INSERT INTO jetpack_monitor_event_transitions"); got != 2 {
		t.Fatalf("event close passes = %d, want 2", got)
	}
	deactivateIndex := strings.Index(sql, "UPDATE jetpack_monitor_sites")
	closeIndex := strings.Index(sql, "INSERT INTO jetpack_monitor_event_transitions")
	if deactivateIndex < 0 || closeIndex < 0 {
		t.Fatalf("missing deactivate or close SQL")
	}
	if deactivateIndex > closeIndex {
		t.Fatalf("deactivation must happen before event cleanup to avoid late open event races")
	}
}

func TestWriteVerifySQLV2UsesStableFreshnessSnapshot(t *testing.T) {
	var out bytes.Buffer
	plan := Plan{
		Action: OperationVerify,
		Config: Config{
			Schema:      SchemaV2,
			BlogIDStart: 100,
			Count:       10,
			URLPattern:  "http://site-%d.example.test/",
		},
		FreshSinceMinutes: 7,
	}
	if err := WriteSQL(&out, plan); err != nil {
		t.Fatalf("WriteSQL: %v", err)
	}
	sql := out.String()
	assertContains(t, sql, "SET @uptime_bench_now := UTC_TIMESTAMP();")
	assertContains(t, sql, "SET @uptime_bench_freshness_cutoff := @uptime_bench_now - INTERVAL 7 MINUTE;")
	assertContains(t, sql, "CREATE TEMPORARY TABLE uptime_bench_active_freshness AS")
	assertContains(t, sql, "LEFT JOIN jetpack_monitor_site_runtime r ON r.blog_id = s.blog_id")
	assertContains(t, sql, "CASE\n    WHEN r.last_checked_at IS NULL OR r.last_checked_at < @uptime_bench_freshness_cutoff THEN 1")
	assertContains(t, sql, "COALESCE(SUM(is_stale), 0) AS stale_active_sites")
	assertContains(t, sql, "FROM uptime_bench_active_freshness")
	assertContains(t, sql, "last_checked_at < @uptime_bench_freshness_cutoff")
	assertContains(t, sql, "checked_at >= @uptime_bench_freshness_cutoff")
	assertContains(t, sql, "TIMESTAMPDIFF(SECOND, last_checked_at, @uptime_bench_now)")
	assertNotContains(t, sql, "last_checked_at < UTC_TIMESTAMP() - INTERVAL")
	assertNotContains(t, sql, "checked_at >= UTC_TIMESTAMP() - INTERVAL")
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

func TestValidateRejectsUnsupportedCheckPolicy(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "method",
			cfg: Config{
				Schema:        SchemaV2,
				BlogIDStart:   100,
				Count:         1,
				URLPattern:    "http://site-%d.example.test/",
				RequestMethod: "POST",
			},
		},
		{
			name: "profile",
			cfg: Config{
				Schema:           SchemaV2,
				BlogIDStart:      100,
				Count:            1,
				URLPattern:       "http://site-%d.example.test/",
				DetectionProfile: "deep",
			},
		},
		{
			name: "v1",
			cfg: Config{
				Schema:        SchemaV1,
				BlogIDStart:   100,
				Count:         1,
				URLPattern:    "http://site-%d.example.test/",
				RequestMethod: "GET",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}
}

func TestRenderSeedSafetySQLEscapesGeneratedNamespace(t *testing.T) {
	sql, err := RenderSeedSafetySQL(Config{
		Schema:      SchemaV2,
		BlogIDStart: 100,
		Count:       10,
		URLPattern:  "http://site-%07d.load.example.test/path_value/",
	})
	if err != nil {
		t.Fatalf("RenderSeedSafetySQL: %v", err)
	}
	assertContains(t, sql, "monitor_url LIKE 'http://site-%.load.example.test/path\\_value/' ESCAPE '\\\\'")
	assertContains(t, sql, "WHERE blog_id BETWEEN 100 AND 109")
}

func TestRenderSeedSafetySQLRejectsMultiplePlaceholders(t *testing.T) {
	_, err := RenderSeedSafetySQL(Config{
		Schema:      SchemaV2,
		BlogIDStart: 100,
		Count:       10,
		URLPattern:  "http://site-%07d-%02d.load.example.test/",
	})
	if err == nil {
		t.Fatal("RenderSeedSafetySQL succeeded, want placeholder error")
	}
}

func TestRenderActiveURLSamplesSQLUsesBucketAndEdgeSamples(t *testing.T) {
	sql, err := RenderActiveURLSamplesSQL(Config{
		Schema:      SchemaV2,
		BlogIDStart: 8000001000000000,
		Count:       1000,
		URLPattern:  "http://site-%07d.load.example.test/",
		BucketMin:   0,
		BucketMax:   9,
	}, 1000)
	if err != nil {
		t.Fatalf("RenderActiveURLSamplesSQL: %v", err)
	}
	assertContains(t, sql, "blog_id,\n  bucket_no,\n  monitor_url")
	assertContains(t, sql, "8000001000000000")
	assertContains(t, sql, "8000001000000009")
	assertContains(t, sql, "8000001000000500")
	assertContains(t, sql, "8000001000000999")
	assertContains(t, sql, "monitor_active = 1")
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
