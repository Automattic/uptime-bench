# Inter-run monitor state — design spec

**Status:** Draft, 2026-04-26. Resolves the open design questions on ROADMAP.md active priorities #3 (maintenance window suppression) and #4 (alert cooldown interaction between runs). Once implementation lands, the live content here moves into ARCHITECTURE.md and this file is deleted.

## Why these two features belong in one spec

Both features are about **monitor state that persists across scenario boundaries** and that the harness needs to either control or observe to keep its measurements honest.

- **Maintenance windows** — a vendor-side scheduling primitive that suppresses alerts. The harness wants to *test* whether monitors honour these windows correctly: a scenario declares "this failure runs during a maintenance window," and the expected outcome is "no alert" rather than "alert fired."
- **Alert cooldowns** — a vendor-side rate-limiting primitive that suppresses repeated alerts for the same monitor. The harness wants to *avoid* this confusing back-to-back scenario runs against a long-lived monitor: run 1 fires the alert, run 2's "missed alert" looks like a false negative when it's actually the cooldown working as designed.

Same shape: vendor-side suppression of an expected alert, harness needs to either invoke it (maintenance) or invalidate it (cooldown). The plumbing is mostly shared.

## Per-vendor research

This table is the load-bearing reference. Verify each row against the live API before implementing the corresponding adapter — APIs drift, and several entries below are inferred from public docs without live confirmation.

### Maintenance windows

| Service | API endpoint | Granularity | Notes |
|---|---|---|---|
| Pingdom | `POST /api/3.1/maintenance` with `from` / `to` (Unix timestamps), `checks` array, optional `recurrencetype` | Per-check; minute resolution | Strong support; requires the check ID we already have on the handle |
| UptimeRobot | `POST /v2/newMWindow` with `start_time` (HH:mm), `duration`, `value` (monitor IDs), `type` (`1=once`, `2-5=recurring`) | Per-monitor; minute resolution; UTC | Quirk: one-shot windows take HH:mm not date+time, so they only work for "today." Date-bounded one-shots are not directly expressible — workaround: use type=1 and accept the same-day limit, or recreate at scenario start. **Verify live.** |
| Datadog Synthetics | Reuses the **Downtime API**: `POST /api/v1/downtime` with `start`, `end` (Unix epochs), `monitor_id` for monitor-attached downtimes, or `scope` for tag-based. Synthetic tests have an attached monitor. | Per-monitor or tag-scoped; second resolution | The downtime applies to the underlying monitor, not the synthetic test directly. Need to retrieve the synthetic's monitor_id at provision time and remember it on the handle. **Verify the relationship between synthetic and monitor IDs against the live API.** |
| Better Uptime | Unclear — Better Stack has "Status Pages" with maintenance windows but those are status-page-facing, not alert-suppression. There may be a `policies` or `escalation_policies` field; the public docs are sparse. | TBD | **High research risk.** First action when implementing: ask Better Stack support whether their API exposes a per-monitor "suppress alerts" window. If not, set `SupportsMaintenanceWindows = false` and let the runner gate scenarios as `capability_mismatch`. |
| Jetmon (self-hosted) | jetmon-bridge needs a new endpoint, e.g. `POST /maintenance` writing to a `jetpack_monitor_maintenance` table the agent reads. | Per-monitor; arbitrary granularity | Trivial since we control both ends. Implement *after* the probe-based adapters land so the bridge change isn't on the critical path. |

### Cooldown reset (clean-state-on-Deprovision)

| Service | Approach | Notes |
|---|---|---|
| Pingdom | Cooldown is per-account notification rule, not per-check. Pause+resume the check (`POST /api/3.1/checks/{id}` with `paused=true`, then `paused=false`) doesn't reliably reset cooldown. **Recommendation: delete + recreate** between runs. | Cost: extra API call per run. Acceptable. |
| UptimeRobot | No traditional cooldown — every state change emits a notification. Pause/unpause cycles state. | Likely a no-op; verify by running consecutive scenarios and checking whether the second alert fires. |
| Datadog Synthetics | Alert state lives on the attached monitor; mute/unmute via `POST /api/v1/monitor/{id}/mute` and `/unmute`. Or delete + recreate the synthetic test (which we already do per run). | We already delete + recreate per run today. **No additional work needed.** |
| Better Uptime | Each incident has a lifecycle (Started → Acknowledged → Resolved). Closing the incident may or may not clear back-pressure. | Verify live. If unclear, default to delete + recreate. |
| Jetmon (self-hosted) | Direct DB access; trivial. | Match whatever the agent does — likely a `last_alert_at` column we can clear. |

