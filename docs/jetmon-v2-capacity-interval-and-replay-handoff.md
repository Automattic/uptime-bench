# Jetmon v2 Capacity Handoff: Check Interval Drift and Replay Sample Hygiene

Generated from `/home/gaarai/code/jetmon` on 2026-05-09.

This handoff is for the `uptime-bench` project at:

`/home/gaarai/code/uptime-bench`

It summarizes two uptime-bench-side issues found while reviewing Jetmon v2 capacity reports:

1. Capacity activation does not reapply the configured `check_interval`, so a run that claims to test 5-minute cadence can silently keep older 1-minute seeded rows.
2. Replay detection can mark Jetmon v2 as missing injected failures when sampled hosts were already in a pre-existing Down/Seems Down event before the injection window.

No running Jetmon services were changed while producing this handoff.

## Relevant Reports

Primary report set:

- `reports/20260509T054337Z-30m-sysadmin-equal-10k-v1-v2-5m-replay-detection/`
- `reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/`
- `reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/`

Specific files inspected:

- `reports/20260509T054337Z-30m-sysadmin-equal-10k-v1-v2-5m-replay-detection/report.md`
- `reports/20260509T054337Z-30m-sysadmin-equal-10k-v1-v2-5m-replay-detection/capacity.md`
- `reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/batch-0010000/run.json`
- `reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/batch-0010000/jetmon-v2-activate-10000.sql`
- `reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/batch-0010000/capacity-replay-detection.json`
- `reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/batch-0001000/run.json`
- `reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/batch-0001000/jetmon-v2-activate-1000.sql`
- `reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/batch-0001000/capacity-replay-detection.json`

## Issue 1: Activation Does Not Reapply `check_interval`

### Problem

The report `20260509T054337Z-30m-sysadmin-equal-10k-v1-v2-5m-replay-detection` says uptime-bench configured both Jetmon services for a 5-minute lifecycle interval, but Jetmon v2 event metadata reported:

```text
normal_check_interval_seconds = 60
next_check_interval_seconds   = 60
```

Jetmon v2's current scheduler metadata logic means this is not just a retry artifact. For a 5-minute site that fails, Jetmon should report:

```text
normal_check_interval_seconds = 300
next_check_interval_seconds   = 60
```

`next_check_interval_seconds=60` is expected for failed probes because Jetmon retries failed sites sooner than their normal interval. `normal_check_interval_seconds=60` means the site row itself had `check_interval = 1`.

The same pattern appears in newer performance branch capacity reports:

- The run metadata/target observer is configured for `check_interval_seconds=300`.
- Jetmon v2 replay event metadata still reports `normal_check_interval_seconds=60`.
- Target request volume is materially higher than a clean 5-minute cadence comparison would imply.

### Evidence

Current live capacity config says Jetmon v2 should use 5 minutes:

`configs/capacity/jetmon.fleet.toml`

```toml
[checks]
interval = "5m"

[jetmon_v2.lifecycle]
check_interval = "5m"
```

The normalized run data agrees. In both current capacity report runs, the Jetmon v2 service lifecycle has:

```json
{
  "id": "jetmon-v2",
  "schema": "v2",
  "check_interval_minutes": 5,
  "blog_id_start": 8000001000000000,
  "url_number_start": 500001
}
```

But generated activation SQL does not update `check_interval`:

`reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/batch-0010000/jetmon-v2-activate-10000.sql`

```sql
UPDATE jetpack_monitor_sites
   SET monitor_active = 1,
       site_status = 1,
       last_status_change = UTC_TIMESTAMP(),
       last_checked_at = NULL,
       next_check_at = NULL,
       last_alert_sent_at = NULL,
       maintenance_start = NULL,
       maintenance_end = NULL
 WHERE blog_id BETWEEN 8000001000000000 AND 8000001000009999;
```

Same issue in:

`reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/batch-0001000/jetmon-v2-activate-1000.sql`

The seed path does write the configured interval:

`internal/jetmoncapacity/planner.go`

