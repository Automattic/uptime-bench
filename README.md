# uptime-bench

A benchmark suite for evaluating uptime monitoring services — by being deliberately and reproducibly mean to a fleet of webservers, then grading the watchers on whether they noticed.

`uptime-bench` runs controlled failure scenarios against target endpoints and measures how each monitoring service detects, classifies, and reports each failure. It can run one targeted scenario at a time or run a randomized campaign that builds a statistically useful comparison set. The output is structured, apples-to-apples data across services that otherwise expose very different dashboards, alerting semantics, and terminology.

## Why

Every monitoring vendor publishes their own uptime numbers. Their detection intervals vary, their definitions of "down" disagree in subtle ways, their behavior under flapping conditions or partial outages is rarely documented, and their dashboards are designed to demo well rather than to support a head-to-head comparison.

So we built a controlled environment where the failures are scripted, the timestamps are ground truth, and the same scenario can run against five services on the same afternoon.

## What it does

- Runs a fleet of controllable target servers that simulate specific failure modes — hard downtime, slow responses, intermittent 5xxs, content tampering, TLS errors, DNS anomalies, and more
- Points monitoring services at those targets and waits for detection
- Records what each service detected, when it detected it, and how it classified it
- Produces structured comparison data: detection latency, accuracy, false-positive rate, and classification fidelity
- Runs deterministic randomized campaigns and reports aggregate per-service results

A handful of the seventeen shipped scenarios involve serving a fully-rendered ransomware demand, a hacktivist defacement, or hidden SEO spam — all with a `200 OK` status. A monitor that only watches status codes is going to have a rough time. The full menu lives in [`SCENARIOS.md`](SCENARIOS.md); the per-scenario file format in [`SCHEMA.md`](SCHEMA.md).

## Services under evaluation

