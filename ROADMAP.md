# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items below the active line are accommodated in the schema and data model so they can be added without breaking changes — but the implementation work is deferred. Items above the line are next-up.

**Active priorities (next-up, in rough order):**
1. [Keyword-monitoring capability is dead-wired](#keyword-monitoring-capability-is-dead-wired)
2. [TLS target implementation](#tls-target-implementation)
3. [Maintenance window suppression](#maintenance-window-suppression)
4. [Alert cooldown interaction between runs](#alert-cooldown-interaction-between-runs)
5. [Probe IP CIDR refresh tool](#probe-ip-cidr-refresh-tool)

**Deferred:**
- [Staggered failure measurement matching](#staggered-failure-measurement-matching)
- [Probe IP discoverability (vendor-side)](#probe-ip-discoverability-vendor-side)
- [Per-component timing retrieval from adapters](#per-component-timing-retrieval-from-adapters)
- [Redirect baseline change detection](#redirect-baseline-change-detection)
- [Heartbeat and agent-based reverse checks](#heartbeat-and-agent-based-reverse-checks)

---

# Active priorities

## Keyword-monitoring capability is dead-wired

**Status:** Plumbing in place but not connected end-to-end. Design locked 2026-04-25; ready to implement.

Found during a review pass on 2026-04-25. The pieces exist independently but never meet:

- `adapter.Capabilities.SupportsKeyword` is set to `true` on Pingdom, UptimeRobot, Datadog, and Better Uptime; to `false` on `jetmon-v1`. Currently nothing in `internal/runner` reads either flag, so it has no effect.
- `adapter.ProvisionConfig.Keyword` exists on the struct (`internal/adapter/adapter.go:91`) but the runner builds the config with only `CheckFrequency` (`internal/runner/runner.go:144`) — `Keyword` is never populated.
- Each adapter's `Provision` ignores `config.Keyword`. Pingdom always creates a status (`type=http`) check; UptimeRobot uses `monitorTypeHTTP = 1` (a keyword check would be `type=2`); Datadog only adds a `statusCode` assertion; Better Uptime always uses `monitor_type = "status"`.
- Scenario TOMLs already carry `keyword = "uptime-bench-canary"` for the keyword scenarios, and the runner forwards that string to the target binary's control plane (`internal/runner/runner.go:403`), which uses it to know what string to remove or inject when serving tampered content. So the target side is keyword-aware — the monitor side is not.

Net effect today: any scenario with `monitors = ["pingdom" | "uptimerobot" | "datadog-synthetics" | "better-uptime"]` plus a content failure produces a status-only check that sees `200 OK` and reports nothing. The benchmark would record a false negative, but the failure is in the adapter, not the service.

### Locked-in design

- **Scenario format** — `keyword` is a top-level scenario field (not per-failure), since it's a property of monitor configuration. Sibling field `keyword_check` takes values `present` (alert when keyword absent — the canary case) or `absent` (alert when keyword present — the injected-bad-keyword case). When a scenario contains any content failure but doesn't set `keyword` explicitly, the runner defaults `keyword = "uptime-bench-canary"` and `keyword_check = "present"`.
- **Capability gating** — when the scenario sets a keyword and the adapter's `SupportsKeyword == false`, the runner skips Provision and writes a single `monitor_reports` row with `Status = Unknown` and a structured `reason_code = "capability_mismatch"` plus a free-form Reason describing the missing capability. Same pattern the runner already uses for `MinCheckFrequency`. **Capability-mismatch results are first-class data**, not noise — they are the support matrix for "which services support which features," which is a project deliverable. Reporting must distinguish them from genuine false negatives (see EVENTS.md).
- **Better Uptime asymmetry** — Better Uptime's `monitor_type = "keyword"` may only support presence checks. If verified during implementation, scenarios with `keyword_check = "absent"` against Better Uptime get gated as a capability mismatch (`SupportsKeyword: true` becomes a more granular pair: `SupportsKeywordPresent` / `SupportsKeywordAbsent`, or a single `SupportsKeyword` flag plus a `SupportsInvertedKeyword` qualifier). The asymmetry is recorded in the data, not papered over.

### Implementation order

1. **Schema migration** — add `reason_code` column to `monitor_reports` (nullable string; existing rows back-fill empty). Update `internal/db` writes and the runner's `logMonitorReport` to set it. This is the prerequisite that lets capability gating be queryable as a support matrix; ship before any of the keyword work to keep the migration small and isolated.
2. **Scenario format** — promote `keyword` to scenario-level and add `keyword_check`; update `internal/scenario` parser, validator, and the corpus check; update existing keyword scenarios to the new format.
3. **Runner** — populate `ProvisionConfig.Keyword` and (new) `ProvisionConfig.KeywordCheck` from the scenario; add capability-gating branch that mirrors the existing `MinCheckFrequency` branch.
4. **Adapters** — branch on `config.Keyword != ""`:
   - **Pingdom**: `newCheckRequest` carries `shouldcontain` (present check) or `shouldnotcontain` (absent check). Type stays `"http"`.
   - **UptimeRobot**: switch `type` from `1` (HTTP) to `2` (Keyword); set `keyword_type=1` for "exists" (present check) or `keyword_type=2` for "not exists" (absent check); set `keyword_value`.
   - **Datadog**: append a `body` assertion to the existing `Assertions` list with operator `contains` (present) or `does not contain` (absent).
   - **Better Uptime**: switch `monitor_type` from `"status"` to `"keyword"`; set `required_keyword` (present check). If absent-mode is unsupported by the API (verify against the live API as part of this step), set the capability flag accordingly and let the runner gate it.
5. **Tests** — unit tests for each adapter's keyword branches; integration test that asserts capability gating writes a `monitor_reports` row with the expected `reason_code` instead of a Provision call; corpus check covers the new scenario fields.

Until all five steps land, the benchmark cannot accurately compare content-tampering detection across the four probe-based services. The `jetmon-v1` adapter (agent-based; `SupportsKeyword = false`) evaluates content scenarios via the agent's own content rules, not via the keyword path — under the new gating it would record a `capability_mismatch` for content scenarios, which is correct: Jetmon's content detection isn't comparable on the keyword axis.

---

## TLS target implementation

**Status:** Schema-defined, not implemented. Active priority.

The target binary serves only HTTP today; all five `tls_*` failure types (`tls_expired`, `tls_expiring`, `tls_invalid`, `tls_handshake`, `tls_deprecated`) are validated and forwarded by the control plane but the target has no HTTPS listener, so they no-op.

### Phase 1 — HTTPS listener with self-signed default

- Add a `:443` listener to `cmd/target` using `crypto/tls`.
- Generate (or load) a single self-signed cert per virtual host on startup; persist under `/etc/uptime-bench/tls/` so a restart doesn't churn fingerprints.
- Wire the listener to the same virtual-host router used for `:80` so existing scenarios work over HTTPS.
- Acceptance: `curl -k https://bench-a.<domain>/` returns the canary body; the existing 11 scenarios still pass on the HTTPS variant.

### Phase 2 — Certificate library

- Pre-generate a library of certs at varying ages: fresh (90 days remaining), expiring soon (1, 5, 30 days remaining), already expired (1 day, 30 days, 1 year), self-signed by an unknown CA, signed for the wrong hostname.
- Tooling: a `make generate-certs` target (or `cmd/cert-mint` binary) that builds the library reproducibly given a seed.
- New control API params for `tls_expired` / `tls_expiring` / `tls_invalid` to select a library member at activation time.
- Library structure: filename encodes age + CA so the target can pick by string match without parsing every cert at request time.
- Acceptance: `tls_expired days_expired=30` causes the target to serve a cert whose notAfter is 30 days in the past; an OpenSSL probe confirms.

### Phase 3 — TLS protocol-level injection

- `tls_handshake`: clamp `tls.Config.MinVersion` / `MaxVersion` per active failure to force version or cipher incompatibility. Probe receives a TLS alert; no HTTP response.
- `tls_deprecated`: clamp `tls.Config.MaxVersion` to TLS 1.1 or TLS 1.0 — the connection succeeds but on a deprecated protocol, and the response actually completes. Distinct outcome from `tls_handshake`.
- Acceptance: `openssl s_client -tls1_3 ...` fails handshake when `tls_handshake` is active; `openssl s_client -tls1_1 ...` succeeds when `tls_deprecated` is active.

**Measurement note for `tls_deprecated`**: because the request actually returns 200 OK, monitor outcomes split three ways — missed advisory, correct "TLS advisory" classification, false outage report. The measurement engine needs a third category here, distinct from true-positive and false-negative.

### Phase 4 — Long-term: fleet CA

- Generate a single CA root for the fleet; sign all virtual-host certs from it.
- Each monitor under test must trust the fleet CA. Status by service:
  - **Jetmon (self-hosted)** — trivial. Install the fleet CA on the agent host.
  - **Datadog Synthetics** — supports custom CA via API; needs concrete validation.
  - **Pingdom / UptimeRobot / Better Uptime** — research needed; some likely don't support custom CAs at all.
- For monitors that don't support custom CAs, the interim path is real Let's Encrypt certs: keep Phase 1's listener but back it with a Let's Encrypt issuer. Document the tradeoffs (Phase 2 cert library is harder to construct from real-issued certs, especially for "1 year expired").
- Phase 4 unblocks ~100% TLS coverage; Phases 1–3 already unblock the bulk using self-signed.

### Cross-cutting: per-virtual-host certs via SNI

Required by Phase 1 once we have multiple virtual hosts, but ordering with the phased work above is flexible. The TLS listener inspects SNI and serves the matching cert; without this, multi-site scenarios run only on whichever cert was bound to the listener default.

---

## Maintenance window suppression

**Status:** Not implemented. Active — schedule a design pass.

Monitors commonly support scheduled maintenance windows during which alerts are suppressed. Testing whether a monitor correctly silences alerts during a declared window is a meaningful accuracy dimension — a monitor that still alerts during maintenance produces false positives; a monitor that never alerts afterward may have also cleared state it shouldn't have.

**What needs to be built:**

- A `ProvisionConfig` extension for maintenance window scheduling (start time, duration).
- Adapters that support this feature implement it in `Provision`; those that don't produce a `capability_mismatch` Unknown for any scenario that declares a window.
- A new outcome in the measurement model: `maintenance_suppressed` — failure active, adapter returned Known with no reports, maintenance window was active. Correct behavior, not a false negative.
- Scenario TOML field (or run parameter) to declare a maintenance window overlay on the failure period. Two natural patterns:
  - *Overlapping*: window covers the failure entirely (tests "alerts suppressed during maintenance").
  - *Edge*: window ends midway through the failure (tests "alerts fire as soon as window closes, even though failure was already active").

**Per-vendor research needed:**

- *Pingdom* — `Maintenance.create` API exists; check window granularity.
- *UptimeRobot* — `newMWindow` API; recurring vs one-shot semantics.
- *Datadog Synthetics* — synthetic tests can be paused, but is there a true scheduled-suppression primitive vs. just `pause`?
- *Better Uptime* — `policies` and `escalation` API may carry this; not yet checked.
- *Jetmon* — likely no first-class concept; would need to be modeled at the agent level.

The capability flag for this is new; add `SupportsMaintenanceWindows` to `Capabilities`.

---

## Alert cooldown interaction between runs

**Status:** Not implemented. Active — second piece of the cross-run-state design.

Most monitors suppress repeated alerts for the same site within a cooldown window (commonly 30 minutes). When uptime-bench runs multiple consecutive scenarios against the same provisioned monitor, the second run's alert may be suppressed by the cooldown from the first — producing a result that looks like a missed detection but is actually the monitor working correctly.

**The correct approach in `Deprovision`**: if the service's API supports resetting alert state or the cooldown clock, do so. Otherwise, delete and recreate the monitor (accepting the cost of full reprovisioning).

**If the API doesn't support either:** record the cooldown state at the time of Retrieve and include it in `MonitorReport.Metadata`. The measurement engine then classifies the suppressed result separately rather than as a false negative.

**Per-vendor research needed:**

- *Pingdom* — `pause` / `unpause` resets some state; investigate whether it resets cooldown.
- *UptimeRobot* — `editMonitor status=0` then `status=1` toggles; behavior on cooldown unknown.
- *Datadog Synthetics* — alert state can be reset via the monitor (not synthetic) API; cross-product API touch.
- *Better Uptime* — incident closure resets some state; documentation is sparse.
- *Jetmon* — under operator control via the bridge; trivial.

Each adapter must document how it handles this in its implementation notes.

This and "Maintenance window suppression" should be designed together — they're both ways the monitor's *current state* affects future detections, and both want the same cross-cutting infrastructure (a per-adapter "reset to clean state" capability that Deprovision can call, and a measurement category for "suppressed but correct").

---

## Probe IP CIDR refresh tool

**Status:** Not implemented. Manual chore today; without automation it goes stale and `http-geo-503` produces noise.

`services.toml`'s `[services.probe_ranges]` blocks list per-region CIDRs for each vendor's published probe pool. Vendors update these lists periodically. Each vendor publishes (or doesn't) in machine-readable form:

| Service | Source | Format | Region tags |
|---|---|---|---|
| Pingdom | `https://my.pingdom.com/probes/ipv4` | JSON | Yes |
| UptimeRobot | `https://uptimerobot.com/inc/files/ips/IPv4andIPv6.txt` | Plain text | No |
| Datadog Synthetics | `https://ip-ranges.datadoghq.com/` | JSON, `synthetics` key | Yes |
| Better Uptime | `https://betterstack.com/docs/uptime/ip-addresses/` | HTML | Partially (in prose) |

**Build:** `cmd/probe-ips-refresh` Go tool that fetches each list, normalizes regions, and emits a TOML fragment to stdout. Operator pipes it to a file, diffs against the current `services.toml`, and applies changes by hand.

**Why not auto-write `services.toml`:** vendor changes can include unexpected region renames, IPv6-only additions, or removals that should be noticed, not silently merged. Manual review is the safety check.

**Region mapping:**

- Pingdom and Datadog tag each IP/CIDR with a region; pass through.
- UptimeRobot doesn't. Maintain a hand-edited `internal/probeips/uptimerobot_regions.json` that maps IP prefixes to regions; the tool warns when a new IP doesn't fall in any known prefix. Keep this file in the repo so updates are visible in PRs.
- Better Uptime publishes IPs in HTML prose with region annotations; first attempt is a polite request to BetterStack for a structured feed. Until then, hand-curated list with a comment noting last refresh.

**Run cadence:** weekly via a GitHub Action that opens a PR with the diff. Operator merges (or rejects) within a day.

---

# Deferred

## Staggered failure measurement matching

**Status:** Runner support done; measurement engine matching rule pending. Punted — current "earliest active failure" rule produces sound results for the simultaneous-failure scenarios that exist today.

The `offset` field is honored by the runner: failures activate at `scenario_start + offset` and run for `duration` from that point (`internal/runner/runner.go:scheduleFailureEvents`). Ground-truth events emit independent `failure_start` / `failure_end` pairs per failure with the correct timestamps.

**Still to design (when staggered scenarios become a focus):**

- *Measurement engine:* detection latency is calculated against the first failure window an alert falls inside (`internal/measurement/measurement.go`). When failures are staggered and overlapping, "which failure did the monitor respond to?" matters for accurate latency attribution. Today's "earliest active failure" rule loses signal when multiple layers fail together (e.g., DNS at t=0, HTTP at t=30, alert at t=45 — was the monitor responding to DNS or HTTP?).
- The matching rule should probably be: the failure whose normalized classification best matches the monitor's reported classification, falling back to earliest-active when classification doesn't disambiguate. Spec it before implementing.

---

## Probe IP discoverability (vendor-side)

**Status:** Not implemented. Not key right now — documented for future reference.

Some vendors don't make their probe IP list easily discoverable: buried in support docs, gated behind login, or not published at all. The benchmark depends on knowing these IPs to inject geographic failures (`http-geo-503` only fails for matching source IPs). For vendors that don't publish, options are:

- *Sniff probe IPs* by logging connections to `:80` / `:443` over a long observation window and clustering by frequency.
- *Skip geo scenarios* for that vendor and record the limitation.
- *Lobby the vendor* for a published list via support.

Worth a tracking note in case a target vendor goes dark on this; the [Probe IP CIDR refresh tool](#probe-ip-cidr-refresh-tool) entry above assumes vendors continue to publish.

---

## Per-component timing retrieval from adapters

**Status:** Not implemented. Punted.

Some monitors (including Jetmon) record per-component timing breakdowns: DNS resolution time, TCP connection time, TLS handshake time, time to first byte. This data exists in their APIs but is not retrieved by the adapter and not stored in `monitor_reports`.

**What it would enable:**

- Verifying that `dns_latency` failures increase DNS resolution time specifically, not TCP or TTFB.
- Verifying that `http_timeout phase=ttfb` failures appear in the TTFB component, not the DNS component.
- Layer-level attribution accuracy: does the monitor correctly identify which layer is slow?

**What it would need:**

- `MonitorReport.Metadata` already exists as `map[string]any`. Adapters that retrieve timing breakdowns should populate `dns_ms`, `tcp_ms`, `tls_ms`, `ttfb_ms` keys.
- A new metric in the measurement engine: `timing_layer_attribution`.

---

## Redirect baseline change detection

**Status:** Not implemented. Punted.

Some monitors track a site's expected redirect chain and alert when it changes — distinct from alerting on broken redirects (loops, excessive hops). Real failure mode: a site that normally redirects HTTP → HTTPS suddenly redirects to a different domain (compromised DNS, misconfiguration).

Simulating this requires:

- A per-site "normal redirect" configuration in `fleet.toml` (the path that redirects, and where it redirects to in the healthy state).
- A new failure type `http_redirect_change` with a `to` field specifying the altered destination.
- The target serving the configured redirect during healthy operation and the changed redirect during the failure window.
- Adapter provisioning with redirect-tracking enabled, since most monitors require explicit opt-in.

---

## Heartbeat and agent-based reverse checks

**Status:** Not implemented. Deferred until monitor services ship this capability.

Heartbeat monitoring (dead-man's switch) and agent-based checks (wp-cron, scheduled task monitoring) require the monitored system to actively send signals to the monitor, rather than the monitor probing the site.

uptime-bench's target fleet is currently passive — it responds to probes. Simulating heartbeat failure requires:

- A heartbeat sender process on the target VM that pings a monitor's ingest endpoint on a schedule.
- A control command (`heartbeat_stopped`) that pauses the sender for the failure window.
- Adapter support to provision a heartbeat monitor (endpoint URL, expected interval).

This architectural extension should be designed when the first monitor service ships heartbeat support. The control API and scenario schema are designed to accommodate new failure types without breaking changes.
