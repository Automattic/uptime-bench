# uptime-bench

A benchmark suite for evaluating uptime monitoring services — by being deliberately and reproducibly mean to a fleet of webservers, then grading the watchers on whether they noticed.

`uptime-bench` runs controlled failure scenarios against target endpoints and measures how each monitoring service detects, classifies, and reports each failure. It produces structured, apples-to-apples comparisons across services that otherwise expose very different dashboards, alerting semantics, and terminology.

## Why

Every monitoring vendor publishes their own uptime numbers. Their detection intervals vary, their definitions of "down" disagree in subtle ways, their behavior under flapping conditions or partial outages is rarely documented, and their dashboards are designed to demo well rather than to support a head-to-head comparison.

So we built a controlled environment where the failures are scripted, the timestamps are ground truth, and the same scenario can run against five services on the same afternoon.

## What it does

- Runs a fleet of controllable target servers that simulate specific failure modes — hard downtime, slow responses, intermittent 5xxs, content tampering, TLS errors, DNS anomalies, and more
- Points monitoring services at those targets and waits for detection
- Records what each service detected, when it detected it, and how it classified it
- Produces structured comparison data: detection latency, accuracy, false-positive rate, and classification fidelity

A handful of the eleven shipped scenarios involve serving a fully-rendered ransomware demand, a hacktivist defacement, or hidden SEO spam — all with a `200 OK` status. A monitor that only watches status codes is going to have a rough time. The full menu lives in [`SCENARIOS.md`](SCENARIOS.md); the per-scenario file format in [`SCHEMA.md`](SCHEMA.md).

## Services under evaluation

| Service | Type | Adapter |
|---|---|---|
| [Jetmon 1](https://github.com/Automattic/jetmon) | Agent-based (WordPress/Jetpack) | Implemented (`jetmon-v1`, via [jetmon-bridge](https://github.com/Automattic/jetmon-bridge)) |
| [Jetmon 2](https://github.com/Automattic/jetmon) | Agent-based (WordPress/Jetpack) | Stub — blocked on Jetmon 2's public REST API |
| [UptimeRobot](https://uptimerobot.com) | Probe-based | Not yet implemented |
| [Pingdom](https://www.pingdom.com) | Probe-based | Not yet implemented |
| [Datadog Synthetics](https://www.datadoghq.com/product/synthetic-monitoring/) | Probe-based | Not yet implemented |
| [Better Uptime](https://betterstack.com/better-uptime) | Probe-based | Not yet implemented |

Adding a new adapter is a small, well-defined exercise — implement the [`adapter.Adapter`](internal/adapter/adapter.go) interface (five methods), drop a normalization table next to it, register the type in `cmd/harness/main.go`. See [`ADAPTER.md`](ADAPTER.md) for the contract.

## Status

The end-to-end pipeline runs: target server, DNS server, control plane, harness, runner, MySQL event log, and the Jetmon 1 adapter. CI (`go vet`, `gofmt`, `go test -race`) is green on every push. Eleven scenarios across HTTP, TCP, DNS, and content failures are defined and runnable; the TLS scenarios are schema-defined but waiting on an HTTPS listener in the target binary (see [`ROADMAP.md`](ROADMAP.md)).

Notable design choices, all enforced by the code or the tests:

- **The event log is the canonical record.** Detection metrics are computed from the raw log in a separate pass; nothing is ever patched in place.
- **Adapters absorb service-specific complexity.** The harness never branches on which service is under evaluation. Every adapter owns its own raw-label mapping table.
- **`Unknown` is not a missed detection.** When an adapter can't reach its service's API, the result is `RetrieveUnknown` — not a false negative. It's recorded separately and excluded from accuracy metrics.
- **Reproducibility is non-negotiable.** Every randomized injection is seeded, every seed is recorded with the run, and every adapter version is pinned.

## Architecture

[`ARCHITECTURE.md`](ARCHITECTURE.md) is the full design doc. The other docs in this directory each cover one piece:

- [`SCENARIOS.md`](SCENARIOS.md) — the failure scenario library
- [`SCHEMA.md`](SCHEMA.md) — scenario file format (TOML)
- [`ADAPTER.md`](ADAPTER.md) — monitor adapter interface
- [`EVENTS.md`](EVENTS.md) — ground-truth event log and output schema
- [`OPERATIONS.md`](OPERATIONS.md) — fleet provisioning, deployment, and operations
- [`TESTING.md`](TESTING.md) — local POC quick-start
- [`ROADMAP.md`](ROADMAP.md) — deferred features and unfinished work

## Local development

Requires Go 1.26+ and Docker.

```sh
cp .env.example .env                       # configure local credentials
make dev                                   # start MySQL + Adminer
cp fleet.example.toml fleet.toml           # configure fleet
cp services.example.toml services.toml    # configure monitoring services
make build                                 # build all binaries
```

Adminer (database UI) is available at `http://localhost:8081` after `make dev`.

For the full local POC including Jetmon and the bridge, see [`TESTING.md`](TESTING.md).

## Testing

```sh
go test -race ./...
```

Roughly sixty test cases across ten packages, including a corpus check that asserts every shipped scenario file parses cleanly and every documented config example loads without error. The DNS server and control plane are tested as units (the partial-read, latency-parallelism, and timing-attack regression tests are doing real work).

## Deployment

Fleet servers run Ubuntu Server 24.04. Provisioning is split into two phases: setup once, deploy whenever the binary changes.

```sh
make provision-target TARGET_HOST=203.0.113.20 HARNESS_IP=203.0.113.5
make deploy-target    TARGET_HOST=203.0.113.20
```

The provisioning script is idempotent — re-running it after a config or systemd-unit change is the supported upgrade path. Configuration files are auto-created from skeletons with correct ownership; the operator just edits values. See [`OPERATIONS.md`](OPERATIONS.md) for the full fleet bring-up sequence.

## Scope

In scope:
- Detection latency (time from failure start to first alert)
- Detection accuracy (true positives, false positives, missed incidents)
- Incident classification fidelity
- Behavior under ambiguous conditions: slow responses, intermittent failures, DNS anomalies, content tampering with a healthy status code

Out of scope:
- Dashboard or UI evaluation
- Pricing or plan comparisons
- Load-testing the monitoring services themselves
- Alerting channel evaluation (PagerDuty, Slack integrations)

If you find `uptime-bench-canary` somewhere it shouldn't be, you've discovered our marker string in a healthy response body. It exists so content-inspecting monitors have something to anchor on, and it's how we tell a tampered page apart from a normal one in the test fleet.

## License

GPL v2.0. See [LICENSE](LICENSE) for details.