| Service | Type | Adapter |
|---|---|---|
| [Jetmon 1](https://github.com/Automattic/jetmon) | Agent-based (WordPress/Jetpack) | Implemented (`jetmon-v1`, via [jetmon-bridge](https://github.com/Automattic/jetmon-bridge)) |
| [Jetmon 2](https://github.com/Automattic/jetmon) | Agent-based (WordPress/Jetpack) | Implemented (`jetmon-v2`, via Jetmon 2's internal REST API) — live-tested against the internal API |
| [UptimeRobot](https://uptimerobot.com) | Probe-based | Implemented (`uptimerobot`) — live-tested against the public API |
| [Pingdom](https://www.pingdom.com) | Probe-based | Implemented (`pingdom`) — live-tested against the public API |
| [Datadog Synthetics](https://www.datadoghq.com/product/synthetic-monitoring/) | Probe-based | Implemented (`datadog-synthetics`) — live-tested against the public API |
| [Better Uptime](https://betterstack.com/better-uptime) | Probe-based | Implemented (`better-uptime`) — live-tested against the public API |

Adding a new adapter is a small, well-defined exercise — implement the [`adapter.Adapter`](internal/adapter/adapter.go) interface (five methods), drop a normalization table next to it, register the type in `cmd/harness/main.go`. See [`ADAPTER.md`](ADAPTER.md) for the contract.

## Status

The end-to-end pipeline runs: target server, DNS server, control plane, harness, runner, MySQL event log, metric derivation, and reporting. Six service adapters are implemented: Jetmon 1, Jetmon 2, UptimeRobot, Pingdom, Datadog Synthetics, and Better Uptime. Jetmon 2 and the public probe-based adapters have been live-tested against their APIs.

Seventeen shipped scenarios across HTTP, TCP, method-sensitive, and content failures are defined and runnable. TLS scenarios are schema-defined and target-backed: the target has an HTTPS listener, SNI-aware certificate selection, certmint manifest loading for `tls_expired` / `tls_expiring`, self-signed and hostname-mismatch variants for `tls_invalid`, TLS 1.0 / 1.1 clamping for `tls_deprecated`, and deterministic handshake aborts for `tls_handshake`. The `cmd/certmint` daemon mints publicly-trusted Let's Encrypt certificates that feed the cert library, using DNS-01 challenges fanned out to the in-fleet `cmd/dns` members. Remaining TLS work is mostly external probe acceptance against deployed targets and real certmint-produced libraries; see [`ROADMAP.md`](ROADMAP.md).

Campaign mode is implemented for serial execution: the harness accepts `-campaign=<config.toml>`, records a `campaign_runs` audit row, runs scheduled scenario replays, derives campaign metrics, and `uptime-bench-report` summarizes results as table, TSV, or JSON. Reports disclose aggregation scope, emit bias self-checks before the table, include confidence intervals for detection rate and p50/p95 latency, and surface `capability_mismatch` counts separately from misses. Multi-host campaign designs are still deferred because the scenario format is single-target; for now, practical campaigns should use `patterns = ["single"]`.

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
- [`docs/inter-run-state-design.md`](docs/inter-run-state-design.md) — maintenance windows and cooldown reset design
- [`docs/certmint-operator.md`](docs/certmint-operator.md) — operating the certmint daemon
- [`deploy/acme-hooks/README.md`](deploy/acme-hooks/README.md) — certbot manual DNS hook scripts certmint drives

## Local development

Requires Go 1.26+ and Docker Compose v2.

```sh
cp .env.example .env                       # configure local credentials
make dev                                   # start MySQL + Adminer
cp fleet.example.toml fleet.toml           # configure fleet
cp services.example.toml services.toml    # configure monitoring services
make build                                 # build all binaries
```

Adminer (database UI) is available at `http://localhost:8081` after `make dev`.

For an end-to-end local fleet in Docker:

```sh
cp .env.example .env
cp services.example.toml services.toml
make dev-fleet
make run-scenario SCENARIO=scenarios/http-503.toml
```

`make dev-fleet` starts MySQL, Adminer, the target, and the DNS members. The local target is available as HTTP on `localhost:8080` and HTTPS on `localhost:8443` from the host, and as `http://bench.local/` inside the Docker network. `make logs` tails the fleet logs; `make dev-fleet-down` stops the containers while keeping volumes.

Scenario runs require `services.toml` to have at least one enabled service matching the scenario's `monitors` list. Campaign runs use every enabled service in `services.toml`.

For the full local POC including Jetmon and the bridge, see [`TESTING.md`](TESTING.md).

## Campaigns

Single-scenario mode is useful for targeted checks:

```sh
uptime-bench-harness \
  -fleet=fleet.toml \
  -services=services.toml \
  -scenario=scenarios/http-503.toml
```

Campaign mode generates and schedules many deterministic scenario designs from one campaign config:

```sh
uptime-bench-harness \
  -fleet=fleet.toml \
  -services=services.toml \
  -campaign=campaign.toml
```

At campaign end, the harness derives metrics for the whole campaign run. Reports can be generated by run ID or stable campaign config ID:

```sh
make report-campaign CAMPAIGN=<campaign-run-id-or-config-id>
make report-campaign CAMPAIGN=<campaign-run-id-or-config-id> REPORT_FORMAT=json
```

Current campaign scope is serial execution over single-target scenarios. The generator understands `single`, `two_random`, and `all` host patterns, but translation and runner execution currently support only one target per scenario; configure runnable campaigns with `patterns = ["single"]` until multi-host scenario support lands.

## Testing

```sh
go test ./...
go vet ./...
go build ./...
go test -race ./...
```

CI verifies `go.mod` tidiness, `gofmt`, `go build ./...`, `go vet ./...`, `go vet -tags live ./...`, and `go test -race ./...`. The repo currently has hundreds of test functions across the harness, runner, adapters, campaign generator, reporting, DNS server, target server, scenario parser, and cert-library selection code.

Corpus tests assert every shipped scenario parses cleanly and the documented config examples stay loadable. Live build-tagged smoke tests under each adapter (`internal/adapter/<name>/live_test.go`) exercise the Provision/Retrieve/Deprovision contract against real APIs; CI compiles them but does not run them because they require credentials.

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
