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

**Status:** Schema-defined, partially implemented. Active priority.

The target binary now exposes an HTTPS listener with a generated self-signed fallback certificate. With `-cert-library-manifest`, healthy requests use the longest-valid matching library certificate, while active `tls_expired` and `tls_expiring` failures select the closest matching expired/expiring certificate for the request SNI. `tls_invalid` can force the generated self-signed cert or a generated hostname-mismatch cert, `tls_deprecated` can clamp the HTTPS listener to TLS 1.0 or TLS 1.1, and `tls_handshake` aborts the handshake before certificate selection. Remaining TLS work: end-to-end OpenSSL/probe acceptance tests against a real cert library.

### Phase 1 — HTTPS listener with self-signed default

- Add a `:443` listener to `cmd/target` using `crypto/tls`.
- Generate a self-signed fallback cert on startup; future work may persist it under `/etc/uptime-bench/tls/` if stable fingerprints become useful.
- Wire the listener to the same virtual-host router used for `:80` so healthy pages work over HTTPS.
- Acceptance: `curl -k https://bench-a.<domain>/` returns the canary body; the existing 11 scenarios still pass on the HTTPS variant.

### Phase 2 — Certificate library

- Pre-generate a library of certs at varying ages: fresh (90 days remaining), expiring soon (1, 5, 30 days remaining), already expired (1 day, 30 days, 1 year), self-signed by an unknown CA, signed for the wrong hostname.
- Tooling split: `uptime-bench-certmint` owns real Let's Encrypt/certbot issuance and writes an immutable library plus `manifest.json`; this repo owns manifest loading, target-side SNI selection, and deterministic fleet-CA/self-signed fallbacks. The `uptime-bench-dns` member exposes `PUT/DELETE /acme/txt` control endpoints so certmint's certbot manual hooks ([deploy/acme-hooks/](deploy/acme-hooks/)) can install DNS-01 challenge records on the same nameservers that resolve the benchmark hostnames — no Cloudflare delegation needed for the runtime domains.
- New control API params for `tls_expired` / `tls_expiring` select a library member at activation time. `tls_invalid` still needs variant-specific selection.
- Library structure: `manifest.json` is the contract. Filenames may encode age/profile for operator readability, but the target must select by manifest metadata rather than reparsing certificates at request time.
- Acceptance: `tls_expired days_expired=30` causes the target to serve a cert whose notAfter is 30 days in the past; an OpenSSL probe confirms.

### Phase 3 — TLS protocol-level injection

- `tls_handshake`: implemented for target-side config selection by returning a deterministic handshake error before certificate selection. Probe receives a TLS alert; no HTTP response.
- `tls_deprecated`: implemented for target-side config selection by clamping `tls.Config.MaxVersion` to TLS 1.1 or TLS 1.0. In-process TLS handshake tests cover the target behavior.
- Acceptance still needed: external `openssl s_client -tls1_3 ...` fails handshake when `tls_handshake` is active; external `openssl s_client -tls1_1 ...` succeeds when `tls_deprecated` is active.

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

**Status:** Not implemented. Methodology locked 2026-04-27 (this entry). Implementation gated on the maintenance/cooldown work above. Foundational for the project's statistical-comparison value proposition.

The harness today runs one scripted scenario at a time. That model is fine for *targeted* tests ("does Pingdom detect a 503?") but it can't produce the data the project actually exists to publish: **min, max, and average detection times of specific kinds of failures across the different services**, computed from enough samples that the numbers are defensible.

A campaign is a long-running orchestration mode where the harness self-generates randomized scenarios for hours or days against the live fleet, accumulating thousands of `scenario_runs` rows that can be aggregated into per-(failure_type, service) statistics. Existing single-scenario mode remains for targeted testing.

### Methodology: stratified random sampling

Pure weighted-random ("Chaos Monkey style") was considered and rejected. It's the right tool for *resilience testing in production* but produces a Poisson-like sample distribution across failure modes that breaks every requirement of a benchmark: reproducibility, coverage parity, comparability, and statistical power. Some cells get oversampled; others undersampled; per-service comparisons over different distributions become meaningless.

Instead, every campaign uses **stratified random sampling**:

