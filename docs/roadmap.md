# uptime-bench Roadmap

Deferred features that are intentionally not yet implemented. Items below the active line are accommodated in the schema and data model so they can be added without breaking changes — but the implementation work is deferred. Items above the line are next-up. The completed section summarizes major shipped capabilities from the commit history so the roadmap shows how the current shape of the system came together.

**Active priorities (next-up, in rough order):**
1. [Jetmon v2 deployed scenario smoke](#jetmon-v2-deployed-scenario-smoke) — blocked on runner-to-API reachability and deployed harness config.
2. [Alert cooldown interaction between runs](#alert-cooldown-interaction-between-runs) — blocked on Jetmon v1 bridge write-mode provisioning.
3. [TLS monitor-facing validation](#tls-monitor-facing-validation)
4. [Live maintenance-window validation](#live-maintenance-window-validation)
5. [Report-driven provider reliability](#report-driven-provider-reliability)
6. [Provider-state preflight cleanup](#provider-state-preflight-cleanup)
7. [Active-run operational guardrails](#active-run-operational-guardrails)
8. [Campaign hardening dry run](#campaign-hardening-dry-run)
9. [Provider feature coverage gaps](#provider-feature-coverage-gaps)
10. [Next-wave adapter expansion](#next-wave-adapter-expansion)
11. [Jetmon capacity benchmark](#jetmon-capacity-benchmark)

**Lower-priority follow-ups:**
- [Probe IP CIDR refresh tool](#probe-ip-cidr-refresh-tool)
- [Additional self-hosted monitor adapters](#additional-self-hosted-monitor-adapters)

**Recently completed but kept here for audit context:**
- [Keyword-monitoring capability is dead-wired](#keyword-monitoring-capability-is-dead-wired)
- [Maintenance window suppression](#maintenance-window-suppression)

**Deferred:**
- [Staggered failure measurement matching](#staggered-failure-measurement-matching)
- [Method-sensitive HTTP behavior beyond status](#method-sensitive-http-behavior-beyond-status)
- [Probe IP discoverability (vendor-side)](#probe-ip-discoverability-vendor-side)
- [Per-component timing retrieval from adapters](#per-component-timing-retrieval-from-adapters)
- [Redirect baseline change detection](#redirect-baseline-change-detection)
- [Heartbeat and agent-based reverse checks](#heartbeat-and-agent-based-reverse-checks)
- [Internet control-plane and provider-scale stretch tests](#internet-control-plane-and-provider-scale-stretch-tests)

---

# Completed

## Core platform and architecture

- **Project foundation** — repo scaffold, Makefile targets, Docker Compose local dev, operations docs, schema docs, adapter docs, event model, and the system map are in place.
- **Service-agnostic design** — the harness no longer carries vendor-specific branches; service details live in `services.toml` and adapter implementations.
- **Canonical event log** — MySQL-backed `scenario_runs`, `ground_truth_events`, `monitor_reports`, `derived_metrics`, and `campaign_runs` preserve raw data and support recomputation.

## End-to-end run pipeline

- **Single-scenario execution** — the harness can provision monitors, activate controlled failures, record ground truth, retrieve monitor events, deprovision, and close the run.
- **Measurement engine** — raw events are converted into true positive, false negative, false positive, unknown, maintenance-suppressed, cooldown-suppressed/uncertain, TLS advisory, method-sensitive, and latency metrics.
- **Reporting tool** — `cmd/uptime-bench-report` produces table, TSV, and JSON campaign summaries with aggregation metadata, bias checks, confidence intervals, capability-mismatch counts, suppression counts, and TLS advisory counts.

## Monitoring adapters

- **Adapter contract and capability gating** — adapters declare check frequency, keyword, maintenance, cooldown, and agent support; incompatible scenario/service pairs become `capability_mismatch` rows instead of misleading false negatives.
- **Implemented adapters** — Jetmon v1, Jetmon v2, UptimeRobot, Pingdom, Datadog Synthetics, Better Uptime, Gatus, and Uptime Kuma all have concrete adapters.
- **Live API smoke coverage** — the public probe-based adapters, Jetmon v1 bridge, and Jetmon v2 API have build-tagged live smoke tests or live-test history captured in docs.
- **Per-adapter normalization** — each adapter owns raw classification mapping into uptime-bench's common vocabulary.
- **Adapter live-run hardening** — Jetmon v1 retrieval now treats its initial `SITE_DOWN` transition as an outage report, and UptimeRobot provisioning can clean up duplicate harness-owned monitors or adopt the single matching monitor after a timed-out create call.

## Failure-injection fleet

- **Target server** — `cmd/target` serves realistic virtual hosts and injects HTTP status, timeout, partial body, redirect, content, method-specific status, TCP, and TLS failures.
- **Content failure library** — target pages cover canary removal, bad-keyword injection, CMS-style error pages, ransomware, defacement, malicious scripts, and hidden spam links while preserving realistic HTTP behavior.
- **DNS server** — `cmd/dns` acts as authoritative DNS for the fleet, supports DNS failure modes, serves A/NS/SOA/TXT records, and has ACME TXT control endpoints.
- **Geo and method-sensitive cases** — source-IP filtering enables geo-scoped failures, and HEAD/GET mismatch scenarios cover false-up and false-down risks for HEAD-only monitors across status, redirect, timeout, and partial-body failures.

## TLS and certificate infrastructure

- **Certmint integrated in-repo** — `cmd/certmint` mints and archives certificates, publishes a cert library API, trims stale entries, and has operator docs and provisioning hooks.
- **ACME DNS-01 path** — DNS members support ACME TXT records and certbot manual hooks, so certmint can issue public certificates through the benchmark nameservers.
- **Target TLS support** — the target has HTTPS/SNI handling, library certificate selection, healthy certificate fallback, expired/expiring selection, invalid-certificate variants, deprecated TLS modes, and handshake-abort behavior.
- **Dynamic cert-library distribution** — the harness forwards cert-library URLs to targets; targets poll, cache, swap, prune, and persist certificate-library config across restarts.
- **TLS acceptance tooling** — local OpenSSL tests cover target TLS behavior, and `deploy/tls-smoke.sh` exercises deployed target HTTPS, handshake-abort, and deprecated-TLS paths through the real control API.

## Campaigns and statistical comparison

- **Campaign methodology** — stratified sampling, two-tier sample depth, reproducible seeds, audit trail, and anti-favoritism constraints are documented.
- **Campaign implementation** — config parsing, pure design/schedule generation, `campaign_runs` schema, scenario translation, serial campaign runner, campaign CLI, checked-in starter campaign config, run-rate budget preflight, campaign replay metadata, mixed-content escalation audit flags, and campaign metric derivation are implemented.
- **Bias-aware reporting** — reports flag service/sample imbalance, missing failure/service cells, capability mismatches, uncategorized Unknown rows, and include confidence intervals before readers compare latency numbers.

## Inter-run state and suppression

- **Maintenance windows** — scenario parsing, runner gating, adapter provisioning, vendor-side APIs for Pingdom, UptimeRobot, Datadog, Better Uptime, and Jetmon v2, plus `maintenance_suppressed` measurement classification are implemented.
- **Overlapping failure-window suppression math** — maintenance coverage is computed over the merged union of failure windows, so layered campaign escalations do not double-count overlap when deciding whether an absent alert was suppressed.
- **Cooldown reset and classification** — capability flags, delete/recreate cleanup paths, campaign replay gating, Jetmon v1 write-mode bridge reset semantics, and cooldown-suppression measurement categories exist where supported.

## Operations, testing, and hardening

- **Fleet provisioning and deploy flow** — scripts create config skeletons, install systemd units, handle DNS port conflicts, deploy binaries, and cover target, DNS, harness, and certmint roles.
- **Deployed fleet smoke tooling** — `deploy/target-smoke.sh` and `deploy/dns-smoke.sh` exercise target HTTP/TCP/TLS injection and DNS-member injection through the real deployed control APIs, including cleanup checks that fail if active failures remain.
- **Adapter smoke ergonomics** — the harness supports a `-monitors` override for single-scenario runs, so operators can reuse the checked-in scenario corpus against a specific adapter without creating temporary scenario copies.
- **HEAD/GET multi-service comparison** — `reports/headget-20260429-030809Z/` preserves the first long-form run across the HTTP 503 control and HEAD/GET mismatch matrix, including raw TSV exports, derived JSON, a redacted service snapshot, and analysis notes.
- **Documentation front door** — the root README is now a concise project overview, while detailed design, operation, scenario, adapter, event, and roadmap references live under `docs/`.
- **Probe IP refresh automation** — `cmd/probe-ips-refresh` generates reviewable probe-range fragments, `make refresh-probe-ips` gives operators a local review command, and a weekly GitHub Action opens a PR with the latest generated fragment for operator review.
- **Regression coverage** — tests cover parsers, runner error handling, adapter factories, DNS handlers, target handlers, cert selection, campaign anti-favoritism, reporting, and live-test compilation.
- **CI checks** — build, vet, live-test compilation, race testing, and formatting/tidiness checks are represented in the project workflow.

---

# Active priorities

## Jetmon v2 deployed scenario smoke

**Status:** Partially validated; blocked on the deployed harness path. Direct deployed target/DNS smoke passed on 2026-04-28, local Jetmon v2 API contract smoke passes with a current token, and workstation-run harness smoke against the deployed fleet now passes the two highest-priority HEAD/GET mismatch scenarios. The full deployed-harness path against Jetmon v2 is still blocked because the runner host cannot currently reach the dev API and its service config is not enabled for Jetmon v2.

Rechecked on 2026-04-28:

- The Jetmon v2 API health endpoint at the current dev address is reachable from the workstation.
- A replacement API token returns `200 OK` from `/api/v1/me` and the build-tagged Jetmon v2 live adapter tests pass locally, including provision, retrieve, API contract, and deprovision.
- Workstation-run harness smoke passed `http-503`: run `5aa0a909c9e92249ec657ad995cf3daa` closed with `planned_completion`, retrieved raw `server` / normalized `http_failure` with `http_code=503`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, `detection_latency_s=246.447102`, and left the target control registry clean. No resolve event arrived within the three-minute grace window.
- Workstation-run harness smoke passed `http-timeout-ttfb`: run `4f13f9bf24fca09e86149f498decda47` closed with `planned_completion`, retrieved raw `timeout` / normalized `timeout` with `error_code=1`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, `detection_latency_s=287.160244`, and left the target control registry clean. An earlier run (`c96b5e683135158b85d17111a8bb4f73`) exposed a scoring edge where a timeout probe can report just after `failure_end`; measurement now extends `http_timeout` windows by the configured delay to credit in-flight probes correctly.
- Workstation-run harness smoke completed `http-partial`: run `076d59b17823d827056fe73259fa6c51` closed with `planned_completion`, retrieved `status=known reports=0`, derived `false_negative=1`, `true_positive=0`, `false_positive=0`, and left the target control registry clean. This is useful benchmark evidence: current Jetmon v2 does not detect a 200 OK response that closes after a truncated body.
- Workstation-run harness smoke passed `content-keyword-missing`: run `cf9d071f96faedeec31a61ddcfb267c2` closed with `planned_completion`, retrieved raw `keyword` / normalized `content_failure` with `error_code=5`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, `detection_latency_s=183.007297`, and left the target control registry clean.
- Workstation-run harness smoke confirmed `content-keyword-injected` is a Jetmon v2 capability mismatch for inverted keyword checks: run `7d87d3dab8d6f5c29d77cab5e88bcc1a` recorded `reason_code=capability_mismatch` and derived `unknown=1` with no false negative. That live run also showed the runner still activated the target after all adapters were gated; the runner now short-circuits all-gated runs before failure injection.
- Workstation-run harness smoke using a temporary Jetmon v2 services config passed `http-head-200-get-503` against the deployed target fleet: run `81d439ab1fb15ba53df06dc5adaad71b` closed with `planned_completion`, retrieved `alert_fired` / `alert_resolved`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, and left the target control registry clean.
- Workstation-run harness smoke passed `http-head-405-get-200` after fixing method-sensitive metric semantics: run `d00ad54bf28dcb9577e78f02f2b0d17c` closed with `planned_completion`, retrieved `status=known reports=0`, derived `false_negative=0`, `false_positive=0`, and left the target control registry clean.
- Workstation-run harness smoke passed `tls-invalid-self-signed` against the deployed target fleet: run `ae0ca69b79f065a7c1817ad291f7d159` closed with `planned_completion`, retrieved `alert_fired` / `alert_resolved`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, `detection_latency_s=36.043472`, and left the target control registry clean. Rerun `e8aa57f15b872004d2ab5dad6cbe1293` confirmed the updated adapter stores raw `ssl`, normalized `tls_failure`, `error_code=3`, `true_positive=1`, and `detection_latency_s=35.250424`.
- Workstation-run harness smoke completed `tls-deprecated-tls11`: run `320b79e25edea3fd2c1f76bf8addb4f6` closed with `planned_completion`, retrieved raw `ssl` / normalized `tls_failure`, derived `tls_advisory_false_outage=1`, `tls_advisory_missed=0`, `true_positive=0`, and left the target control registry clean. This shows current Jetmon v2 behavior treats TLS 1.1 as a hard TLS failure rather than a warning-level advisory.
- Workstation-run harness smoke passed `tls-handshake-version-mismatch`: run `0843d3e1c73357184f2e023dd2d1f1e6` closed with `planned_completion`, retrieved raw `ssl` / normalized `tls_failure`, derived `true_positive=1`, `false_negative=0`, `false_positive=0`, `detection_latency_s=252.643508`, and left the target control registry clean. No resolve event arrived within the three-minute grace window, so recovery-latency scoring may need a longer grace or a final post-deprovision refresh if it becomes a benchmark metric.
- Jetmon v2 adapter retrieval now includes `tls_expiry` events and preserves Jetmon `error_code` semantics in raw classifications, so TLS, timeout, redirect, and keyword failures no longer collapse into generic HTTP state labels.
- The harness server times out when calling the same API health endpoint, so runner-to-API reachability is still blocked.
- The deployed harness `/etc/uptime-bench/services.toml` still has `jetmon-v2` disabled with no API URL or token configured.

Attempted on 2026-04-28:

- The deployed harness binary was stale and could not parse `http_method_status`; redeploying the current harness fixed that parser gap.
- The deployed harness did not have a configured Jetmon v2 `url` / token in `/etc/uptime-bench/services.toml`.
- The deployed harness could not reach the developer Jetmon v2 API at the private test address, while the local workstation could reach `/health`.
- The previously supplied Jetmon v2 tokens returned 401 from `/api/v1/me`, so local smoke could not provision a site. The partial run deactivated the target failure normally and the target control registry was clean afterward.

Resume this item when the runner host has a reachable Jetmon v2 API URL and the deployed harness service config is enabled with a current write-scope token. The harness now supports `-monitors=jetmon-v2`, so the checked-in scenario corpus can be reused for Jetmon v2 smoke without creating temporary scenario copies.

Run the remaining small monitor-facing scenario set through the real deployed fleet and the Jetmon v2 adapter before broadening to cross-vendor campaigns. This should validate the complete loop: harness provisioning, Jetmon v2 API calls, monitor behavior against injected target failures, retrieval, metric derivation, cleanup, and no remaining active fleet failures.

Initial scenario set:

- HEAD/GET mismatch cases: `http-head-405-get-200.toml` and `http-head-200-get-503.toml` are passing from the workstation-run harness path; repeat from the deployed harness once reachability/config are fixed.
- Basic outage and timing cases: `http-503.toml` and `http-timeout-ttfb.toml` are passing from the workstation-run harness path; `http-partial.toml` runs cleanly but is a Jetmon v2 false negative.
- Content/keyword cases: `content-keyword-missing.toml` is passing from the workstation-run harness path; `content-keyword-injected.toml` is correctly gated as `capability_mismatch` for Jetmon v2 because inverted keyword checks are unsupported; one high-signal compromise page such as `content-defacement.toml` remains to run.

Acceptance:

- Every run records `scenario_runs`, `ground_truth_events`, `monitor_reports`, and derived metrics without adapter errors.
- Jetmon v2 monitor provisioning and cleanup leave no orphaned test sites.
- Target and DNS control registries are clean after every run.
- The HEAD/GET mismatch scenarios specifically exercise the false-down and false-up risks that Jetmon v1 is expected to fail and Jetmon v2 should not fail.

## Keyword-monitoring capability is dead-wired

**Status:** Implemented. Design locked 2026-04-25; end-to-end wiring, capability gating, adapter keyword branches, and tests are now in place.

Found during a review pass on 2026-04-25. At the time, the pieces existed independently but never met:

- `adapter.Capabilities.SupportsKeyword` is set to `true` on Pingdom, UptimeRobot, Datadog, and Better Uptime; to `false` on `jetmon-v1`. Currently nothing in `internal/runner` reads either flag, so it has no effect.
- `adapter.ProvisionConfig.Keyword` exists on the struct (`internal/adapter/adapter.go:91`) but the runner builds the config with only `CheckFrequency` (`internal/runner/runner.go:144`) — `Keyword` is never populated.
- Each adapter's `Provision` ignores `config.Keyword`. Pingdom always creates a status (`type=http`) check; UptimeRobot uses `monitorTypeHTTP = 1` (a keyword check would be `type=2`); Datadog only adds a `statusCode` assertion; Better Uptime always uses `monitor_type = "status"`.
- Scenario TOMLs already carry `keyword = "uptime-bench-canary"` for the keyword scenarios, and the runner forwards that string to the target binary's control plane (`internal/runner/runner.go:403`), which uses it to know what string to remove or inject when serving tampered content. So the target side is keyword-aware — the monitor side is not.

Current state: scenario-level `keyword` / `keyword_check` are parsed, defaulted for content scenarios, passed through the runner into `ProvisionConfig`, and gated against `SupportsKeyword` / `SupportsInvertedKeyword`. Pingdom, UptimeRobot, Datadog Synthetics, Better Uptime, and Jetmon v2 all exercise their supported keyword paths in unit tests; unsupported combinations produce `reason_code = "capability_mismatch"` instead of false negatives.

### Locked-in design

- **Scenario format** — `keyword` is a top-level scenario field (not per-failure), since it's a property of monitor configuration. Sibling field `keyword_check` takes values `present` (alert when keyword absent — the canary case) or `absent` (alert when keyword present — the injected-bad-keyword case). When a scenario contains any content failure but doesn't set `keyword` explicitly, the runner defaults `keyword = "uptime-bench-canary"` and `keyword_check = "present"`.
- **Capability gating** — when the scenario sets a keyword and the adapter's `SupportsKeyword == false`, the runner skips Provision and writes a single `monitor_reports` row with `Status = Unknown` and a structured `reason_code = "capability_mismatch"` plus a free-form Reason describing the missing capability. Same pattern the runner already uses for `MinCheckFrequency`. **Capability-mismatch results are first-class data**, not noise — they are the support matrix for "which services support which features," which is a project deliverable. Reporting must distinguish them from genuine false negatives (see [events.md](events.md)).
- **Better Uptime inverted keyword support** — Better Stack's monitor API supports both `monitor_type = "keyword"` and `monitor_type = "keyword_absence"`. The adapter now exposes `SupportsInvertedKeyword = true` on GET lanes and gates keyword checks on HEAD lanes, where no response body exists.

### Implementation order

1. ✅ **Schema migration** — add `reason_code` column to `monitor_reports` (nullable string; existing rows back-fill empty). Update `internal/db` writes and the runner's `logMonitorReport` to set it. This is the prerequisite that lets capability gating be queryable as a support matrix; ship before any of the keyword work to keep the migration small and isolated.
2. ✅ **Scenario format** — promote `keyword` to scenario-level and add `keyword_check`; update `internal/scenario` parser, validator, and the corpus check; update existing keyword scenarios to the new format.
3. ✅ **Runner** — populate `ProvisionConfig.Keyword` and `ProvisionConfig.KeywordCheck` from the scenario; add capability-gating branch that mirrors the existing `MinCheckFrequency` branch.
4. ✅ **Adapters** — branch on `config.Keyword != ""`:
   - **Pingdom**: `newCheckRequest` carries `shouldcontain` (present check) or `shouldnotcontain` (absent check). Type stays `"http"`.
   - **UptimeRobot**: switch `type` from `1` (HTTP) to `2` (Keyword); set `keyword_type=1` for "exists" (present check) or `keyword_type=2` for "not exists" (absent check); set `keyword_value`.
   - **Datadog**: append a `body` assertion to the existing `Assertions` list with operator `contains` (present) or `does not contain` (absent).
   - **Better Uptime**: switch `monitor_type` from `"status"` to `"keyword"` for present checks and `"keyword_absence"` for absent checks; set `required_keyword`. Keyword monitors force GET.
5. ✅ **Tests** — unit tests for each adapter's keyword branches; integration test that asserts capability gating writes a `monitor_reports` row with the expected `reason_code` instead of a Provision call; corpus check covers the new scenario fields.

Remaining follow-up: broaden live API smoke coverage for each vendor's keyword branch, especially inverted keyword checks and Better Uptime's `keyword_absence` mode. Jetmon v1 remains `SupportsKeyword = false` for this comparable keyword axis.

---

## TLS monitor-facing validation

**Status:** Target/DNS direct acceptance is implemented; scenario corpus coverage is now present for monitor-facing TLS validation. Aged-certificate and live monitor-facing TLS validation remain active priorities.

The target binary now exposes an HTTPS listener with a generated self-signed fallback certificate. With `-cert-library-manifest`, healthy requests use the longest-valid matching library certificate, while active `tls_expired` and `tls_expiring` failures select the closest matching expired/expiring certificate for the request SNI. `tls_invalid` can force the generated self-signed cert or a generated hostname-mismatch cert, `tls_deprecated` can clamp the HTTPS listener to TLS 1.0 or TLS 1.1, and `tls_handshake` aborts the handshake before certificate selection. Remaining TLS work: deployed-fleet probe acceptance against a real certmint-produced library.

Deployed direct acceptance now covers healthy HTTP/HTTPS, method-sensitive HEAD/GET mismatches, HTTP status/body/redirect/partial/timeout failures, global `tcp_refused`, `tls_invalid` self-signed and hostname-mismatch variants, `tls_handshake`, and `tls_deprecated` across both live cert-library domains. It also covers direct DNS-member injection on both nameservers. Checked-in monitor-facing scenarios now cover `tls_invalid` self-signed and hostname mismatch, `tls_handshake` version mismatch, `tls_deprecated` TLS 1.1, `tls_expiring` at five days, and `tls_expired` at thirty days. First monitor-facing Jetmon v2 smoke passed for `tls-invalid-self-signed` on 2026-04-28; a rerun after the adapter fix stored raw `ssl` and normalized `tls_failure` for Jetmon v2 `error_code=3` instead of generic `http_failure`. `tls-deprecated-tls11` also runs end-to-end, but Jetmon v2 currently reports it as a hard TLS outage, so uptime-bench correctly records `tls_advisory_false_outage=1`. `tls-handshake-version-mismatch` now has monitor-facing true-positive evidence, with a note that late detection did not resolve within the current grace window. Remaining TLS work is narrower: run the rest of those scenarios through real monitors, and repeat `tls_expired` / `tls_expiring` acceptance once certmint has aged snapshots suitable for those scenarios.

### Phase 1 — HTTPS listener with self-signed default

- Add a `:443` listener to `cmd/target` using `crypto/tls`.
- Generate a self-signed fallback cert on startup; future work may persist it under `/etc/uptime-bench/tls/` if stable fingerprints become useful.
- Wire the listener to the same virtual-host router used for `:80` so healthy pages work over HTTPS.
- Acceptance: `curl -k https://bench-a.<domain>/` returns the canary body; the shipped HTTP/TCP/content scenarios still pass on the HTTPS variant.

### Phase 2 — Certificate library

- Pre-generate a library of certs at varying ages: fresh (90 days remaining), expiring soon (1, 5, 30 days remaining), already expired (1 day, 30 days, 1 year), self-signed by an unknown CA, signed for the wrong hostname.
- Producer/consumer split inside the repo: `cmd/certmint` owns real Let's Encrypt/certbot issuance and writes an immutable library plus `manifest.json` (`internal/certmint/`); the target's TLS listener consumes the manifest via `internal/certlibrary/` for SNI-aware selection, with deterministic fleet-CA/self-signed fallbacks when no public cert applies. The `uptime-bench-dns` member exposes `PUT/DELETE /acme/txt` control endpoints so certmint's certbot manual hooks ([deploy/acme-hooks/](../deploy/acme-hooks/)) can install DNS-01 challenge records on the same nameservers that resolve the benchmark hostnames — no Cloudflare delegation needed for the runtime domains.
- New control API params for `tls_expired` / `tls_expiring` select a library member at activation time. `tls_invalid` supports self-signed and hostname-mismatch variants.
- Library structure: `manifest.json` is the contract. Filenames may encode age/profile for operator readability, but the target must select by manifest metadata rather than reparsing certificates at request time.
- Local acceptance: real in-process TLS handshakes and OpenSSL `s_client` checks confirm `tls_expired days_expired=30` and `tls_expiring days_remaining=5` serve the matching library certificate. Remaining fleet acceptance should repeat this against a certmint-produced library on deployed targets.

### Phase 3 — TLS protocol-level injection

- `tls_handshake`: implemented for target-side config selection by returning a deterministic handshake error before certificate selection. Probe receives a TLS alert; no HTTP response.
- `tls_deprecated`: implemented for target-side config selection by clamping `tls.Config.MaxVersion` to TLS 1.1 or TLS 1.0. In-process TLS handshake tests cover the target behavior.
- OpenSSL acceptance: `openssl s_client -tls1_3 ...` fails handshake when `tls_handshake` is active; `openssl s_client -tls1_1 ...` succeeds when `tls_deprecated` is active. `deploy/tls-smoke.sh` repeats the protocol checks against deployed targets through the control API, and `deploy/target-smoke.sh` folds those checks into broader HTTP/TCP/TLS deployed-target acceptance. Remaining fleet acceptance is monitor-facing probe smoke against a real certmint-produced library with aged snapshots for `tls_expired` / `tls_expiring`.

**Measurement note for `tls_deprecated`**: implemented. Because the request actually returns 200 OK, monitor outcomes split three ways: missed advisory, correct `tls_advisory` classification, or false outage report. The measurement engine records these as `tls_advisory_missed`, `tls_advisory_detected`, and `tls_advisory_false_outage`, distinct from true-positive and false-negative.

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

## Campaign hardening dry run

**Status:** Next after Jetmon v2 scenario smoke, cooldown, TLS validation, and maintenance validation. The campaign runner is implemented for serial single-target execution, but it still needs a small live dry run before it is trusted for publishable data.

Run a deliberately small campaign against Jetmon v2 only, using a single target and a narrow scenario mix. The point is not statistical power yet; it is to expose long-run cleanup, scheduling, rate-limit, retry/error, database, and reporting problems while the blast radius is small.

Acceptance:

- Campaign preflight accepts the config without exceeding service run-rate budgets.
- Every replay either produces usable metrics or a classified, queryable reason such as `capability_mismatch`, `adapter_error`, `cooldown_suppressed`, or `cooldown_uncertain`.
- `uptime-bench-report` produces table, TSV, and JSON output from the resulting campaign run.
- No monitor, target, or DNS state is left active after the dry run.

## Provider feature coverage gaps

**Status:** In progress. A provider feature pass found several service capabilities that uptime-bench should model explicitly before or alongside the next adapter wave. Some are now represented as scenario files; others need new adapter capability fields or monitor-kind support so results are not distorted by forcing every provider into an HTTP-status monitor shape.

Implemented locally in the scenario corpus:

- Additional content-body failures: `content-empty` and `content-error-page`.
- Redirect coverage: `http-redirect-loop` and `http-redirect-chain`.
- Timeout coverage: `http-timeout-body` and `http-timeout-total` in addition to the existing TTFB stall.
- TCP coverage: `tcp-timeout` in addition to `tcp-refused`.
- DNS coverage: `dns-nxdomain`, `dns-servfail`, `dns-timeout`, `dns-cname-nxdomain`, `dns-latency`, and both `dns_ns_unavailable` modes.
- Slow-success coverage: `http-latency-threshold` exercises response-time threshold assertions without turning the HTTP request into a timeout.
- Header-sensitive coverage: `http-header-status` verifies that an adapter can configure custom request headers and that the target can fail only matching monitor probes.

Adapter-surface improvements started:

- Better Uptime now supports GET/HEAD status-lane configuration and `keyword_absence` for forbidden-content checks on GET lanes.
- Datadog Synthetics now supports GET/HEAD HTTP API-test configuration, disables body assertions on HEAD lanes, configures custom request headers, configures response-time assertions, and preserves result location metadata when the API returns it.
- Jetmon v2 now receives custom request headers from scenario config through its adapter.
- `services.example.toml` documents the optional `http_method` setting for Better Uptime and Datadog Synthetics.
- Scenario parsing, adapter capabilities, and runner capability gating now understand native `monitor_kind` values (`http`, `dns`, `tcp`, `ssl_certificate`, `heartbeat`), custom `request_headers`, and `response_time_threshold`.
- DNS scenario execution now targets DNS control members directly, supports
  `fresh_hostname = true` for generated per-run hostnames, and records
  `failure_not_observable` unknown rows when authoritative preflight probes
  cannot see the injected DNS failure.

Feature gaps to model next:

- **Native DNS/TCP/SSL monitor adapter paths.** The schema/gating layer is implemented, but every current adapter still provisions HTTP monitors unless explicitly extended. This is deferred because each provider uses a different request and result schema for native DNS, TCP/port, and SSL/certificate monitors; enabling those paths without stale cleanup and classification tests would produce misleading comparisons.
- **DNS resolver-exposure artifacts.** Authoritative DNS preflight is implemented. A later reporting pass should also capture service-host resolver checks when available, raw Jetmon v2 DNS event metadata/transitions, and `dns.*` StatsD metrics so DNS failures can be separated into "not injected", "not recursively observable", and "observable but missed by the service" buckets.
- **Dedicated certificate and domain-expiry products.** Current TLS scenarios test HTTPS probe behavior. Native SSL/certificate monitor kinds should come next after one provider adapter is wired through `monitor_kind = "ssl_certificate"`. Domain-expiry monitors remain deferred because reliable simulation requires registrar/RDAP behavior rather than only target TLS behavior.
- **Heartbeat/push checks.** `monitor_kind = "heartbeat"` is reserved, but no adapter provisions heartbeat monitors yet. The intended first implementation is harness-owned heartbeat sending: adapter provisions a heartbeat endpoint, the harness sends check-ins during healthy periods, and `heartbeat_stopped` pauses those check-ins. This is deferred until the first adapter exposes heartbeat creation and stale cleanup.
- **Header/auth-sensitive checks beyond custom headers.** Custom request-header support is implemented for Datadog and Jetmon v2. Authentication flows and user-agent divergence remain deferred because they need clearer cross-provider request-shape controls and target fixtures beyond a single deterministic header.
- **ICMP/ping checks.** Treat ping checks as a separate monitor kind. Deferred because credible ICMP failure injection needs host/firewall-level or isolated-VM control; doing this on the shared target fleet could interfere with concurrent HTTP/DNS/TLS scenarios.
- **Browser/API assertion checks.** Datadog and Checkly can run richer API or browser assertions. Deferred from the first comparison table because browser checks have different cost, flake, dependency, and timing behavior than single-probe uptime checks. API assertions should be the first sub-track when a provider adapter needs richer structured checks.
- **Regional quorum and probe-location behavior.** Datadog result location metadata is now preserved when present. Provider-controlled location selection and alert quorum are deferred because they require provider-specific provisioning fields and report dimensions; publishable geo results should wait until location/quorum settings are explicit in run metadata.

Acceptance:

- Scenario docs and campaign configs include the new scenario files without requiring temporary one-off TOML copies.
- Adapter capability reporting distinguishes unsupported monitor kinds from false negatives.
- New monitor-kind adapter paths include stale-resource cleanup before they are enabled in campaign runs.
- Reports can break out "HTTP monitor observing DNS/TLS/TCP failure" from "native DNS/TLS/TCP monitor" so service comparisons stay fair.

## Next-wave adapter expansion

**Status:** In progress. Gatus and Uptime Kuma have deployed self-hosted instances on single-service hosts plus narrow uptime-bench bridges. Initial harness-driven smoke covered provision, retrieve, deprovision, cleanup, and an injected HTTP failure for both adapters. They are ready for controlled campaign inclusion, with the caveat that reports must identify them as single-origin self-hosted checks rather than global SaaS probe networks.

Recommended order:

1. **Uptime Kuma** — first self-hosted UI-driven comparison point. Deployed with pinned `louislam/uptime-kuma:2.3.0` and an uptime-bench bridge because direct automation uses Uptime Kuma's internal Socket.IO surface. Initial adapter coverage is HTTP status and present-keyword checks; inverted keyword, maintenance, TCP, DNS, and TLS/cert support remain deferred until validated.
2. **Gatus** — second self-hosted comparison point. Deployed with pinned `ghcr.io/twin/gatus:v5.35.0` and an uptime-bench bridge that manages a generated config fragment while reading endpoint status/history from the public API. Initial adapter coverage is HTTP status, present/inverted keyword checks, response-time threshold, and custom request headers. Next capability work is validating Gatus native DNS/TCP/TLS checks behind explicit `monitor_kind` support.
3. **updown.io** — first additional third-party service. Its API is simple, supports create/update/delete checks, exposes downtimes, publishes node/IP APIs, supports HTTP/TCP/ICMP-like coverage, string matching, and configurable `GET/HEAD` behavior.
4. **StatusCake** — useful market comparison with uptime APIs and period/history endpoints.
5. **Checkly** — high-capability API checks with method/assertion support; valuable after the simpler API-shaped adapters prove out the expansion path.
6. **Grafana Cloud Synthetic Monitoring** — useful blackbox-style synthetic monitor with a REST API, but setup/auth and result retrieval are more involved.

Expansion acceptance:

- Each new adapter implements provision, retrieve, deprovision, capability declaration, normalization, and stale-resource cleanup before it is enabled for campaign runs.
- `services.example.toml` documents auth, capacity, and any probe-range/region fields for the service.
- The first live smoke for each adapter covers at least `http-503`, one HEAD/GET mismatch, one content/keyword scenario if supported, and cleanup verification.
- Self-hosted adapters are labeled as single-origin/self-hosted in reports so they are not confused with global SaaS probe networks.
- Any adapter relying on an unstable or internal upstream API must pin the upstream version and document the automation risk.

## Report-driven provider reliability

**Status:** In progress after `reports/v2-regression-9am-20260502-063755Z/`.
The latest long run had no Jetmon v2, Pingdom, or Better Uptime adapter
errors, but it exposed 56 provision-time errors across UptimeRobot and Datadog
Synthetics:

- UptimeRobot: 39 total (`maintenance start_time invalid_parameter`: 15,
  `newMonitor already_exists`: 22, API timeout after `newMonitor`: 2).
- Datadog Synthetics: 17 total (`downtime invalid scope`: 17).

Implemented hardening from that report:

- UptimeRobot one-shot maintenance windows now round/clamp `start_time` to the
  next full second so a subsecond run start is not sent as a timestamp that is
  already just barely in the past.
- UptimeRobot monitor names now include a short hash of the monitor URL, and
  the runner spreads monitor URLs across configured site paths with a
  per-scenario query token. This reduces duplicate-name and duplicate-URL
  collisions in parallel matrices while keeping failures scoped to the same
  path the monitor checks.
- UptimeRobot uncertain create timeouts retry safely after attempting to adopt
  a single matching harness-owned monitor.
- Datadog downtime creation now sends an explicit global scope alongside the
  monitor id.
- Provision/retrieve failures now write `monitor_reports` rows with
  `reason_code = "adapter_error"`, and markdown campaign reports include a
  reason-code table so these failures are visible without reconstructing them
  from logs.
- Human-readable and JSON campaign reports now also include reason-detail buckets
  split by provider/adapter error text, so errors such as duplicate monitor
  collisions, maintenance timestamp validation failures, API timeouts, and
  provider schema errors can be compared without mining raw logs.

Acceptance for the next run:

- Live smoke UptimeRobot and Datadog on
  `maintenance-http-503-full-cover`; both should provision maintenance without
  adapter errors and should score as maintenance-suppressed if the vendor
  behaves as expected.
- The next matrix should show zero, or nearly zero, UptimeRobot
  `already_exists` errors. If they persist, enhance stale cleanup to search by
  benchmark URL in addition to exact friendly name and add a plan-level
  duplicate URL preflight.
- Report generation buckets adapter-error reasons by provider error text, not
  only by the structured `adapter_error` code. The next report review should
  confirm those buckets are specific enough in real output; if they are still
  too noisy, adapter-specific error normalization can add stable subcodes
  without changing the raw-detail table.
- Geo-scoped `http-geo-503` remains a benchmark validation gap, not a
  service-specific finding, because every service failed it in the latest run.
  Before publishing geo results, audit probe CIDRs against actual source IPs
  observed by the target and flag services with unverifiable probe ranges.

## Provider-state preflight cleanup

**Status:** MVP implemented for public API adapters. `cmd/uptime-bench-cleanup` can run independently before a matrix batch, loads the enabled services and fleet scope, supports dry-run/delete modes, summarizes per-service counts, reports reason/kind breakdowns for idempotency diagnostics, and uses adapter-owned stale cleanup for UptimeRobot, Pingdom, Datadog Synthetics, and Better Uptime. Jetmon v1/v2 intentionally remain unsupported by this generic provider cleanup until their synthetic benchmark-site ownership rules are explicit enough to delete safely.

Add a cleanup command or harness preflight that lists harness-owned monitors/tests/checks for every enabled provider, filters only resources with uptime-bench-owned names/tags/URLs, and deletes stale resources left by an interrupted run, timed-out provider API call, or host reboot. The command should have a dry-run mode, print per-provider counts, fail closed when ownership is ambiguous, and leave non-benchmark monitors untouched.

Provider-specific ownership signals:

- **UptimeRobot**: `friendly_name` prefix `uptime-bench:` plus matching benchmark URL.
- **Pingdom**: check name prefix `uptime-bench:` plus host/path match.
- **Datadog Synthetics**: `uptime-bench` tag and `uptime-bench:<target_id>` tag.
- **Better Uptime**: `pronounceable_name` prefix `uptime-bench:` plus benchmark URL.
- **Jetmon v1/v2**: bridge/API-created synthetic benchmark sites only; read-only/pre-seeded Jetmon v1 monitors should not be deleted.

Acceptance:

- ✅ Preflight can run independently before a matrix batch and summarize `found`, `deleted`, `skipped`, and `error` counts per service.
- Stale cleanup happens before provider capacity checks so leaked monitors do not consume Better Uptime/UptimeRobot/Pingdom caps.
- ✅ Deletion uses the same idempotent deprovision paths as normal teardown where possible.
- ✅ Ambiguous matches are reported but not deleted automatically.
- ✅ Cleanup summaries include reason/kind breakdowns so repeated dry-runs can show whether remaining resources are unsupported, ambiguous, stale, or outside scope.

## Active-run operational guardrails

**Status:** Documented as an operator constraint; tooling guardrails are not implemented. During a long campaign or capacity suite, do not run provider cleanup, deploy binaries, sync fleet config, restart target/DNS services, run adapter live smokes, or mutate report directories. Those actions can delete monitors, change target routing, restart the failure surface, or alter provider state while the run is collecting evidence.

This was intentionally skipped during an active overnight test. Safe concurrent work is limited to read-only inspection and local-only code, docs, and unit tests that do not call the live fleet or provider APIs.

Remaining follow-up:

- Add an active-run guard to mutating operator commands (`uptime-bench-cleanup`, deploy helpers, fleet sync helpers, and live smoke wrappers) so they warn or fail closed when a campaign/capacity run appears to be active.
- Define the active-run signal. Good candidates are an in-progress `campaign_runs` row, a capacity suite state/lock file, or an explicit operator-owned lock path. The signal should be local to the orchestrator and should not require provider API calls.
- Add a documented override flag for emergency cleanup, with the command printing exactly which running campaign or lock was ignored.
- After the current active test window completes, run the parked live tasks: provider cleanup dry-run, adapter smoke checks, target/DNS cleanup checks, and any needed fleet config sync/restart.
- Keep report finalization separate from provider cleanup so analysis can be regenerated without changing live monitor state.

## Automated randomized testing campaigns

**Status:** Partially implemented. Methodology locked 2026-04-27. Config parsing, deterministic generation, cooldown-aware scheduling, serial execution, single-target scope preflight, a checked-in starter config, run-rate budget preflight, mixed-content escalation instrumentation, metric derivation, markdown/JSON finalization, preflight timing estimates, and reporting are in place; remaining work is final published-campaign sampling policy, concurrent execution, and live-campaign hardening.

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
patterns = ["layered", "replacement", "recovery"]

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

The scenario format's `[[failures]]` blocks now support both `offset` and per-failure `duration`, so layered, replacement, and recovery patterns are representable without a new stage abstraction. The campaign generator accepts `[escalation].patterns = ["layered", "replacement", "recovery"]`; omitted `patterns` defaults to `["layered"]` for compatibility. Reports label multi-stage runs by escalation pattern and stage order. Remaining escalation work is deciding which pattern mix to use for published benchmark configs.

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
5. With `escalation.probability`, append additional stages on the same scenario using one of the configured escalation patterns.
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
#   - sample counts per failure/service cell, with missing cells counted as 0
#   - any capability_mismatch counts or uncategorized Unknown rows

failure_type | service     | n  | tp_rate | tp_rate_ci95 | cap_mismatch | min_s | avg_s | p50_s | p50_ci95_s | p95_s | p95_ci95_s | max_s
http_status  | pingdom     | 60 | 0.98    | 0.91-0.99    | 0            | 41.0  | 72.0  | 68.0  | 55.0-80.0   | 120.0 | 95.0-180.0  | 180.0
http_status  | uptimerobot | 60 | 0.96    | 0.88-0.99    | 0            | 62.0  | 98.0  | 95.0  | 80.0-120.0  | 145.0 | 110.0-220.0 | 220.0
...
```

Output formats: human-readable table (default), TSV, JSON. Backed by SQL queries joining `scenario_runs` ↔ `derived_metrics` filtered on the campaign's run-id list.

Per (failure_type, service) statistics:

- Detection rate (true_positive / (true_positive + false_negative), excluding capability_mismatch, maintenance_suppressed, cooldown_suppressed, cooldown_uncertain, and TLS advisory outcomes).
- Detection latency min/max/avg/p50/p95, with 95% confidence intervals for p50 and p95.
- False-positive rate.
- `capability_mismatch` count (separately surfaced; not folded into detection rate).
- Sample count (so readers can judge meaning of the percentiles).

### Implementation phases

1. ✅ **Campaign config format + parser** — `internal/campaign` package, parser + validator. Tests in `campaign_test.go`.
2. ✅ **Pure design + schedule generator** — `(config, masterSeed) → (designs, schedule)`. `internal/campaign/generator.go`; deterministic, fixed-seed regression coverage in `generator_test.go` + `no_favoritism_test.go`.
3. ✅ **Schema migration for `campaign_runs`** — `schema/003_campaign_runs.sql`; `campaign_id` FK on `scenario_runs`. `db.InsertCampaignRun` / `CloseCampaignRun` shipped.
4. ✅ **Runner outer loop (serial)** — `runner.RunCampaign` walks `Plan.Schedule`, calls existing `Run()` per replay via `WithCampaignRunID`, rejects unsupported multi-host patterns, and rejects generated schedules that exceed any active service's `budget.max_runs_per_hour` before inserting a campaign row or calling vendor APIs. Per-replay errors don't abort the campaign. Tests in `internal/runner/campaign_test.go`. `cmd/harness` accepts `-campaign=<config.toml>` as a mutually exclusive alternative to `-scenario`; campaign mode runs every enabled service from `services.toml`. Metrics are derived in one batch at campaign end via `measurement.DeriveCampaign`, keyed by `scenario_runs.campaign_id`.
5. ✅ **Initial `cmd/uptime-bench-report`** — campaign metrics can be summarized from `derived_metrics` into table / TSV / JSON output. Current scope: per-(failure_type, service) samples, detection rate, TP/FN/FP/Unknown/maintenance/cooldown/TLS advisory counts, and latency min/avg/p50/p95/max.
6. ✅ **Full report statistics** — table/JSON reports now include bias self-checks, Wilson 95% detection-rate intervals, deterministic nearest-rank percentile intervals for p50/p95, and explicit `capability_mismatch` counts from `monitor_reports.reason_code`. TSV stays row-only for scripts but includes the additional columns.
7. **Escalation support** — per-failure `duration` overrides and generator pattern sampling now cover layered, replacement, and recovery representations. Reports use campaign replay metadata to label multi-stage shapes by pattern and stage order. [`configs/campaign/example.toml`](../configs/campaign/example.toml) carries a runner-safe starter mix for single-target campaigns; remaining work is settling the exact pattern mix and sample depth for published benchmark configs after live data confirms the noise floor.

Each phase is independently mergeable. Phases 1–5 deliver the "campaigns work, no escalation" milestone — that alone produces useful comparison data.

### Cross-cutting concerns

- **Budget interplay with vendor cooldowns**: the generator now enforces `cooldown.per_target_minimum` in the replay schedule and rejects infeasible schedules. `RunCampaign` also preflights the generated schedule against each active service's `budget.max_runs_per_hour`; a violation fails before a `campaign_runs` row is inserted or vendor APIs are called. Vendor-side alert cooldowns can still matter when an adapter cannot guarantee clean state. Campaign replays require `SupportsCooldownReset = true`; adapters where it's false get gated as `capability_mismatch` for the campaign's runs. If a retrieve still carries cooldown metadata, measurement emits `cooldown_suppressed` or `cooldown_uncertain` rather than `false_negative`. Currently all probe-based adapters with Phase B set this true (delete-recreate cycles state); Jetmon-v1 needs bridge work to do the same.
- **Concurrent and multi-host execution**: a 1,000-run campaign at ~8 minutes per scenario is ~133 sequential hours. Campaigns must run scenarios concurrently across non-overlapping (target, service) pairs, and multi-host cells need a scenario representation that can express more than one target. The runner currently runs one single-target scenario at a time end-to-end and now rejects multi-host campaign patterns before execution. Concurrent/multi-host campaign mode is an explicit extension. Open question for the design pass: where the parallelism axis lives (per-target, per-service, per-(target,service) pair). Single-scenario mode remains serial.
- **Reproducibility under randomness**: every campaign records its master seed and config in `campaign_runs`. Re-running with the same seed against the same fleet+adapter versions produces the same design set and schedule. The project's existing reproducibility invariant scales to campaigns.
- **Per-stage keyword config (deferred)**: the scenario format carries one `Keyword` + `KeywordCheck` pair per run. An escalation that mixes a `keyword_injected` stage with a non-injected http_body stage (`ransomware`, `defacement`, `keyword_missing`, …) collapses to `KeywordCheck="absent"` with the injected keyword, silencing the canary-missing signal the non-injected stage was meant to measure. `applyHTTPBodyDefaults` documents this. Instrumentation is in place: campaign replays write `campaign_mixed_content_escalation` into `scenario_runs.parameters`, and `RunCampaign` logs the count of generated designs with this shape at campaign start. The fix is per-failure `Keyword`/`KeywordCheck` fields and adapter rework to switch keyword config mid-run — most adapters configure once at Provision and can't. Only schedule that schema change if real campaign data shows mixed escalations are >5% of designs in practice.

### Methodology disclosure (to be in published results)

When campaign data is published, the methodology section must include, at minimum:

- The full campaign config (TOML) and master seed.
- Per-cell sample counts (showing where high-discrimination depth was applied and where it wasn't).
- Adapter and target-fleet commit SHAs.
- Total wall-clock duration and any campaign interruptions.
- Confidence intervals on all reported percentiles.
- The full count of `capability_mismatch`, `adapter_error`, `maintenance_suppressed`, `cooldown_suppressed`, `cooldown_uncertain`, `tls_advisory_detected`, `tls_advisory_missed`, and `tls_advisory_false_outage` outcomes per service — these are part of the data, not filtered out.
- Both sample-weighted service scores and scenario/category-normalized service scores so uneven scenario mixes do not hide a provider weakness behind a high-volume easy category.
- The percentile method used. `cmd/uptime-bench-report` uses the **nearest-rank** convention (`idx = ⌈p·N⌉ − 1` on the sorted sample, NIST / Wikipedia "C = 1"). Different from R's default `quantile()` (type 7, linear interpolation) and numpy's default `percentile()`, which produce slightly different numbers for the same data. Nearest-rank always returns an observed sample value — the published p95 is a number that actually occurred in the campaign — but skeptics recomputing with a different method will see ±1-bucket drift.
- The aggregation depth. `cmd/uptime-bench-report` accepts either a concrete `campaign_runs.id` (one run) or a stable `campaign_id` from the campaign TOML (every matching run aggregated). The report header line discloses which interpretation matched and how many runs were folded together — quote that line in any published post so readers know the numbers span N runs, not 1.
- The exact finalization command used. Re-running finalization should recompute derived metrics and refresh `report.md`, machine-readable JSON, and the report manifest without hand-editing output files.

The methodology choices (which failure types are high-discrimination, what the sample-count target is) are explicit human judgments and should be argued for in any published post — different operators will care about different failures, and a transparent methodology lets them re-weight from the raw data if their concerns differ.

---

## Maintenance window suppression

**Status:** Implemented. Design draft at [inter-run-state-design.md](inter-run-state-design.md), 2026-04-26; scenario parsing, runner capability gating, adapter `ProvisionConfig.MaintenanceWindow`, vendor-side maintenance APIs, and `maintenance_suppressed` metric classification are in place.

Monitors commonly support scheduled maintenance windows during which alerts are suppressed. Testing whether a monitor correctly silences alerts during a declared window is a meaningful accuracy dimension — a monitor that still alerts during maintenance produces false positives; a monitor that never alerts afterward may have also cleared state it shouldn't have.

**Implemented pieces:**

- `ProvisionConfig.MaintenanceWindow` schedules absolute vendor-side suppression windows.
- Runner gates scenarios with `[maintenance]` against `SupportsMaintenanceWindows`; unsupported adapters produce `reason_code = "capability_mismatch"` and are not provisioned.
- The measurement model emits `maintenance_suppressed` when a failure is active, the adapter returned Known with no reports, and the maintenance window covered at least 80% of the failure period. Correct behavior, not a false negative.
- Scenario TOML supports a `[maintenance]` block with relative `start_offset` and `duration`. Natural patterns:
  - *Overlapping*: window covers the failure entirely (tests "alerts suppressed during maintenance").
  - *Edge*: window ends midway through the failure (tests "alerts fire as soon as window closes, even though failure was already active").

**Per-vendor implementation status:**

- *Pingdom* — creates a one-shot maintenance window with the check attached; unit-covered.
- *UptimeRobot* — creates and attaches a maintenance window; unit-covered.
- *Datadog Synthetics* — creates downtime scoped to the synthetic monitor id; unit-covered.
- *Better Uptime* — patches monitor pause/maintenance fields for the requested window; unit-covered.
- *Jetmon v2* — patches `maintenance_start` / `maintenance_end`; unit-covered and live API-contract covered.
- *Jetmon v1* — still no first-class bridge/API support; maintenance scenarios gate as `capability_mismatch`.

Remaining follow-up: run true live fail-during-maintenance scenarios against each vendor to verify suppression behavior, not just API request shape and metric classification.

## Live maintenance-window validation

**Status:** Active validation item. Implementation and unit/API-shape coverage are in place; true live fail-during-maintenance behavior still needs to be observed.

After the Jetmon v2 baseline scenario smoke passes, run targeted maintenance-window scenarios against Jetmon v2 first, then broaden to vendors that claim `SupportsMaintenanceWindows`. The validation should cover at least one fully-covered maintenance window and one edge case where the maintenance window ends while the failure is still active.

Acceptance:

- Suppressed alerts during fully covered maintenance windows become `maintenance_suppressed`, not false negatives.
- Failures that continue after the maintenance window closes still produce alerts when the service supports that behavior.
- Unsupported services remain explicitly gated as `capability_mismatch`.

---

## Alert cooldown interaction between runs

**Status:** Implemented for adapters that can guarantee clean state; Jetmon v1 live validation is blocked before cooldown can be tested. Design draft at [inter-run-state-design.md](inter-run-state-design.md), 2026-04-26. Capability flags are wired, delete/recreate adapters claim reset support, Jetmon v2 disables alert cooldown at provision time, Jetmon v1 write mode claims reset support through the bridge's POST/DELETE state reset semantics, campaign scheduling enforces per-target spacing, campaign replays gate adapters without `SupportsCooldownReset`, and the measurement engine emits `cooldown_suppressed` / `cooldown_uncertain` outcomes from retrieve metadata.

Most monitors suppress repeated alerts for the same site within a cooldown window (commonly 30 minutes). When uptime-bench runs multiple consecutive scenarios against the same provisioned monitor, the second run's alert may be suppressed by the cooldown from the first — producing a result that looks like a missed detection but is actually the monitor working correctly.

**The correct approach in `Deprovision`**: if the service's API supports resetting alert state or the cooldown clock, do so. Otherwise, delete and recreate the monitor (accepting the cost of full reprovisioning).

**If the API doesn't support either:** record the cooldown state at the time of Retrieve and include it in `MonitorReport.Metadata`. The measurement engine then classifies the suppressed result separately rather than as a false negative.

**Current implementation status:**

- *Pingdom* — delete/recreate cycles check state; `SupportsCooldownReset = true`.
- *UptimeRobot* — delete/recreate cycles monitor state; `SupportsCooldownReset = true`.
- *Datadog Synthetics* — delete/recreate cycles the synthetic test and attached monitor state; `SupportsCooldownReset = true`.
- *Better Uptime* — delete/recreate cycles monitor and incident state; `SupportsCooldownReset = true`.
- *Jetmon v2* — adapter provisions sites with `alert_cooldown_minutes = 0`; `SupportsCooldownReset = true`.
- *Jetmon v1* — write-mode bridge runs reset `site_status` / `last_status_change` on POST and DELETE, so `SupportsCooldownReset = true` only when `auth.write_mode = "true"`; read-only bridge runs still gate as `capability_mismatch`.

Each adapter must document how it handles this in its implementation notes.

Live validation attempt on 2026-04-28:

- Deployed the current harness binary to the harness host so `-monitors=jetmon-v1` can target the Jetmon v1 adapter alone.
- Applied the append-only `003_campaign_runs.sql` migration to the deployed harness database; `002_reason_code.sql` was already present.
- Confirmed the Jetmon v1 bridge is reachable from the harness host and accepts the configured bearer token.
- First `http-503` run failed before cooldown could be tested because Jetmon v1 bridge write mode returned `500` from `POST /monitors`; the run `6e737b679e6052851a34ea3ade1ad2a9` closed as `adapter_error`.
- The target failure deactivated normally and the target control registry was clean afterward.

Remaining follow-up: fix or redeploy the Jetmon v1 bridge write-mode `POST /monitors` path, then run a back-to-back Jetmon v1 write-mode scenario pair to confirm reset behavior with the real worker loop.

---

## Probe IP CIDR refresh tool

**Status:** MVP implemented. `cmd/probe-ips-refresh` fetches public vendor probe lists, normalizes IPs/CIDRs, and emits a reviewable TOML fragment. `make refresh-probe-ips` runs the tool locally, and a weekly GitHub Action opens a PR with the latest generated fragment. Manual review is still required for vendor feeds that lack stable regional metadata.

`services.toml`'s `[services.probe_ranges]` blocks list per-region CIDRs for each vendor's published probe pool. Vendors update these lists periodically. Each vendor publishes (or doesn't) in machine-readable form:

| Service | Source | Format | Region tags |
|---|---|---|---|
| Pingdom | `https://my.pingdom.com/probes/ipv4` | Plain text currently; parser also tolerates JSON | No in current feed |
| UptimeRobot | `https://uptimerobot.com/inc/files/ips/IPv4andIPv6.txt` | Plain text | No |
| Datadog Synthetics | `https://ip-ranges.datadoghq.com/` | JSON, `synthetics` key | Yes |
| Better Uptime | `https://betterstack.com/docs/uptime/frequently-asked-questions/` | HTML | Partially (in prose) |

**Build:** `cmd/probe-ips-refresh` Go tool fetches each list, normalizes regions where possible, and emits a TOML fragment to stdout. Operator pipes it to a file, diffs against the current `services.toml`, and applies changes by hand.

**Why not auto-write `services.toml`:** vendor changes can include unexpected region renames, IPv6-only additions, or removals that should be noticed, not silently merged. Manual review is the safety check.

**Region mapping:**

- Datadog tags each IP/CIDR with a provider location; the tool folds those into coarse uptime-bench regions such as `us-east`, `eu-west`, and `ap-sea`.
- Pingdom's current public IPv4 feed is untagged plain text. Decision: keep Pingdom under `global` unless Pingdom publishes stable region tags or an operator-maintained map with verified provenance; the tool emits a warning so operators can keep the all-probe pool fresh without pretending it is region-specific.
- UptimeRobot doesn't tag regions. Maintain a hand-edited `internal/probeips/uptimerobot_regions.json` that maps IP prefixes to regions; the tool warns when a new IP doesn't fall in any known prefix. Keep this file in the repo so updates are visible in PRs.
- Better Uptime publishes IPs in HTML prose with broad region annotations; the tool parses the current FAQ page best-effort and warns that mappings need review.

**Remaining:** seed the UptimeRobot region map with verified prefixes. Pingdom stays `global` until an official tagged feed or a verified curated map exists.

## Additional self-hosted monitor adapters

**Status:** Lower-priority expansion backlog after Uptime Kuma and Gatus. These are useful comparison points, but each needs either heavier infrastructure or a less direct adapter model.

Recommended order:

1. **Prometheus + blackbox_exporter + Alertmanager** — best self-hosted reference baseline for probe-level behavior. Supports HTTP/HTTPS, DNS, TCP, ICMP, and gRPC probes with detailed timing, TLS, and cert-expiry metrics. Treat this as an observability-stack baseline rather than a product-style uptime monitor, and pin the rule/alerting configuration in report metadata.
2. **Zabbix** — mature self-hosted monitoring with web scenarios, response-code checks, response-time data, string checks, triggers, and history. Valuable because it is widely deployed, but adapter work is heavier.
3. **Monika** — CLI/config-driven synthetic monitoring with HTTP/TCP probes and flexible assertions over status, body, headers, timing, and size. Likely needs webhook or log ingestion for clean event retrieval.
4. **Statping-ng** — lightweight uptime/status-page monitor with a REST API and HTTP/TCP/UDP/ICMP/gRPC coverage. Useful, but smaller ecosystem and lower priority than Uptime Kuma/Gatus.

Deferred for different scenario lanes:

- **Upptime** — useful zero-infrastructure/GitHub Actions monitor, but the normal five-minute cadence is a poor fit for minute-level detection comparisons. Consider later as a distinct "free/CI-backed monitor" category.
- **Healthchecks.io self-hosted** — reverse heartbeat/dead-man-switch semantics belong with future heartbeat and agent-based reverse-check scenarios, not the current probe-based uptime matrix.

## Jetmon capacity benchmark

**Status:** Initial observability path, generated target DNS support, guarded
Jetmon lifecycle automation, and scenario-run capacity report artifacts are
implemented on `trunk`.

The first capacity track compares Jetmon v1 and Jetmon v2 as active monitor count grows. It is intentionally separate from scenario accuracy campaigns: scenario runs answer whether monitors detect controlled failures, while capacity runs answer how resource use, check timeliness, lifecycle throughput, and service health scale with batch size.

Implemented:

- `cmd/uptime-bench-capacity` summarizes Prometheus range windows for `jetmon-v1.example.com` and `jetmon-v2.example.com`.
- `cmd/uptime-bench-dockerstats-exporter` exposes Docker API container stats as Prometheus metrics for hosts where cAdvisor cannot identify Docker 29 `overlayfs` / containerd-snapshotter writable layers.
- The exporter is deployed on both Jetmon hosts at `203.0.113.170:9103` and `203.0.113.171:9103`.
- `fleet.toml` supports `[[targets.generated_sites]]` ranges so DNS can resolve million-scale synthetic hostnames without expanding all hosts into the zone map.
- `cmd/uptime-bench-targetload` can probe generated host ranges against DNS and
  HTTP before those hosts are loaded into Jetmon, with Markdown output for
  saving HTTP-only and DNS-path target capacity reports.
- `cmd/uptime-bench-jetmon-capacity-run` can execute guarded Jetmon v1/v2
  capacity lifecycle batches from a private fleet config.
- Capacity `run-suite` invocations persist the last completed, last clean, and
  first problem batches and resume from the last clean batch by default;
  `-full-suite` restores a complete first-batch-to-last-batch pass, while
  `-batch-sizes`, `-duration`, and `-cooldown` support quick scout passes.
- `make capacity-jetmon-scout` provides the standard 1k/5k/10k Jetmon capacity
  ladder so operators can run the next scalability gate without reconstructing
  the command by hand.
- Capacity `run-suite` directories include `capacity.md` and `capacity.json`
  rollups for batch pass/fail status, DB health, missed-check threshold status,
  freshness throughput margin, thresholds, Prometheus highlights, last clean
  batch, and first problem batch.
- Capacity live batches validate exact activated target URL samples before the
  timed window starts, so a generated DNS/URL pattern mismatch fails as target
  setup rather than being misread as Jetmon missed checks.
- `uptime-bench-finalize -capacity` writes `capacity.md` and `capacity.json`
  alongside `report.md`/`report.json`, using the finalized campaign window as
  the Prometheus query range.
- `docs/capacity-benchmark.md` records the test shape, stop thresholds, target direction, and bulk lifecycle approach.

Remaining follow-up:

- Stress test target-side DNS and HTTP capacity before monitor-side million-site runs.
- Extend target preflight to run exact activated URL checks from each Jetmon
  service host and Veriflier host, not only from the capacity runner host.
- Use the guarded Jetmon lifecycle runner for staged active-monitor growth
  suites and compare suite `capacity.md` findings against detection behavior.

---

# Deferred

## Staggered failure measurement matching

**Status:** Runner support done; measurement engine matching rule pending. Punted — current "earliest active failure" rule produces sound results for the simultaneous-failure scenarios that exist today.

The `offset` field is honored by the runner: failures activate at `scenario_start + offset` and run for `duration` from that point (`internal/runner/runner.go:scheduleFailureEvents`). Ground-truth events emit independent `failure_start` / `failure_end` pairs per failure with the correct timestamps.

**Still to design (when staggered scenarios become a focus):**

- *Measurement engine:* detection latency is calculated against the first failure window an alert falls inside (`internal/measurement/measurement.go`). When failures are staggered and overlapping, "which failure did the monitor respond to?" matters for accurate latency attribution. Today's "earliest active failure" rule loses signal when multiple layers fail together (e.g., DNS at t=0, HTTP at t=30, alert at t=45 — was the monitor responding to DNS or HTTP?).
- The matching rule should probably be: the failure whose normalized classification best matches the monitor's reported classification, falling back to earliest-active when classification doesn't disambiguate. Spec it before implementing.

---

## Method-sensitive HTTP behavior beyond status

**Status:** Partially implemented. `http_method_status` covers the two high-priority HEAD/GET status mismatches: HEAD failure with healthy GET, and healthy HEAD with GET failure. Measurement now scores `http_method_status method="HEAD"` as a healthy-GET false-down trap rather than as a missed outage when no alert fires, while `method="GET"` remains a visitor-visible outage. The target also honors optional `method = "GET"` / `"HEAD"` predicates for `http_redirect`, `http_timeout`, `http_partial`, and `http_body`, with shipped scenarios for GET-only redirect loops, GET-only truncated bodies, GET-only TTFB stalls, and HEAD-only TTFB stalls. One custom-header status variant is implemented; broader user-agent, accept-header, auth, and body-shape divergence remains deferred until the method cases produce more benchmark data.

These are expected Jetmon-v1 pitfalls if it relies on shallow HEAD/status checks, and they should become Jetmon-v2 regression cases if v2 probes the user-visible GET path:

- **Method-scoped redirects:** HEAD returns 200 while GET enters a redirect loop. Inverse cases such as GET healthy but HEAD redirected/challenged, wrong-host redirects, and HTTPS downgrade redirects remain future variants.
- **Method-scoped latency and truncation:** HEAD returns quickly with 200 while GET stalls before first byte or closes mid-response; the inverse HEAD-stalls/GET-healthy case is also represented for TTFB stalls. Body-phase stalls remain future variants.
- **Request-header divergence:** the origin, WAF, cache, or bot protection serves different status/content for monitor-specific `User-Agent`, `Accept`, `Accept-Language`, auth headers, or missing browser-like headers. The checked-in `http-header-status` scenario covers a deterministic custom-header status failure; richer request-shape divergence can create either false-up or false-down results depending on which headers the monitor actually sends.

Implementation shape:

- Extend the target failure matcher beyond `(type, host, path)` to include optional request predicates. `method` and one custom-header status path are implemented; user-agent substring and richer header predicates remain deferred.
- Add method/header-scoped variants for `http_redirect`, `http_timeout`, `http_partial`, and selected `http_body` scenarios once the matcher can express them cleanly. Method-scoped target support exists now; remaining work is adding richer header-scoped variants and deciding which method/body combinations deserve campaign weight.
- Keep the existing content scenarios as the baseline for "GET body is bad while HEAD/status looks fine"; those already cover ransomware, defacement, malicious script, SEO spam, keyword missing, and keyword injection.

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

**Status:** Not implemented. `monitor_kind = "heartbeat"` is reserved in the scenario schema; provisioning and target-side sender support are deferred until a first adapter implementation is selected.

Heartbeat monitoring (dead-man's switch) and agent-based checks (wp-cron, scheduled task monitoring) require the monitored system to actively send signals to the monitor, rather than the monitor probing the site.

uptime-bench's target fleet is currently passive — it responds to probes. Simulating heartbeat failure requires:

- A heartbeat sender process on the target server that pings a monitor's ingest endpoint on a schedule.
- A control command (`heartbeat_stopped`) that pauses the sender for the failure window.
- Adapter support to provision a heartbeat monitor (endpoint URL, expected interval).

This architectural extension should be designed when the first monitor service ships heartbeat support. The control API and scenario schema are designed to accommodate new failure types without breaking changes, and the monitor-kind field can now route such scenarios away from HTTP-only adapters.

---

## Internet control-plane and provider-scale stretch tests

**Status:** Research-grade stretch goals. Not scheduled. Some may be impossible to run safely without sacrificial domains, owned network resources, commercial provider partnerships, or a dedicated isolated lab.

The current benchmark mainly controls the target, authoritative DNS, TLS certificates, and HTTP behavior. That already covers a large set of real outages, but it does not fully model failures where the internet control plane, registrar, edge provider, transit path, or monitoring provider control plane fails around an otherwise healthy target.

These tests are valuable because they answer a different question: "Does the monitoring service understand this external dependency or global failure mode, or does it only notice the final HTTP symptom?" They should remain separate capability axes. A provider should not be scored as a false negative for a registrar, BGP, browser, CDN, or heartbeat scenario unless the adapter explicitly provisions that capability and the service claims to support it.

Candidate stretch tracks:

- **Registrar and registry failures:** domain expiration, domain approaching expiration, `clientHold` / `serverHold`, registrar parking pages, registrar lock changes, RDAP/WHOIS lookup failures, and registry-side status drift. These require sacrificial domains or registrar/API control and should never risk production-like domains.
- **Parent-zone delegation failures:** parent NS records diverge from child-zone NS records, glue records are missing or wrong, DS records are broken, or all parent-delegated nameservers become unreachable. This tests delegation awareness rather than ordinary authoritative DNS behavior. It likely needs real delegated test domains or a controlled resolver/registry simulation.
- **DNSSEC failures:** bogus signatures, expired RRSIGs, broken DS/DNSKEY chains, NSEC/NSEC3 edge cases, and validation failures that only DNSSEC-validating resolvers see. This requires DNSSEC-capable authoritative infrastructure and careful separation between native DNS monitors and HTTP monitors that only observe downstream lookup failure.
- **IPv6-specific reachability:** AAAA exists but IPv6 is unreachable while IPv4 works, IPv6 TLS differs from IPv4 TLS, or IPv6 latency is pathological. This is more achievable than BGP work, but it requires an IPv6-capable fleet and report metadata that records which address family a monitor used.
- **Network path and ASN partitions:** failures scoped to one monitor region, one cloud provider, one ASN, one country, or one known probe CIDR group. The practical version is target-side firewall filtering using verified probe IP metadata. The risky version is real route manipulation.
- **BGP and transit failures:** destination prefix withdrawal, route leaks, blackholes, route flapping, upstream transit provider outage, or nullrouting by a major ISP. These are high-value but likely require an owned prefix/ASN, a network lab, or provider partnerships. They should not be attempted on shared production networks.
- **CDN and edge-provider failures:** origin down while CDN serves stale 200s, CDN-branded 52x responses, one edge POP serving stale or poisoned content, cache-key bugs exposing another tenant/user view, or edge-only TLS/cert mismatch. This probably needs either a real CDN sandbox or a benchmark-owned "edge simulator" in front of the target.
- **WAF, bot-protection, and reputation failures:** monitor probes get 403, 429, or JavaScript challenges while ordinary browser traffic succeeds; monitor-specific user agents are blocked; or a provider's probe IPs are reputation-blocked. This builds on request-header divergence but needs reliable probe IP/source metadata and more request-shape controls.
- **Multi-region quorum and disagreement:** one region fails, a minority of regions fail, all regions fail, or regions disagree on content. This should measure whether a provider alerts on one failed probe, a quorum, or all probes. It needs adapter-level location selection and report metadata for probe location/quorum policy.
- **Browser/runtime correctness:** HTML returns 200 but a SPA fails to hydrate, JavaScript throws, client-side routing breaks, Core Web Vitals regress, or a critical third-party browser dependency fails. These belong in a separate browser-check track because their cost, flakiness, and semantics differ from single-probe uptime checks.
- **Data consistency and stale-state failures:** wrong vhost/tenant served, stale content persists after origin update, logged-in content is cached for anonymous users, or region-localized content is served to the wrong audience. These require baseline learning or paired comparisons rather than a single response assertion.
- **Monitoring provider control-plane failure:** monitor creation succeeds but result retrieval is delayed, provider APIs rate-limit or timeout, incident logs are incomplete, alert state is stale, or deprovision fails. This is not a target outage, but it is a real reliability dimension for the benchmark; results should be classified as provider reliability data, not detection failures.

Possible implementation strategy:

1. Start with the least dangerous stretch tracks: IPv6-specific failures, regional/ASN firewall partitions, and CDN/edge simulation. These can mostly stay within benchmark-owned infrastructure.
2. Add schema/report support before live tests: capability flags, `monitor_kind` values where needed, address-family metadata, probe location/quorum metadata, and separate provider-control-plane metrics.
3. Treat registrar, parent-zone, DNSSEC, BGP, and real transit-provider tests as research projects. Each needs a written safety plan, rollback plan, and explicit non-production test assets before implementation.
4. Keep all stretch-track results out of the ordinary uptime accuracy denominator unless the service capability is explicitly configured and verified.

---

## Centralized fleet-topology API on the harness

**Status:** Not implemented. Deferred while the file-shipped `fleet.toml` model is sufficient.

The current model has every member that needs fleet topology — DNS for zones, certmint for DNS control URLs, and (in Phase B) targets for the certmint URL — read a copy of `fleet.toml` from `/etc/uptime-bench/`. When the operator changes topology, they edit the file on the harness and redeploy it to every member that consumes it. Same friction as today's DNS deployment.

The desired endpoint is a harness-mediated control API: the harness reads `fleet.toml`, exposes a small read-only HTTP endpoint (e.g. `GET /fleet/topology`), and every other fleet member polls it on a schedule. Operators edit `fleet.toml` once on the harness, restart the harness, and the rest of the fleet picks up the change without any further action. New hosts joining the fleet need only know the harness's URL and the shared control token — they don't need a synchronized copy of `fleet.toml`.

**What's needed:**

- An inbound HTTP server on the harness, behind the existing bearer-token middleware. The harness has none today; it's currently a pure client of the control plane.
- A polling client baked into target, dns, and certmint binaries, with cached last-known-good fallback so a brief harness outage doesn't take down dependents.
- Schema for the topology payload (subset of fleet.toml relevant to each role).
- A migration path: each role keeps the file-based fallback, gains the polling client, and the systemd units pass the harness URL via env when the operator opts in.

**Why deferred:** the file-shipped model does the job at our scale (single-digit fleet size, low rate of topology change). Centralizing wins clearly when (a) fleets get bigger, (b) topology changes more often, or (c) auto-recovery / dynamic fleet membership becomes a real requirement. Until then, a `make deploy-dns; make deploy-certmint` after a `fleet.toml` edit is acceptable friction. Worth pulling forward if any of those three pressures materialize.
