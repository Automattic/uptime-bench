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

For a finished scenario report directory, capture the exact run window from
`run.meta.tsv` and write capacity artifacts into the report:

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

The monitoring Prometheus for this work is `prometheus.example.com:9090` on
`monitoring.example.com`; do not use any retired or unrelated Prometheus running on
the network.

Useful readiness checks:

```promql
up{job=~"node|cadvisor|dockerstats|process",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
uptime_bench_dockerstats_scrape_success{job="dockerstats",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
namedprocess_namegroup_num_procs{job="process",instance=~"jetmon-v1.example.com|jetmon-v2.example.com"}
```

All returned series should be `1`.

## Grafana Dashboards

The Grafana instance for this work is `http://grafana.example.com:3000`. The admin
password is stored on `monitoring.example.com` in
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
`deploy/monitoring/jetmon/`. Sync it to `monitoring.example.com` with:

```sh
deploy/monitoring/jetmon/sync-to-host.sh
```

Grafana SQLite backups are scheduled by `jetmon-grafana-backup.timer` on
`monitoring.example.com` and retained under
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
Docker-published `alpine:3.20` container on host port `9103`.

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

The current helper outputs SQL only. The next automation step is a DB executor
that takes per-service DSNs, applies these plans to both Jetmon services, records
the exact UTC activation/deactivation timestamps, and runs the existing
Prometheus capture for each batch window.
