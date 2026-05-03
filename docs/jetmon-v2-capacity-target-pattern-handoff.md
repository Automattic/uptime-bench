# Handoff: Jetmon v2 Capacity Target Pattern Mismatch

This handoff captures uptime-bench-specific follow-up from a Jetmon v2
1,000-site capacity run that failed the freshness gate. The failure initially
looked like a Jetmon v2 scalability regression, but the strongest evidence
points to a target setup mismatch: uptime-bench activated Jetmon rows whose
`monitor_url` hostnames did not match the generated DNS range that had been
verified before the run.

This is a Jetmon v2 capacity-harness issue. Do not change Jetmon v1 behavior to
improve this result, and do not treat a target setup failure as proof of a
Jetmon v2 monitor regression.

## Source Context

- Jetmon repo: `/home/gaarai/code/jetmon`
- uptime-bench repo: `/home/gaarai/code/uptime-bench`
- Capacity harness repo mentioned by the Jetmon handoff:
  `/home/gaarai/code/uptime-bench-capacity-bench`
- Failed run report directory on the capacity host:
  `/home/jetmon/uptime-bench-capacity/reports/gate-1000-20260503-033206Z`
- Failed active window:
  `2026-05-03T03:32:36Z` to `2026-05-03T03:47:36Z`
- Failed gate:
  `missed_check_percent exceeded threshold for jetmon-v2`

The Jetmon-side handoff that prompted this review was
`/home/gaarai/code/jetmon/docs/uptime-bench-jetmon-v2-capacity-handoff.md`.

## What Was Observed

At the end of the 1,000-site window, Jetmon v2 had the expected active row
count but most rows were stale:

| Metric | Value |
| --- | ---: |
| Active rows | `1000` |
| Stale active sites | `700` |
| Missed check percent | `70.00` |
| Recent check history rows | `300` |
| Recent checks per minute | `60.00` |
| p95 check age | `903s` |
| Open v2 events at window end | `809` |

Jetmon v2 journal summaries showed raw check dispatch was not the dominant
cost. Each 100-site scheduler page spent about 1m50s in event processing:

```text
03:32:37 checking 100 sites (scheduler page 1)
03:34:33 page 1: process=1m51.278s events=1m50.083s
03:36:24 page 2: process=1m50.897s events=1m49.752s
...
03:49:36 round summary: pages=9 selected=900 completed=900
         duration=16m58.394s process=16m46.422s events=16m35.928s
```

There was also a MySQL deadlock in the event-open path:

```text
open seems-down event blog_id=8000001000000824:
insert event: Error 1213 (40001): Deadlock found when trying to get lock
```

The event path became hot because synthetic targets that were expected to be
healthy were being classified as down.

## Key Finding

The generated DNS range documented for the run was:

```toml
[[targets.generated_sites]]
id           = "capacity-load"
host_pattern = "site-%07d.steadycadence.party"
start        = 1
count        = 1000000
paths        = ["/"]
```

Those hostnames were manually tested before the run. For example:

```text
site-0001000.steadycadence.party
```

resolved and returned HTTP 200.

However, the active Jetmon v2 rows sampled after the run used the `.load.`
subdomain:

```text
http://site-0000001.load.steadycadence.party/
http://site-0001000.load.steadycadence.party/
```

From the Jetmon service host, the `.load.` names did not resolve and `curl`
returned HTTP code `000`. The non-`.load.` names did resolve and returned HTTP
200.

Recent Jetmon v2 check history for the active benchmark prefix during the
failed window showed the expected symptom of a DNS/connectivity setup failure:

```text
http_code=0
error_code=2
request_method=GET
rows_count=900
```

In Jetmon v2, `checker.ErrorConnect == 2`.

## Diagnosis

The capacity run appears to have activated rows for one hostname pattern while
the generated DNS fleet served another hostname pattern.

That caused Jetmon v2 to do the correct thing for the URLs it was given: it
checked them with GET, could not connect, and opened Seems Down events. The
false-down storm then made event handling dominate scheduler page time, which
caused the freshness gate to fail.

The pre-run verification was not strong enough because it tested a manually
chosen hostname that was similar to, but not exactly the same as, the activated
Jetmon `monitor_url` values.

## Required uptime-bench Fixes

### 1. Make Capacity URL Pattern And Generated DNS Pattern Share One Source

The capacity runner and generated DNS config should not be independently
typed templates that can drift.

The desired invariant is:

```text
activated jetpack_monitor_sites.monitor_url host
  == generated DNS host_pattern instance
  == target HTTP host served by the synthetic fleet
```

Recommended implementation:

- Define a single capacity target host pattern in the capacity run config.
- Use it to generate or validate DNS target config.
- Use the same value to build Jetmon activation `monitor_url` values.
- Include the resolved pattern in the run report.

Near-term acceptable fixes:

- If the capacity namespace should be `.load.steadycadence.party`, update
  generated DNS to include `site-%07d.load.steadycadence.party`.
- If the existing generated DNS namespace is correct, update the capacity
  activation URL pattern to use `site-%07d.steadycadence.party`.

The first option keeps capacity traffic visually separated under `.load.`, but
it requires the DNS generator/deployment to include that subdomain. The second
option matches what was already verified in this run, but it loses that
namespace separation.

### 2. Add An Activated-URL Preflight

Before starting the timed capacity window, uptime-bench should query the exact
Jetmon rows it just activated and validate those exact URLs from the same
network positions Jetmon will use.

