# uptime-bench

A benchmark suite for evaluating uptime monitoring services.

`uptime-bench` runs controlled failure scenarios against target endpoints and measures how each monitoring service detects, classifies, and reports each failure. It produces structured, apples-to-apples comparisons across services that otherwise expose very different dashboards, alerting semantics, and terminology.

## Why

Uptime monitoring services are hard to compare. Vendors publish their own uptime numbers, their detection intervals vary, their definitions of "down" differ, and their behavior under flapping conditions, partial outages, or DNS failures is rarely documented. `uptime-bench` provides a reproducible, instrumented way to evaluate them side by side.

## What it does

- Runs a fleet of controllable target endpoints that simulate specific failure modes: hard downtime, slow responses, intermittent failures, TLS errors, DNS failures, and more
- Points monitoring services at those targets and waits for detection
- Records what each service detected, when it detected it, and how it classified the failure
- Produces structured comparison data: detection latency, accuracy, classification fidelity, and flapping behavior

## Services under evaluation

| Service | Type |
|---|---|
| [Jetmon](https://github.com/Automattic/jetmon) | Agent-based (WordPress/Jetpack) |
| [UptimeRobot](https://uptimerobot.com) | Probe-based |
| [Pingdom](https://www.pingdom.com) | Probe-based |
| [Datadog Synthetics](https://www.datadoghq.com/product/synthetic-monitoring/) | Probe-based |
| [Better Uptime](https://betterstack.com/better-uptime) | Probe-based |

## Status

Early development. Core design is complete — scenario schema, adapter interface, fleet architecture, and database schema are all defined. Implementation of the target server, harness, and first adapter (Jetmon) is in progress.

Not yet usable as a drop-in tool.

## Architecture

See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the full system design. Key documents:

- [`SCENARIOS.md`](SCENARIOS.md) — failure scenario library
- [`SCHEMA.md`](SCHEMA.md) — scenario file format (TOML)
- [`ADAPTER.md`](ADAPTER.md) — monitor adapter interface
- [`EVENTS.md`](EVENTS.md) — ground-truth event log and output schema
- [`ROADMAP.md`](ROADMAP.md) — deferred features

## Local development

Requires: Go 1.22+, Docker

```sh
cp .env.example .env          # configure local credentials
make dev                      # start MySQL + Adminer
cp fleet.example.toml fleet.toml  # configure fleet (edit for your environment)
make build                    # build all binaries
```

Adminer (database UI) is available at `http://localhost:8081` after `make dev`.

## Deployment

Fleet servers run Ubuntu Server 24.04. To provision a new server:

```sh
make provision-target TARGET_HOST=203.0.113.20 HARNESS_IP=203.0.113.5
make deploy-target TARGET_HOST=203.0.113.20
```

See `deploy/` for provisioning scripts and systemd unit files.

## Scope

In scope:
- Detection latency (time from failure start to first alert)
- Detection accuracy (true positives, false positives, missed incidents)
- Incident classification fidelity
- Behavior under ambiguous conditions: slow responses, intermittent failures, DNS anomalies

Out of scope:
- Dashboard or UI evaluation
- Pricing or plan comparisons
- Load testing the monitoring services themselves
- Alerting channel evaluation (PagerDuty, Slack integrations)

## License

GPL v2.0. See [LICENSE](LICENSE) for details.
