package jetmoncapacity

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	SchemaV1 = "v1"
	SchemaV2 = "v2"

	OperationSeed       Operation = "seed"
	OperationActivate   Operation = "activate"
	OperationDeactivate Operation = "deactivate"
	OperationVerify     Operation = "verify"

	defaultURLPattern           = "http://site-%07d.load.example.com/"
	defaultHostPattern          = "site-%07d.load.example.com"
	defaultBlogIDStart          = int64(8_000_000_000_000_000)
	defaultCount                = 1_000_000
	defaultURLNumberStart       = int64(1)
	defaultBucketMin            = 0
	defaultBucketMax            = 99
	defaultCheckIntervalMinutes = 1
	defaultBatchSize            = 1000
	defaultFreshSinceMinutes    = 5
	maxInt64                    = int64(1<<63 - 1)
)

// Operation is a benchmark site lifecycle action.
type Operation string

// Config describes the benchmark-owned Jetmon site namespace.
type Config struct {
	Schema               string
	BlogIDStart          int64
	Count                int
	URLPattern           string
	URLNumberStart       int64
	BucketMin            int
	BucketMax            int
	CheckIntervalMinutes int
	BatchSize            int
}

// Plan describes one SQL lifecycle plan to render.
type Plan struct {
	Action            Operation
	Config            Config
	ActiveCount       int
	FreshSinceMinutes int
}

// Normalize fills in conservative defaults for omitted fields.
func (c Config) Normalize() Config {
	if c.Schema == "" {
		c.Schema = SchemaV2
	}
	if c.BlogIDStart == 0 {
		c.BlogIDStart = defaultBlogIDStart
	}
	if c.Count == 0 {
		c.Count = defaultCount
	}
	if c.URLPattern == "" {
		c.URLPattern = defaultURLPattern
	}
	if c.URLNumberStart == 0 {
		c.URLNumberStart = defaultURLNumberStart
	}
	if c.BucketMax == 0 && c.BucketMin == 0 {
		c.BucketMin = defaultBucketMin
		c.BucketMax = defaultBucketMax
	}
	if c.CheckIntervalMinutes == 0 {
		c.CheckIntervalMinutes = defaultCheckIntervalMinutes
	}
	if c.BatchSize == 0 {
		c.BatchSize = defaultBatchSize
	}
	c.Schema = strings.ToLower(strings.TrimSpace(c.Schema))
	return c
}

// Normalize fills in conservative defaults for omitted fields.
func (p Plan) Normalize() Plan {
	p.Config = p.Config.Normalize()
	if p.Action == "" {
		p.Action = OperationVerify
	}
	if p.FreshSinceMinutes == 0 {
		p.FreshSinceMinutes = defaultFreshSinceMinutes
	}
	p.Action = Operation(strings.ToLower(strings.TrimSpace(string(p.Action))))
	return p
}

// BlogIDEnd returns the inclusive end of the reserved blog_id range.
func (c Config) BlogIDEnd() int64 {
	c = c.Normalize()
	return c.BlogIDStart + int64(c.Count) - 1
}

