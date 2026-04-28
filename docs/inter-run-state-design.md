# Inter-run monitor state — design spec

**Status:** Partially implemented, updated 2026-04-28. The maintenance-window half of this design has landed in scenario parsing, runner gating, adapter provisioning, and measurement. The cooldown-reset half now has `SupportsCooldownReset` flags, delete/recreate adapter behavior, Jetmon v2 zero-cooldown provisioning, campaign replay gating for adapters that cannot reset cooldown state, and derived `cooldown_suppressed` / `cooldown_uncertain` measurement categories. Jetmon v1 bridge/API reset support remains future work.

## Why these two features belong in one spec

Both features are about **monitor state that persists across scenario boundaries** and that the harness needs to either control or observe to keep its measurements honest.

- **Maintenance windows** — a vendor-side scheduling primitive that suppresses alerts. The harness wants to *test* whether monitors honour these windows correctly: a scenario declares "this failure runs during a maintenance window," and the expected outcome is "no alert" rather than "alert fired."
- **Alert cooldowns** — a vendor-side rate-limiting primitive that suppresses repeated alerts for the same monitor. The harness wants to *avoid* this confusing back-to-back scenario runs against a long-lived monitor: run 1 fires the alert, run 2's "missed alert" looks like a false negative when it's actually the cooldown working as designed.

Same shape: vendor-side suppression of an expected alert, harness needs to either invoke it (maintenance) or invalidate it (cooldown). The plumbing is mostly shared.

## Per-vendor research

This table is the load-bearing reference. Each entry below was verified against the vendor's documentation on 2026-04-26 except where noted as "live verification needed" — those typically involve runtime behaviour that docs don't pin down (e.g. how synthetic-test IDs map to monitor IDs).

### Maintenance windows

| Service | API endpoint | Granularity | Notes |
|---|---|---|---|
| Pingdom | `POST /api/3.1/maintenance` form-encoded: `description`, `from` / `to` (Unix timestamps, integer seconds), `checks` (comma-separated list of check IDs), optional `recurrencetype` | Per-check; minute resolution | Verified via working curl examples in vendor forum + community libraries. The check ID we already store on the handle goes straight into `checks`. |
| UptimeRobot | Two-step: (1) `POST /v2/newMWindow` to create the window, (2) `POST /v2/editMonitor` with `mwindows=<id>` (dash-separated for multiple) to attach the window to monitors. `newMWindow` fields: `friendly_name`, `type` (1=Once, 2=Daily, 3=Weekly, 4=Monthly), `start_time` (Unix timestamp for type=1), `duration` (minutes), `value` (only for weekly/monthly: day numbers like `2-4-5`). | Per-window, attached to N monitors | **Spec correction**: earlier draft said `value` carries monitor IDs and `start_time` is HH:mm — both wrong. Monitor association is via `editMonitor.mwindows`, not the window-creation call. Cost: provision becomes 3 API calls (`newMonitor` → `newMWindow` → `editMonitor`); free tier's 10-req/min budget is tight but workable. |
| Datadog Synthetics | Two flavours, **only one of which we want**: <br/>• **Monitor Downtime** — `POST /api/v1/downtime` with `start`/`end` (POSIX timestamps), `monitor_id` (single int), `scope` (tag list), `message`. Mutes alerts but probes keep running. **This is what we want.** <br/>• **Synthetic Scheduled Downtime** — separate concept, no public API as of 2026-04-26; UI-only. *Stops probe execution entirely* during the window, which defeats our test. | Per-monitor | **Live-verified 2026-04-27.** `monitor_id` is a top-level int64 field on the response of `GET /api/v1/synthetics/tests/api/{public_id}` (and the no-`/api/`-infix variant — both work). The *create* response does not expose it; the adapter does an extra GET after Provision to discover it, then uses it on `POST /api/v1/downtime`. |
| Better Uptime | **Spec correction**: maintenance API DOES exist. `PATCH /api/v2/monitors/{id}` with `maintenance_from` / `maintenance_to` (HH:MM:SS format, **not** absolute timestamps), `maintenance_days` (array like `["mon","tue",...]`), `maintenance_timezone` (e.g. `"UTC"`, `"Prague"`). Recurring-day model — there is no one-shot "from absolute T1 to absolute T2" form. | Per-monitor, recurring | Adapter has to convert the scenario's absolute window into today's HH:MM:SS plus today's day-name. Caveat: scenarios that cross midnight in the configured timezone need two adjacent days in `maintenance_days` plus careful HH:MM:SS — easier to reject mid-night-crossing scenarios for this adapter. After the run, restore the monitor with empty maintenance fields to avoid the recurrence applying tomorrow. |
| Jetmon v2 | `PATCH /api/v1/sites/{id}` with `maintenance_start` / `maintenance_end`. | Per-monitor; arbitrary granularity | Implemented in the API-backed adapter and covered by the build-tagged live API contract test. |
| Jetmon v1 (self-hosted) | jetmon-bridge needs a new endpoint, e.g. `POST /maintenance` writing to a `jetpack_monitor_maintenance` table the agent reads. | Per-monitor; arbitrary granularity | Still deferred; v1 gates maintenance scenarios as `capability_mismatch`. |