- A **cell** is one combination of `(failure_type, duration_bucket, host_pattern)`. Every scenario generated for a cell tests **all enabled services simultaneously** — same target, same timing, all monitors fanout — so per-service comparisons are over an identical distribution by construction.
- Each cell receives a target sample count (n=20 default; see two-tier rule below). The campaign generates exactly that many scenario *designs* per cell, deterministically from the master seed.
- Each design is **replayed** N times across the campaign duration, with the replay times distributed across hour-of-day and day-of-week to break cadence-alignment bias. This is the part that picks up the Chaos-Monkey instinct: same logical scenario, run at unpredictable absolute times, so a service whose internal scheduling happens to align (or misalign) with our cadence can't get a systematically wrong-looking number.
- Within each cell, the *details* are randomized per-design: which exact duration in the bucket's range, which exact status code, which target from the pool — but the cell totals and the global structure are not.

Net effect: campaigns are reproducible (same master seed → same design set → same execution sequence), comparable (every service sees the same distribution), and resistant to cadence/parameter-alignment bias.

### Two-tier sampling: default + high-discrimination

Not every cell needs the same number of samples. The driver isn't real-world frequency of the failure mode — it's the **expected size of inter-service differences** in the resulting numbers. Counter-intuitively, "common" failures often need *more* samples, not fewer:

- **Wide-margin cells** (e.g., content tampering, certificate revocation): some services support them, some don't. The interesting result is the support matrix and the fact-of-detection. n=20 is plenty to establish "service A detects, service B doesn't, service C is `capability_mismatch`."
- **Narrow-margin cells** (e.g., HTTP 5xx, TLS expiration): every service detects these. Differences are seconds, not orders of magnitude. To say "service A is faster than service B" with confidence, you need enough samples that the confidence intervals don't overlap — typically n ≥ 50.

So the campaign config specifies a **default tier** (n=20 per cell) and an explicit **high-discrimination tier** (n=60 per cell) for failure types where service-to-service differences are expected to be small.

### Baseline budget

For a first-iteration campaign, the methodology locks in:

- **K = 50 scenario designs** total (across all cells in both tiers).
- **N = 20 default replays per design** for default-tier cells; **N = 60 replays per design** for high-discrimination-tier cells.
- Per-campaign run total: roughly 1,000–1,500 scenario runs depending on the tier split.
- **Diminishing returns hit around n=60–80**. Beyond that, extra samples don't visibly tighten confidence intervals on a typical detection-latency distribution. n=60 is the cap for the high-discrimination tier; pushing it higher mostly buys smugness, not signal.

These numbers can be re-evaluated after the first real campaign produces data showing where the actual noise floor lies.

### Anti-favoritism: in-code, not policy