## Decisions

### 1. Capability flags

Add two new fields to `adapter.Capabilities`:

```go
type Capabilities struct {
    // ... existing fields ...

    // SupportsMaintenanceWindows: adapter can configure a vendor-side
    // suppression window so the monitor still runs but does not fire
    // alerts during the declared interval.
    SupportsMaintenanceWindows bool

    // SupportsCooldownReset: Deprovision (or a separate ResetState call)
    // can clear vendor-side alert cooldown so the next run's first alert
    // is not suppressed by the previous run.
    SupportsCooldownReset bool
}
```

**Rationale:** Mirrors the existing `SupportsKeyword` / `SupportsInvertedKeyword` pattern. The runner already gates scenarios that need a capability against adapters that lack it (writing `capability_mismatch` rows); these flags slot into that pattern.

### 2. Scenario format extension

Add a `[maintenance]` block to scenarios that want to test maintenance-window suppression:

```toml
id = "maintenance-window-suppress"
# ... usual fields ...

[maintenance]
# Window opens this far after scenario start. Defaults to scenario_start = 0s.
start_offset = "0s"
# Window duration. The failure period is independent — they may overlap fully,
# partially, or the failure may extend beyond the window's end.
duration = "300s"

[[failures]]
type = "http_status"
status_code = 503
```

**Rationale:**

- Top-level `[maintenance]` block (rather than per-failure) because it's a property of the *monitor's configured behaviour*, not the failure being injected. Same logic as why we hoisted `keyword` to scenario level.
- `start_offset` lets scenarios cover the three test patterns: window-fully-overlaps-failure (full suppression expected), window-overlaps-leading-edge (alerts after window closes), window-overlaps-trailing-edge (alerts during failure, suppression hides only the resolution).
- `duration` is window-relative, not absolute; the runner converts to absolute timestamps at provision time.
- No second-level fields beyond start/duration in v1. Future extensions (recurrence, multiple windows) can be added without breaking the scenario format.

### 3. ProvisionConfig extension

```go
type ProvisionConfig struct {
    // ... existing fields ...

    // MaintenanceWindow describes a vendor-side alert-suppression window
    // for this run. Empty = no maintenance window. Adapter must honour it
    // if Capabilities.SupportsMaintenanceWindows is true; otherwise the
    // runner gates the scenario as capability_mismatch before reaching
    // Provision.
    MaintenanceWindow *MaintenanceWindow
}

type MaintenanceWindow struct {
    Start time.Time     // absolute, UTC
    End   time.Time     // absolute, UTC
}
```

**Rationale:** Pointer-with-nil follows the same convention as TLS-related fields will (in the upcoming TLS work). Absolute times because the adapter shouldn't have to know about scenario start.

### 4. New `reason_code` values

The `reason_code` column already exists (shipped with the keyword work). Add three new codes:

| Code | Meaning |
|---|---|
| `maintenance_suppressed` | A failure was active and the monitor returned no alerts, AND a maintenance window was active during the failure. Correct behaviour, not a false negative. |
| `cooldown_suppressed` | A failure was active, the monitor returned no alerts, the maintenance gate didn't fire, AND a recent prior run on the same monitor had alerted. Correct (cooldown working) but uninformative for benchmark purposes; record separately. |
| `cooldown_reset_failed` | Deprovision attempted to reset alert cooldown and failed. Recorded so the next run's data can be flagged as potentially-cooldown-affected. |

The first two are written by the **measurement engine**, not the runner. The third is written by the runner when Deprovision returns a non-fatal cooldown-reset error.

### 5. Measurement engine — three new outcome categories

| Outcome | When it applies |
|---|---|
| Existing: `true_positive` | Failure active, monitor alerted in window, no maintenance, no cooldown suppression. |
| Existing: `false_negative` | Failure active, monitor did not alert, no maintenance, no cooldown suppression. |
| New: `maintenance_suppressed` | Failure active, monitor did not alert, maintenance window covered ≥80% of the failure period. **Not a missed detection.** |
| New: `cooldown_suppressed` | Failure active, monitor did not alert, no maintenance, prior run within cooldown window had alerted. **Possibly a missed detection; we can't tell.** |
| New: `cooldown_uncertain` | Same as `cooldown_suppressed` but for the second-run-after-reset case where Deprovision said "I don't know if reset worked." Rare. |