Sample rows should include at least:

- first active row in the range
- middle active row in the range
- last active row in the range
- one row from each configured bucket span, when the run spans multiple buckets

Example SQL:

```sql
SELECT blog_id, monitor_url
  FROM jetpack_monitor_sites
 WHERE blog_id BETWEEN ? AND ?
   AND monitor_active = 1
 ORDER BY blog_id ASC
 LIMIT 10;
```

For each sampled `monitor_url`, the preflight should validate:

- DNS resolution from each Jetmon monitor host under test.
- HTTP GET returns the expected 2xx status from each Jetmon monitor host.
- DNS resolution from each Veriflier host that participates in Jetmon v2
  verification.
- HTTP GET returns the expected 2xx status from each Veriflier host.

The preflight must fail fast if any sampled activated URL does not resolve or
does not return the expected HTTP status. It should not let the timed capacity
window start in that state.

### 3. Record The Exact Sampled URLs In Reports

Each capacity report should include the sampled activated URLs and the result
of DNS/HTTP validation.

Suggested report fields:

```json
{
  "target_preflight": {
    "status": "pass",
    "sampled_urls": [
      {
        "blog_id": 8000001000000000,
        "url": "http://site-0000001.load.steadycadence.party/",
        "checks": [
          {
            "source_host": "jetmon-service-host-2",
            "dns_ok": true,
            "http_status": 200,
            "error": ""
          }
        ]
      }
    ]
  }
}
```

The human summary should print the same data in compact table form. The point
is to make hostname drift obvious without requiring SSH or manual database
inspection after a failed run.

### 4. Add A Pattern-Mismatch Guard

If uptime-bench has both a capacity activation URL pattern and a generated DNS
host pattern in config, validate them before the run.

This can start as a conservative guard:

- Strip URL scheme, path, and trailing dot.
- Render both patterns for the first, middle, and last generated IDs.
- Compare the hostnames exactly.
- If they differ, refuse to run unless an explicit override is provided.

If an override is added, it should be noisy and recorded in the report. Capacity
tests are expensive enough that a false start should be treated as a serious
operator warning.

### 5. Classify Target Setup Failures Separately

If a run produces mostly connection errors against supposedly healthy targets,
the report should distinguish target setup failure from Jetmon service failure.

Suggested heuristic:

- During the active window, query recent Jetmon v2 check history for the active
  benchmark prefix.
- If a large majority of rows have `http_code = 0` and a connect/DNS error
  code, mark the run with a setup warning.
- Point the operator to the target preflight artifact and sampled URLs.

This should not hide real Jetmon problems. It should prevent a DNS-pattern
mismatch from being summarized only as "Jetmon v2 missed checks."

## Suggested Debug Queries

Use these when investigating a future Jetmon v2 capacity failure. Adjust table
or column names only if the live Jetmon schema differs.

Inspect activated URL samples:

```sql
SELECT blog_id, monitor_url
  FROM jetpack_monitor_sites
 WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000999
   AND monitor_active = 1
 ORDER BY blog_id ASC
 LIMIT 20;
```

Count recent check outcomes for the active prefix:

```sql
SELECT
  http_code,
  error_code,
  request_method,
  COUNT(*) AS rows_count
FROM jetmon_check_history
WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000999
  AND checked_at >= '2026-05-03 03:32:36'
  AND checked_at <  '2026-05-03 03:47:36'
GROUP BY http_code, error_code, request_method
ORDER BY rows_count DESC;
```

Inspect open v2 events in the benchmark range:

```sql
SELECT state, severity, check_type, COUNT(*) AS rows_count
  FROM jetmon_events
 WHERE blog_id BETWEEN 8000001000000000 AND 8000001000000999
   AND ended_at IS NULL
 GROUP BY state, severity, check_type
 ORDER BY rows_count DESC;
```

## Acceptance Criteria For The Harness Fix

A corrected uptime-bench capacity run should satisfy these pre-window checks:

- The exact activated `monitor_url` samples resolve from every Jetmon v2 monitor
  host under test.
- The exact activated `monitor_url` samples return HTTP 200 or the configured
  expected healthy status from every Jetmon v2 monitor host under test.
- The same samples resolve and return the expected healthy status from every
  participating Veriflier host.
- The report records the sampled URLs and per-source-host results.
- A deliberate mismatch between capacity URL pattern and generated DNS pattern
  fails before activation or before the timed window.

The next Jetmon v2 1,000-site test should then be interpreted using Jetmon's
own scheduler outcome metrics:

- Healthy target runs should have `scheduler.round.check.success.count` close
  to `scheduler.round.completed.count`.
- High `scheduler.round.check.connect_error.count` means the target URLs still
  are not reachable from Jetmon.
- Non-zero `eventstore.mutation.retry.count` means MySQL returned deadlocks or
  lock-wait timeouts; sustained values point to real event/projection write
  contention.

## Related Jetmon-Side Follow-Up

The Jetmon efficiency branch added instrumentation to make this class of setup
failure clear in future runs:

- Page and round check outcome counters by failure type.
- Page and round summary log fields for check outcomes.
- Retry handling around MySQL event mutation deadlocks and lock-wait timeouts.
- Scalability test-plan notes requiring exact activated URL validation.

Those changes improve diagnosis and resilience, but they do not replace the
uptime-bench-side need to validate the exact synthetic targets before starting
the capacity clock.
