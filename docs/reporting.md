# Reporting Standard

This document defines the report bundle that should be generated after every
uptime-bench run. A run is not complete until its raw evidence, operator
summary, analysis, and cleanup status are preserved in the report directory.
For the follow-on interpretation checklist, see
[Post-Run Analysis](post-run-analysis.md).

## Canonical Location

Generated reports belong under the canonical reports tree:

```text
/home/gaarai/code/uptime-bench/reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION>/
```

Use a stable, sortable directory name with the UTC start timestamp first,
followed by a compact planned or actual duration and a short human description:

```text
20260502T063755Z-7h-v2-regression
20260503T044255Z-8h-v1-v2-overnight
20260503T151102Z-34m-capacity-scout
```

The timestamp format is `YYYYMMDDTHHMMSSZ`. Durations use compact forms such
as `15m`, `1h30m`, or `8h`; exact start/end times remain in `run.meta.tsv` and
`manifest.json`. Keep the description short and path-safe. Put full campaign
IDs, monitor lists, notes, caveats, and analysis in the report files rather
than the directory name.

When `uptime-bench-finalize` writes a report without `-out-dir`, it uses the
campaign's earliest start, actual completed duration, and campaign config ID.
Jetmon capacity runs use the command start time, planned window length or suite
runtime estimate, and either `-description` or the capacity config ID.

When working from a sibling worktree, still write or move generated reports to
`/home/gaarai/code/uptime-bench/reports`. Do not leave final report bundles in
temporary worktree-local `reports/` directories.

## Required Bundle

Every scenario or campaign run should include these files. For campaign runs,
`uptime-bench-finalize` now writes the durable database/report artifacts
directly. Run launchers/controllers are still responsible for copying
process-local artifacts such as logs, generated ad-hoc scenario files, and
post-run target cleanup snapshots. The checked-in harness can write those
controller-owned artifacts when invoked with
`-out-dir=/home/gaarai/code/uptime-bench/reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION>`.

| Path | Purpose |
|---|---|
| `report.md` | Human-readable analysis and executive summary. This is the first file an operator or developer should read. |
| `manifest.json` | Machine-readable list of generated artifacts, input campaign/run identity, included campaign runs, and generation timestamp. |
| `run.meta.tsv` | Run tag, campaign ID, UTC start/end timestamps, monitor set, timing parameters, and other high-level run metadata. |
| `scenario-plan.tsv` | Planned scenario list with IDs, source scenario files, replay count, monitor set, target scope, and schedule intent. |
| `schedule.tsv` | Actual scenario execution order and timing, including skipped or not-started rows when a deadline stops a run. |
| `scenario_runs.tsv` | Raw `scenario_runs` export from MySQL for the included run IDs. |
| `ground_truth_events.tsv` | Raw injected target/DNS/TLS event export from MySQL. |
| `monitor_reports.tsv` | Raw adapter/provider report export from MySQL, including `reason_code` and metadata. |
| `derived_metrics.tsv` | Raw derived scoring export from MySQL. |
| `target-status-after.json` | Post-run target and DNS cleanup verification. |
| `controller-summary.md` | Human-readable cleanup summary derived from `target-status-after.json`, including per-member active failure and control-plane error counts. |
| `logs/` | Harness, controller, adapter, and target-control logs needed to debug failures. |
| `scenarios/` | Exact generated or selected scenario TOML files used by the run. |
| `campaigns/` | Exact campaign TOML configs for campaign-generated runs. |

If the normal runner or finalizer does not produce the full bundle, backfill
the missing files from the harness database, run logs, generated scenario
directory, and controller output before considering the run finished.

`uptime-bench-finalize` writes:

- `report.md`
- `report.json`
- `manifest.json`
- `run.meta.tsv`
- `campaign_runs.tsv`
- `scenario_runs.tsv`
- `ground_truth_events.tsv`
- `monitor_reports.tsv`
- `derived_metrics.tsv`
- `scenario-plan.tsv`
- `schedule.tsv`
- `campaigns/*.toml`
- `controller-summary.md` when `target-status-after.json` exists in the report
  directory before finalization
- `capacity.md`, `capacity.json`, and `capacity.txt` when `-capacity` is used.

