# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items here have been accounted for in the schema and data model — they can be added without breaking changes — but the implementation work has been deferred.

---

## Staggered failure measurement matching

**Status:** Runner support done; measurement engine matching rule pending.

The `offset` field is now honored by the runner: failures activate at `scenario_start + offset` and run for `duration` from that point (`internal/runner/runner.go:scheduleFailureEvents`). Ground-truth events emit independent `failure_start` / `failure_end` pairs per failure with the correct timestamps.

**Still to design:**

- *Measurement engine:* detection latency is calculated against the first failure window an alert falls inside (`internal/measurement/measurement.go`). When failures are staggered and overlapping, "which failure did the monitor respond to?" matters for accurate latency attribution. Today's "earliest active failure" rule is a reasonable default but loses signal when multiple layers fail together (e.g., DNS at t=0, HTTP at t=30, alert at t=45 — was the monitor responding to DNS or HTTP?).
- The matching rule should probably be: the failure whose normalized classification best matches the monitor's reported classification, falling back to earliest-active when classification doesn't disambiguate. Spec it before implementing.

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

---

## Keyword-monitoring capability is dead-wired

**Status:** Plumbing in place but not connected end-to-end. Real capability gap, not just polish.

Found during a review pass on 2026-04-25. The pieces exist independently but never meet:

- `adapter.Capabilities.SupportsKeyword` is set to `true` on Pingdom, UptimeRobot, Datadog, and Better Uptime; to `false` on `jetmon-v1`. Currently nothing in `internal/runner` reads either flag, so it has no effect.
- `adapter.ProvisionConfig.Keyword` exists on the struct (`internal/adapter/adapter.go:91`) but the runner builds the config with only `CheckFrequency` (`internal/runner/runner.go:144`) — `Keyword` is never populated.
- Each adapter's `Provision` ignores `config.Keyword`. Pingdom always creates a status (`type=http`) check; UptimeRobot uses `monitorTypeHTTP = 1` (a keyword check would be `type=2`); Datadog only adds a `statusCode` assertion; Better Uptime always uses `monitor_type = "status"`.
- Scenario TOMLs already carry `keyword = "uptime-bench-canary"` for the keyword scenarios, and the runner forwards that string to the target binary's control plane (`internal/runner/runner.go:403`), which uses it to know what string to remove or inject when serving tampered content. So the target side is keyword-aware — the monitor side is not.

Net effect today: any scenario with `monitors = ["pingdom" | "uptimerobot" | "datadog-synthetics" | "better-uptime"]` plus a content failure produces a status-only check that sees `200 OK` and reports nothing. The benchmark would record a false negative, but the failure is in the adapter, not the service.

To close the gap:

1. **Runner** — pass keyword from scenario into `ProvisionConfig`. The current scenario model carries `Keyword` per-failure; the simplest mapping is to use the first failure's `Keyword` (most scenarios have a single failure). Cleaner long-term: hoist `keyword` to scenario level since it's a property of the monitor configuration, not the failure.
2. **Capability gating** — when the scenario contains a content failure, skip adapters where `Capabilities().SupportsKeyword == false` and emit the same `capability_mismatch` Unknown status the runner already does for `MinCheckFrequency` (`internal/runner/runner.go:127`). The pattern is already there.
3. **Each keyword-supporting adapter** — branch on `config.Keyword != ""`:
   - **Pingdom**: `newCheckRequest` should carry `shouldcontain` (the keyword to look for in the response body) when set. Pingdom v3.1 supports keyword matching on `type=http` checks via the `shouldcontain` / `shouldnotcontain` fields, so the type stays `"http"`.
   - **UptimeRobot**: switch `type` from `1` (HTTP) to `2` (Keyword) and add `keyword_type` (`1` for "exists" / `2` for "not exists") and `keyword_value` to the form body.
   - **Datadog**: append a `body` assertion (`{type:"body", operator:"contains", target:keyword}`) to the existing assertions list.
   - **Better Uptime**: switch `monitor_type` from `"status"` to `"keyword"` and add `required_keyword`.

Until all three steps land, the benchmark cannot accurately compare content-tampering detection across the four probe-based services. The `jetmon-v1` adapter (agent-based; `SupportsKeyword = false`) can already evaluate content scenarios, but not via the keyword path — it relies on the agent's own content rules.
