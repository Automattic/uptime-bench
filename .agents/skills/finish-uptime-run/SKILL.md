---
name: finish-uptime-run
description: Complete an uptime-bench run report bundle after a scenario or capacity run finishes.
---

# Finish uptime-bench Run

Use this when Chris says a run completed, asks for findings for a run, or says
to "finish this run".

## Inputs To Discover

- Run tag or report directory.
- Whether Jetmon v1, Jetmon v2, Gatus, Uptime Kuma, or other local services were
  in scope.
- Actual UTC start and end timestamps.
- Whether the run was a normal scenario/campaign run or a capacity growth suite.

## Required Behavior

1. Use `/home/gaarai/code/uptime-bench/reports/<run-tag>` as the final bundle
   location.
2. Follow `docs/reporting.md`.
3. Preserve raw artifacts before writing conclusions.
4. Generate or backfill `report.md`, `manifest.json`, raw TSV exports, logs,
   scenarios/campaigns, and cleanup status.
5. Add capacity artifacts for Jetmon or other local services using the actual
   run window.
6. Keep unsupported/capability mismatch, unknown, adapter errors, cooldown,
   maintenance suppression, setup crashes, and not-started rows separate from
   detection failures.
7. Summarize the result, confidence, cleanup state, and recommended next action.

## Safety

- Do not change provider state or remote services while finishing a report
  unless Chris explicitly asks for cleanup or deployment.
- Never include secrets in generated artifacts.