The finalizer intentionally does not contact target controls or provider APIs
after the run. `target-status-after.json`, `logs/`, `scenarios/`, and optional
driver/controller files must come from the run controller.

`uptime-bench-harness -out-dir=<report-dir>` writes:

- `logs/harness.log`
- `controller.log`
- `run-results.tsv`
- `target-status-after.json`
- `scenarios/<input>.toml` for single-scenario runs
- `campaigns/<input>.toml` for campaign runs

The post-run status snapshot queries every distinct target and DNS control
endpoint in `fleet.toml`. Per-member control errors are preserved inside
`target-status-after.json` instead of being collapsed into one log line, so the
report can distinguish clean target state from an unreachable control plane.
When that snapshot is present, `uptime-bench-finalize` also writes
`controller-summary.md` and appends the cleanup status to `report.md`.
Use the same `-out-dir` value when later running `uptime-bench-finalize` so the
controller artifacts and durable database/report artifacts land in one bundle.
The finalizer scans the report directory before writing `manifest.json`, so
pre-existing controller artifacts are included in the final manifest.

Before marking ad-hoc or long-running validation bundles complete, run a local
artifact scan over the final report directory:

```bash
bin/uptime-bench-report-scan \
  -dir=/home/gaarai/code/uptime-bench/reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION> \
  -out=/home/gaarai/code/uptime-bench/reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION>/90-artifact-scan-final.txt
```

The scanner looks for real WPCOM/SVN endpoints and credential-shaped values
without failing on prose-only safety statements in `report.md`. It skips prior
`90-artifact-scan-*.txt` files so repeated scans do not self-match.

## Capacity Artifacts

Any run that includes Jetmon v1, Jetmon v2, Gatus, Uptime Kuma, or other
locally hosted services should also include capacity artifacts from the
monitoring Prometheus window that matches the actual scenario run:

| Path | Purpose |
|---|---|
| `capacity.md` | Human-readable resource and service-capacity analysis. |
| `capacity.json` | Machine-readable Prometheus summaries and metadata. |
| `capacity.txt` | Plain-text summary for quick terminal review. |

Capacity capture must use the run's actual UTC start and end timestamps, not a
post-run approximation. Include the Prometheus URL, scrape step, queried
instances, and any scrape-health gaps in the capacity report.

For Jetmon v1/v2 capacity growth suites, the suite directory should include
`capacity.md`, `capacity.json`, `run.json`, `summary.txt`, per-batch manifests,
SQL lifecycle artifacts, execution results, exact UTC window timestamps, and
`prometheus-window.json` when Prometheus capture is enabled.

## Jetmon v2 Reporting Notes

Jetmon v2 API cleanup is a soft delete. Reports should say that API cleanup
soft-deleted benchmark sites by setting `monitor_active=0`; the legacy
`jetpack_monitor_sites` row remains. If a test also physically purges
benchmark-only sidecar or runtime rows, describe that as a separate
benchmark-only cleanup step and name the affected tables or artifact. Do not
summarize Jetmon v2 API-created site cleanup as "deleted N sites" unless the
line is explicitly about a physical benchmark-only purge.

For PR 109-style staged HEAD/GET rollout runs, use a strict schema/runtime log
scan as the pass/fail signal. Normal streaming telemetry fields such as
`error_timeout`, `error_connect`, `error_keyword`, and `error_body_read` are
expected metric names, not schema errors. A clean run should be reported as:

```text
Strict schema/runtime scan: clean
```

Broad exploratory scans may still be useful during manual debugging, but label
them as exploratory/noisy if they can match normal metric names.

When StatsD/Graphite capture is available for Jetmon v2 staged rollout runs,
validate that the expected low-cardinality cohort counters are present and
non-zero for each cohort exercised by the run:

```text
scheduler.streaming.check.method.head.profile.legacy.count
scheduler.streaming.check.method.get.profile.simple_http.count
scheduler.streaming.check.method.get.profile.full.count
```

StatsD absence should not fail a correctness run by itself. If the raw metrics
were not captured, report: "StatsD cohort counter validation: not captured;
MySQL check-history evidence used instead."

## Optional But Preferred Files

