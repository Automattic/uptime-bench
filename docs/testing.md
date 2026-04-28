# Testing Guide — uptime-bench

This guide gets you from zero to a running end-to-end benchmark against a monitoring service.

## Prerequisites

- Docker and Docker Compose v2
- Go 1.26+ (for local builds only; Docker handles compilation)
- A running monitoring service with an adapter configured in the harness

## 1. First-time Setup

Copy the example config files:

```bash
cp .env.example .env
cp services.example.toml services.toml
```

Edit `services.toml` and enable the monitoring service(s) you want to test. Set `enabled = true`
and fill in the `url` and `auth` fields for each service. See `services.example.toml` for the
required auth keys for each service type.

Generate secure tokens for any environment that matters:

```bash
openssl rand -hex 32   # → CONTROL_TOKEN
```

## 2. Start the Fleet

```bash
make dev-fleet
```

This starts the bench fleet (target, DNS servers, MySQL, Adminer):

| Service | URL | Purpose |
|---------|-----|---------|
| MySQL | `localhost:3306` | Benchmark run data |
| Adminer | `http://localhost:8081` | DB UI |
| target-01 | `http://localhost:8080` | HTTP target under test |

Wait ~30 seconds for MySQL to be healthy. Watch progress with:

```bash
docker compose logs -f
```

## 3. Configure your Monitoring Service

Point your monitoring service at `http://bench.local/` (and optionally `http://probe.local/health`).
Both names resolve to `target-01` on the fleet Docker network.

For services that require pre-seeded monitors (for example, Jetmon v1 in
read-only bridge mode), set them up now and verify the adapter can reach them
before running a scenario. API-backed adapters such as Jetmon v2 create and
soft-delete synthetic monitors for each run.

## 4. Run a Scenario

```bash
make run-scenario SCENARIO=scenarios/http-503.toml
```

Or run any scenario from the `scenarios/` directory:

```bash
make run-scenario SCENARIO=scenarios/tcp-refused.toml
make run-scenario SCENARIO=scenarios/http-timeout-ttfb.toml
make run-scenario SCENARIO=scenarios/http-partial.toml
```

### What to expect

The harness logs each phase:

```
harness: fleet loaded (1 targets, 2 nameservers)
harness: scenario loaded: http-503 v1
harness: database connected
harness: 1 adapter(s) loaded
harness: starting scenario: http-503
runner: provisioned <service> (monitor <id>)
runner: activated http_status on bench-a
runner: failure active for 5m0s
runner: deactivated http_status
runner: grace period 3m0s
runner: retrieved <service>: status=known reports=2
harness: deriving metrics for run <run-id>
harness: done
```

A full run takes ~8 minutes (5m failure window + 3m grace period).

## 5. View Results

Browse the database using Adminer at `http://localhost:8081`:
- Server: `mysql`, Username: `uptime_bench`, Password: `devpassword`, Database: `uptime_bench`

Key tables:

```sql
-- All runs
SELECT * FROM scenario_runs ORDER BY started_at DESC;

-- Ground truth: when each failure was injected
SELECT * FROM ground_truth_events WHERE run_id = '<run-id>';

-- What the service reported
SELECT * FROM monitor_reports WHERE run_id = '<run-id>';

-- Derived metrics: true positives, false negatives, suppression categories, latency
SELECT * FROM derived_metrics WHERE run_id = '<run-id>';
```

Campaign runs can also be summarized from the command line after the
harness has derived per-replay metrics:

```bash
make report-campaign CAMPAIGN=<campaign-run-id-or-config-id>
make report-campaign CAMPAIGN=<campaign-run-id-or-config-id> REPORT_FORMAT=json
```

The default table report starts with `#` comment lines that disclose the
matched campaign scope and bias self-checks, then prints per-failure/service
statistics. The bias checks cover per-service sample balance,
per-failure/service cell balance with missing cells rendered as zero,
capability mismatches, and uncategorized Unknown rows. TSV output is row-only
for scripts. JSON output wraps the same data as `meta`, `bias_checks`, and
`summaries`.

## 6. Available v1 Scenarios

| Scenario | Failure | What it tests |
|----------|---------|---------------|
| `http-503.toml` | HTTP 503 status | Basic downtime detection |
| `http-geo-503.toml` | HTTP 503 from a region's probe IPs only | Geo-scoped failure detection |
| `http-head-405-get-200.toml` | HEAD 405, GET 200 | False-down detection for HEAD-only checks |
| `http-head-200-get-503.toml` | HEAD 200, GET 503 | False-up detection for HEAD-only checks |
| `tcp-refused.toml` | TCP connection refused | Network-layer reachability |
| `http-timeout-ttfb.toml` | 35s TTFB stall | Timeout detection at connection |
| `http-partial.toml` | Response truncated at 100 bytes | Incomplete response detection |
| `content-defacement.toml` | Defacement HTML | Content integrity detection |
| `content-ransomware.toml` | Ransomware page | Malicious content detection |
| `content-keyword-missing.toml` | Keyword removed | Keyword monitoring |
| `content-keyword-injected.toml` | Keyword injected | Keyword monitoring |
| `content-spam-links.toml` | Spam link injection | Content integrity |
| `content-malicious-script.toml` | Malicious JS | Content integrity |

> Note: content scenarios require the monitoring service to support keyword monitoring
> (`Capabilities.SupportsKeyword`). Services that don't are skipped at provision time
> and recorded as `reason_code = "capability_mismatch"` in `monitor_reports` —
> never counted as false negatives. See [events.md](events.md) for the reporting rules.

## 7. Stopping

```bash
make dev-fleet-down        # stop containers, keep volumes (data persists)
make dev-fleet-reset       # stop and wipe all volumes (fresh start)
```
