# Testing Guide — uptime-bench POC

This guide gets you from zero to a running end-to-end benchmark test against a local Jetmon instance.

## Prerequisites

- Docker and Docker Compose v2 installed
- `../jetmon` repo checked out alongside this one
- Go 1.22+ (for local builds only; Docker handles compilation)

## 1. First-time Setup

Copy the example env file and optionally change tokens:

```bash
cp .env.example .env
```

The defaults work for local testing. For any environment that matters, regenerate the tokens:

```bash
# Generate a secure control token
openssl rand -hex 32   # → paste into CONTROL_TOKEN in .env

# Generate a secure bridge token
openssl rand -hex 32   # → paste into JETMON_BRIDGE_TOKEN in .env
```

## 2. Start the Full POC Stack

```bash
make dev-poc
```

This builds and starts all services:

| Service | URL | Purpose |
|---------|-----|---------|
| MySQL (uptime-bench) | `localhost:3306` | Benchmark run data |
| MySQL (jetmon) | `localhost:3307` | Jetmon monitoring data |
| Adminer | `http://localhost:8081` | DB UI |
| target-01 | `http://localhost:8080` | HTTP target under test |
| jetmon-bridge | `http://localhost:9200` | Read-only Jetmon API bridge |
| Jetmon dashboard | `http://localhost:8082` | Jetmon operator view |

Wait ~60 seconds for all services to be healthy. You can watch progress with:

```bash
docker compose logs -f jetmon jetmon-bridge
```

Jetmon begins checking `http://bench.local/` and `http://probe.local/health` once it starts.
You should see `"check"` events in the Jetmon audit log after the first round.

## 3. Verify Jetmon is Monitoring

Check that the bridge can find the pre-seeded monitors:

```bash
# Should return bench.local monitor with blog_id 1001
curl -s -H "Authorization: Bearer dev-insecure-bridge-token-replace-before-use" \
  "http://localhost:9200/monitors?url=http://bench.local/" | jq .
```

Expected response:
```json
{
  "blog_id": 1001,
  "monitor_url": "http://bench.local/",
  "monitor_active": true,
  "site_status": 1,
  "check_interval": 1
}
```

Check recent Jetmon audit events:

```bash
SINCE=$(date -u -d '5 minutes ago' '+%Y-%m-%dT%H:%M:%SZ' 2>/dev/null || \
        date -u -v-5M '+%Y-%m-%dT%H:%M:%SZ')   # macOS fallback

curl -s -H "Authorization: Bearer dev-insecure-bridge-token-replace-before-use" \
  "http://localhost:9200/events?blog_id=1001&since=${SINCE}" | jq .
```

You should see `"check"` events with `"http_code": 200`.

## 4. Run a Scenario

### Quick run (harness container with pre-configured fleet):

```bash
make run-scenario SCENARIO=scenarios/http-503.toml
```

Or run any scenario:

```bash
make run-scenario SCENARIO=scenarios/tcp-refused.toml
make run-scenario SCENARIO=scenarios/http-timeout-ttfb.toml
make run-scenario SCENARIO=scenarios/http-partial.toml
```

### Manual run (from host with local binaries):

```bash
# Build first
make build

# Set env
export DB_DSN="uptime_bench:devpassword@tcp(127.0.0.1:3306)/uptime_bench?parseTime=true"
export CONTROL_TOKEN="dev-insecure-token-replace-before-use"
export JETMON_BRIDGE_URL="http://localhost:9200"
export JETMON_BRIDGE_TOKEN="dev-insecure-bridge-token-replace-before-use"

# Run a scenario
./bin/uptime-bench-harness \
  -fleet=docker/fleet.local.toml \
  -scenario=scenarios/http-503.toml
```

### What to expect during a run

The harness logs each phase:

```
harness: fleet loaded (1 targets, 2 nameservers)
harness: scenario loaded: http-503 v1
harness: database connected
harness: 1 adapter(s) loaded
harness: starting scenario: http-503
runner: provisioned jetmon (monitor 1001)
runner: activated http_status on bench-a
runner: failure active for 5m0s
# ... 5 minutes pass, Jetmon detects the failure ...
runner: deactivated http_status
runner: grace period 3m0s
# ... 3 minutes pass ...
runner: retrieved jetmon: status=known reports=2
harness: deriving metrics for run <run-id>
harness: done
```

## 5. View Results

Browse the database using Adminer at `http://localhost:8081`:
- Server: `mysql`, Username: `uptime_bench`, Password: `devpassword`, Database: `uptime_bench`

Key tables:

```sql
-- All runs
SELECT * FROM scenario_runs ORDER BY started_at DESC;

-- Ground truth: when each failure was injected
SELECT * FROM ground_truth_events WHERE run_id = '<run-id>';

-- What Jetmon reported
SELECT * FROM monitor_reports WHERE run_id = '<run-id>';

-- Derived metrics: true positives, false negatives, detection latency
SELECT * FROM derived_metrics WHERE run_id = '<run-id>';
```

## 6. Available v1 Scenarios

| Scenario | Failure | What it tests |
|----------|---------|---------------|
| `http-503.toml` | HTTP 503 status | Basic downtime detection |
| `tcp-refused.toml` | TCP connection refused | Network-layer reachability |
| `http-timeout-ttfb.toml` | 35s TTFB stall | Timeout detection at connection |
| `http-partial.toml` | Response truncated at 100 bytes | Incomplete response detection |
| `content-defacement.toml` | Defacement HTML | Content integrity detection |
| `content-ransomware.toml` | Ransomware page | Malicious content detection |
| `content-keyword-missing.toml` | Keyword removed | Keyword monitoring |
| `content-keyword-injected.toml` | Keyword injected | Keyword monitoring |
| `content-spam-links.toml` | Spam link injection | Content integrity |
| `content-malicious-script.toml` | Malicious JS | Content integrity |

> Note: content scenarios require Jetmon's keyword monitoring feature to detect
> them (`SupportsKeyword: false` for the current adapter). They will record as
> `false_negative` until keyword support is added.

## 7. Troubleshooting

**Harness reports "no monitor pre-seeded for http://bench.local/"**
- The jetmon-mysql init SQL may not have run. Reset the POC stack:
  ```bash
  make dev-poc-reset
  ```

**No status_transition events in Jetmon audit log after injecting failure**
- Wait: Jetmon checks every 30-60 seconds. A 5-minute failure window gives it
  several opportunities to detect.
- Check Jetmon is running: `docker compose logs jetmon`
- Verify bench.local resolves inside the fleet network:
  ```bash
  docker compose exec jetmon nslookup bench.local
  ```

**Build fails for jetmon-bridge**
- Ensure `../jetmon` exists and `go.sum` is present:
  ```bash
  ls ../jetmon/go.sum
  ```

**Adminer shows empty derived_metrics**
- Metrics are only derived if the adapter returns `RetrieveKnown`. Check that
  Jetmon fired at least one status transition during the run window.

## 8. Stopping

```bash
make dev-poc-down        # stop containers, keep volumes (data persists)
make dev-poc-reset       # stop and wipe all volumes (fresh start)
```
