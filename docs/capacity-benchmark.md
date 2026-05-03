# Jetmon Capacity Benchmark

This track measures how Jetmon v1 and Jetmon v2 behave as active monitor
count grows. It is separate from scenario accuracy campaigns: scenario runs
answer whether a monitor detects a controlled failure; capacity runs answer
how resource use, check timeliness, and service health scale with monitor
count.

## Current Scope

Prometheus collection can be used independently:

```sh
make capacity-metrics
```

Defaults:

- Prometheus: `http://prometheus.example.com:9090`
- instances: `jetmon-v1.example.com,jetmon-v2.example.com`
- window: last `15m`
- output: table

Equivalent direct command:

```sh
bin/uptime-bench-capacity \
  -prometheus-url=http://prometheus.example.com:9090 \
  -instances=jetmon-v1.example.com,jetmon-v2.example.com \
  -duration=15m
```

Use exact timestamps for benchmark windows:

```sh
bin/uptime-bench-capacity \
  -prometheus-url=http://prometheus.example.com:9090 \
  -instances=jetmon-v1.example.com,jetmon-v2.example.com \
  -start=2026-04-30T18:00:00Z \
  -end=2026-04-30T18:30:00Z \
  -format=json
```

For a finished scenario campaign, finalization can capture the exact campaign
window and write capacity artifacts beside the normal scenario report:

```sh
make finalize-campaign \
  CAMPAIGN=example-campaign-id \
  REPORT_OUT_DIR=reports/example-run-20260430-191911Z \
  FINALIZE_CAPACITY=true \
  CAPACITY_PROMETHEUS_URL=http://prometheus.example.com:9090 \
  CAPACITY_INSTANCES=jetmon-v1.example.com,jetmon-v2.example.com
```

This writes `report.md`, `report.json`, `capacity.md`, `capacity.json`,
`capacity.txt`, raw database TSV exports, deterministic campaign plan/schedule
TSVs, campaign config snapshots, and a manifest that lists all generated files.
Capacity finalization requires the campaign to be complete because it uses the
campaign's earliest start and latest end timestamps as the Prometheus range
window.

See [Reporting Standard](reporting.md) for the complete post-run report bundle
that should be produced after every campaign, including raw TSV exports, logs,
scenario definitions, cleanup verification, and capacity artifacts.

For older report directories that contain `run.meta.tsv`, capture the exact
run window into a nested capacity directory:

```sh
make capacity-capture-run CAPACITY_RUN_DIR=reports/example-run-20260430-191911Z
```

This writes `capacity/prometheus-window.{json,txt}` and, by default, a
15-minute post-run baseline. Set `CAPACITY_POSTRUN_DURATION=0` to skip the
post-run capture.

## Required Prometheus Targets

The capacity collector expects these scrape labels:

| Job | Required instances | Purpose |
|---|---|---|
| `node` | `jetmon-v1.example.com`, `jetmon-v2.example.com` | host CPU, memory, disk, network, scrape health |
| `cadvisor` | `jetmon-v1.example.com`, `jetmon-v2.example.com` | host/system cgroup metrics and cAdvisor scrape health |
| `dockerstats` | `jetmon-v1.example.com`, `jetmon-v2.example.com` | Docker container CPU, memory, network, and scrape health |
| `process` | `jetmon-v1.example.com`, `jetmon-v2.example.com` | native Jetmon process CPU, RSS, counts, threads, and open file descriptors |

The examples below use `http://prometheus.example.com:9090` on
`monitoring.example.com` as placeholders. Replace them with the Prometheus that
scrapes the Jetmon hosts under test, and do not mix data from an unrelated
monitoring stack.

Useful readiness checks:

```promql
up{job=~"node|cadvisor|dockerstats|process",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
uptime_bench_dockerstats_scrape_success{job="dockerstats",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
namedprocess_namegroup_num_procs{job="process",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
```

All returned series should be `1`.

## Grafana Dashboards

The example Grafana instance is `http://grafana.example.com:3000`. In a live
environment, keep the admin password on the monitoring host outside the repo,
for example in `/opt/jetmon-monitoring/.env`.

