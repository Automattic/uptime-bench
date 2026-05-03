# uptime-bench

**A controlled obstacle course for uptime monitors.**

Monitoring services are great at telling you when something is down. They are less great at proving, side by side, how they behave when the failure is weird: a `HEAD` request lies, DNS is slow, TLS is broken, the page is defaced but still returns `200 OK`, or one region sees a problem while another does not.

`uptime-bench` creates those situations on purpose. It runs scripted failures against a dedicated test fleet, points monitoring services at that fleet, records the ground truth, and turns the results into comparable data.

It is not a dashboard benchmark. It is not a pricing comparison. It is a measurement rig for one question:

**When the site fails in a specific way, who notices, how fast, and how accurately?**

The core benchmark story stays simple:

```text
scripted failure -> real monitor probes -> normalized evidence -> comparison reports
```

## Why This Matters

Uptime vendors all publish confidence. They do not publish the same definitions.

One service may probe with `HEAD`, another with `GET`. One may classify TLS trouble precisely, another may call everything "down." One may support content integrity checks, another may only watch status codes. When you compare their dashboards directly, you are often comparing different tests.

| Audience | What Gets Better |
|---|---|
| Monitoring evaluators | A controlled benchmark instead of dashboard-by-dashboard guesswork. |
| SRE and operations teams | Ground-truth failure windows, detection latency, false positives, and classification fidelity in one comparable event model. |
| Product and platform teams | Evidence for which monitor capabilities matter: method behavior, content integrity, DNS, TLS, maintenance, cooldown, and geo-scoped checks. |
| Adapter contributors | A small service boundary: declare capabilities, provision a monitor, retrieve reports, and normalize vendor vocabulary. |
| Benchmark readers | Results that separate misses from unsupported, unknown, maintenance-suppressed, and cooldown-suppressed cases. |

The final comparison is about monitor behavior, not about guesswork: the same target site, failure window, ground-truth timestamps, scenario definition, and preserved vendor output.

## What It Can Throw At Monitors

The scenario library covers the failure modes that make uptime monitoring interesting:

- plain HTTP outages like `503`
- slow time-to-first-byte and partial responses
- TCP reachability failures
- DNS `NXDOMAIN`, `SERVFAIL`, latency, and nameserver outages
- method-sensitive traps where `HEAD` and `GET` disagree
- content failures that still return `200 OK`
- TLS certificate, protocol, and handshake failures
- maintenance-window and cooldown edge cases
- geo-scoped failures using probe IP ranges

Some scenarios are deliberately unfair to shallow checks. A page can show a defacement, hidden spam links, or a ransomware demand while the HTTP status is perfectly healthy. That is the point.

## How The System Works

```text
scenario
   |
   v
harness  ->  target and DNS fleet  ->  controlled failure
   |                 |
   |                 v
   |           ground-truth events
   |
   v
monitor adapters  ->  Jetmon, Pingdom, UptimeRobot, Datadog, Better Uptime, Gatus, Uptime Kuma
   |
   v
monitor reports  ->  derived metrics  ->  campaign reports
```

The fleet is made of real servers running small Go binaries:

- target servers host realistic websites and inject HTTP, TCP, TLS, and content failures
- DNS servers act as authoritative nameservers and inject DNS failures
- certmint builds the certificate library used for TLS-age scenarios
- the harness orchestrates scenarios and writes every raw event to MySQL
- adapters translate each monitoring service into the same benchmark contract

The important rule: **the harness does not special-case services.** Service quirks live in adapters. The comparison layer works from normalized events.

## Services In Scope

| Service | Adapter | Notes |
|---|---|---|
| Jetmon 1 | `jetmon-v1` | Via `jetmon-bridge` |
| Jetmon 2 | `jetmon-v2` | Via Jetmon v2 REST API |
| UptimeRobot | `uptimerobot` | Probe-based public API |
| Pingdom | `pingdom` | Probe-based public API |
| Datadog Synthetics | `datadog-synthetics` | Probe-based public API |
| Better Uptime | `better-uptime` | Probe-based public API |
| Gatus | `gatus` | Self-hosted single-origin monitor via uptime-bench bridge |
| Uptime Kuma | `uptime-kuma` | Self-hosted single-origin monitor via uptime-bench bridge |

New services are added by implementing the adapter interface, declaring capabilities, and mapping vendor event vocabulary into uptime-bench's normalized model.

## What The Results Mean

The benchmark records raw facts first, then computes metrics later.

That keeps the data honest:

- **True positive:** the monitor detected the injected failure.
- **False negative:** the monitor missed a failure it was capable of detecting.
- **False positive:** the monitor reported trouble when the fleet was healthy.
- **Capability mismatch:** the monitor was never asked to do something it cannot support.
- **Unknown:** uptime-bench could not retrieve reliable monitor data.
- **Suppressed:** maintenance or cooldown behavior intentionally muted an alert.

Unknown, unsupported, and intentionally suppressed cases are not counted as misses. They are part of the support matrix.

## Try It Locally

For a local fleet:

```sh
cp .env.example .env
cp services.example.toml services.toml
make dev-fleet
make run-scenario SCENARIO=scenarios/http-503.toml
```

For a long-running comparison campaign:

```sh
uptime-bench-harness \
  -fleet=fleet.toml \
  -services=services.toml \
  -campaign=configs/campaign/example.toml
```

The local quick start is useful for proving the loop. Real benchmark data comes from a deployed fleet with real domains, DNS, TLS, and monitor credentials.

## Documentation

| Document | Start Here For |
|---|---|
| [docs/README.md](docs/README.md) | Complete map of project docs |
| [docs/architecture.md](docs/architecture.md) | System shape and design principles |
| [docs/fleet-overview.md](docs/fleet-overview.md) | Each deployed component and how they communicate |
| [docs/scenarios.md](docs/scenarios.md) | Failure library and scenario families |
| [docs/scenario-format.md](docs/scenario-format.md) | TOML fields and scenario examples |
| [docs/adapters.md](docs/adapters.md) | How monitoring services plug in |
| [docs/events.md](docs/events.md) | Output model and scoring rules |
| [docs/reporting.md](docs/reporting.md) | Required post-run report bundle and report contents |
| [docs/testing.md](docs/testing.md) | Local end-to-end setup |
| [docs/operations.md](docs/operations.md) | Deployed fleet provisioning and smoke tests |
| [docs/post-run-analysis.md](docs/post-run-analysis.md) | Checklist for analyzing completed benchmark artifacts |
| [docs/roadmap.md](docs/roadmap.md) | Completed work, active priorities, and deferred ideas |

## Status

The core system is running end to end: harness, target fleet, DNS, certmint, adapters, campaign generation, metric derivation, and reporting.

The active work is focused on monitor-facing validation against deployed services, especially Jetmon v2 scenario smoke, TLS behavior through real probes, maintenance-window behavior, and small campaign dry runs.

## License

GPL v2.0. See [LICENSE](LICENSE) for details.
