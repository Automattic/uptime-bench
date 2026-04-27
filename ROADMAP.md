# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items below the active line are accommodated in the schema and data model so they can be added without breaking changes — but the implementation work is deferred. Items above the line are next-up.

**Active priorities (next-up, in rough order):**
1. [Keyword-monitoring capability is dead-wired](#keyword-monitoring-capability-is-dead-wired)
2. [Maintenance window suppression](#maintenance-window-suppression)
3. [Alert cooldown interaction between runs](#alert-cooldown-interaction-between-runs)
4. [Automated randomized testing campaigns](#automated-randomized-testing-campaigns)
5. [TLS target implementation](#tls-target-implementation)
6. [Probe IP CIDR refresh tool](#probe-ip-cidr-refresh-tool)

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

## Automated randomized testing campaigns

**Status:** Not implemented. Design needed before implementation. Foundational for the project's statistical-comparison value proposition; promote when the maintenance/cooldown work above lands.

The harness today runs one scripted scenario at a time. That model is fine for *targeted* tests ("does Pingdom detect a 503?") but it can't produce the data the project actually exists to publish: **min, max, and average detection times of specific kinds of failures across the different services**, computed from enough samples that the numbers are defensible.

A campaign is a long-running orchestration mode where the harness self-generates randomized scenarios for hours or days against the live fleet, accumulating thousands of `scenario_runs` rows that can be aggregated into per-(failure_type, service) statistics. Existing single-scenario mode remains for targeted testing.

### Campaign config format

A new TOML schema parallel to `scenarios/`. Indicative shape:

```toml
id          = "weekly-comparison-2026-q2"
description = "..."
duration    = "24h"   # campaign wall-clock cap; runner stops after this
seed        = 42      # master seed; per-run seeds derive deterministically

[[targets]]
pool = ["bench-a", "bench-b", "probe-a"]   # randomly chosen per run

[[failure_types]]
type = "http_status"
weight = 3                                  # picked 3× as often as weight=1
status_code_choices = [503, 502, 504]       # one chosen per run

[[failure_types]]
type = "tcp_refused"
weight = 1

[[failure_types]]
type = "http_timeout"
phase_choices = ["ttfb", "body"]
delay_range = { min = "5s", max = "60s" }
weight = 2

[duration_buckets]
brief    = { min = "30s",  max = "2m",  weight = 5 }
medium   = { min = "2m",   max = "10m", weight = 3 }
long     = { min = "10m",  max = "1h",  weight = 1 }

[escalation]
probability = 0.20                          # 20% of runs are multi-stage
stages_range = { min = 2, max = 3 }
inter_stage_range = { min = "30s", max = "5m" }

[budget]
pingdom            = { max_runs_per_hour = 10 }
uptimerobot        = { max_runs_per_hour = 1 }   # free-tier rate-limit
datadog-synthetics = { max_runs_per_hour = 30 }
better-uptime      = { max_runs_per_hour = 5 }
jetmon-v1          = {}                          # unlimited (self-hosted)

[cooldown]
per_target_minimum = "10m"   # don't hit the same target more often than this
                              # (interacts with vendor-side cooldown reset)
```

Open questions for the design pass: per-failure-type parameter ranges (how to express "random delay between 5s and 60s" cleanly across all failure types); whether escalation chains use the existing `[[failures]]` list with offsets (likely yes) or a new structure.

### Failure escalation

A *single* run with multiple chained failures, not a sequence of separate runs. Examples the model needs to express:

- **Layered**: DNS slow at t=0 → HTTP 503 joins at t=2m. Both active until run end.
- **Replacement**: HTTP 503 at t=0 → escalates to TCP refused at t=2m. Stage 1 ends when stage 2 begins.
- **Recovery test**: failure at t=0..t=2m → silence until t=5m → second failure at t=5m..t=7m. Tests whether the monitor cleared the first incident before the second arrived.

The current scenario format's `[[failures]]` blocks with `offset` already handle the "layered" pattern. "Replacement" needs either a way to set a failure's `duration` independent of the scenario duration, or a new "stage" abstraction. "Recovery test" works today by setting `offset` and `duration` on each block.

### Random scenario generator

Inside the runner, given a campaign config, produce an in-memory `*scenario.Scenario` per iteration:

1. Sample a failure type by weight.
2. Sample a duration bucket by weight, then a duration uniformly within the bucket's range.
3. Sample any failure-specific params (status code, phase, delay…).
4. Sample a target from the pool.
5. With `escalation.probability`, sample 2–3 stages and stitch them onto the same scenario.
6. Derive the per-run seed from `master_seed XOR run_index` for reproducibility.

The generator emits an in-memory Scenario; the existing pipeline runs it. No new "campaign-only" code path through Provision/Activate/Retrieve.

### Runner extensions

- New CLI flag: `-campaign=<config.toml>` mutually exclusive with `-scenario=…`.
- Outer loop: while campaign duration not elapsed, generate a scenario, run it, sleep until the next slot per budget rules, repeat.
- Per-target cooldown enforced at scheduling: don't pick a target whose most-recent run ended less than `cooldown.per_target_minimum` ago.
- Failure isolation: one bad scenario (adapter error, target unreachable) records a `resolution_reason` and the campaign continues. Don't kill the whole campaign for transient issues.
- Persist a `campaign_runs` row (or similar) so the campaign itself is queryable, not just the individual scenario_runs it produced.

### Reporting / aggregation

A new `cmd/uptime-bench-report` tool that aggregates `derived_metrics` for a campaign:

```sh
uptime-bench-report -campaign=weekly-comparison-2026-q2

failure_type    | service           | runs | tp_rate | min_lat | avg_lat | p50_lat | p95_lat | max_lat
http_status     | pingdom           |  142 | 0.98    |  41s    |  72s    |  68s    |  120s   |  180s
http_status     | uptimerobot       |   24 | 0.96    |  62s    |  98s    |  95s    |  145s   |  220s
http_timeout    | pingdom           |  118 | 0.91    |  35s    |  85s    |  80s    |  150s   |  240s
...
```

Output formats: human-readable table (default), TSV, JSON. Backed by a single SQL query joining `scenario_runs` ↔ `derived_metrics` filtered on the campaign's run-id list.

Statistics worth computing per (failure_type, service) pair:

- Detection rate (true_positive / (true_positive + false_negative), excluding capability_mismatch and maintenance_suppressed).
- Detection latency: min, max, avg, p50, p95.
- False-positive rate.
- Sample count (so readers can judge the confidence interval).

### Implementation phases

1. **Campaign config format + parser** — new `internal/campaign` package mirroring `internal/scenario`. Validation rules. Tests.
2. **Random scenario generator** — pure function: `(config, runIndex, seed) → *scenario.Scenario`. Heavily unit-testable; fix-seed → fixed scenario. No I/O.
3. **Runner outer loop** — campaign mode flag; budget+cooldown scheduler; resilience to per-run errors. Reuses existing `Run()` for each iteration.
4. **Escalation support** — extends the generator to emit multi-stage scenarios. Decide whether escalation needs scenario-format changes or only generator-level chaining.
5. **`cmd/uptime-bench-report`** — aggregation tool with the metrics listed above. Output flags for table / TSV / JSON.

Each phase is independently mergeable.

### Cross-cutting concerns

- **Budget interplay with vendor cooldowns**: even with `cooldown.per_target_minimum`, vendor-side alert cooldowns may suppress the second of two same-target runs that fire close together. The cooldown-reset capability flag (already designed) handles this; campaigns should require `SupportsCooldownReset` on every adapter they touch, or accept that some runs get classified as `cooldown_suppressed`.
- **Reproducibility under randomness**: every campaign records its master seed in `campaign_runs.parameters`. Re-running with the same seed produces the same sequence of generated scenarios — the project's existing reproducibility invariant scales to campaigns.
- **Cost ceiling**: a 24-hour campaign at the budgets above is on the order of ~480 runs across all enabled services. Some scenarios run 8 minutes each; running all sequentially would take ~64 hours, so campaigns must run scenarios concurrently across non-overlapping (target, service) pairs. The runner currently runs one scenario at a time end-to-end; concurrent campaign mode is an explicit extension. (Single-scenario mode remains serial.)

---

## Maintenance window suppression

**Status:** Design draft at [`docs/inter-run-state-design.md`](docs/inter-run-state-design.md), 2026-04-26. Reviewed/approved → ready to implement. Combined with alert-cooldown work below since they share infrastructure.

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

**Status:** Design draft at [`docs/inter-run-state-design.md`](docs/inter-run-state-design.md), 2026-04-26. Designed alongside maintenance windows above; implementation gated on user review of the spec.

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