// Validate checks whether a config can be rendered safely.
func (c Config) Validate() error {
	c = c.Normalize()
	switch c.Schema {
	case SchemaV1, SchemaV2:
	default:
		return fmt.Errorf("schema must be %q or %q", SchemaV1, SchemaV2)
	}
	if c.BlogIDStart <= 0 {
		return fmt.Errorf("blog_id start must be positive")
	}
	if c.Count <= 0 {
		return fmt.Errorf("count must be positive")
	}
	if int64(c.Count) > maxInt64-c.BlogIDStart+1 {
		return fmt.Errorf("blog_id range overflows int64")
	}
	if c.URLNumberStart <= 0 {
		return fmt.Errorf("URL number start must be positive")
	}
	if int64(c.Count) > maxInt64-c.URLNumberStart+1 {
		return fmt.Errorf("URL number range overflows int64")
	}
	if c.BucketMin < 0 || c.BucketMin > 65535 {
		return fmt.Errorf("bucket_min must be between 0 and 65535")
	}
	if c.BucketMax < 0 || c.BucketMax > 65535 {
		return fmt.Errorf("bucket_max must be between 0 and 65535")
	}
	if c.BucketMax < c.BucketMin {
		return fmt.Errorf("bucket_max must be >= bucket_min")
	}
	if c.CheckIntervalMinutes <= 0 || c.CheckIntervalMinutes > 65535 {
		return fmt.Errorf("check interval must be between 1 and 65535 minutes")
	}
	if c.BatchSize <= 0 {
		return fmt.Errorf("batch size must be positive")
	}
	firstURL, err := formatMonitorURL(c.URLPattern, c.URLNumberStart)
	if err != nil {
		return err
	}
	lastURL, err := formatMonitorURL(c.URLPattern, c.URLNumberStart+int64(c.Count)-1)
	if err != nil {
		return err
	}
	if len(firstURL) > 2083 || len(lastURL) > 2083 {
		return fmt.Errorf("generated monitor_url exceeds 2083 characters")
	}
	return nil
}

// Validate checks whether a plan can be rendered safely.
func (p Plan) Validate() error {
	p = p.Normalize()
	if err := p.Config.Validate(); err != nil {
		return err
	}
	switch p.Action {
	case OperationSeed, OperationDeactivate, OperationVerify:
	case OperationActivate:
		if p.ActiveCount <= 0 {
			return fmt.Errorf("active count must be positive for activate")
		}
		if p.ActiveCount > p.Config.Count {
			return fmt.Errorf("active count cannot exceed reserved count")
		}
	default:
		return fmt.Errorf("action must be %q, %q, %q, or %q", OperationSeed, OperationActivate, OperationDeactivate, OperationVerify)
	}
	if p.FreshSinceMinutes < 0 {
		return fmt.Errorf("fresh-since minutes cannot be negative")
	}
	return nil
}

// WriteSQL renders a SQL lifecycle plan.
func WriteSQL(w io.Writer, plan Plan) error {
	plan = plan.Normalize()
	if err := plan.Validate(); err != nil {
		return err
	}

	writeHeader(w, plan)
	switch plan.Action {
	case OperationSeed:
		return writeSeedSQL(w, plan.Config)
	case OperationActivate:
		writeActivateSQL(w, plan.Config, plan.ActiveCount)
	case OperationDeactivate:
		writeDeactivateSQL(w, plan.Config)
	case OperationVerify:
		writeVerifySQL(w, plan.Config, plan.FreshSinceMinutes)
	}
	return nil
}

// RenderSQL renders a SQL lifecycle plan to a string.
func RenderSQL(plan Plan) (string, error) {
	var out bytes.Buffer
	if err := WriteSQL(&out, plan); err != nil {
		return "", err
	}
	return out.String(), nil
}

// RenderActiveCountSQL renders a verification query for the active row count.
func RenderActiveCountSQL(c Config) (string, error) {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf(`SELECT
  COUNT(*) AS active_sites
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d
  AND monitor_active = 1;
`, c.BlogIDStart, c.BlogIDEnd()), nil
}

// RenderActiveCheckIntervalSQL renders active-row check interval distribution.
func RenderActiveCheckIntervalSQL(c Config) (string, error) {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return "", err
	}
	return fmt.Sprintf(`SELECT
  check_interval,
  COUNT(*) AS active_sites
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d
  AND monitor_active = 1
GROUP BY check_interval
ORDER BY check_interval;
`, c.BlogIDStart, c.BlogIDEnd()), nil
}

