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
| `event_type` | enum | `failure_start`, `failure_end`, `run_start`, `run_end`, `maintenance_start`, `maintenance_end`, `setup_exposure_failure` |
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
| `raw_classification` | string | The service's own label or service-native reason code, before uptime-bench normalization. |
| `normalized_classification` | string, null | uptime-bench's normalized label, for cross-service comparison. |
| `reported_at` | timestamp | When the service recorded this event (service's own clock, if available). |
| `retrieved_at` | timestamp | When the adapter retrieved this event from the service's API. |
| `metadata` | JSON | Adapter-specific fields: HTTP code reported, probe location, alert channel, etc. |

Structured `reason_code` values identify rows that are operationally invalid
for service behavior scoring:

- `adapter_error` — uptime-bench could not provision, retrieve, or clean up the
  service reliably.
- `capability_mismatch` — the service was skipped because its adapter declares
  that it does not support the scenario capability.
- `failure_not_observable` — the harness could not verify that the injected
  failure was visible on the controlled fleet surface. The row is recorded as
  `retrieve_status = "unknown"` so it is not counted as a service miss.
- `setup_environment_dns_unstable` — a TLS-only scenario ran while DNS baseline
  checks for the target hostname were failing. The row is recorded as
  `retrieve_status = "unknown"` so DNS instability is separated from TLS
  behavior scoring.
- `maintenance_suppressed`, `cooldown_suppressed`, `cooldown_uncertain`, and
  `cooldown_reset_failed` — inter-run or vendor-side suppression states that
  must remain separate from detection failures.

---

## Normalized Classification Vocabulary

`normalized_classification` values used for cross-service comparisons:

- `http_failure`
- `dns_failure`
- `tls_failure`
- `tls_advisory`
- `timeout`
- `content_failure`
- `partial_response` - truncated/partial HTTP body integrity failure.
- `recovered`
- `unknown`
- `unrecognized`

---

## Derived metrics

Metrics are computed from the event log. They are never stored in the primary event tables — they are outputs of the measurement engine, regenerable from the log at any time.

One row per monitor service per metric per run.

| Metric | Definition |
|--------|-----------|
| `detection_latency_s` | `alert_fired.reported_at` − `failure_start.timestamp`. Null if no alert fired. |
| `true_positive` | Alert fired while a visitor-visible ground-truth failure was active. |
| `false_positive` | Alert fired when no visitor-visible ground-truth failure was active. |
| `false_negative` | No alert fired during a visitor-visible ground-truth failure window. |
| `unknown` | Adapter could not retrieve the service's state for this period. |
| `maintenance_suppressed` | No alert fired because the scenario's maintenance window covered the failure period. Correct behaviour, excluded from false negatives. |
| `cooldown_suppressed` | No alert fired and adapter metadata says a prior alert cooldown suppressed it. Excluded from false negatives. |
| `cooldown_uncertain` | No alert fired and adapter metadata says cooldown reset state was uncertain. Excluded from false negatives because the run is not cleanly attributable. |
| `tls_advisory_detected` | A TLS advisory scenario, currently `tls_deprecated` or `tls_expiring`, was active and the service reported a TLS advisory instead of an outage. |
| `tls_advisory_missed` | A TLS advisory scenario was active and the service reported no TLS advisory. Excluded from false negatives because the HTTP request still succeeds. |
| `tls_advisory_false_outage` | A TLS advisory scenario was active and the service reported an outage-style alert instead of an advisory. |
| `classification_match` | Boolean: the service's normalized classification matches the injected failure mode. |

---

## Resolution reasons

Every scenario run records why it ended. This affects whether results are usable for comparison.

- `planned_completion` — scenario ran to its defined end time normally.
- `aborted` — run was interrupted before completion (operator action or harness error).
- `target_independent_failure` — the target failed in a way not caused by the scenario's own injection (e.g., underlying infrastructure issue).
- `adapter_error` — one or more adapters failed to provision or retrieve data, potentially corrupting results for those services.
- `setup_exposure_failure` — the harness activated a failure, but its preflight probe could not observe the intended failure mode on the controlled fleet surface. Per-service rows should use `reason_code = "failure_not_observable"`.
- `setup_environment_dns_unstable` — TLS-only scenario DNS baseline checks failed before, during, or shortly after the active failure window. Per-service rows should use `reason_code = "setup_environment_dns_unstable"`.
- `cleanup_error` — detection and retrieval completed, but one or more adapters failed to deprovision after retries. Monitor reports from the run may still be valid, but the leaked provider state must be investigated before relying on later runs.

---

## Missed detection vs. Unknown vs. capability mismatch

These are three distinct outcomes and must never be conflated:

- **Missed detection (false negative):** the adapter successfully retrieved the service's state and confirmed it did not alert during an active failure window.
- **Unknown:** the adapter could not retrieve data — API outage, rate limit, authentication failure. The service may or may not have detected the failure; we do not know.
- **Capability mismatch:** the scenario required a feature the service does not support (e.g., keyword body inspection on a service that only does status checks), so the harness skipped Provision rather than running an inevitable false negative. The service was never asked.

Both Unknown and capability mismatch are recorded with `retrieve_status = unknown`, but they're distinguished by the `reason_code` field (free-form `reason` carries the human-readable detail). Reporting and any accuracy/coverage calculations must use `reason_code` to keep the three categories separate:

- True/false positives and true/false negatives are computed only over rows where `reason_code` is empty and no suppression-specific metadata explains the missing alert.
- Unknown rates are computed over rows where `reason_code` indicates an adapter-side or API-side problem. Provision and retrieve failures use `reason_code = "adapter_error"`; adapters may use more specific API codes such as `api_unreachable`, `rate_limited`, or `auth_failed` when they can return a structured Unknown result.
- Capability-mismatch rates are computed over rows where `reason_code = "capability_mismatch"` and form the **support matrix** — for any given scenario, which services have the feature needed to detect the failure. This is a first-class deliverable of the project, not a noise filter.
- Maintenance, cooldown, and TLS advisory outcomes are computed as derived metrics (`maintenance_suppressed`, `cooldown_suppressed`, `cooldown_uncertain`, `tls_advisory_detected`, `tls_advisory_missed`, `tls_advisory_false_outage`) and stay out of the false-negative denominator.

### Method-sensitive HTTP scoring

For `http_method_status`, uptime-bench scores against the user-visible `GET` path:

- `method = "GET"` is a visitor-visible outage. A missing alert is a false negative, and an in-window alert is a true positive.
- `method = "HEAD"` with healthy `GET` is a false-down trap for HEAD-only monitors. A missing alert is correct and is not a false negative. An alert during that window is a false positive.

This is why the HEAD/GET mismatch scenarios can test both failure directions without treating "no alert" as a miss when the page a visitor loads remains healthy.

### Timeout Tail Scoring

For `http_timeout` scenarios, the detection window extends past `failure_end` by the scenario's configured `delay`. A monitor probe can start while the timeout failure is active but only emit an incident after the request times out. That is a real detection, not a post-recovery false positive.

Never count Unknown, capability_mismatch, maintenance_suppressed, cooldown_suppressed, cooldown_uncertain, or TLS advisory outcomes as a false negative in accuracy calculations. Reports that aggregate without filtering these categories will conflate "the service missed the failure" with "the service was never asked, was intentionally/possibly suppressed, or saw a successful request with an advisory-level TLS concern," which is the central data-integrity hazard the harness is built to avoid.

`cmd/uptime-bench-report` loads `monitor_reports.reason_code` alongside
`derived_metrics`, surfaces `capability_mismatch` counts as a separate report
column, prints a reason-code table for structured rows such as
`adapter_error`, prints a reason-detail table that buckets those codes by the
provider/adapter error text, and emits bias self-checks so sample imbalance or
uncategorized Unknown rows are visible before latency numbers.

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
3. Unknown, capability_mismatch, maintenance_suppressed, cooldown_suppressed, cooldown_uncertain, TLS advisory outcomes, and healthy-GET method traps do not appear as false negatives in derived metric rows. Capability_mismatch rows are separately queryable so the support matrix can be reported without re-deriving it from logs.
4. `detection_latency_s` is null when no `alert_fired` event exists for that run × service pair — never zero or negative.
5. Adapter deprovision runs and is recorded even when a scenario aborts midway.