```go
fmt.Fprintf(w, "  (..., UTC_TIMESTAMP(), %d, ...)%s\n",
    blogID, bucket, sqlString(monitorURL), c.CheckIntervalMinutes, terminator)
```

But activation only toggles active state and scheduler timestamps. If rows were seeded earlier with 1-minute cadence, later 5-minute tests keep the stale value.

Observed target traffic reinforces this:

10k performance branch capacity ladder:

```text
target observer check_interval_seconds = 300
expected_min_requests                 = 60000
actual total_requests                 = 147218
stale_sites                           = 18
p95 request age                       = 212.74s
max request age                       = 1046.83s
```

1k performance branch capacity bracket:

```text
target observer check_interval_seconds = 300
expected_min_requests                 = 6000
actual total_requests                 = 14500
stale_sites                           = 0
p95 request age                       = 174.59s
max request age                       = 185.38s
```

Those request counts are not proof by themselves, because retry behavior and failures increase traffic, but they line up with the event metadata saying the normal interval is one minute.

### Recommended Fix

Update uptime-bench activation/reset logic so generated SQL reapplies the configured check interval to benchmark-owned rows every time a capacity batch is activated.

Likely code path:

- `internal/jetmoncapacity/planner.go`
- `writeActivateSQL`
- `writeSetRangeActiveSQL`
- `internal/jetmoncapacity/planner_test.go`

Recommended behavior:

1. During activation, update the full benchmark-owned range to the configured interval so stale seeded state cannot leak across tests.
2. Also ensure the activated prefix has the configured interval.
3. Do this for both v1 and v2 schemas unless there is a specific reason not to. The benchmark goal is equal-cadence comparison.
4. Keep resetting `last_checked_at` and `next_check_at` to `NULL` so Jetmon can immediately observe the new active batch.

Example generated SQL shape for v2:

```sql
UPDATE jetpack_monitor_sites
   SET monitor_active = 1,
       site_status = 1,
       last_status_change = UTC_TIMESTAMP(),
       check_interval = 5,
       last_checked_at = NULL,
       next_check_at = NULL,
       last_alert_sent_at = NULL,
       maintenance_start = NULL,
       maintenance_end = NULL
 WHERE blog_id BETWEEN ...;
```

For v1:

```sql
UPDATE jetpack_monitor_sites
   SET monitor_active = 1,
       site_status = 1,
       last_status_change = UTC_TIMESTAMP(),
       check_interval = 5
 WHERE blog_id BETWEEN ...;
```

### Verification To Add

Add an activation/post-activation guard that fails loudly if active rows do not match the test plan.

Suggested query:

```sql
SELECT check_interval, COUNT(*) AS rows
FROM jetpack_monitor_sites
WHERE blog_id BETWEEN ? AND ?
  AND monitor_active = 1
GROUP BY check_interval
ORDER BY check_interval;
```

Expected for a 5-minute v2 run:

```text
check_interval = 5
rows = active_count
```

If any active rows have a different interval, mark the run invalid before collecting a capacity window. This should be visible in `run.json`, `capacity.json`, and the Markdown report.

Also consider adding the check interval distribution to report output even when it passes. This prevents another misleading "5m configured, 60s observed" comparison.

## Issue 2: Replay Detection Treats Pre-Existing Unhealthy Samples as Missed Injected Failures

### Problem

The recent capacity reports show Jetmon v2 replay detection failures, but many of the "missing in-window down" samples were already in Down/Seems Down events before the injected HTTP 503 failure window began.

That means the evaluator is mixing two separate concerns:

1. Did Jetmon detect the injected HTTP 503 during the replay window?
2. Was the sampled host already unhealthy due to a timeout/DNS issue before the replay window?

When a host is already Down before the injection starts, it cannot cleanly produce a new in-window Down event for the injected failure. Counting that as a missed injected failure makes the capacity result harder to interpret.

This does not mean Jetmon v2 is fine. The pre-existing timeout/DNS events are important evidence of noise or false-positive pressure. But uptime-bench should classify them separately instead of folding them into replay-miss counts.

### Evidence: 10k Capacity Ladder

Report:

