---
name: branch-cleanup
description: Classify and safely clean uptime-bench local and remote branches.
---

# Branch Cleanup

Use this when Chris asks to clean branches, merge remaining work, or reconcile
old feature branches.

## Process

1. Identify the repo, current branch, dirty state, remotes, and trunk commit.
2. List local and remote branches.
3. Classify each branch:
   - merged into trunk
   - contains unique commits worth reviewing
   - superseded by another branch
   - stale but uncertain
   - active or protected
4. Remove only branches that are clearly merged or explicitly approved.
5. For unique work, prefer a review/cherry-pick plan over blind merging.

## Active-Test Rule

Branch deletion is usually local/remote metadata, but still avoid broad cleanup
when multiple agents are active unless Chris scopes the repo and confirms other
processes do not need the branches.

## Final Summary

Report removed branches, kept branches, useful commits recovered, and any
remaining decisions.