// RenderActiveURLSamplesSQL renders a query that returns exact activated
// monitor_url samples before a timed capacity window starts.
func RenderActiveURLSamplesSQL(c Config, activeCount int) (string, error) {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return "", err
	}
	if activeCount <= 0 {
		return "", fmt.Errorf("active count must be positive")
	}
	if activeCount > c.Count {
		return "", fmt.Errorf("active count cannot exceed reserved count")
	}
	bucketCount := c.BucketMax - c.BucketMin + 1
	offsets := sampleOffsets(activeCount, bucketCount)
	ids := make([]string, 0, len(offsets))
	for _, offset := range offsets {
		ids = append(ids, strconv.FormatInt(c.BlogIDStart+int64(offset), 10))
	}
	return fmt.Sprintf(`SELECT
  blog_id,
  bucket_no,
  monitor_url
FROM jetpack_monitor_sites
WHERE blog_id IN (%s)
  AND monitor_active = 1
ORDER BY blog_id ASC;
`, strings.Join(ids, ", ")), nil
}

// RenderSeedSafetySQL renders a preflight query for destructive seed resets.
func RenderSeedSafetySQL(c Config) (string, error) {
	c = c.Normalize()
	if err := c.Validate(); err != nil {
		return "", err
	}
	likePattern, err := generatedURLLikePattern(c.URLPattern)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`SELECT
  COUNT(*) AS total_rows,
  SUM(CASE WHEN monitor_url LIKE %s ESCAPE '\\' THEN 1 ELSE 0 END) AS matching_url_rows
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d;
`, sqlString(likePattern), c.BlogIDStart, c.BlogIDEnd()), nil
}

func writeHeader(w io.Writer, plan Plan) {
	c := plan.Config
	fmt.Fprintf(w, "-- uptime-bench Jetmon capacity %s plan\n", plan.Action)
	fmt.Fprintf(w, "-- schema: %s\n", c.Schema)
	fmt.Fprintf(w, "-- reserved blog_id range: %d..%d (%d rows)\n", c.BlogIDStart, c.BlogIDEnd(), c.Count)
	if plan.Action == OperationActivate {
		fmt.Fprintf(w, "-- active rows requested: %d\n", plan.ActiveCount)
	}
	fmt.Fprintln(w, "-- Review before applying. These statements are intended for dedicated benchmark hosts.")
	fmt.Fprintln(w)
}

func writeSeedSQL(w io.Writer, c Config) error {
	fmt.Fprintln(w, "START TRANSACTION;")
	if c.Schema == SchemaV2 {
		writeCloseOpenEventsSQL(w, c, "capacity benchmark seed reset")
	}
	fmt.Fprintln(w, "-- Recreate only the benchmark-owned site rows.")
	fmt.Fprintf(w, "DELETE FROM jetpack_monitor_sites WHERE blog_id BETWEEN %d AND %d;\n", c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	for offset := 0; offset < c.Count; offset += c.BatchSize {
		n := c.BatchSize
		if remaining := c.Count - offset; remaining < n {
			n = remaining
		}
		if err := writeInsertBatchSQL(w, c, offset, n); err != nil {
			return err
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "COMMIT;")
	return nil
}

func writeActivateSQL(w io.Writer, c Config, activeCount int) {
	activeEnd := c.BlogIDStart + int64(activeCount) - 1
	fmt.Fprintln(w, "START TRANSACTION;")
	if c.Schema == SchemaV2 {
		writeCloseOpenEventsSQL(w, c, "capacity benchmark activate reset")
	}
	fmt.Fprintln(w, "-- Reset the full benchmark range to an inactive, healthy baseline.")
	writeSetRangeActiveSQL(w, c, c.BlogIDStart, c.BlogIDEnd(), false)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "-- Activate the requested batch prefix.")
	writeSetRangeActiveSQL(w, c, c.BlogIDStart, activeEnd, true)
	fmt.Fprintln(w, "COMMIT;")
}

func writeDeactivateSQL(w io.Writer, c Config) {
	if c.Schema == SchemaV2 {
		fmt.Fprintln(w, "START TRANSACTION;")
		fmt.Fprintln(w, "-- Deactivate every benchmark-owned site row. Do this before closing events.")
		writeSetRangeActiveSQL(w, c, c.BlogIDStart, c.BlogIDEnd(), false)
		fmt.Fprintln(w, "COMMIT;")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "-- Allow in-flight checks fetched before deactivation to finish writing events.")
		fmt.Fprintln(w, "DO SLEEP(5);")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "START TRANSACTION;")
		writeCloseOpenEventsSQL(w, c, "capacity benchmark bulk deactivate")
		fmt.Fprintln(w, "COMMIT;")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "-- Close any late events that landed during the first event cleanup pass.")
		fmt.Fprintln(w, "DO SLEEP(2);")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "START TRANSACTION;")
		writeCloseOpenEventsSQL(w, c, "capacity benchmark bulk deactivate late pass")
		fmt.Fprintln(w, "COMMIT;")
		return
	}
	fmt.Fprintln(w, "START TRANSACTION;")
	fmt.Fprintln(w, "-- Deactivate every benchmark-owned site row.")
	writeSetRangeActiveSQL(w, c, c.BlogIDStart, c.BlogIDEnd(), false)
	fmt.Fprintln(w, "COMMIT;")
}