`reports/20260509T084239Z-4h-v2-performance-branch-capacity-ladder/batch-0010000/capacity-replay-detection.json`

Replay window:

```text
event:        http-503-sample
activated:    2026-05-09T08:46:31.327036548Z
deactivated:  2026-05-09T08:53:32.467274115Z
```

Summary:

```text
hosts sampled:       25
down_detected:       22
recovery_detected:   25
late_down_detected:  3
missing_down:        3
missing_recovery:    0
error: 3 hosts missing in-window down detection; 3 hosts had only late down detection
```

The three late/missing hosts were:

```text
500882  site-0500882.steadycadence.party  down_at=2026-05-09T09:01:22.143Z  recovery_at=2026-05-09T08:54:17.547Z  raw_events=3
501646  site-0501646.steadycadence.party  down_at=2026-05-09T09:12:33.707Z  recovery_at=2026-05-09T08:54:17.585Z  raw_events=2
503758  site-0503758.steadycadence.party  down_at=2026-05-09T08:58:35.545Z  recovery_at=2026-05-09T08:54:17.600Z  raw_events=2
```

Important detail: these hosts had raw events that started before the injected failure activated and overlapped the injected window.

Examples:

- `site-0500882`: Down started `2026-05-09T08:45:55.872Z`, before activation; ended `2026-05-09T08:54:17.547Z`, after deactivation; failure class `intermittent`, detector `timeout`.
- `site-0501646`: Down started `2026-05-09T08:45:55.867Z`, before activation; ended `2026-05-09T08:54:17.585Z`, after deactivation; detector `dns_servfail`.
- `site-0503758`: Down started `2026-05-09T08:45:56.163Z`, before activation; ended `2026-05-09T08:54:17.600Z`, after deactivation; detector `dns_servfail`.

These are not clean "missed HTTP 503" samples. They are pre-existing unhealthy samples.

### Evidence: 1k Capacity Bracket

Report:

`reports/20260509T091537Z-3h25m-v2-performance-branch-capacity-bracket/batch-0001000/capacity-replay-detection.json`

Replay window:

```text
event:        http-503-sample
activated:    2026-05-09T09:19:08.886574730Z
deactivated:  2026-05-09T09:26:10.066879252Z
```

Summary:

```text
hosts sampled:       25
down_detected:       14
recovery_detected:   25
late_down_detected:  11
missing_down:        11
missing_recovery:    0
error: 11 hosts missing in-window down detection; 11 hosts had only late down detection
```

The late/missing hosts were:

```text
500057  site-0500057.steadycadence.party
500073  site-0500073.steadycadence.party
500240  site-0500240.steadycadence.party
500324  site-0500324.steadycadence.party
500358  site-0500358.steadycadence.party
500377  site-0500377.steadycadence.party
500524  site-0500524.steadycadence.party
500652  site-0500652.steadycadence.party
500692  site-0500692.steadycadence.party
500693  site-0500693.steadycadence.party
500722  site-0500722.steadycadence.party
```

Manual inspection showed these also had raw timeout events that started before replay activation and closed after the injection window. Many had `rtt_ms` around the 10-second timeout. Again, this is valuable signal about Jetmon/test-target behavior, but it should be reported as pre-existing sample contamination rather than only as "missing in-window down."

### Recommended Fix

Update replay planning and/or detection so sampled hosts with pre-existing unhealthy state are classified separately.

Likely code paths:

- `internal/jetmoncapacity/replay.go`
- `internal/jetmoncapacity/replay_detection.go`
- `internal/jetmoncapacity/replay_detection_test.go`
- `internal/jetmoncapacity/suite_report.go`
- `internal/jetmoncapacity/suite_report_test.go`

Recommended classification model:

- `detected_down_during_failure`: Down/Seems Down event starts within the injected failure window and matches the expected failure type well enough.
- `preexisting_down_overlapped_failure`: an event started before activation and remained open into the injected failure window.
- `late_down_only`: no qualifying event during the window, but a Down/Seems Down starts after the window.
- `missing_down`: no relevant event at all.
- `recovery_detected`: recovery after deactivation.
- `sample_invalid_preexisting_state`: optional status for cases that should be excluded from the pass/fail denominator.

