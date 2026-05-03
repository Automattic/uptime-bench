# Post-Run Analysis

Use this checklist after a campaign or capacity run completes. It is intentionally
read-only: finalize and report commands should write artifacts, but analysis
should not mutate provider monitors, fleet services, or target state.

Use [Reporting Standard](reporting.md) as the artifact contract for what must
exist in the report directory before starting this analysis.

## Preserve The Run

Before interpreting results, make sure the run directory has the artifacts that
make the conclusions reproducible:

- `run.meta.tsv` with the exact campaign window.
- `report.md` and `report.json` for scenario accuracy.
- `capacity.md`, `capacity.json`, and `capacity.txt` when
  `uptime-bench-finalize -capacity` was used or a Jetmon capacity suite was
  run.
- Raw exports for `scenario_runs`, `ground_truth_events`, `monitor_reports`,
  and `derived_metrics` when the run was produced from a live database.
- A redacted service snapshot showing enabled services, monitor IDs or kinds,
  scenario list, run width, replay count, timing, and adapter versions.

If any of those artifacts are missing, regenerate them before doing manual
analysis so the report does not depend on transient database state.

## Scenario Accuracy

Start with the service-by-service pass counts, then inspect the reasons behind
non-passing rows:

- `true_positive`, `false_negative`, and `false_positive` counts by service and
  scenario.
- `capability_mismatch` rows as the support matrix, not as failures.
- `adapter_error` rows by provider error text so API drift, duplicate resources,
  and rate limits stay visible.
- `maintenance_suppressed`, `cooldown_suppressed`, and `cooldown_uncertain`
  rows separately from outage accuracy.
- Unknown rows with no derived metric, because those usually point to retrieval
  or normalization gaps.

For HEAD/GET mismatch scenarios, verify that the expected direction was scored:

- HEAD failure with healthy GET should fail a service only if it reports
  downtime for a visitor-healthy site.
- GET failure with healthy HEAD should fail a service only if it reports uptime
  for a visitor-broken site.

## Timing

Compare detection latency only after filtering to rows where detection actually
happened:

- Minimum, maximum, mean, median, and p95 detection latency per service/scenario.
- Samples near the scenario end time, which can indicate probe cadence or
  measurement-window edge cases.
- Resolve/clear events, when available, separately from down-detection events.
- Probe-location metadata, if the adapter preserved it, for geo-sensitive or
  quorum-sensitive interpretation.

Latency comparisons are only fair between services that were configured with
comparable check intervals and monitor kinds.

## Adapter Health

Adapter errors are benchmark data, but repeated errors should still become
engineering follow-ups:

- Group provider errors by stable text or subcode.
- Check whether errors cluster by service, scenario, monitor kind, request
  method, or replay position.
- Look for leaked provider resources after interrupted or errored runs.
- Confirm cleanup artifacts distinguish deleted, skipped, and ambiguous
  resources.
- Treat duplicate-resource errors as a cleanup or ownership bug until proven
  otherwise.

Do not rerun provider cleanup while another campaign is active.

## Capacity Artifacts

When `capacity.md` or `capacity.json` exists, read it alongside accuracy data:

- Last clean batch and first problem batch.
- DB health at the end of each active window.
- Missed-check percentage, recent checks per minute, p95 check age, and oldest
  check age.
- Prometheus scrape health before trusting CPU, memory, disk, process, or
  container series.
- Threshold failures and `stop_recommended` reasons.
- Cleanup status after each batch.

Capacity failures should not be collapsed into scenario misses. They explain why
later scenario accuracy may degrade as active monitor count rises.

## Regression Review

Compare the completed run to the most recent comparable baseline:

- Same service ID, adapter version, scenario, monitor kind, method, and interval.
- Pass/total deltas by service.
- Detection-latency shifts large enough to exceed one provider check interval.
- New unsupported/capability-mismatch cells.
- New adapter-error buckets.
- Jetmon v1/v2 behavior on scenarios that previously exposed known issues.

If setup changed between runs, call that out before attributing a regression to
adapter or service behavior.

## Suggested Summary Shape

A useful human report should include:

- Run metadata: window, services, scenarios, sample count, width, and timing.
- Service pass/total table.
- Scenario/service matrix with pass, fail, unsupported, unknown, and suppressed
  categories.
- Detection-latency table for detected failures.
- Adapter-error breakdown.
- Capacity summary when available.
- Key takeaways and recommended next experiments.
