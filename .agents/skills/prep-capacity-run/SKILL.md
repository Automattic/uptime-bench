---
name: prep-capacity-run
description: Prepare uptime-bench and the Jetmon/local-service fleet for a capacity run.
---

# Prepare Capacity Run

Use this when Chris says "prep next capacity run", asks to get ready for tests,
or asks whether the fleet is ready.

## Default Flow

1. Confirm the intended repo path, branch, and whether tests are currently
   running.
2. Pull/update only when safe for the current worktree.
3. Verify local build/test state relevant to the planned run.
4. Deploy harness/fleet changes only when Chris has allowed deployment.
5. Run smoke checks only when they cannot disturb another active run or Chris
   has explicitly allowed them.
6. Report readiness with exact branch, commit, deployed components, skipped
   checks, and remaining risks.

## Active-Test Rule

If any test is active, do not alter deployed services, support hosts, target
fleet state, or provider monitors. Provide a plan and ask for permission before
acting.

## Output Shape

- Ready/not ready.
- What was verified.
- What was intentionally skipped.
- What needs Chris's approval before the run can start.
