# Jetmon Monitoring Stack

This stack is the repo-managed source for the Grafana and Prometheus setup on
`monitoring.example.com`.

## Access

- Grafana: `http://monitoring.example.com:3001`
- Prometheus: `http://monitoring.example.com:9091`
- Grafana admin user: `admin`
- Grafana admin password: stored on the host in `/opt/jetmon-monitoring/.env`

## Deploy

From this repository:

```sh
deploy/monitoring/jetmon/sync-to-host.sh
```

The sync keeps `/opt/jetmon-monitoring/.env`, `dbs/`, and `backups/`
on the host. It only updates compose/config/provisioning files and then runs:

```sh
sudo docker compose -p jetmon-monitoring up -d
sudo docker compose -p jetmon-monitoring up -d --force-recreate prometheus
sudo docker compose -p jetmon-monitoring restart grafana
```

The Prometheus recreate is intentional because `prometheus.yml` is mounted as a
single file; replacing it during sync changes the host-side inode.

## Compose

On `monitoring.example.com`:

```sh
cd /opt/jetmon-monitoring
sudo docker compose -p jetmon-monitoring ps
sudo docker compose -p jetmon-monitoring logs -f
sudo docker compose -p jetmon-monitoring up -d
```

## Scraped Targets

- `node`: all Jetmon service hosts plus `jetmon-lab-1.example.com`, `jetmon-lab-2.example.com`, and `monitoring.example.com`
- `cadvisor`: all Docker-capable Jetmon service/VM hosts except `jetmon-lab-1.example.com`
- `dockerstats`: all Docker-capable Jetmon service/VM hosts except `jetmon-lab-1.example.com`
- `process`: native Jetmon service processes on `jetmon-v1.example.com` and `jetmon-v2.example.com`
- `smartctl`: `monitoring.example.com`
- `prometheus` and `grafana`: this monitoring stack

Target files live in `configs/prometheus/targets.d/`.

## Native Process Metrics

The process-exporter config tracks these process groups:

- Jetmon v1: `jetmon-v1-master`, `jetmon-v1-worker`, `jetmon-v1-server`, `jetmon-bridge`
- Jetmon v2: `jetmon2`
- Shared/support: `veriflier`, `jetmon-testsite`

To reapply the package/config to service hosts:

```sh
deploy/monitoring/jetmon/install-process-exporter.sh
```

The installer also opens `9256/tcp` to the CIDR in `PROMETHEUS_CIDR` via
UFW, matching the existing exporter firewall pattern.

## Provisioned Dashboards

- `Jetmon Fleet Overview`: fleet health, host CPU/memory, container CPU/memory, native process CPU/RSS, and scrape status.
- `Jetmon Host Detail`: per-host CPU, load, memory, filesystem, network, disk, scrape health, and native process CPU/RSS/count/open FDs.
- `Jetmon Container Detail`: per-host container CPU, memory, network, scrape success, and inventory.

The dashboards include top navigation links between overview, host detail, and
container detail while preserving the selected time range and variables.

## Grafana Backups

Grafana stores mutable state in SQLite at:

```sh
/opt/jetmon-monitoring/dbs/grafana/grafana.db
```

Install the daily backup timer on `monitoring.example.com`:

```sh
cd /opt/jetmon-monitoring
./install-grafana-backup-timer.sh
```

Backups are written to:

```sh
/opt/jetmon-monitoring/backups/grafana/
```

The timer retains 30 days by default.