### Cooldown reset (clean-state-on-Deprovision)

| Service | Approach | Notes |
|---|---|---|
| Pingdom | Cooldown is per-account notification rule, not per-check. The current adapter already deletes + recreates each run (`DELETE /checks/{id}` in Deprovision, `POST /checks` in Provision), so cooldown is naturally cycled. | **No additional work needed.** |
| UptimeRobot | No traditional cooldown — every state change emits a notification per docs/community reports. Adapter already delete-recreates each run. | **No additional work needed.** Set `SupportsCooldownReset = true` because deletion is the reset mechanism. |
| Datadog Synthetics | Synthetic test is delete-recreated each run (current adapter behaviour). The attached monitor goes with it. | **No additional work needed.** |
| Better Uptime | Monitor is delete-recreated each run (current adapter behaviour). Any incident state from the prior monitor doesn't transfer. | **No additional work needed.** |
| Jetmon v1 (self-hosted) | Direct DB access via the bridge; add an explicit alert-state reset endpoint before uptime-bench claims cooldown reset support. The endpoint should clear the fields Jetmon uses to suppress repeated alerts for a monitor, such as `last_alert_at` or the equivalent incident/cooldown marker. The uptime-bench adapter should not infer reset support from `DELETE /monitors` until the bridge documents that delete/reactivate clears that state. | Implement alongside maintenance window bridge changes. |

**Implication:** all four probe-based adapters get cooldown reset essentially for free because they already delete+recreate per run. `SupportsCooldownReset = true` is the default for the four; Jetmon v2 disables alert cooldown at provision time; Jetmon v1 still needs bridge work to claim it. Campaign replays now enforce this flag and record `capability_mismatch` rows for adapters that cannot guarantee clean alert state.

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

The `reason_code` column already exists (shipped with the keyword work). Add these suppression-related codes:

| Code | Meaning |
|---|---|
| `maintenance_suppressed` | A failure was active and the monitor returned no alerts, AND a maintenance window was active during the failure. Correct behaviour, not a false negative. |
| `cooldown_suppressed` | A failure was active, the monitor returned no alerts, the maintenance gate didn't fire, AND adapter metadata says a recent prior run on the same monitor had alerted. Correct (cooldown working) but uninformative for benchmark purposes; record separately. |
| `cooldown_uncertain` | Same as `cooldown_suppressed`, but reset state is ambiguous rather than known-suppressed. |
| `cooldown_reset_failed` | Deprovision attempted to reset alert cooldown and failed. Recorded so the next run's data can be flagged as potentially-cooldown-affected. |

The first three are written by the **measurement engine**, not the runner. `cooldown_reset_failed` is written by the runner or adapter metadata when Deprovision/reset state is ambiguous.

### 5. Measurement engine — three new outcome categories

| Outcome | When it applies |
|---|---|
| Existing: `true_positive` | Failure active, monitor alerted in window, no maintenance, no cooldown suppression. |
| Existing: `false_negative` | Failure active, monitor did not alert, no maintenance, no cooldown suppression. |
| New: `maintenance_suppressed` | Failure active, monitor did not alert, maintenance window covered ≥80% of the failure period. **Not a missed detection.** |
| New: `cooldown_suppressed` | Failure active, monitor did not alert, no maintenance, and current retrieve metadata says prior run cooldown suppressed this alert. **Possibly a missed detection; we can't tell.** |
| New: `cooldown_uncertain` | Same as `cooldown_suppressed` but for the second-run-after-reset case where Deprovision said "I don't know if reset worked." Rare. |

Adapters can attach cooldown state to a known no-event retrieve result through `RetrieveResult.Metadata`, for example `{"cooldown_state":"suppressed"}` or `{"cooldown_reset_failed":true}`. The runner writes a no-event `monitor_reports` row for successful zero-event retrieves so the measurement engine has an audit row to classify.

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

1. [done] Add `SupportsMaintenanceWindows` and `SupportsCooldownReset` to `Capabilities`.
2. [done] Add `[maintenance]` block parsing to `internal/scenario`. Validation: start_offset >= 0, duration > 0.
3. [done] Add `MaintenanceWindow` to `ProvisionConfig`. Runner populates it from the scenario.
4. [done] Add `maintenance_suppressed` and cooldown-related reason codes (constants in `internal/adapter`).
5. [done] Runner gates scenarios with `[maintenance]` against adapters where `SupportsMaintenanceWindows = false`. Capability-mismatch row, no Provision.
6. [done] **Tests:** scenario parsing, validator, runner gating.

