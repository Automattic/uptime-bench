# Safe Concurrent Work

Use this guide when an uptime-bench scenario, campaign, or Jetmon capacity run
is already active and another agent needs to keep working without disturbing
the evidence being collected.

## Rule Of Thumb

If a command can change target state, DNS state, provider monitors, Jetmon
monitor rows, deployed binaries, service configs, or the active run database,
do not run it while a live test is active. Prefer local code, docs, and unit
tests until the run owner confirms the test is complete or paused.

## Safe Without Coordination

These tasks are safe because they do not contact or mutate the live fleet:

| Work | Notes |
|---|---|
| Local code and docs changes | Use a feature branch and keep commits scoped. |
| Unit tests and package tests | Avoid live/build-tagged tests and tests that require provider credentials. |
| Static checks, formatting, and local builds | Safe when they only touch the current worktree. |
| Reviewing copied reports and artifacts | Read files already preserved under `reports/` or an isolated local directory. |
| Planning config changes | Generate diffs or example configs, but do not sync or deploy them. |
| Read-only Git operations | Fetching and inspecting branches is fine. Avoid merging into a branch another active run depends on unless coordinated. |

## Coordinate First

These can be safe in some cases, but the active run owner should approve the
specific command and target:

| Work | Risk |
|---|---|
| Read-only Prometheus/Grafana queries | Usually safe, but avoid heavy ad-hoc ranges that could compete with run collection. |
| Finalizing reports from MySQL | `uptime-bench-finalize` can write derived metrics by default; use only for completed runs or run with an explicit read-only posture. |
| SSH inspection on fleet hosts | Simple read-only checks are usually fine, but mistakes in shell sessions can restart or reconfigure services. |
| Branch merges used by deployment automation | Merging code is local, but automation or another agent may deploy from `trunk`. |

## Do Not Run During Active Tests

These actions can invalidate a run, erase cleanup evidence, or change the
system under test:

| Command Class | Examples |
|---|---|
| Harness or capacity runs that mutate state | `uptime-bench-harness`, `uptime-bench-jetmon-capacity-run -apply`, capacity lifecycle apply modes. |
| Provider cleanup | Non-dry-run cleanup commands, provider monitor deletes, adapter smoke tests that create/delete monitors. |
| Fleet deploys and syncs | `make deploy-*`, monitoring config syncs, service binary replacements, systemd restarts. |
| Target or DNS changes | Restarting target/DNS services, changing fleet routing, changing target failure state. |
| Jetmon lifecycle mutations | Seeding, activating, deactivating, or purging capacity DB ranges. |
| Live verifier changes | Restarting, moving, or reconfiguring Verifliers while Jetmon v2 is under test. |

## Branch Practice

When working around an active test, create one branch per independent local
change. Commit each branch cleanly, leave the worktree clean, and record which
branches are ready for later merge. This keeps useful work moving while making
it obvious which changes are safe to review after the run finishes.