func writeVerifySQL(w io.Writer, c Config, freshSinceMinutes int) {
	fmt.Fprintln(w, "-- Reserved range totals.")
	fmt.Fprintf(w, `SELECT
  COUNT(*) AS benchmark_sites,
  SUM(CASE WHEN monitor_active = 1 THEN 1 ELSE 0 END) AS active_sites,
  MIN(blog_id) AS min_blog_id,
  MAX(blog_id) AS max_blog_id
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Bucket distribution for the benchmark-owned range.")
	fmt.Fprintf(w, `SELECT
  bucket_no,
  COUNT(*) AS benchmark_sites,
  SUM(CASE WHEN monitor_active = 1 THEN 1 ELSE 0 END) AS active_sites
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d
 GROUP BY bucket_no
 ORDER BY bucket_no;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Active check interval distribution.")
	fmt.Fprintf(w, `SELECT
  check_interval,
  COUNT(*) AS active_sites
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d
  AND monitor_active = 1
GROUP BY check_interval
ORDER BY check_interval;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	if c.Schema != SchemaV2 {
		return
	}

	fmt.Fprintln(w, "-- Stable freshness reference for all v2 freshness checks.")
	fmt.Fprintf(w, `SET @uptime_bench_now := UTC_TIMESTAMP();
SET @uptime_bench_freshness_cutoff := @uptime_bench_now - INTERVAL %d MINUTE;
`, freshSinceMinutes)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Snapshot active v2 freshness rows so related freshness summaries use the same row state.")
	fmt.Fprintf(w, `DROP TEMPORARY TABLE IF EXISTS uptime_bench_active_freshness;
CREATE TEMPORARY TABLE uptime_bench_active_freshness AS
SELECT
  blog_id,
  bucket_no,
  last_checked_at,
  TIMESTAMPDIFF(SECOND, last_checked_at, @uptime_bench_now) AS check_age_sec,
  CASE
    WHEN last_checked_at IS NULL OR last_checked_at < @uptime_bench_freshness_cutoff THEN 1
    ELSE 0
  END AS is_stale
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN %d AND %d
  AND monitor_active = 1;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Active sites not checked within the freshness window.")
	fmt.Fprintln(w, `SELECT
  COALESCE(SUM(is_stale), 0) AS stale_active_sites
FROM uptime_bench_active_freshness;`)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Open events remaining in the benchmark-owned range.")
	fmt.Fprintf(w, `SELECT
  COUNT(*) AS open_events
FROM jetmon_events
WHERE blog_id BETWEEN %d AND %d
  AND ended_at IS NULL;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Recent check history volume for the benchmark-owned range.")
	fmt.Fprintf(w, `SELECT
  COUNT(*) AS recent_check_history_rows
FROM jetmon_check_history
WHERE blog_id BETWEEN %d AND %d
  AND checked_at >= @uptime_bench_freshness_cutoff;
`, c.BlogIDStart, c.BlogIDEnd())
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Freshness lag percentiles for active checked rows in the benchmark-owned range.")
	fmt.Fprint(w, `WITH freshness AS (
  SELECT check_age_sec
  FROM uptime_bench_active_freshness
  WHERE last_checked_at IS NOT NULL
),
ranked AS (
  SELECT
    check_age_sec,
    CUME_DIST() OVER (ORDER BY check_age_sec) AS cumulative_rank
  FROM freshness
)
SELECT
  COUNT(*) AS freshness_samples,
  MIN(check_age_sec) AS freshest_check_age_sec,
  AVG(check_age_sec) AS average_check_age_sec,
  MIN(CASE WHEN cumulative_rank >= 0.50 THEN check_age_sec END) AS p50_check_age_sec,
  MIN(CASE WHEN cumulative_rank >= 0.95 THEN check_age_sec END) AS p95_check_age_sec,
  MIN(CASE WHEN cumulative_rank >= 0.99 THEN check_age_sec END) AS p99_check_age_sec,
  MAX(check_age_sec) AS oldest_check_age_sec
FROM ranked;
`)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "-- Stale active sites by scheduler bucket.")
	fmt.Fprint(w, `SELECT
  bucket_no,
  COUNT(*) AS active_sites,
  COALESCE(SUM(is_stale), 0) AS stale_active_sites
FROM uptime_bench_active_freshness
GROUP BY bucket_no
ORDER BY bucket_no;
`)
}

func writeInsertBatchSQL(w io.Writer, c Config, offset, n int) error {
	fmt.Fprintf(w, "-- Seed rows %d..%d.\n", offset+1, offset+n)
	if c.Schema == SchemaV2 {
		fmt.Fprintln(w, `INSERT INTO jetpack_monitor_sites
  (blog_id, bucket_no, monitor_url, monitor_active, site_status, last_status_change, check_interval,
   last_checked_at, last_alert_sent_at, check_keyword, maintenance_start, maintenance_end,
   custom_headers, timeout_seconds, redirect_policy, alert_cooldown_minutes)
VALUES`)
	} else {
		fmt.Fprintln(w, `INSERT INTO jetpack_monitor_sites
  (blog_id, bucket_no, monitor_url, monitor_active, site_status, last_status_change, check_interval)
VALUES`)
	}
	for i := 0; i < n; i++ {
		rowOffset := offset + i
		blogID := c.BlogIDStart + int64(rowOffset)
		urlNumber := c.URLNumberStart + int64(rowOffset)
		monitorURL, err := formatMonitorURL(c.URLPattern, urlNumber)
		if err != nil {
			return err
		}
		bucket := c.BucketMin + rowOffset%(c.BucketMax-c.BucketMin+1)
		terminator := ","
		if i == n-1 {
			terminator = ";"
		}
		if c.Schema == SchemaV2 {
			fmt.Fprintf(w, "  (%d, %d, %s, 0, 1, UTC_TIMESTAMP(), %d, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'follow', NULL)%s\n",
				blogID, bucket, sqlString(monitorURL), c.CheckIntervalMinutes, terminator)
		} else {
			fmt.Fprintf(w, "  (%d, %d, %s, 0, 1, UTC_TIMESTAMP(), %d)%s\n",
				blogID, bucket, sqlString(monitorURL), c.CheckIntervalMinutes, terminator)
		}
	}
	return nil
}

func writeSetRangeActiveSQL(w io.Writer, c Config, start, end int64, active bool) {
	activeValue := 0
	if active {
		activeValue = 1
	}
	if c.Schema == SchemaV2 {
		fmt.Fprintf(w, `UPDATE jetpack_monitor_sites
   SET monitor_active = %d,
       site_status = 1,
       last_status_change = UTC_TIMESTAMP(),
       check_interval = %d,
       last_checked_at = NULL,
       next_check_at = NULL,
       last_alert_sent_at = NULL,
       maintenance_start = NULL,
       maintenance_end = NULL
 WHERE blog_id BETWEEN %d AND %d;
`, activeValue, c.CheckIntervalMinutes, start, end)
		return
	}
	fmt.Fprintf(w, `UPDATE jetpack_monitor_sites
   SET monitor_active = %d,
       site_status = 1,
       last_status_change = UTC_TIMESTAMP(),
       check_interval = %d
 WHERE blog_id BETWEEN %d AND %d;
`, activeValue, c.CheckIntervalMinutes, start, end)
}

func writeCloseOpenEventsSQL(w io.Writer, c Config, note string) {
	fmt.Fprintln(w, "-- Close open v2 events in the benchmark-owned range.")
	fmt.Fprintf(w, `DROP TEMPORARY TABLE IF EXISTS uptime_bench_capacity_events_to_close;
CREATE TEMPORARY TABLE uptime_bench_capacity_events_to_close (
  id BIGINT UNSIGNED NOT NULL PRIMARY KEY
) ENGINE=InnoDB;

INSERT IGNORE INTO uptime_bench_capacity_events_to_close (id)
SELECT id
FROM jetmon_events
WHERE blog_id BETWEEN %d AND %d
  AND ended_at IS NULL;

INSERT INTO jetmon_event_transitions
  (event_id, blog_id, severity_before, severity_after, state_before, state_after, reason, source, metadata)
SELECT e.id, e.blog_id, e.severity, NULL, e.state, 'Resolved', 'manual_override', 'uptime-bench-capacity',
       JSON_OBJECT('note', %s, 'source', 'uptime-bench-capacity')
FROM jetmon_events e
JOIN uptime_bench_capacity_events_to_close pending ON pending.id = e.id
WHERE e.ended_at IS NULL;

UPDATE jetmon_events e
JOIN uptime_bench_capacity_events_to_close pending ON pending.id = e.id
   SET e.ended_at = CURRENT_TIMESTAMP(3),
       e.resolution_reason = 'manual_override'
 WHERE e.ended_at IS NULL;

DROP TEMPORARY TABLE uptime_bench_capacity_events_to_close;

`, c.BlogIDStart, c.BlogIDEnd(), sqlString(note))
}

func formatMonitorURL(pattern string, number int64) (string, error) {
	sample := fmt.Sprintf(pattern, number)
	if strings.Contains(sample, "%!") {
		return "", fmt.Errorf("url pattern must contain exactly one fmt integer placeholder")
	}
	parsed, err := url.Parse(sample)
	if err != nil {
		return "", fmt.Errorf("parse generated monitor URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("generated monitor URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("generated monitor URL must include a host")
	}
	return sample, nil
}

func sqlString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func generatedURLLikePattern(pattern string) (string, error) {
	start, end, err := integerPlaceholderBounds(pattern)
	if err != nil {
		return "", err
	}
	return sqlLikeEscape(pattern[:start]) + "%" + sqlLikeEscape(pattern[end:]), nil
}

func integerPlaceholderBounds(pattern string) (int, int, error) {
	foundStart := -1
	foundEnd := -1
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '%' {
			continue
		}
		if i+1 < len(pattern) && pattern[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(pattern) {
			r := rune(pattern[j])
			if strings.ContainsRune("#0+- ", r) || unicode.IsDigit(r) {
				j++
				continue
			}
			if r == '.' {
				j++
				for j < len(pattern) && unicode.IsDigit(rune(pattern[j])) {
					j++
				}
				continue
			}
			break
		}
		if j >= len(pattern) {
			return 0, 0, fmt.Errorf("url pattern contains incomplete fmt placeholder")
		}
		if !strings.ContainsRune("vdboxXU", rune(pattern[j])) {
			return 0, 0, fmt.Errorf("url pattern placeholder must be integer-like")
		}
		if foundStart != -1 {
			return 0, 0, fmt.Errorf("url pattern must contain exactly one fmt integer placeholder")
		}
		foundStart = i
		foundEnd = j + 1
	}
	if foundStart == -1 {
		return 0, 0, fmt.Errorf("url pattern must contain exactly one fmt integer placeholder")
	}
	return foundStart, foundEnd, nil
}

func sqlLikeEscape(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