End of Phase A: scenario format works, runner gates correctly, but no adapter actually configures a maintenance window yet (because all flags default false). Mergeable, no behaviour change for existing scenarios.

### Phase B — adapter implementations (one PR per adapter)

For each adapter:

1. [done] Wire the maintenance-window API call into `Provision` for Pingdom, UptimeRobot, Datadog Synthetics, Better Uptime, and Jetmon v2.
2. [done] Set `SupportsMaintenanceWindows` / `SupportsCooldownReset` to `true` where delete/recreate or API behavior provides clean state.
3. Pending live behavior test: write scenarios with `[maintenance]`, run them against live vendors, and confirm no alerts during the window.

**Recommended order, easiest to hardest** (revised after 2026-04-26 research):

1. **Pingdom** first — `POST /api/3.1/maintenance` is the cleanest API in the bunch (straight Unix timestamps, integer check ID list, single call). Cooldown comes free because the adapter already delete-recreates. Lowest implementation friction.
2. **Datadog Synthetics** — `/api/v1/downtime` is well-documented but requires a one-time live experiment to learn how to get the monitor_id from a freshly-created synthetic test (the docs don't pin it down). Once that's known, straightforward.
3. **Better Uptime** — formerly assumed to lack a maintenance API; **research surfaced it**. The recurring-day-with-HH:MM:SS model is awkward but workable. Adapter must convert absolute window times to today's HH:MM:SS + today's day name, and reset the maintenance fields on Deprovision to avoid the recurrence applying tomorrow.
4. **UptimeRobot** — three-call provision flow (`newMonitor` → `newMWindow` → `editMonitor`) makes this the most expensive in API calls per run. Free-tier 10-req/min budget is tight but workable. Plus the `value` / `start_time` fields needed spec-level corrections (see table above).
5. **Jetmon v1** — requires bridge changes. Schedule alongside the next bridge release, then update `internal/adapter/jetmonv1` to call the bridge reset path during `Deprovision` and set `SupportsCooldownReset = true` only after live reset behavior is verified.

### Phase C — measurement engine extensions

1. [done] Update `internal/measurement` to compute `maintenance_suppressed`.
2. [done] Add the 80%-overlap threshold logic with tests for fully covered, below-threshold partial coverage, above-threshold partial coverage, alert-during-maintenance, and no-maintenance cases.
3. [done] Campaign replay gating requires `SupportsCooldownReset` and records `capability_mismatch` when an adapter cannot guarantee clean alert state.
4. [done] Implement `cooldown_suppressed` / `cooldown_uncertain` classification from retrieve metadata and write known no-event audit rows.

## What this spec deliberately doesn't cover

- **Recurring maintenance windows** (e.g. "every Sunday 02:00 UTC for 1 hour"). Out of scope for v1 — scenarios are ephemeral, recurrence isn't useful here.
- **Multi-window scenarios** (more than one maintenance period in a single run). Out of scope; one window per scenario covers every test pattern we currently care about.
- **Alert *suppression* via downtime severity tagging** (e.g. Datadog's "downtime priority"). Adapters use the simplest form; advanced suppression is a v2 question if it ever matters.
- **Status page maintenance announcements** (Better Stack's status-page primitive). Different concept — that's about user-facing communication, not alert suppression.

## Open questions to resolve at implementation time

1. ~~**Datadog synthetic-vs-monitor ID mapping**~~ — **Resolved 2026-04-27.** `monitor_id` is a top-level int64 on `GET /api/v1/synthetics/tests/api/{public_id}`. Adapter does an extra GET after Provision to discover it; stored on `handle.Fields["monitor_id"]` and `handle.Fields["downtime_id"]` for use in Deprovision.
2. **UptimeRobot type=1 (Once) window straddling midnight UTC**: a one-shot window with start_time near 23:59 UTC and duration spanning into the next day — accepted by the API or rejected? Resolution: live test. Worst case, reject scenarios where `startedAt + StartOffset + Duration` crosses midnight UTC for this adapter. (Adapter currently rejects pre-emptively as the conservative default.)
3. **Better Uptime maintenance window in a single day**: scenarios where the entire window fits in one day are easy. Cross-midnight scenarios require two adjacent days in `maintenance_days` plus careful HH:MM:SS — adapter could just reject these as a capability mismatch for this scenario shape, or implement the two-day workaround. (Adapter currently rejects pre-emptively.)
4. **Cooldown-reset failure semantics**: if delete+recreate fails midway (delete succeeds, recreate fails), the next run's monitor is gone entirely. Is that a `cooldown_reset_failed` row or an `adapter_error` resolution_reason? Resolution: probably the latter, but flag it explicitly in the runner code so operators can distinguish.
