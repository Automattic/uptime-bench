# Jetmon Capacity Benchmark

This track measures how Jetmon v1 and Jetmon v2 behave as active monitor
count grows. It is separate from scenario accuracy campaigns: scenario runs
answer whether a monitor detects a controlled failure; capacity runs answer
how resource use, check timeliness, and service health scale with monitor
count.

## Current Scope

The first implemented piece is read-only Prometheus collection:

```sh
make capacity-metrics
```

Defaults:

- Prometheus: `http://10.0.0.67:9091`
- instances: `jetmon-service-host-1,jetmon-service-host-2`
- window: last `15m`
- output: table

Equivalent direct command:

```sh
bin/uptime-bench-capacity \
  -prometheus-url=http://10.0.0.67:9091 \
  -instances=jetmon-service-host-1,jetmon-service-host-2 \
  -duration=15m
```

Use exact timestamps for benchmark windows:

```sh
bin/uptime-bench-capacity \
  -prometheus-url=http://10.0.0.67:9091 \
  -instances=jetmon-service-host-1,jetmon-service-host-2 \
  -start=2026-04-30T18:00:00Z \
  -end=2026-04-30T18:30:00Z \
  -format=json
```

For a finished scenario report directory, capture the exact run window from
`run.meta.tsv` and write capacity artifacts into the report:

```sh
make capacity-capture-run CAPACITY_RUN_DIR=reports/unrun90m-20260430-191911Z
```

This writes `capacity/prometheus-window.{json,txt}` and, by default, a
15-minute post-run baseline. Set `CAPACITY_POSTRUN_DURATION=0` to skip the
post-run capture.

## Required Prometheus Targets

The capacity collector expects these scrape labels:

| Job | Required instances | Purpose |
|---|---|---|
| `node` | `jetmon-service-host-1`, `jetmon-service-host-2` | host CPU, memory, disk, network, scrape health |
| `cadvisor` | `jetmon-service-host-1`, `jetmon-service-host-2` | host/system cgroup metrics and cAdvisor scrape health |
| `dockerstats` | `jetmon-service-host-1`, `jetmon-service-host-2` | Docker container CPU, memory, network, and scrape health |
| `process` | `jetmon-service-host-1`, `jetmon-service-host-2` | native Jetmon process CPU, RSS, counts, threads, and open file descriptors |

The monitoring Prometheus for this work is `10.0.0.67:9091` on
`jetmon-vm-host-3`; do not use any retired or unrelated Prometheus running on
the network.

Useful readiness checks:

```promql
up{job=~"node|cadvisor|dockerstats|process",instance=~"jetmon-service-host-1|jetmon-service-host-2"}
uptime_bench_dockerstats_scrape_success{job="dockerstats",instance=~"jetmon-service-host-1|jetmon-service-host-2"}
namedprocess_namegroup_num_procs{job="process",instance=~"jetmon-service-host-1|jetmon-service-host-2"}
```

All returned series should be `1`.

## Grafana Dashboards

The Grafana instance for this work is `http://10.0.0.67:3001`. The admin
password is stored on `jetmon-vm-host-3` in
`/home/jetmon/jetmon-monitoring/.env`.

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
`deploy/monitoring/jetmon/`. Sync it to `jetmon-vm-host-3` with:

```sh
deploy/monitoring/jetmon/sync-to-host.sh
```

Grafana SQLite backups are scheduled by `jetmon-grafana-backup.timer` on
`jetmon-vm-host-3` and retained under
`/home/jetmon/jetmon-monitoring/backups/grafana/`.

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
    instance: jetmon-service-host-1
  targets:
    - 10.0.0.170:9103
- labels:
    instance: jetmon-service-host-2
  targets:
    - 10.0.0.171:9103
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
deploy/dockerstats-exporter.sh 10.0.0.170 jetmon
deploy/dockerstats-exporter.sh 10.0.0.171 jetmon
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

Before adding generated hosts to Jetmon, stress the target path directly:

```sh
bin/uptime-bench-targetload \
  -url-pattern=http://site-%07d.load.example.com/ \
  -hosts=1000000 \
  -requests=10000 \
  -concurrency=100 \
  -dns-server=<authoritative-dns-ip>:53
```

For HTTP-only target testing that bypasses DNS while preserving the generated
Host header, use `-connect-address=<target-ip>:80`.

