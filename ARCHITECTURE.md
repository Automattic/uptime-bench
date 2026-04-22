# uptime-bench — Architecture

uptime-bench evaluates uptime monitoring services by running controlled failure scenarios against target endpoints and measuring how each service under test detects, classifies, and reports each failure.

This document covers uptime-bench's own system architecture. For the library of failure scenarios the benchmark covers, see [`SCENARIOS.md`](SCENARIOS.md). For the scenario file format and field reference, see [`SCHEMA.md`](SCHEMA.md). For the monitor adapter interface, see [`ADAPTER.md`](ADAPTER.md). For the event log and output schema, see [`EVENTS.md`](EVENTS.md). For known future work, see [`ROADMAP.md`](ROADMAP.md). For Jetmon-specific design reference, see [`jetmon/TAXONOMY.md`](../jetmon/TAXONOMY.md).

---

## System components

### Target fleet

The target fleet is the set of infrastructure that uptime-bench controls and can manipulate on command. It has two tiers:

**Target VMs** host the test websites. Each VM runs a single Go binary that handles all non-DNS failure injection. The binary serves multiple virtual hosts on a single instance — sites are distinguished by `Host` header for HTTP and SNI for TLS. Each site can have multiple independently configured paths; failure state is tracked per `(host, path)` pair. Multiple failures can be active simultaneously on the same site or across sites on the same VM.

**DNS VMs** run the fleet's custom authoritative nameservers. The nameserver binary handles all DNS failure types: returning incorrect records (`dns_nxdomain`, `dns_servfail`), adding latency (`dns_latency`), and going partially or fully offline (`dns_ns_unavailable`). Multiple DNS VMs are required — this enables nameserver-level failure scenarios where one NS server fails while others remain up, and tests whether monitors correctly detect registrar-level DNS outages.

**Fleet registry:** The harness maintains a registry mapping fleet member IDs to type (target VM or DNS VM), address, and control port. Scenario failure injection routes commands to the appropriate fleet member type: DNS failures go to DNS VMs, all other failures go to target VMs.

**Failure injection layers within a target VM:**

| Layer | Handler | Notes |
|---|---|---|
| DNS | DNS VM nameserver binary | Separate fleet member, separate control plane |
| TCP | TCP proxy layer in the Go binary | Sits in front of the HTTP server; handles `tcp_refused`, `tcp_timeout` |
| TLS | Go `crypto/tls` layer | Per-site certs via SNI; `tls_expired`, `tls_invalid`, `tls_handshake` |
| HTTP | Go `net/http` request handler | `http_status`, `http_timeout`, `http_partial`, `http_redirect`, `http_body` |

The TCP proxy layer accepts raw connections, checks the failure registry for active TCP-level failures on that `(host, port)`, and either applies the failure (close immediately, hold open silently) or forwards the connection to the HTTP/TLS server. This keeps TCP failure injection self-contained within the Go binary without requiring root or iptables.

**Control plane:** Each fleet member listens on a dedicated control port (separate from the data-plane ports 80 and 443). The control API is an authenticated HTTP/JSON service. The harness sends activate and deactivate commands for specific `(host, path)` failure states. Failures carry a `duration` and expire automatically; the harness also sends explicit deactivate commands at scenario end to guarantee clean state.

**Requirements:**
- Targets must look like real websites to the monitors under test. An endpoint that monitors can fingerprint as a test rig produces useless data.
- Failure injection must be controllable at fine granularity — per-virtual-host, per-path, per-time-window. "Hard down" is easy; "returns 503 for 40% of requests for 90 seconds while DNS remains healthy" is the more realistic and more revealing scenario.
- Failure injection must maintain a ground-truth log of every state change — exactly when each failure started and stopped. Without this, latency measurements are meaningless.
- Scenarios should support composition: multiple failure modes active simultaneously, on the same or different sites, reflecting realistic cascading outages (e.g., DNS slow + origin returning 503).
- The fleet must be provisionable: the harness can bring new VMs into the fleet when needed, but the normal operating mode is always-on dedicated VMs. Always-on VMs also enable passive false-positive detection — if a monitor alerts on a site that has no active failure, that is a false positive captured in the run record.

**DNS infrastructure:** The fleet runs its own authoritative nameservers rather than using a managed DNS provider. This gives the harness direct control over every DNS record, the ability to inject failures at the resolution layer, and the ability to take individual nameservers offline for `dns_ns_unavailable` scenarios. Target domains use very low TTLs (≤30s) so that DNS state changes take effect quickly during scenario runs. The fleet operates multiple domains to enable mixed scenarios — some domains in a failure state while others remain healthy — and to test registrar-level DNS failure by taking all NS records for one domain offline simultaneously.