Include these when available because they make later investigation faster:

| Path | Purpose |
|---|---|
| `report.json` | Machine-readable version of `report.md` output from `uptime-bench-finalize`. |
| `driver.log` | Top-level driver or launcher log for ad-hoc controlled runs. |
| `controller.log` | Controller output for generated schedule execution. |
| `launcher.log` | Remote launcher output when a run was started by a wrapper script. |
| `run-results.tsv` | Controller-level scenario status rows. |
| `evaluation_rows.tsv` | External evaluator output, if a separate evaluator was used. |
| `order.tsv` | Scenario order chosen by a custom driver. |
| `live-sha256sums.txt` | Checksums of deployed binaries/configs used for the run. |
| `services.redacted.toml` | Redacted adapter configuration snapshot. Never store secrets. |

## `report.md` Content

`report.md` should present the most important information first and should be
useful to sysadmins, service owners, and adapter developers. Use this shape:

1. Executive summary: overall result, major regressions, major improvements,
   stop conditions, and whether cleanup completed.
2. Run scope: run tag, campaign ID, exact UTC window, monitors included, target
   domain/fleet, timing settings, scenario count, and any deadline or manual
   stop behavior.
3. Outcome table: pass/fail/degraded/unsupported/adapter-error/setup-crash
   counts by service and scenario family.
4. Notable failures: the smallest set of concrete findings that explain the
   run, with scenario IDs and service names.
5. Capacity summary: CPU, memory, process count, open file descriptors, scrape
   health, and any local-service saturation or missed-check evidence.
6. Detection latency: p50/p95/max latency for comparable true positives,
   separated from unsupported, unknown, and suppressed rows.
7. Capability and error matrix: `capability_mismatch`, `adapter_error`,
   `unknown`, `maintenance_suppressed`, `cooldown_suppressed`,
   `cooldown_uncertain`, setup crashes, and not-started rows.
8. Cleanup verification: target/DNS state, provider monitor cleanup, and any
   known leaked or intentionally retained monitors.
9. Caveats and follow-up: data-quality limits, suspicious provider behavior,
   implementation bugs, and recommended next tests.
10. Raw artifacts: list the raw TSV/JSON/log files readers should use to
    reproduce or challenge the analysis.

## Scoring Rules

Reports must keep these categories separate:

- `true_positive`, `false_negative`, and `false_positive` are behavioral
  accuracy outcomes.
- `capability_mismatch` means the service was not asked to do something its
  adapter declares unsupported. It is a support-matrix result, not a miss.
- `adapter_error` means uptime-bench could not reliably provision, retrieve, or
  clean up that service. It is provider/control-plane reliability evidence, not
  target detection evidence.
- `unknown` means no trustworthy service outcome was available.
- `failure_not_observable` means the target/DNS preflight could not confirm
  that the intended failure was actually visible to the controlled fleet
  surface. Treat it as setup/exposure failure evidence, not a service miss.
- `maintenance_suppressed`, `cooldown_suppressed`, and `cooldown_uncertain`
  must not be folded into false negatives.
- TLS advisory scenarios must distinguish advisory detection from hard outage
  alerts and missed advisories.
- Method-trap scenarios where `HEAD` fails but `GET` succeeds should treat a
  hard outage alert as a false positive when the intended monitor behavior is a
  healthy GET check.
- Setup crashes and not-started rows must be visible even when they do not
  appear in `derived_metrics.tsv`.

## Completion Checklist

Before declaring a run complete:

1. Confirm `report.md` exists and summarizes both behavioral results and
   capacity context when local services are in scope.
2. Confirm raw MySQL exports exist for scenario runs, ground truth, monitor
   reports, and derived metrics.
3. Confirm logs and exact scenario definitions are preserved.
4. Confirm target/DNS cleanup status is captured after the run.
5. Confirm capacity artifacts exist for Jetmon/local-service runs and use the
   actual run window.
6. Confirm all generated artifacts are under
   `/home/gaarai/code/uptime-bench/reports/<START_TIMESTAMP>-<DURATION>-<DESCRIPTION>/`.
7. Confirm no secrets are present in copied configs, logs, or redacted service
   snapshots.