### Local Target Capacity Lab

For the current Jetmon lab, use the generated target namespace under
`load.steadycadence.party`:

```toml
[[targets]]
id           = "capacity-a"
address      = "167.99.13.237"
control_port = 9000

  [[targets.generated_sites]]
  id           = "capacity-load"
  host_pattern = "site-%07d.load.steadycadence.party"
  start        = 1
  count        = 1000000
  paths        = ["/"]
```

This only requires updating the fleet DNS config and restarting the fleet DNS
services after the updated DNS binary is deployed. It does not require registrar
changes because `steadycadence.party` already delegates to the fleet DNS hosts.

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

ssh -F ~/.ssh/config jetmon-deploy-test \
  'mkdir -p /tmp/uptime-bench-target-capacity/bin'
scp -F ~/.ssh/config \
  /tmp/uptime-bench-target /tmp/uptime-bench-dns /tmp/uptime-bench-targetload \
  jetmon-deploy-test:/tmp/uptime-bench-target-capacity/bin/
scp -F ~/.ssh/config configs/capacity/targetload.local.toml \
  jetmon-deploy-test:/tmp/uptime-bench-target-capacity/fleet.toml
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
  -connect-address=127.0.0.1:18080
```

Then include the DNS path:

```sh
./bin/uptime-bench-targetload \
  -url-pattern=http://site-%07d.load.localtest.example:18080/ \
  -hosts=100000 \
  -requests=100000 \
  -concurrency=100 \
  -dns-server=127.0.0.1:15353
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
open events, and recent check history:

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

To generate the full seed and activation SQL review set for every configured
batch size, run:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=plan \
  -out-dir=reports/capacity/full-plan
```

Live runs require MySQL DSNs from environment variables or local secret files.
Inline `dsn` values in TOML are intentionally rejected so credentials do not end
up in checked-in configs or dry-run artifacts. If the DB ports are only bound on
the service hosts, open SSH tunnels before running the local orchestrator:

```sh
ssh -F ~/.ssh/config -N -L 13307:127.0.0.1:3307 jetmon-service-host-1
ssh -F ~/.ssh/config -N -L 23307:127.0.0.1:3307 jetmon-service-host-2

export JETMON_V1_DB_DSN='root:...@tcp(127.0.0.1:13307)/jetmon_db?parseTime=true&loc=UTC'
export JETMON_V2_DB_DSN='jetmon:...@tcp(127.0.0.1:23307)/jetmon_db?parseTime=true&loc=UTC'
```

For unattended runs, prefer `dsn_file` entries that point at `0600` local files
outside the repo. The runner rejects secret files that are readable by group or
other users. Use DB users scoped to the benchmark tables instead of broad
administrative accounts where possible.

Seed the inactive benchmark-owned ranges during a maintenance window:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=seed \
  -apply
```

The seed action refuses to delete existing rows unless the reserved range is
empty. If the preflight finds rows and every row in the range already matches
the generated capacity URL namespace, rerun with `-force-reseed` to deliberately
delete and recreate that benchmark-owned range:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=seed \
  -apply \
  -force-reseed
```

Run a small smoke window before increasing batch size:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=run-batch \
  -active-count=10 \
  -duration=5m \
  -apply
```

After the smoke passes, run the configured growth sequence:

```sh
bin/uptime-bench-jetmon-capacity-run \
  -config=configs/capacity/jetmon.example.toml \
  -mode=run-suite \
  -apply
```

The runner writes a `summary.txt` operator summary, a `run.json` machine-readable
manifest, generated SQL files, execution results, exact UTC window timestamps,
and `prometheus-window.json` when Prometheus capture is enabled. The manifest
also includes per-service DB health snapshots, threshold pass/fail/not-measured
entries, suite batch count/runtime estimates, and a `stop_recommended` flag when
a growth suite should stop before the next batch. Applying any mutating
lifecycle action requires the explicit `-apply` flag so planning can continue
safely while another benchmark is active.

During a live batch, the runner verifies active counts before starting the
window, captures a DB health snapshot at the recorded end time, deactivates the
benchmark rows, then captures Prometheus for the exact `[window_start,
window_end]` range. If the process receives SIGINT or SIGTERM during a batch, it
uses a short fresh cleanup context to deactivate rows before returning.