This project is open-source, public, and intended to evaluate Jetmon (the maintainer's product) honestly against the competition. To make the benchmark trustworthy for everyone — readers, competing services, and the maintainer — anti-favoritism must be enforced **mechanically in the code**, not relied on as a policy:

- The campaign generator and reporter contain **no service-specific branches**. No `if serviceID == "jetmon-v1"` anywhere in the campaign or reporting code, ever. A simple lint test in CI grepping for known service IDs in those files would enforce this.
- The reporter's **bias self-checks run before any latency numbers**: per-service sample counts, per-cell sample counts, and any service whose count deviates from the others by more than a stated threshold is flagged in the output. If Jetmon ran 100× and Pingdom 80× because of budget differences, that's the *first* line of the report, not buried.
- **Confidence intervals are mandatory**, not optional. Every percentile in the output ships with a CI so readers can judge whether differences are meaningful. "Pingdom 70s ± 12s, UptimeRobot 95s ± 18s" is publishable; "Pingdom 70, UptimeRobot 95" is not.
- **Errors are part of the data, not retried away.** If a campaign run got `adapter_error` because a vendor's API was flaky that day, that's the truth — not retried, not filtered out, not silently dropped. The report shows it. Same principle as `capability_mismatch` rows: they're queryable categories, not noise.

### Methodology audit trail

Every published result must be regenerable from a small set of pinned inputs. The schema needs to make these queryable:

- `campaign_runs.config_toml` — the full campaign config, stored verbatim.
- `campaign_runs.master_seed` — the seed that generated the design set.
- `campaign_runs.adapter_versions` — JSON map of each adapter's commit SHA at campaign start.
- `campaign_runs.target_fleet_version` — commit SHA of the target/DNS binaries.
- `campaign_runs.started_at`, `ended_at` — wall-clock dates.

Anyone reading a published comparison post should be able to clone the repo at the recorded SHAs, run the recorded campaign config with the recorded seed, and verify the numbers (modulo external-service flakiness on the day they re-run).

### Campaign config format

A new TOML schema parallel to `scenarios/`. Indicative shape:

```toml
id          = "weekly-comparison-2026-q2"
description = "..."
duration    = "24h"   # campaign wall-clock cap; runner stops after this
seed        = 42      # master seed; per-run seeds derive deterministically

[targets]
pool = ["bench-a", "bench-b", "probe-a"]
patterns = ["single", "two_random", "all"]   # which host_pattern values to stratify on

[duration_buckets]
brief  = { min = "30s",  max = "2m"  }
medium = { min = "2m",   max = "10m" }
long   = { min = "10m",  max = "1h"  }

[sampling]
samples_per_cell_default = 20

# High-discrimination tier: failure modes where service-to-service
# differences are expected to be small enough that n=20 won't reliably
# separate them. Extra samples buy tighter confidence intervals.
[[sampling.high_discrimination]]
failure_types = ["http_status", "tls_expired", "tls_expiring"]
samples_per_cell = 60

[[failure_types]]
type = "http_status"
status_code_choices = [503, 502, 504]

[[failure_types]]
type = "tcp_refused"

[[failure_types]]
type = "http_timeout"
phase_choices = ["ttfb", "body"]
delay_range = { min = "5s", max = "60s" }

[[failure_types]]
type = "http_redirect"
variant_choices = ["loop", "chain"]

[[failure_types]]
type = "http_body"
content_choices = ["empty", "error_page", "keyword_missing", "keyword_injected", "ransomware", "defacement", "malicious_script", "spam_links"]
keyword_choices = ["uptime-bench-canary", "HACKED", "BTC"]

[[failure_types]]
type = "tls_expired"
days_expired_choices = [1, 7, 30]

[[failure_types]]
type = "tls_expiring"
days_remaining_choices = [6, 13, 29]

[escalation]
probability = 0.20                          # 20% of designs are multi-stage
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
```

### Failure escalation

A *single* run with multiple chained failures, not a sequence of separate runs. Examples the model needs to express:

- **Layered**: DNS slow at t=0 → HTTP 503 joins at t=2m. Both active until run end.
- **Replacement**: HTTP 503 at t=0 → escalates to TCP refused at t=2m. Stage 1 ends when stage 2 begins.
- **Recovery test**: failure at t=0..t=2m → silence until t=5m → second failure at t=5m..t=7m. Tests whether the monitor cleared the first incident before the second arrived.

The current scenario format's `[[failures]]` blocks with `offset` already handle the "layered" pattern. "Recovery test" works today by setting `offset` and `duration` on each block. **"Replacement" doesn't fit cleanly** — every failure currently runs for the scenario's full duration from its activation, so stage 1 can't be terminated when stage 2 begins. This needs either a per-failure `duration` override or a new "stage" abstraction; design to be resolved before the escalation phase implements it.

### Two-tier execution: designs and replays

The campaign generator produces two artifacts deterministically from the master seed:

1. **Design set** — K scenario designs. Each design is a fully-specified (failure params, host set, escalation timing) artifact. Designs are written to `campaign_runs.designs` for audit.
2. **Schedule** — for each design, a list of N replay times distributed across the campaign duration. Distribution isn't strictly random; it's quasi-random with constraints that no two replays of the same design fall in the same hour-of-day bucket, no two replays of *any* design overlap on the same target within `cooldown.per_target_minimum`, and replays are spread across weekday/weekend if the campaign spans both.

Each replay invokes the existing single-scenario `Run()` once — campaigns are an orchestrator over many ordinary runs, not a new scenario shape. The per-replay `scenario_runs.parameters` records the design-id and replay-index for join-back.

### Random scenario generator

Pure function: `(config, masterSeed) → ([]Design, []ReplayPlan)`. No I/O. Heavily unit-testable; fix-seed → fixed design set and schedule. The generator runs once per campaign; the runner consumes its output.

For each design, generation proceeds as:

1. Pick a cell `(failure_type, duration_bucket, host_pattern)` from the cell list (cells are enumerated, not randomly sampled — every cell gets its declared sample count).
2. Pick a duration uniformly within the bucket's range.
3. Pick failure-specific params (status code, phase, delay…) randomly within their declared choices.
4. Pick a host set matching the host_pattern (single random target, two random targets, all targets, …).
5. With `escalation.probability`, append additional stages on the same scenario per the escalation rules.
6. Assign a per-design seed derived from `masterSeed XOR designIndex`.

For each design's replays, schedule generation picks N times within the campaign duration that satisfy the distribution constraints above. The schedule is pinned at campaign start, not generated lazily, so the audit trail shows "this design was supposed to run at times T1…TN" even if the campaign was interrupted.

### Runner extensions

- New CLI flag: `-campaign=<config.toml>` mutually exclusive with `-scenario=…`.
- Campaign loop: walk the schedule (pre-generated and time-sorted), run each replay via the existing `Run()`, advance to the next scheduled time.
- Per-target cooldown enforced at scheduling time (not at execution): the schedule generator already respects `cooldown.per_target_minimum`.
- Failure isolation: one bad scenario (adapter error, target unreachable) records its `resolution_reason` and the campaign continues with the next scheduled replay. The bad row is *kept*, not retried.
- Persist a `campaign_runs` row at campaign start with the audit-trail columns above; update `ended_at` at campaign end.

### Reporting

A new `cmd/uptime-bench-report` tool that aggregates `derived_metrics` for a campaign:

```sh
uptime-bench-report -campaign=weekly-comparison-2026-q2

# Bias self-checks (printed first):
#   - sample counts per service (flagged if any deviation > 5%)
#   - sample counts per cell (flagged if any cell short of target n)
#   - any failure_type/service pairs with elevated capability_mismatch or
#     adapter_error rates

failure_type | service     | n  | tp_rate | min | avg | p50 | p95 (CI)        | max
http_status  | pingdom     | 60 | 0.98    | 41s | 72s | 68s | 120s (±15s)     | 180s
http_status  | uptimerobot | 60 | 0.96    | 62s | 98s | 95s | 145s (±19s)     | 220s
...
```

Output formats: human-readable table (default), TSV, JSON. Backed by SQL queries joining `scenario_runs` ↔ `derived_metrics` filtered on the campaign's run-id list.

Per (failure_type, service) statistics:

- Detection rate (true_positive / (true_positive + false_negative), excluding capability_mismatch and maintenance_suppressed).
- Detection latency min/max/avg/p50/p95, each with 95% confidence interval.
- False-positive rate.
- `capability_mismatch` count (separately surfaced; not folded into detection rate).
- Sample count (so readers can judge meaning of the percentiles).

### Implementation phases

1. ✅ **Campaign config format + parser** — `internal/campaign` package, parser + validator. Tests in `campaign_test.go`.
2. ✅ **Pure design + schedule generator** — `(config, masterSeed) → (designs, schedule)`. `internal/campaign/generator.go`; deterministic, fixed-seed regression coverage in `generator_test.go` + `no_favoritism_test.go`.
3. ✅ **Schema migration for `campaign_runs`** — `schema/003_campaign_runs.sql`; `campaign_id` FK on `scenario_runs`. `db.InsertCampaignRun` / `CloseCampaignRun` shipped.
4. ✅ **Runner outer loop (serial)** — `runner.RunCampaign` walks `Plan.Schedule`, calls existing `Run()` per replay via `WithCampaignRunID`. Per-replay errors don't abort the campaign. Tests in `internal/runner/campaign_test.go`. `cmd/harness` accepts `-campaign=<config.toml>` as a mutually exclusive alternative to `-scenario`; campaign mode runs every enabled service from `services.toml`. Metrics are derived in one batch at campaign end via `measurement.DeriveCampaign`, keyed by `scenario_runs.campaign_id`.
5. ✅ **Initial `cmd/uptime-bench-report`** — campaign metrics can be summarized from `derived_metrics` into table / TSV / JSON output. Current scope: per-(failure_type, service) samples, detection rate, TP/FN/FP/Unknown/maintenance counts, and latency min/avg/p50/p95/max.
6. **Full report statistics** — add bias self-checks, confidence intervals, and explicit capability_mismatch counts from `monitor_reports.reason_code`.
7. **Escalation support** — resolves the "replacement" pattern in the scenario format (per-failure `duration` override or new stage abstraction); generator emits multi-stage scenarios. Layered escalation already works end-to-end.

Each phase is independently mergeable. Phases 1–5 deliver the "campaigns work, no escalation" milestone — that alone produces useful comparison data.

### Cross-cutting concerns

- **Budget interplay with vendor cooldowns**: even with `cooldown.per_target_minimum`, vendor-side alert cooldowns may suppress the second of two same-target runs that fire close together. The cooldown-reset capability flag (already designed; Phase B implementations in progress) handles this. Campaigns require `SupportsCooldownReset = true` on every adapter they touch; adapters where it's false get gated as `capability_mismatch` for the campaign's runs. Currently all probe-based adapters with Phase B set this true (delete-recreate cycles state); Jetmon-v1 needs bridge work to do the same.
- **Concurrent execution**: a 1,000-run campaign at ~8 minutes per scenario is ~133 sequential hours. Campaigns must run scenarios concurrently across non-overlapping (target, service) pairs. The runner currently runs one scenario at a time end-to-end; concurrent campaign mode is an explicit extension. Open question for the design pass: where the parallelism axis lives (per-target, per-service, per-(target,service) pair). Single-scenario mode remains serial.
- **Reproducibility under randomness**: every campaign records its master seed and config in `campaign_runs`. Re-running with the same seed against the same fleet+adapter versions produces the same design set and schedule. The project's existing reproducibility invariant scales to campaigns.
- **Per-stage keyword config (deferred)**: the scenario format carries one `Keyword` + `KeywordCheck` pair per run. An escalation that mixes a `keyword_injected` stage with a non-injected http_body stage (`ransomware`, `defacement`, `keyword_missing`, …) collapses to `KeywordCheck="absent"` with the injected keyword, silencing the canary-missing signal the non-injected stage was meant to measure. `applyHTTPBodyDefaults` documents this. The fix is per-failure `Keyword`/`KeywordCheck` fields and adapter rework to switch keyword config mid-run — most adapters configure once at Provision and can't. Before paying that cost, instrument prevalence in real campaign runs (log when a translated scenario contains a mixed-content escalation) and only schedule the schema change if mixed escalations are >5% of designs in practice.

### Methodology disclosure (to be in published results)

When campaign data is published, the methodology section must include, at minimum:

- The full campaign config (TOML) and master seed.
- Per-cell sample counts (showing where high-discrimination depth was applied and where it wasn't).
- Adapter and target-fleet commit SHAs.
- Total wall-clock duration and any campaign interruptions.
- Confidence intervals on all reported percentiles.
- The full count of `capability_mismatch`, `adapter_error`, and `maintenance_suppressed` outcomes per service — these are part of the data, not filtered out.
- The percentile method used. `cmd/uptime-bench-report` uses the **nearest-rank** convention (`idx = ⌈p·N⌉ − 1` on the sorted sample, NIST / Wikipedia "C = 1"). Different from R's default `quantile()` (type 7, linear interpolation) and numpy's default `percentile()`, which produce slightly different numbers for the same data. Nearest-rank always returns an observed sample value — the published p95 is a number that actually occurred in the campaign — but skeptics recomputing with a different method will see ±1-bucket drift.
- The aggregation depth. `cmd/uptime-bench-report` accepts either a concrete `campaign_runs.id` (one run) or a stable `campaign_id` from the campaign TOML (every matching run aggregated). The report header line discloses which interpretation matched and how many runs were folded together — quote that line in any published post so readers know the numbers span N runs, not 1.

The methodology choices (which failure types are high-discrimination, what the sample-count target is) are explicit human judgments and should be argued for in any published post — different operators will care about different failures, and a transparent methodology lets them re-weight from the raw data if their concerns differ.

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
