# Reporting Standard

This document defines the report bundle that should be generated after every
uptime-bench run. A run is not complete until its raw evidence, operator
summary, analysis, and cleanup status are preserved in the report directory.
For the follow-on interpretation checklist, see
[Post-Run Analysis](post-run-analysis.md).

## Canonical Location

Generated reports belong under the canonical reports tree:

```text
/home/gaarai/code/uptime-bench/reports/<run-tag>/
```

Use a stable, sortable run tag that includes the campaign purpose and UTC start
time, for example:

```text
v2-regression-20260502-063755Z
overnight-v1-inclusive-20260503-044255Z
capacity-scout-20260503-151102Z
```

When working from a sibling worktree, still write or move generated reports to
`/home/gaarai/code/uptime-bench/reports`. Do not leave final report bundles in
temporary worktree-local `reports/` directories.

## Required Bundle

Every scenario or campaign run should include these files. For campaign runs,
`uptime-bench-finalize` now writes the durable database/report artifacts
directly. Run launchers/controllers are still responsible for copying
process-local artifacts such as logs, generated ad-hoc scenario files, and
post-run target cleanup snapshots.

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
- `capacity.md`, `capacity.json`, and `capacity.txt` when `-capacity` is used.

The finalizer intentionally does not contact target controls or provider APIs
after the run. `target-status-after.json`, `logs/`, `scenarios/`, and optional
driver/controller files must come from the run controller.

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
   `/home/gaarai/code/uptime-bench/reports/<run-tag>/`.
7. Confirm no secrets are present in copied configs, logs, or redacted service
   snapshots.