At minimum, if a raw event has:

```text
event.started_at < replay.activated_at
AND (event.ended_at is null OR event.ended_at > replay.activated_at)
```

then mark that host as `preexisting_down_overlapped_failure`.

Do not count those hosts as ordinary `missing_down`. Either:

1. Exclude them from the replay pass/fail denominator and report the count prominently.
2. Keep the run as failed/noisy if too many samples are contaminated, but fail it as "sample contamination / pre-existing unhealthy hosts", not "Jetmon missed injected HTTP 503."

Preferred behavior:

- Oversample candidate hosts before activation.
- Filter out hosts with open/recent unhealthy events before assigning the final replay sample.
- If service APIs cannot cheaply prove clean state before activation, classify after the run using raw event history and make the report explicit.
- If contamination exceeds a threshold, mark replay detection as `invalid_noisy_sample` or `inconclusive`, not a clean provider failure.

### Reporting Improvements

Add a replay detection section that breaks down host outcomes:

```text
sampled_hosts: 25
eligible_hosts: 22
detected_down_during_failure: 22
preexisting_down_overlapped_failure: 3
late_down_only: 0
missing_down: 0
recovery_detected: 25
```

For contaminated samples, include the dominant pre-existing failure classes:

```text
preexisting failure classes:
- intermittent / timeout
- intermittent / dns_servfail
```

This preserves the signal that Jetmon/test infrastructure had timeout/DNS noise while keeping replay-specific scoring honest.

## Why These Fixes Matter Together

The check-interval issue can make a report claim "5-minute cadence" while Jetmon v2 is actually running benchmark-owned rows at 1-minute cadence. That invalidates strict resource comparisons.

The replay sample issue can make a report claim "Jetmon missed HTTP 503" when the sampled host was already Down before the injected HTTP 503 started. That invalidates strict correctness comparisons.

Together, these two issues can push analysis in the wrong direction:

- Resource usage may look worse because the service is checking too often.
- Replay correctness may look worse because noisy/pre-existing samples are counted as injected-failure misses.

Fixing both gives the Jetmon v2 work a cleaner benchmark signal.

## Suggested Implementation Order

1. Fix activation SQL to set `check_interval` from the normalized lifecycle config.
2. Add post-activation verification of active-row `check_interval` distribution.
3. Add report output for the active-row interval distribution.
4. Update replay detection to classify pre-existing overlapping events.
5. Add replay report output for contaminated/ineligible samples.
6. Optionally add oversampling/resampling so the final sample contains the requested number of clean hosts when possible.

## Commands Run While Investigating

From `/home/gaarai/code/jetmon`:

```bash
rg -n "check_interval|USE_VARIABLE_CHECK_INTERVALS|MIN_TIME_BETWEEN_ROUNDS|normal_check_interval|next_check|last_checked|DATASET_SIZE|NUM_TO_PROCESS" internal cmd config docs -S
rg -n "check_interval|normal_check_interval|5-minute|5 minute|300|60" /home/gaarai/code/uptime-bench/reports/20260509T054337Z-30m-sysadmin-equal-10k-v1-v2-5m-replay-detection -S
rg -n "check_interval|CheckInterval|interval_seconds|5m|300|60|jetmon-v2|jetmon_v2" /home/gaarai/code/uptime-bench -S
jq '.events[0].services[] | {service, status, hosts, down_detected, recovery_detected, late_down_detected, missing_down, missing_recovery, error}' reports/.../capacity-replay-detection.json
```

The important outcome: Jetmon's code reports `normal_check_interval_seconds` from the site row's `check_interval`; the report metadata showing `60` means the benchmark-owned v2 rows had `check_interval=1` at runtime.

## Guardrails

- Do not include secrets or DSNs in any generated report output.
- Do not change running Jetmon services as part of implementing these uptime-bench fixes unless Chris explicitly asks.
- The uptime-bench working tree had unrelated local modifications when this handoff was generated. Avoid overwriting existing changes; make a focused branch before editing.
