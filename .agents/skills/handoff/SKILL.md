---
name: handoff
description: Create a self-contained handoff for another uptime-bench or Jetmon agent.
---

# Handoff

Use this when Chris asks for a handoff doc or wants another agent to continue a
thread of work.

## Include

- Repo path and current branch.
- Relevant sibling repos/worktrees.
- Active test or deployment locks.
- Problem statement and why it matters.
- Evidence: report directories, logs, commits, metrics, exact files.
- Current state and what has already been ruled out.
- Risks, caveats, and assumptions.
- Suggested next steps with commands when useful.

## Placement

During active tests, prefer agent-specific files under `.agents` or global
memory. Ask before adding or editing non-agent docs.

## Secrets

Do not include tokens, passwords, private keys, or unredacted service configs.