Provisioned dashboards:

- `Jetmon Fleet Overview` for fleet health, host CPU/memory, container
  CPU/memory, native process CPU/RSS, and scrape status.
- `Jetmon Host Detail` for per-host CPU, load, memory, filesystem, network,
  disk, scrape health, and native process CPU/RSS/count/open FDs.
- `Jetmon Container Detail` for per-host container CPU, memory, network,
  dockerstats scrape health, and inventory.

The dashboards are managed in the repository at
`deploy/monitoring/jetmon/configs/grafana/dashboards/`. They include top-level
links between fleet overview, host detail, and container detail while preserving
the selected time range and variables.

The monitoring stack itself is managed in
`deploy/monitoring/jetmon/`. Sync it to `monitoring.example.com` with:

```sh
deploy/monitoring/jetmon/sync-to-host.sh
```

Grafana SQLite backups are scheduled by `jetmon-grafana-backup.timer` on
`monitoring.example.com` and retained under
`/opt/jetmon-monitoring/backups/grafana/`.

The Jetmon hosts currently run Docker 29 with the `overlayfs` containerd
snapshotter layout. cAdvisor can be scraped, but it cannot identify the Docker
container writable layer on that layout and may only expose host/root cgroups.
The `uptime-bench-dockerstats-exporter` command is the Docker API fallback for
per-container metrics without changing Jetmon application code.

Example Prometheus file discovery entry for the Docker stats exporter:

```yaml
- job_name: dockerstats
  file_sd_configs:
    - files:
        - /etc/prometheus/targets.d/dockerstats_*.yml
```

Example `targets.d/dockerstats_jetmon.yml`:

```yaml
- labels:
    instance: jetmon-v1.example.com
  targets:
    - 203.0.113.170:9103
- labels:
    instance: jetmon-v2.example.com
  targets:
    - 203.0.113.171:9103
```

The same example is tracked in
`configs/capacity/dockerstats-targets.example.yml`.

## Metrics Collected

The first-pass collector summarizes each returned time series with sample
count, average, p50, p95, max, and last value.

| Metric | Unit | Source |
|---|---|---|
| `host_cpu_used` | percent | `node_cpu_seconds_total` |
| `host_memory_used` | percent | `node_memory_MemAvailable_bytes` / `node_memory_MemTotal_bytes` |
| `host_root_disk_used` | percent | root `node_filesystem_*` |
| `host_net_rx` | bytes/sec | `node_network_receive_bytes_total` |
| `host_net_tx` | bytes/sec | `node_network_transmit_bytes_total` |
| `container_cpu_used` | percent of one core | `container_cpu_usage_seconds_total` |
| `container_memory_working_set` | bytes | `container_memory_working_set_bytes` |
| `docker_container_cpu_used` | percent of one core | `uptime_bench_docker_container_cpu_percent` |
| `docker_container_cpu_rate` | percent of one core | `uptime_bench_docker_container_cpu_usage_seconds_total` |
| `docker_container_memory_working_set` | bytes | `uptime_bench_docker_container_memory_working_set_bytes` |
| `docker_container_net_rx` | bytes/sec | `uptime_bench_docker_container_network_receive_bytes_total` |
| `docker_container_net_tx` | bytes/sec | `uptime_bench_docker_container_network_transmit_bytes_total` |
| `dockerstats_scrape_success` | state | `uptime_bench_dockerstats_scrape_success` |
| `process_cpu_used` | percent of one core | `namedprocess_namegroup_cpu_seconds_total` |
| `process_memory_resident` | bytes | `namedprocess_namegroup_memory_bytes{memtype="resident"}` |
| `process_count` | count | `namedprocess_namegroup_num_procs` |
| `process_threads` | count | `namedprocess_namegroup_num_threads` |
| `process_open_fds` | count | `namedprocess_namegroup_open_filedesc` |
| `scrape_up` | state | `up` |

Deploy the Docker stats exporter to a Jetmon host with:

```sh
deploy/dockerstats-exporter.sh 203.0.113.170 jetmon
deploy/dockerstats-exporter.sh 203.0.113.171 jetmon
```

The deploy helper installs the binary at
`/usr/local/bin/uptime-bench-dockerstats-exporter` and runs it in a
Docker-published `alpine:3.20` container on host port `9103`. It defaults to a
256 MiB memory limit; override with `MEMORY_LIMIT=512m` if a host has enough
containers for Docker stats collection to need more headroom.

The binary can also run directly if host firewall rules expose the port:

```sh
uptime-bench-dockerstats-exporter -listen=:9103 -docker-socket=/var/run/docker.sock
```

## Test Shape

The intended capacity sequence is:

1. Record a no-benchmark baseline window.
2. Reset both Jetmon systems to a clean benchmark-owned site set.
3. Activate the same batch of synthetic URLs in both services.
4. Record exact UTC start and end timestamps.
5. Collect Prometheus summaries for that window.
6. Record Jetmon health signals: missed checks, lag, API errors, service errors,
   and active monitor counts.
7. Remove or deactivate benchmark sites.
8. Increase the batch size until stop thresholds are reached.

Initial batch sizes live in `configs/capacity/jetmon.example.toml`.

## Synthetic Target Direction

Capacity tests should use controlled target domains instead of real internet
sites. `fleet.toml` supports generated host ranges so names like
`site-0000001.load.example.com` through `site-1000000.load.example.com` can
resolve without a million-line file:

```toml
[[targets]]
id           = "target-01"
address      = "203.0.113.20"
control_port = 9000

  [[targets.generated_sites]]
  id           = "load"
  host_pattern = "site-%07d.load.example.com"
  start        = 1
  count        = 1000000
  paths        = ["/"]
```

The DNS server resolves generated hosts directly from the pattern and range.
The HTTP target already generates healthy content from arbitrary Host and path
values, so it does not need a matching million-entry site list.

The Jetmon capacity config must use the same generated namespace. The
`[targets] host_pattern` is the DNS-side hostname pattern, and `url_pattern`
must render to that same host for the first, middle, and last generated IDs:

```toml
[targets]
domain = "load.example.com"
host_pattern = "site-%07d.load.example.com"
url_pattern = "http://site-%07d.load.example.com/"
count = 1000000
url_start = 1
```

Live capacity runs refuse a mismatch such as
`host_pattern = "site-%07d.example.com"` with
`url_pattern = "http://site-%07d.load.example.com/"`. This catches the class of
failure where Jetmon is seeded with URLs that the generated DNS fleet is not
serving.

Before adding generated hosts to Jetmon, stress the target path directly:

```sh
bin/uptime-bench-targetload \
  -url-pattern=http://site-%07d.load.example.com/ \
  -hosts=1000000 \
  -requests=10000 \
  -concurrency=100 \
  -dns-server=<authoritative-dns-ip>:53 \
  -format=markdown
```

For HTTP-only target testing that bypasses DNS while preserving the generated
Host header, use `-connect-address=<target-ip>:80`. The `markdown` format is
intended to be saved beside capacity run artifacts so HTTP-only and DNS-path
target capacity checks can be compared before monitor-side million-site runs.

### Local Target Capacity Lab

For a delegated capacity lab, use a generated target namespace under a placeholder such as
`load.example.com`:

```toml
[[targets]]
id           = "capacity-a"
address      = "203.0.113.20"
control_port = 9000

  [[targets.generated_sites]]
  id           = "capacity-load"
  host_pattern = "site-%07d.load.example.com"
  start        = 1
  count        = 1000000
  paths        = ["/"]
```

This requires updating the private fleet DNS config and restarting the fleet DNS
services after the updated DNS binary is deployed. In a real run, use a test
domain that delegates to the fleet DNS hosts; `example.com` is only a committed
placeholder.

`configs/capacity/targetload.local.toml` defines a one-host lab for
target-capacity checks. It serves one million generated hosts under
`load.localtest.example` without requiring delegation from a real domain:

- target HTTP: `:18080`
- target control: `:19000`
- DNS: `:15353` UDP/TCP
- DNS control: `:19100`

Build and copy the binaries to the lab host, then run the target and DNS using
the local fleet file:

```sh
go build -o /tmp/uptime-bench-target ./cmd/target
go build -o /tmp/uptime-bench-dns ./cmd/dns
go build -o /tmp/uptime-bench-targetload ./cmd/uptime-bench-targetload

ssh -F ~/.ssh/config target-lab.example.com \
  'mkdir -p /tmp/uptime-bench-target-capacity/bin'
scp -F ~/.ssh/config \
  /tmp/uptime-bench-target /tmp/uptime-bench-dns /tmp/uptime-bench-targetload \
  target-lab.example.com:/tmp/uptime-bench-target-capacity/bin/
scp -F ~/.ssh/config configs/capacity/targetload.local.toml \
  target-lab.example.com:/tmp/uptime-bench-target-capacity/fleet.toml
```

On the lab host, run the target and DNS commands in separate terminals or under
a service runner:

```sh
cd /tmp/uptime-bench-target-capacity
test -s control-token || openssl rand -hex 32 > control-token

./bin/uptime-bench-target \
  -id=target-local \
  -http-port=18080 \
  -https-port=0 \
  -control-port=19000 \
  -token-file=/tmp/uptime-bench-target-capacity/control-token

./bin/uptime-bench-dns \
  -id=ns-local \
  -dns-port=15353 \
  -control-port=19100 \
  -fleet=/tmp/uptime-bench-target-capacity/fleet.toml \
  -token-file=/tmp/uptime-bench-target-capacity/control-token
```

Run HTTP-only checks first to isolate target capacity from DNS behavior:

```sh
./bin/uptime-bench-targetload \
  -url-pattern=http://site-%07d.load.localtest.example/ \
  -hosts=100000 \
  -requests=100000 \
  -concurrency=500 \
  -connect-address=127.0.0.1:18080 \
  -format=markdown
```

Then include the DNS path:

```sh
./bin/uptime-bench-targetload \
  -url-pattern=http://site-%07d.load.localtest.example:18080/ \
  -hosts=100000 \
  -requests=100000 \
  -concurrency=100 \
  -dns-server=127.0.0.1:15353 \
  -format=markdown
```

## Bulk Lifecycle Direction

Monitor lifecycle throughput and steady-state monitor capacity should be
measured separately.

For steady-state capacity, avoid creating or deleting a million monitors via
one HTTP request per monitor. Prefer a benchmark-owned namespace:

- reserved synthetic `blog_id` range;
- reserved bucket or ownership label;
- pre-seeded inactive rows when possible;
- bulk activate/deactivate for the first `N` rows;
- reset status, cooldown, timestamps, and active events at batch boundaries.

The `uptime-bench-jetmon-capacity` helper emits guarded SQL for that lifecycle.
It does not need Jetmon v1 or v2 code changes, and it keeps lifecycle throughput
separate from the steady-state capacity measurement.

Build it with:

```sh
make bin/uptime-bench-jetmon-capacity
```

Create the inactive benchmark-owned row set for Jetmon v2:

```sh
bin/uptime-bench-jetmon-capacity \
  -action=seed \
  -schema=v2 \
  -blog-id-start=8000001000000000 \
  -count=1000000 \
  -url-pattern='http://site-%07d.load.example.com/' \
  -bucket-min=0 \
  -bucket-max=99 \
  -check-interval=1 \
  > /tmp/jetmon-v2-capacity-seed.sql
```

Create a same-shaped seed file for Jetmon v1 with `-schema=v1` and the v1
reserved `blog_id` range. Review the SQL before applying it. The seed plan
deletes and recreates rows only inside the reserved range; for v2 it first
closes any still-open benchmark events with a transition row.

Activate one batch for a test window:

```sh
bin/uptime-bench-jetmon-capacity \
  -action=activate \
  -schema=v2 \
  -blog-id-start=8000001000000000 \
  -count=1000000 \
  -active-count=10000 \
  > /tmp/jetmon-v2-capacity-activate-10000.sql
```

