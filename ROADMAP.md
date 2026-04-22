# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items here have been accounted for in the schema and data model — they can be added without breaking changes — but the implementation work has been deferred.

---

## Staggered failure start times

**Status:** Schema-ready, not implemented.

The `offset` field is defined on every `[[failures]]` block and validated by the schema parser, but the runner ignores it. All failures currently start simultaneously at scenario start regardless of what `offset` is set to.

**What it enables:**

- Models realistic cascading failures where one layer degrades before another (e.g., DNS latency appears 30 seconds before TCP connections start failing).
- Tests detection sensitivity: does a monitor fire on the first failing layer, or only after multiple layers compound?
- Enables recovery-and-re-failure within a single run without requiring two separate scenarios.

**What needs to be built:**

- *Runner:* schedule each failure block's injection start at `scenario_start + offset` rather than injecting all failures at once.
- *Ground-truth log:* already correct — each failure block emits its own `failure_start` and `failure_end` events with the actual timestamps.
- *Measurement engine:* detection latency must be calculated against the right `failure_start` event. When failures are staggered, "which failure did the monitor respond to?" becomes the hard question. The metric calculation needs a matching rule — either the earliest active failure or the failure whose classification best matches the monitor's reported classification.

**Complexity note:** the measurement engine change is the substantive work, not the runner scheduling. Design the matching rule before implementing.

---

## TLS target implementation

**Status:** Schema-defined, not implemented in the target binary.

The target binary currently only serves HTTP (port 80). The failure types `tls_expired`, `tls_expiring`, `tls_invalid`, `tls_handshake`, and `tls_deprecated` are all defined, validated, and sent to the target via the control API, but the target has no HTTPS listener and no TLS injection logic.

**What needs to be built:**

- *Target:* TLS listener on the HTTPS port. Per-virtual-host TLS configuration via SNI. A certificate store that holds certs at various ages (newly issued, expiring-soon, already-expired) so the right cert is served based on the active `tls_expiring`/`tls_expired` failure.
- *`tls_invalid`:* serve a self-signed cert or a cert with wrong hostname, selectable per request.
- *`tls_handshake`:* restrict `tls.Config.MinVersion`/`MaxVersion` to force a version or cipher incompatibility.
- *`tls_deprecated`:* restrict `tls.Config.MaxVersion` to `tls.VersionTLS11` or `tls.VersionTLS10` so the connection succeeds but uses a deprecated protocol.
- *Certificate provisioning:* see OPERATIONS.md for the interim (Let's Encrypt) and long-term (fleet CA) strategies.

**Dependency:** The long-term strategy requires the fleet CA root to be trusted by each monitor under test. For monitors that don't support custom CA roots, the interim Let's Encrypt approach is the only option.

---

## `tls_deprecated` — advisory TLS version detection

**Status:** Schema-ready, validator-ready, target not implemented (blocked on TLS target implementation above).

`tls_deprecated` serves TLS 1.0 or 1.1 successfully. The connection completes and the monitor receives an HTTP response — this is not a hard failure. The scenario tests whether monitors distinguish advisory-level TLS issues (deprecated but functional) from blocking failures (handshake error, expired cert).

This is particularly revealing: monitors that report "site is down" for a deprecated-TLS connection are less accurate than monitors that report a separate "TLS advisory" classification. The benchmark measurement for this scenario must handle a third outcome beyond true-positive/false-negative — correct advisory classification.

---

## Redirect baseline change detection

**Status:** Not implemented. Requires design.

Some monitors track a site's expected redirect chain and alert when it changes — distinct from alerting on broken redirects (loops, excessive hops). This is a real failure mode: a site that normally redirects HTTP → HTTPS suddenly redirects to a different domain (compromised DNS, misconfiguration).

Simulating this requires:
- A per-site "normal redirect" configuration in `fleet.toml` (the path that redirects, and where it redirects to in the healthy state).
- A new failure type `http_redirect_change` with a `to` field specifying the altered destination.
- The target serving the configured redirect during healthy operation and the changed redirect during the failure window.

The adapter must also be provisioned with redirect-tracking enabled, since most monitors require explicit opt-in for redirect change alerting.

---

## Maintenance window suppression

**Status:** Not implemented. Requires adapter API design.

Monitors commonly support scheduled maintenance windows during which alerts are suppressed. Testing whether a monitor correctly silences alerts during a declared window is a meaningful accuracy dimension — a monitor that still alerts during maintenance produces false positives; a monitor that never alerts afterward may have also cleared state it shouldn't have.

**What needs to be built:**

- A `ProvisionConfig` extension for maintenance window scheduling (start time, duration).
- Adapters that support this feature implement it in `Provision`; those that don't skip it.
- A new outcome in the measurement model: `maintenance_suppressed` — failure active, adapter returned Known with no reports, maintenance window was active. This is correct behavior, not a false negative.
- Scenario TOML field (or run parameter) to declare a maintenance window overlay on the failure period.

---

## Alert cooldown interaction between runs

**Status:** Not implemented. Requires adapter `Deprovision` design.

Most monitors suppress repeated alerts for the same site within a cooldown window (commonly 30 minutes). When uptime-bench runs multiple consecutive scenarios against the same provisioned monitor, the second run's alert may be suppressed by the cooldown from the first — producing a result that looks like a missed detection but is actually the monitor working correctly.

**The correct approach in `Deprovision`:** if the service's API supports resetting alert state or the cooldown clock, do so. Otherwise, delete and recreate the monitor (accepting the cost of full reprovisioning).

**If the API doesn't support either:** record the cooldown state at the time of Retrieve and include it in `MonitorReport.Metadata`. The measurement engine then classifies the suppressed result separately rather than as a false negative.

Each adapter must document how it handles this in its implementation notes.

---

## Per-component timing retrieval from adapters

**Status:** Not implemented. Requires adapter API design and data model extension.

Some monitors (including Jetmon) record per-component timing breakdowns: DNS resolution time, TCP connection time, TLS handshake time, time to first byte. This data exists in their APIs but is not retrieved by the adapter and not stored in `monitor_reports`.

**What it enables:**

- Verifying that `dns_latency` failures increase DNS resolution time specifically, not TCP or TTFB.
- Verifying that `http_timeout phase=ttfb` failures appear in the TTFB component, not the DNS component.
- Layer-level attribution accuracy: does the monitor correctly identify which layer is slow?

**What needs to be built:**

- `MonitorReport.Metadata` already exists as `map[string]any`. Adapters that retrieve timing breakdowns should populate `dns_ms`, `tcp_ms`, `tls_ms`, `ttfb_ms` keys.
- The measurement engine needs a new metric: `timing_layer_attribution` — did the delay appear in the correct timing component?

---

## Heartbeat and agent-based reverse checks

**Status:** Not implemented. Deferred until monitor services ship this capability.

Heartbeat monitoring (dead-man's switch) and agent-based checks (wp-cron, scheduled task monitoring) require the monitored system to actively send signals to the monitor, rather than the monitor probing the site.

uptime-bench's target fleet is currently passive — it responds to probes. Simulating heartbeat failure requires:
- A heartbeat sender process on the target VM that pings a monitor's ingest endpoint on a schedule.
- A control command (`heartbeat_stopped`) that pauses the sender for the failure window.
- Adapter support to provision a heartbeat monitor (endpoint URL, expected interval).

This architectural extension should be designed when the first monitor service ships heartbeat support. The control API and scenario schema are designed to accommodate new failure types without breaking changes.
