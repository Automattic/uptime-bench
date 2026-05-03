---
name: safe-background-work
description: Pick useful uptime-bench work that cannot affect active tests.
---

# Safe Background Work

Use this when Chris asks what can be done while tests run, or says to keep work
going without interrupting a live run.

## Allowed Work

- Agent files and playbooks.
- Local-only documentation drafts when Chris has allowed docs changes.
- Report analysis of completed runs.
- Branch inspection and non-mutating comparisons.
- Local code review.
- Local commits on branches that are not deployed and will not trigger
  automation.

## Avoid Unless Explicitly Allowed

- Deploying binaries or configs.
- Restarting services.
- Running smoke tests against active providers or target controls.
- Modifying fleet configs or remote host state.
- Deleting branches that another active process might use.

## Blocker Policy

If a task needs approval, note it and move to the next safe task instead of
waiting.