Deactivate the whole benchmark-owned range after the window:

```sh
bin/uptime-bench-jetmon-capacity \
  -action=deactivate \
  -schema=v2 \
  -blog-id-start=8000001000000000 \
  -count=1000000 \
  > /tmp/jetmon-v2-capacity-deactivate.sql
```

Generate verification queries for counts, bucket distribution, stale checks,
open events, recent check history, freshness lag, and stale buckets:

```sh
bin/uptime-bench-jetmon-capacity \
  -action=verify \
  -schema=v2 \
  -blog-id-start=8000001000000000 \
  -count=1000000
```

The SQL-only helper remains useful for review, but the guarded runner can now
generate artifacts and, only with `-apply`, execute the lifecycle against both
Jetmon DBs.

First create a dry-run smoke artifact set. This does not connect to either
Jetmon DB:

```sh
make capacity-jetmon-run
```

To generate the full seed and activation SQL review set for every example batch
size, run:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=plan \
  -out-dir=reports/capacity/full-plan
```

Live runs require MySQL DSNs from local secret files. Inline `dsn` values in
TOML are intentionally rejected so credentials do not end up in checked-in
configs or dry-run artifacts. Copy
`configs/capacity/jetmon.fleet.example.toml` to a private
`configs/capacity/jetmon.fleet.toml` for live fleet runs; the committed example
configs are rejected by live `-apply` runs because they contain placeholder
Prometheus labels.

The recommended setup is to run long capacity suites from `monitoring.example.com`
using DSN files and systemd-managed SSH tunnels. This keeps MySQL bound to the
service hosts while still giving the runner stable local TCP endpoints:

```sh
ORCHESTRATOR=monitoring.example.com \
ORCHESTRATOR_ADDR=203.0.113.67 \
SERVICE_V1_HOST=jetmon-v1.example.com \
SERVICE_V1_ADDR=203.0.113.170 \
SERVICE_V2_HOST=jetmon-v2.example.com \
SERVICE_V2_ADDR=203.0.113.171 \
CAPACITY_CONFIG=configs/capacity/jetmon.fleet.toml \
deploy/capacity-db-access.sh
```

The helper:

- creates or reuses a private tunnel key under the capacity install root on
  `monitoring.example.com`;
- authorizes that key on the service hosts for the systemd-managed `ssh -N`
  tunnel services;
- installs an sshd drop-in on each service host that allows local TCP forwarding
  for `jetmon` connections from `monitoring.example.com` to `127.0.0.1:3307`;
- creates or rotates an `uptime_bench_capacity` MySQL user on each Jetmon DB;
- grants only `SELECT`, `INSERT`, `UPDATE`, `DELETE`, and
  `CREATE TEMPORARY TABLES` on each Jetmon database for localhost and Docker
  bridge gateway sources;
- writes `0600` DSN files under
  `/run/secrets/`;
- installs `uptime-bench-capacity-tunnel-v1.service` and
  `uptime-bench-capacity-tunnel-v2.service`;
- installs the capacity-runner binary and fleet config under
  `/opt/uptime-bench-capacity/`.

The example fleet config expects these secret paths:

```text
/run/secrets/jetmon-v1-capacity.dsn
/run/secrets/jetmon-v2-capacity.dsn
```

The runner rejects secret files that are readable by group or other users.
Long-running `-apply` commands should be started from `monitoring.example.com`:

```sh
ssh -F ~/.ssh/config monitoring.example.com
cd /opt/uptime-bench-capacity
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=verify \
  -apply
```

Seed the inactive benchmark-owned ranges during a maintenance window:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=seed \
  -apply
```

The seed action refuses to delete existing rows unless the reserved range is
empty. If the preflight finds rows and every row in the range already matches
the generated capacity URL namespace, rerun with `-force-reseed` to deliberately
delete and recreate that benchmark-owned range:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=seed \
  -apply \
  -force-reseed
