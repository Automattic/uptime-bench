# uptime-bench Agent Guide

This file is for Codex and other coding agents. Read `CLAUDE.md` for the
project architecture and coding conventions, then use the workflow notes below
for Chris's recurring operations.

## Current Safety Default

- If Chris says a test is running, do not change deployed services, fleet
  configuration, provider state, remote hosts, databases, or runtime config.
- During active tests, limit work to local analysis, agent files, report review,
  branch inspection, and other tasks that cannot affect the run.
- Ask for explicit permission before touching non-agent project files while a
  test is active.

## Canonical Paths

- Canonical repo: `/home/gaarai/code/uptime-bench`.
- Canonical reports tree: `/home/gaarai/code/uptime-bench/reports`.
- Sibling worktrees may exist, but final generated reports still belong in the
  canonical reports tree.
- Reporting contract: `docs/reporting.md`.
- Post-run interpretation checklist: `docs/post-run-analysis.md`.

## Workflow Shorthand

- `finish this run <tag>`: complete the report bundle per `docs/reporting.md`,
  including capacity artifacts when Jetmon or other local services are in scope.
- `prep next capacity run`: update the allowed branch/worktree, verify harness
  and fleet readiness, deploy only when allowed, run approved smoke checks, and
  report readiness.
- `safe background work`: choose local-only work that cannot disturb active
  tests; move past blockers instead of waiting for user input.
- `clean branches`: classify branches, remove only clearly safe merged/stale
  branches, and list uncertain branches before acting.
- `merge when ready`: inspect, update from trunk, verify, commit, push, merge
  to trunk, return to trunk, and clean up the merged branch when safe.
- `handoff <topic>`: write complete agent context with paths, branches,
  evidence, risks, commands, and next actions.

## Local Agent Skills

Project-specific playbooks live under `.agents/skills`:

- `finish-uptime-run`
- `prep-capacity-run`
- `safe-background-work`
- `branch-cleanup`
- `handoff`

Use these playbooks when the user's request matches the workflow name, even if
the request is short.

## Reporting Defaults

- Keep behavioral outcomes separate from unsupported, unknown, adapter errors,
  cooldown suppression, maintenance suppression, setup crashes, and not-started
  rows.
- Capture capacity over the actual UTC run window for Jetmon/local-service
  runs.
- Do not store secrets in reports, copied configs, logs, or handoff material.

## Permission Boundary

The following are not agent-only files and should not be changed during active
tests without permission: code, deploy scripts, runtime config, project docs,
report data, generated scenarios, fleet/service configuration, and anything on
remote hosts.
