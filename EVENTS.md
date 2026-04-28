# uptime-bench — Event Log and Output Schema

This document defines the event formats uptime-bench uses for its ground-truth log and for recording what each monitoring service reports during a scenario run.

## Why an event log

uptime-bench's source of truth is an append-only event log. Derived metrics — detection latency, accuracy, classification fidelity — are computed from the log, not stored alongside it. If a metric calculation changes or turns out to be wrong, it can be recomputed from the raw events without re-running scenarios.

---

## Scenario run record

Each scenario run produces one top-level record.

| Field | Type | Notes |
|-------|------|-------|
| `id` | identifier | Stable, deterministic — see Identity below. |
| `scenario_id` | string | Scenario definition identifier. |
| `scenario_version` | string | Version of the scenario definition. |
| `seed` | integer | Random seed used for this run. |
| `target_id` | FK | Target endpoint used. |
| `target_version` | string | Version of the target implementation. |
| `monitor_services` | JSON array | Monitor service IDs and adapter versions in scope for this run. |
| `check_frequency_seconds` | integer | Check interval configured for all monitors in this run. |
| `started_at` | timestamp | When the run began. |
| `failure_started_at` | timestamp | When failure injection began. |
| `failure_ended_at` | timestamp | When failure injection stopped. |
| `ended_at` | timestamp | When the run ended (including grace period). |
| `resolution_reason` | enum | Why the run ended — see Resolution reasons below. |
| `metadata` | JSON | Run-level annotations. |

---

## Ground-truth events

Ground-truth events record what the target fleet actually did. Written by the scenario runner; these are the authoritative record of when failures were active.

| Field | Type | Notes |
|-------|------|-------|
| `id` | identifier | Stable, deterministic — see Identity below. |
| `run_id` | FK | The scenario run this event belongs to. |
| `target_id` | FK | Which target endpoint was affected. |
| `event_type` | enum | `failure_start`, `failure_end`, `run_start`, `run_end`, `maintenance_start`, `maintenance_end` |
| `failure_mode` | string | What kind of failure was injected (e.g., `http_5xx`, `dns_nxdomain`, `tcp_timeout`). |
| `failure_params` | JSON | Injection parameters: rate, region, status code, duration, etc. |
| `timestamp` | timestamp | When this event occurred. |
| `notes` | string, null | Free-text for out-of-band conditions (e.g., "target failed to start injection on schedule"). |

---

## Monitor report events

Monitor report events record what each monitoring service under test reported. Written by monitor adapters after a scenario run completes.

| Field | Type | Notes |
|-------|------|-------|
| `id` | identifier | Stable, deterministic — see Identity below. |
| `run_id` | FK | The scenario run this event belongs to. |
| `monitor_service_id` | FK | Which monitoring service reported this. |
| `target_id` | FK | Which target endpoint was involved. |
| `event_type` | enum | `alert_fired`, `alert_resolved`, `status_change`, `unknown` |
| `raw_classification` | string | The service's own label, unmodified. |
| `normalized_classification` | string, null | uptime-bench's normalized label, for cross-service comparison. |
| `reported_at` | timestamp | When the service recorded this event (service's own clock, if available). |
| `retrieved_at` | timestamp | When the adapter retrieved this event from the service's API. |
| `metadata` | JSON | Adapter-specific fields: HTTP code reported, probe location, alert channel, etc. |

---

## Derived metrics

Metrics are computed from the event log. They are never stored in the primary event tables — they are outputs of the measurement engine, regenerable from the log at any time.

One row per monitor service per metric per run.

| Metric | Definition |
|--------|-----------|
| `detection_latency_seconds` | `alert_fired.reported_at` − `failure_start.timestamp`. Null if no alert fired. |
| `true_positive` | Alert fired while a ground-truth failure was active. |
| `false_positive` | Alert fired when no ground-truth failure was active. |
| `false_negative` | No alert fired during a ground-truth failure window. |
| `unknown` | Adapter could not retrieve the service's state for this period. |
| `classification_match` | Boolean: the service's normalized classification matches the injected failure mode. |

---

## Resolution reasons

Every scenario run records why it ended. This affects whether results are usable for comparison.

- `planned_completion` — scenario ran to its defined end time normally.
- `aborted` — run was interrupted before completion (operator action or harness error).
- `target_independent_failure` — the target failed in a way not caused by the scenario's own injection (e.g., underlying infrastructure issue).
- `adapter_error` — one or more adapters failed to deprovision or retrieve data, potentially corrupting results for those services.

---

## Missed detection vs. Unknown vs. capability mismatch

These are three distinct outcomes and must never be conflated:

- **Missed detection (false negative):** the adapter successfully retrieved the service's state and confirmed it did not alert during an active failure window.
- **Unknown:** the adapter could not retrieve data — API outage, rate limit, authentication failure. The service may or may not have detected the failure; we do not know.
- **Capability mismatch:** the scenario required a feature the service does not support (e.g., keyword body inspection on a service that only does status checks), so the harness skipped Provision rather than running an inevitable false negative. The service was never asked.

Both Unknown and capability mismatch are recorded with `event_type = unknown`, but they're distinguished by the `reason_code` field (free-form `reason` carries the human-readable detail). Reporting and any accuracy/coverage calculations must use `reason_code` to keep the three categories separate:

- True/false positives and true/false negatives are computed only over rows where `reason_code` is empty (i.e. genuine adapter results).
- Unknown rates are computed over rows where `reason_code` indicates an adapter-side or API-side problem (e.g. `api_unreachable`, `rate_limited`, `auth_failed`).
- Capability-mismatch rates are computed over rows where `reason_code = "capability_mismatch"` and form the **support matrix** — for any given scenario, which services have the feature needed to detect the failure. This is a first-class deliverable of the project, not a noise filter.

Never count Unknown or capability_mismatch as a false negative in accuracy calculations. Reports that aggregate without filtering on `reason_code` will conflate "the service missed the failure" with "the service was never asked," which is the central data-integrity hazard the harness is built to avoid.

`cmd/uptime-bench-report` loads `monitor_reports.reason_code` alongside
`derived_metrics`, surfaces `capability_mismatch` counts as a separate report
column, and emits bias self-checks so sample imbalance or uncategorized Unknown
rows are visible before latency numbers.

---

## Identity and idempotency

Event IDs are derived from stable inputs so that adapter retries and scenario replays do not produce duplicate rows.

- Scenario runs: keyed by `(scenario_id, scenario_version, target_id, seed, started_at_bucket)`
- Ground-truth events: keyed by `(run_id, target_id, event_type, timestamp_bucket)`
- Monitor report events: keyed by `(run_id, monitor_service_id, target_id, event_type, reported_at)`

If the same event is written twice, the second write updates the existing row rather than creating a new one.

---

## Invariants worth testing

1. Every scenario run has a `resolution_reason` on close — no run ends without one.
2. Replaying the same scenario with the same seed produces the same ground-truth event sequence.
3. Neither Unknown nor capability_mismatch adapter results appear as false negatives in derived metric rows. Capability_mismatch rows are separately queryable so the support matrix can be reported without re-deriving it from logs.
4. `detection_latency_seconds` is null when no `alert_fired` event exists for that run × service pair — never zero or negative.
5. Adapter deprovision runs and is recorded even when a scenario aborts midway.