```

Run a small smoke window before increasing batch size:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=run-batch \
  -active-count=10 \
  -duration=5m \
  -apply
```

After the smoke passes, run the configured growth sequence:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=run-suite \
  -apply
```

By default, `run-suite` resumes from the last successfully completed batch for
the same capacity plan. The runner writes a suite-state file beside the suite
output directory after each successful live batch, then the next `run-suite`
starts at that recorded batch size instead of repeating every lower control
batch. This keeps repeated tuning runs from spending another full window on
already-clean low-load batches while still rechecking the last known load point.

Use a full pass when you need a clean end-to-end baseline from the first batch:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=run-suite \
  -full-suite \
  -apply
```

For quick scout passes, override the suite shape without editing the TOML. The
batch list must be strictly increasing so resume behavior remains predictable:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/jetmon.fleet.toml \
  -mode=run-suite \
  -batch-sizes=1000,10000,100000,500000,1000000 \
  -duration=10m \
  -cooldown=2m \
  -apply
```

The common Jetmon v2 scalability scout is the 1k/5k/10k ladder. It is short
enough to run before a longer overnight suite but large enough to catch the
usual scheduler, MySQL, DNS, and target-capacity regressions:

```sh
make capacity-jetmon-scout \
  JETMON_CAPACITY_SCOUT_ARGS="-config=configs/jetmon.fleet.toml -apply"
```

By default this preset runs:

```sh
./bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.fleet.example.toml \
  -mode=run-suite \
  -batch-sizes=1000,5000,10000 \
  -duration=10m \
  -cooldown=2m
```

Override `JETMON_CAPACITY_SCOUT_ARGS` when the live config path, batch sizes,
window length, or `-apply` posture differs from the default.

Use `-suite-start-count=N` to start at the first configured or overridden batch
that is at least `N`. Use `-suite-state-path=PATH` when multiple labs share the
same reports parent and need separate resume state.

The runner writes a `summary.txt` operator summary, a `run.json` machine-readable
manifest, generated SQL files, execution results, target preflight samples,
exact UTC window timestamps, and `prometheus-window.json` when Prometheus capture is enabled. For
`run-suite`, the suite directory also gets `capacity.md` and `capacity.json`.
Those files roll up each batch's pass/fail state, DB health, thresholds,
target preflight status, Prometheus highlights, last clean batch, and first
problem batch while preserving the per-batch Prometheus summaries in JSON. The manifest also includes
lifecycle, Prometheus, health, and cleanup statuses; per-service DB health
snapshots; freshness lag details; threshold pass/fail/not-measured entries;
suite batch count/runtime estimates; and a `stop_recommended` flag when a growth
suite should stop before the next batch. Applying any mutating lifecycle action
requires the explicit `-apply` flag so planning can continue safely while
another benchmark is active.

During a live batch, the runner preflights Prometheus, activates the benchmark
rows, verifies active counts, samples the exact activated `monitor_url` values
from each Jetmon database, checks those URLs against the configured target
pattern, and performs DNS/HTTP GET checks from the runner host before starting
the timed window. If this target preflight fails, the runner deactivates the
benchmark rows and refuses to start the clock. After a passing preflight, it
captures a DB health snapshot at the recorded end time, deactivates the
benchmark rows, then captures Prometheus for the exact `[window_start,
window_end]` range. A Prometheus capture failure is recorded as
`prometheus_status=fail`, but DB health and cleanup still run so missed-check
thresholds are not hidden by monitoring failures. If the process receives
SIGINT or SIGTERM during a batch, it uses a short fresh cleanup context to
deactivate rows before returning.

`uptime-bench-jetmon-capacity-run -apply` also creates a local active-run lock
for the command duration. The default path is
`/tmp/uptime-bench-active-run.lock`, or set `UPTIME_BENCH_ACTIVE_RUN_LOCK` /
`-active-run-lock` when the orchestrator uses a different lock location. Other
mutating tools should refuse to run while this lock exists; `-allow-active-run`
is reserved for confirmed emergency cleanup or stale-lock recovery.