### Monitor adapters

Each monitoring service under evaluation has an adapter. All adapters implement the same three operations:

1. **Provision** — configure a monitor against a target URL with a specified check interval and alerting configuration.
2. **Retrieve** — poll or receive the service's incident data after a scenario runs: what it detected, when, how it classified the failure, whether it alerted, and whether it flapped.
3. **Deprovision** — remove the monitor cleanly between runs so state does not leak across scenarios.

Adapter design must accommodate the full range of API quality across services: well-documented REST APIs, services requiring dashboard scraping, rate-limited APIs, and services with no push delivery requiring polling. The adapter interface must support async and polling patterns without surfacing them to the core harness.

**Fair comparison:** monitoring services have different default check frequencies and retry policies. Benchmarks must either normalize these (configure all services to the same check frequency) or measure across a range. Either choice must be documented per scenario run.

### Scenario runner

A scenario definition specifies:

- The target endpoint(s) to use
- The failure mode(s) to inject and their parameters (type, rate, duration, region)
- The set of monitor adapters in scope for this run
- Timing: failure start, failure end, and a grace period for monitors to resolve after the failure stops
- A seed for any randomized injection parameters, so the run is reproducible

The runner drives failure injection on the target fleet, records ground-truth events, waits out the grace period, then collects adapter data.

**Open decision:** scenario authoring format — declarative YAML, code-defined, or a hybrid. Declarative is lower friction for simple scenarios; code-defined is needed for compositional or branching ones. A hybrid (declarative scenarios, code-defined scenario builders) is the likely right shape.

### Measurement engine

Derived from the ground-truth event log and monitor adapter data. Four measurement categories:

- **Detection latency** — time from failure start (ground truth) to alert delivery. Must define upfront: alert-received-by-harness vs. alert-first-visible-via-API can differ by minutes.
- **Detection accuracy** — true positive / false positive / false negative / missed. Requires scenarios that include non-failures as well as failures — a monitor that always alerts has perfect true-positive rate and useless false-positive rate.
- **Classification fidelity** — does the service correctly distinguish failure types (DNS failure vs. 5xx vs. slow response)? Scored against ground truth, accounting for the granularity the service's own API exposes.
- **Flapping behavior** — how the monitor handles intermittent failure: alert on first failure, debounce, require multi-region confirmation? This is where monitors most commonly diverge.

All metrics are computed from the event log, not stored as raw values alongside it. If a metric calculation needs to change, recompute from the log.

### Output schema

Each scenario run produces a structured output record. See [`EVENTS.md`](EVENTS.md) for the full schema. At a high level, each run produces:

- One scenario run record (parameters, timing, resolution reason)
- Ground-truth events (target state changes)
- Monitor report events (what each service detected and when)
- Derived metric rows (one per monitor per metric, computed from the above)

The output schema must be designed for cross-run and cross-service comparison. Design it carefully before implementation — it is the data the final comparison reports run over.

---

## Design principles

### Event-sourced ground truth

The benchmark's record of what each scenario did is an append-only event log. Derived metrics are computed from the log, not stored alongside it. If metrics are ever suspect, they can be recomputed from the raw events.

### Separate raw classification from normalized scores

Preserve each monitoring service's raw incident classification alongside any normalized score uptime-bench applies. Different services use different vocabularies. Normalize for comparison; keep the raw output for audit and verification.

### Unknown is not a detection failure

If an adapter cannot reach a monitoring service's API during a run (service outage, rate limit, authentication failure), that is Unknown — not a missed detection or false negative. uptime-bench must distinguish "monitor did not detect the failure" from "we could not retrieve the monitor's detection state." Conflating these corrupts accuracy measurements and is unfair to the service under test.

### Idempotent identifiers

Scenario runs, target failure events, and monitor report events all need stable, deterministic identifiers so that retries and replays do not produce duplicates in the output.

### Resolution reason is required

When a scenario run ends, record why — planned completion, aborted, target failed independently of the scenario, adapter error. This affects whether a run's results are usable for comparison.

### Reproducibility is non-negotiable

Benchmark results that cannot be reproduced are marketing, not engineering. Every scenario run must be:

- Deterministic given the same scenario definition and monitor configuration
- Seeded so that randomized injection parameters produce the same sequence on replay
- Versioned — scenario definitions, target implementations, and adapter code must all be pinned in the output record

### Adapters absorb service-specific complexity

Service differences — rate limits, API quality, polling requirements, proprietary terminology — are handled inside the adapter. The core harness never branches on which service is under test. A new service is a new adapter, not a change to the harness.

---

## What is out of scope

- Dashboard or UI evaluation of monitoring services
- Pricing comparisons
- Load testing the monitoring services themselves
- Evaluating monitoring alerting channels end-to-end (PagerDuty, Slack integrations) — measure alert creation in the monitor's own data model only
- Real-traffic correlation — controlled scenarios only, never monitoring against real production sites

---

## Decided

- **Language:** Go.
- **Repository layout:** Single repo. Harness, target VM binary, DNS VM binary, and all adapters live together. Go module rooted at the repo root. Three commands under `cmd/`: `cmd/harness`, `cmd/target`, `cmd/dns`.
- **Event log storage:** MySQL. Schema lives in `schema/` as numbered migration files. Local development uses `docker compose up` (MySQL + Adminer). Production uses dedicated MySQL. The measurement engine reads from and writes to the same MySQL instance; derived metrics are written in a separate pass, never in the same transaction as raw event rows.
- **Scenario authoring format:** TOML. Failure params are flattened directly into each `[[failures]]` block (no nested `params` sub-object) to keep the format clean. The `type` field is the discriminator; all other fields in the block are type-specific.
- **Initial services under evaluation:** Jetmon, UptimeRobot, Pingdom, Datadog Synthetics, Better Uptime. These span the full range from simple/free (UptimeRobot) to enterprise (Datadog), and include the only agent-based service in the set (Jetmon), which is required to benchmark reverse-check scenarios.
- **Fleet configuration:** TOML file (`fleet.toml`, not committed). Defines nameserver VMs, target VMs (with their virtual hosts and paths), and domain-level settings. See `fleet.example.toml` for the reference format.
- **MVP proof-of-concept scope:** `http_status` (503) against one target VM, Jetmon adapter as the first implementation. Proves the end-to-end pipeline — provision, inject, retrieve, record — before building out additional scenario types and adapters.
- **Target hosting model:** Always-on dedicated VMs. The harness can provision new VMs when needed, but the normal operating mode is a persistent fleet. Always-on VMs make false-positive detection passive — a monitor alerting on a healthy site is captured automatically.
- **TCP-level failure injection:** TCP proxy layer within the target VM binary. Sits between the network and the HTTP server; no iptables or root required.
- **Control plane isolation:** Dedicated port on each fleet member (target VM and DNS VM), separate from data-plane ports 80/443. SSH tunnel available as a fallback transport but not the primary.
- **DNS infrastructure:** Custom authoritative nameservers operated by the fleet. Multiple nameserver VMs, multiple domains. Enables nameserver-level failure scenarios (`dns_ns_unavailable`) and mixed-state testing across domains.
- **TLS cert strategy (interim):** Let's Encrypt classic (90-day) and shortlived (160-hour) profiles used on a staggered issuance schedule to build a library of certs at varying ages — from newly issued through fully expired. The harness selects the cert whose remaining lifetime best matches the scenario's `days_remaining` target.
- **TLS cert strategy (long-term):** Self-generated certs signed by a fleet CA, with the CA root installed in Jetmon's trust store. Enables precise expiry control and tests classification fidelity in the Jetmon adapter specifically. Other services classify these as "untrusted CA" rather than "expired" — tiered scoring accounts for this.
- **TLS classification scoring:** Tiered. Full credit for correct classification (e.g., "expiring cert" vs. "expired cert"). Partial credit for detecting a TLS problem without correctly classifying it. Zero for missing the failure entirely.
- **Adapter call budgeting:** Per-adapter call limits configured in `fleet.toml` under `[adapters.<service_id>]`. Each adapter declares a default `MaxCallsPerRun` in `Capabilities` as a fallback when no config entry exists. The harness aborts a run and records `resolution_reason = "budget_exceeded"` if any adapter reaches its limit — never silently continues. Zero means unlimited (used for self-hosted services like Jetmon with no API cost).
- **Run output retention:** Full fidelity forever. Raw events, monitor reports, and derived metrics are never deleted or summarized. Preserves the ability to recompute metrics from the log if a calculation is found to be wrong. Storage can be monitored and a time-based deletion policy added later if needed.