The 80% threshold for maintenance is a heuristic — fully-covered windows are unambiguous, but partial overlaps need a rule. 80% covers the "window-overlaps-trailing-edge" case where the failure ran for 5 min and the window covered the last 4 min: the monitor had time to alert during the first minute, so a missing alert is genuinely a false negative.

### 6. New scenario validation rule

```
If scenario has [maintenance] block, every monitor in the monitors list
must have Capabilities.SupportsMaintenanceWindows == true. Otherwise the
runner gates the adapter as capability_mismatch.
```

Same pattern as `SupportsKeyword` for content scenarios.

## Implementation order

Three phases. Each phase compiles, tests, and ships independently.

### Phase A — capability flags + scenario format

1. Add `SupportsMaintenanceWindows` and `SupportsCooldownReset` to `Capabilities`. Default `false` on all adapters.
2. Add `[maintenance]` block parsing to `internal/scenario`. Validation: start_offset ≥ 0, duration > 0.
3. Add `MaintenanceWindow` to `ProvisionConfig`. Runner populates it from the scenario.
4. Add `maintenance_suppressed` and `cooldown_suppressed` reason codes (constants in `internal/adapter`).
5. Runner gates scenarios with `[maintenance]` against adapters where `SupportsMaintenanceWindows = false`. Capability-mismatch row, no Provision.
6. **Tests:** scenario parsing, validator, runner gating.

End of Phase A: scenario format works, runner gates correctly, but no adapter actually configures a maintenance window yet (because all flags default false). Mergeable, no behaviour change for existing scenarios.

### Phase B — adapter implementations (one PR per adapter)

For each adapter:

1. Wire the maintenance-window API call into `Provision`.
2. Wire the cooldown-reset API call (or document that it's a no-op) into `Deprovision`.
3. Set `SupportsMaintenanceWindows` / `SupportsCooldownReset` to `true`.
4. Live test: write a scenario with `[maintenance]`, run it, confirm no alerts during the window.

**Recommended order, easiest to hardest:**

1. **Datadog Synthetics** first — Downtime API is well-documented and we already delete-recreate so cooldown is no-op. Lowest research risk.
2. **Pingdom** — `POST /api/3.1/maintenance` is straightforward; cooldown via delete+recreate already happens.
3. **UptimeRobot** — `newMWindow` API with the same-day-only limitation. May need a special-case to recreate the window if the scenario crosses midnight.
4. **Better Uptime** — research first. If no maintenance API, leave `SupportsMaintenanceWindows = false`; runner gates content scenarios as capability_mismatch (data, not failure).
5. **Jetmon** — requires bridge changes. Schedule alongside the next bridge release.

### Phase C — measurement engine extensions

After at least one adapter has Phase B complete:

1. Update `internal/measurement` to compute the three new outcome categories.
2. Add the 80%-overlap threshold logic with a test that covers the four overlap shapes (no overlap, fully covered, leading-edge partial, trailing-edge partial).
3. Update queries in EVENTS.md / OPERATIONS.md to filter on the new reason codes.

## What this spec deliberately doesn't cover

- **Recurring maintenance windows** (e.g. "every Sunday 02:00 UTC for 1 hour"). Out of scope for v1 — scenarios are ephemeral, recurrence isn't useful here.
- **Multi-window scenarios** (more than one maintenance period in a single run). Out of scope; one window per scenario covers every test pattern we currently care about.
- **Alert *suppression* via downtime severity tagging** (e.g. Datadog's "downtime priority"). Adapters use the simplest form; advanced suppression is a v2 question if it ever matters.
- **Status page maintenance announcements** (Better Stack's status-page primitive). Different concept — that's about user-facing communication, not alert suppression.

## Open questions to resolve at implementation time

1. **Better Uptime maintenance API**: does it exist? If yes, what's the endpoint? Resolution: ask support, or set capability false and move on.
2. **UptimeRobot one-shot window crossing midnight**: does `type=1` with `duration > now-until-midnight` work, or does the API reject? Resolution: live test.
3. **Datadog synthetic-vs-monitor ID mapping**: which ID does the Downtime API want? Resolution: live experiment, then pin it on the handle's `Fields` map.
4. **Cooldown-reset failure semantics**: if delete+recreate fails midway (e.g. delete succeeds, recreate fails), the next run's monitor is gone entirely. Is that a `cooldown_reset_failed` row, or an `adapter_error` resolution_reason? Resolution: probably the latter, but flag it explicitly in the runner code.
