# uptime-bench — Operations Guide

This guide covers everything needed to stand up a working uptime-bench fleet: VPS requirements, domain configuration, provisioning, credential setup, and starting the service.

> **Implementation status:** The target binary, DNS binary, harness, and five adapters — Jetmon 1 (`jetmon-v1`), UptimeRobot (`uptimerobot`), Pingdom (`pingdom`), Better Uptime (`better-uptime`), and Datadog Synthetics (`datadog-synthetics`) — are implemented. The `jetmon-v2` type is a stub blocked on the Jetmon 2 public REST API and will fail fast if enabled. All four probe-based adapters have been exercised against their live APIs via build-tagged smoke tests under `internal/adapter/<name>/live_test.go`.

---

## Fleet overview

uptime-bench runs across four server roles:

```
┌──────────────────────┐      control plane (port 9000/9100)
│     Harness VM       │ ─────────────────────────────────────────┐
│  cmd/harness         │                                           │
│  MySQL               │      ┌────────────────────────────────────┼──────────┐
└──────────────────────┘      │                                    │          │
         │                    ▼                                    ▼          ▼
         │            ┌───────────────┐                  ┌───────────────┐
         │            │  Target VM(s) │                  │   DNS VM(s)   │
         │            │  cmd/target   │                  │   cmd/dns     │
         │            │  :80  :443    │                  │   :53 UDP/TCP │
         │            │  :9000 ctrl   │                  │   :9100 ctrl  │
         │            └───────────────┘                  └───────────────┘
         │                    │                                    │
         └────────────────────┴────────────────────────────────────┘
               monitors under test probe target VMs via DNS VMs
```

The harness orchestrates everything: it tells target VMs to inject failures, collects results from monitoring service adapters, and writes all data to MySQL.

---

## VPS requirements

### Minimum fleet (MVP)

| Role | Count | Recommended spec | Purpose |
|---|---|---|---|
| Harness | 1 | 2 vCPU, 4 GB RAM, 40 GB disk | Runs the harness binary and MySQL |
| Target | 1 | 2 vCPU, 2 GB RAM, 20 GB disk | Hosts test websites monitored by external services |
| DNS | 2 | 1 vCPU, 1 GB RAM, 10 GB disk | Authoritative nameservers for test domains |

**Total minimum: 4 VPSs.**

Two DNS VMs are the minimum — `dns_ns_unavailable` scenarios require at least two so one can fail while the other remains up. A single-DNS setup is also valid if you only run HTTP/TCP/TLS scenarios.

### Expanded fleet (recommended)

Add more target VMs as your scenario library grows. Each target VM hosts multiple virtual sites, but separating them across VMs lets you run unrelated scenarios simultaneously without state interference.

### OS

All VPSs must run **Ubuntu Server 24.04 LTS**. The provisioning scripts target this OS specifically.

---

## Step 1 — Acquire and note IP addresses

Before anything else, provision your VPSs and record their public IP addresses. You will need them for domain setup, fleet configuration, and provisioning.

Example (replace with your actual IPs):

| Server | IP |
|---|---|
| harness-01 | `203.0.113.5` |
| target-01 | `203.0.113.20` |
| ns-01 | `203.0.113.10` |
| ns-02 | `203.0.113.11` |

---

## Step 2 — Register and configure test domains

You need at least two domains. Having multiple domains lets you test nameserver failures against one domain while another continues operating normally.

### 2a. Register domains

Register your test domains at any registrar (Namecheap, Cloudflare, etc.). These domains exist solely for testing — they do not need to look legitimate to humans, only to uptime monitoring probes.

Example domains used throughout this guide:
- `bench-example.com`
- `probe-example.net`

### 2b. Set up glue records at the registrar

Because the uptime-bench DNS VMs will be the authoritative nameservers for your test domains, and those nameservers live on subdomains of those same domains, you need **glue records** — IP addresses registered directly at the registrar alongside the NS records.

At your registrar, for each test domain:

1. **Add glue records** (also called "host records" or "nameserver registration"):

   | Hostname | IP |
   |---|---|
   | `ns1.bench-example.com` | `203.0.113.10` ← ns-01 IP |
   | `ns2.bench-example.com` | `203.0.113.11` ← ns-02 IP |

2. **Set the domain's nameservers** to those hostnames:
   ```
   ns1.bench-example.com
   ns2.bench-example.com
   ```

3. Repeat for `probe-example.net` using the same nameserver IPs.

> **Why glue records?** When a resolver asks "who is authoritative for bench-example.com?", the TLD registry returns `ns1.bench-example.com`. Without glue records, the resolver cannot look up that hostname (it would need to ask bench-example.com itself — a loop). Glue records break the loop by embedding the IP directly in the TLD zone.

DNS propagation for NS record changes typically takes 15 minutes to a few hours depending on the registrar and TLD. You can verify propagation with:

```sh
dig NS bench-example.com @8.8.8.8
```

### 2c. Plan your test site hostnames

Each test site is a subdomain of one of your test domains. Plan these before writing your fleet config:

| Site ID | Hostname | Target VM |
|---|---|---|
| bench-a | `bench-a.bench-example.com` | target-01 |
| bench-b | `bench-b.bench-example.com` | target-01 |
| probe-a | `probe-a.probe-example.net` | target-01 |

The DNS VMs will serve A records for these hostnames, pointing to the target VM's IP. You do not need to configure these at the registrar — the DNS VMs handle all records for their authoritative domains.

---

## Step 3 — Set up MySQL on the harness VM

SSH into the harness VM and install MySQL:

```sh
sudo apt-get update
sudo apt-get install -y mysql-server
sudo mysql_secure_installation
```

Create the database and service user:

```sh
sudo mysql <<'SQL'
CREATE DATABASE uptime_bench
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

CREATE USER 'uptime_bench'@'localhost'
  IDENTIFIED BY 'CHOOSE_A_STRONG_PASSWORD';

GRANT ALL PRIVILEGES ON uptime_bench.* TO 'uptime_bench'@'localhost';
FLUSH PRIVILEGES;
SQL
```

Apply the schema. From your local machine (with the repo checked out):

```sh
scp schema/001_initial.sql ubuntu@203.0.113.5:/tmp/
ssh ubuntu@203.0.113.5 \
  "mysql -u uptime_bench -p uptime_bench < /tmp/001_initial.sql"
```

---

## Step 4 — Generate the shared control token

The harness authenticates to all fleet members using a single shared bearer token. Generate it once and use it everywhere:

```sh
openssl rand -hex 32
```

Save the output — you will distribute it to every VM in the next step. Treat it like a password: do not commit it, do not log it, do not reuse it across environments.

---

## Step 5 — Provision all VPSs

From your local machine (with the repo checked out), run the provisioning script for each VM. The `HARNESS_IP` variable restricts the control port to accept connections only from the harness VM — always set this in production.

If the SSH user on a VM is not `ubuntu`, pass `DEPLOY_USER=<name>`. The provisioning script writes `AllowUsers ${DEPLOY_USER}` into the SSH hardening drop-in, so this must match the user you actually log in as.

```sh
# Harness VM
make provision-harness HARNESS_HOST=203.0.113.5

# Target VM
make provision-target TARGET_HOST=203.0.113.20 HARNESS_IP=203.0.113.5

# DNS VMs
make provision-dns DNS_HOST=203.0.113.10 HARNESS_IP=203.0.113.5
make provision-dns DNS_HOST=203.0.113.11 HARNESS_IP=203.0.113.5
```

Each provisioning run:
- Updates all packages
- Creates the `uptime-bench` service account
- Hardens SSH (disables root login and password auth)
- Configures UFW with role-appropriate port rules
- Installs fail2ban, unattended-upgrades, chrony
- Creates a 2 GB swap file if none exists
- Drops `*.example` skeleton files into `/etc/uptime-bench/` (per-role `<type>.env.example`, `control-token.example`, plus `fleet.example.toml` on harness/dns and `services.example.toml` on harness)
- Installs the systemd unit; enables it on target and DNS hosts. The harness unit is installed but **not** enabled — the harness binary requires `-scenario` per invocation and is run on demand (see Step 11).

After provisioning completes, re-authentication as root is disabled. All subsequent SSH must use the `ubuntu` user (or whichever `DEPLOY_USER` you specified).

The script's "Next steps" output at the end of each run lists the exact commands to copy and edit each `.example` file. The next three steps cover the same ground in narrative form.

---

## Step 6 — Place credential files on each VM

Provisioning has already dropped skeletons into `/etc/uptime-bench/` on each VM:

- `<type>.env.example` — the env file the systemd unit (or harness CLI) reads. Each header comment names every variable.
- `control-token.example` — the bare-token file that `fleet.toml`'s `auth_token_file` setting points at.

Copy each skeleton to its real name and fill in the values. The pattern is the same on every VM:

```sh
ssh <user>@<vm-ip>

# Replace TYPE with harness, target, or dns to match the role.
sudo cp /etc/uptime-bench/TYPE.env.example /etc/uptime-bench/TYPE.env
sudo chown root:uptime-bench /etc/uptime-bench/TYPE.env
sudo chmod 640 /etc/uptime-bench/TYPE.env
sudoedit /etc/uptime-bench/TYPE.env

sudo cp /etc/uptime-bench/control-token.example /etc/uptime-bench/control-token
sudo chown root:uptime-bench /etc/uptime-bench/control-token
sudo chmod 640 /etc/uptime-bench/control-token
sudoedit /etc/uptime-bench/control-token  # delete the comment block, paste the token
```

Per-role values to fill in:

| File | Variable | Value |
|---|---|---|
| `harness.env` | `DB_DSN` | `"uptime_bench:<password>@tcp(127.0.0.1:3306)/uptime_bench?parseTime=true"` (keep the double quotes — the `tcp(...)` parens are a shell syntax error if you ever source this file via `. harness.env`) |
| `harness.env` | `CONTROL_TOKEN` | The token from Step 4 |
| `target.env` | `CONTROL_TOKEN` | Same token |
| `target.env` | `MEMBER_ID` | This VM's `id` from its `[[targets]]` entry in `fleet.toml` (e.g. `target-01`) — the target binary reports it in control responses so the harness can correlate results across multiple targets |
| `dns.env`    | `CONTROL_TOKEN` | Same token |
| `dns.env`    | `MEMBER_ID` | This VM's `id` from its `[[nameservers]]` entry in `fleet.toml` (e.g. `ns-01`, `ns-02`) — the DNS binary uses it to find its own zone records |
| `control-token` (every VM) | (file body) | Same token, on a single line, no other content |

The `CONTROL_TOKEN` value must be identical on every VM.

---

## Step 7 — Deploy fleet.toml

`fleet.toml` is never committed — it contains your real IPs and hostnames. The harness reads it to orchestrate runs; each DNS VM also reads it at startup to derive the A records it serves. It must be present on the harness and on every DNS VM, with the same content. Target VMs do not need it.

Provisioning has already uploaded `fleet.example.toml` to `/etc/uptime-bench/` on the harness and on each DNS VM. To use it, copy and edit on each of those VMs:

```sh
ssh <user>@<vm-ip>
sudo cp /etc/uptime-bench/fleet.example.toml /etc/uptime-bench/fleet.toml
sudo chown root:uptime-bench /etc/uptime-bench/fleet.toml
sudo chmod 640 /etc/uptime-bench/fleet.toml
sudoedit /etc/uptime-bench/fleet.toml
```

Keep the content identical across all three (or more) VMs. A common workflow: write the canonical version on the harness, then scp it to each DNS VM:

```sh
ssh <user>@harness 'sudo cat /etc/uptime-bench/fleet.toml' \
  | ssh <user>@dns-vm 'sudo tee /etc/uptime-bench/fleet.toml >/dev/null \
      && sudo chown root:uptime-bench /etc/uptime-bench/fleet.toml \
      && sudo chmod 640 /etc/uptime-bench/fleet.toml'
```

A minimal `fleet.toml` for the example fleet:

```toml
[control]
timeout         = "10s"
auth_token_file = "/etc/uptime-bench/control-token"

[adapters.jetmon-v1]
max_calls_per_run = 0

[[nameservers]]
id           = "ns-01"
address      = "203.0.113.10"
control_port = 9100
dns_port     = 53
domains      = ["bench-example.com", "probe-example.net"]

[[nameservers]]
id           = "ns-02"
address      = "203.0.113.11"
control_port = 9100
dns_port     = 53
domains      = ["bench-example.com", "probe-example.net"]

[[targets]]
id           = "target-01"
address      = "203.0.113.20"
control_port = 9000

  [[targets.sites]]
  id    = "bench-a"
  host  = "bench-a.bench-example.com"
  paths = ["/", "/api/health"]

  [[targets.sites]]
  id    = "bench-b"
  host  = "bench-b.bench-example.com"
  paths = ["/"]

  [[targets.sites]]
  id    = "probe-a"
  host  = "probe-a.probe-example.net"
  paths = ["/"]

[[domains]]
name        = "bench-example.com"
registrar   = "Namecheap"
nameservers = ["ns-01", "ns-02"]
ttl         = 30

[[domains]]
name        = "probe-example.net"
registrar   = "Namecheap"
nameservers = ["ns-01", "ns-02"]
ttl         = 30
```

---

## Step 7b — Create services.toml on the harness VM

`services.toml` declares which monitoring services to evaluate and their credentials. It lives only on the harness and is never committed.

Provisioning has already uploaded `services.example.toml` to `/etc/uptime-bench/` on the harness. To use it:

```sh
ssh <user>@harness
sudo cp /etc/uptime-bench/services.example.toml /etc/uptime-bench/services.toml
sudo chown root:uptime-bench /etc/uptime-bench/services.toml
sudo chmod 640 /etc/uptime-bench/services.toml
sudoedit /etc/uptime-bench/services.toml
```

Edit each `[[services]]` block: set `enabled = true` for the services you want to evaluate, and fill in the `url` and `auth` fields. The `id` field in each block must match the IDs used in scenario `monitors` lists.

`jetmon-v1`, `uptimerobot`, `pingdom`, `better-uptime`, and `datadog-synthetics` have implemented adapters today — set those `enabled = true` (with credentials filled in) to participate. The `jetmon-v2` entry is a stub; enabling it causes the harness to exit with "jetmon-v2: adapter not implemented — blocked on Jetmon 2 public API".

### Pre-seeding monitors for `jetmon-v1`

Jetmon 1 has no public API; the adapter talks to a sidecar `jetmon-bridge` that fronts Jetmon's MySQL. Two modes are supported, controlled by the `write_mode` auth key:

- `write_mode = "false"` (default) — read-only. The adapter looks up each target URL in Jetmon's `jetpack_monitor_sites` table during Provision. If the row is missing, the run fails fast with `jetmon-v1: no monitor pre-seeded for <url> — add it to jetpack_monitor_sites`. **You must insert one row per site URL declared in `fleet.toml` before the first scenario runs.** Each row needs at minimum `blog_id`, `bucket_no`, `monitor_url`, `monitor_active = 1`, and a sensible `check_interval`. Rows are persistent — pre-seed once per fleet, not per run.
- `write_mode = "true"` — read/write. Provision creates (or reactivates) the row automatically; Deprovision soft-deletes it at the end of the run. Use this only if your `jetmon-bridge` deployment was started with write capability enabled, and only against a Jetmon environment whose contents you fully control.

The other four adapters create their monitors via API on every run and have no equivalent pre-seeding step.

---

## Step 8 — Deploy binaries

Build and push all three binaries from your local machine. The `deploy-*` targets cross-compile for `linux/amd64`. As with `provision-*`, pass `DEPLOY_USER=<name>` if the SSH user is not `ubuntu`.

```sh
make deploy-dns     DNS_HOST=203.0.113.10
make deploy-dns     DNS_HOST=203.0.113.11
make deploy-target  TARGET_HOST=203.0.113.20
make deploy-harness HARNESS_HOST=203.0.113.5
```

Each deploy:
1. Builds the binary for `linux/amd64`
2. scps the binary to `/tmp/uptime-bench-<role>.new` and `sudo install`s it into `/usr/local/bin/` (the temp-file-plus-rename pattern avoids `ETXTBSY` when overwriting a running binary)
3. Re-applies `cap_net_bind_service` (target and DNS only)
4. Restarts the systemd service (target and DNS only — the harness is invoked per-scenario, see Step 11)

---

## Step 9 — Start and verify services

The target and DNS systemd units are enabled by `provision-server.sh` and restarted by `deploy.sh`, so they should already be running after Step 8. Verify:

```sh
ssh <user>@203.0.113.10 'sudo systemctl status uptime-bench-dns    --no-pager'
ssh <user>@203.0.113.11 'sudo systemctl status uptime-bench-dns    --no-pager'
ssh <user>@203.0.113.20 'sudo systemctl status uptime-bench-target --no-pager'
```

If a unit isn't running, tail its journal:

```sh
ssh <user>@203.0.113.20 'sudo journalctl -u uptime-bench-target -n 50 --no-pager'
```

The harness has no long-running mode — its binary requires `-scenario` per invocation. The `uptime-bench-harness` systemd unit is installed but deliberately not enabled; do not try to `systemctl start` it. Run scenarios manually via Step 11 instead.

---

## Step 10 — Verify DNS resolution

Once the DNS VMs are running and the registrar NS change has propagated, verify that your test domains resolve:

```sh
# Should return the NS-01 and NS-02 IPs
dig NS bench-example.com

# Should return the target VM IP (203.0.113.20)
dig A bench-a.bench-example.com

# Verify low TTL is in effect
dig A bench-a.bench-example.com | grep -i ttl
```

The DNS binary loads A records from `fleet.toml` at startup — it serves every site hostname from its configured domains, pointing to the corresponding target VM IP, using the TTL from the `[[domains]]` block.

---

## Step 11 — Run a scenario

Scenario TOML files live in the repo's `scenarios/` directory; copy the ones you want to run onto the harness (e.g. `scp scenarios/http-503.toml <user>@harness:/tmp/`). Then on the harness, source `harness.env` so `DB_DSN` and `CONTROL_TOKEN` are visible, and invoke the harness binary:

```sh
ssh <user>@203.0.113.5
sudo -u uptime-bench bash -c '
  set -a; . /etc/uptime-bench/harness.env; set +a
  exec /usr/local/bin/uptime-bench-harness \
    -fleet=/etc/uptime-bench/fleet.toml \
    -services=/etc/uptime-bench/services.toml \
    -scenario=/tmp/http-503.toml
'
```

Running as `uptime-bench` matches the systemd unit's user; the `sudo -u` step is necessary because `/etc/uptime-bench/harness.env` is mode 0640 root:uptime-bench and `sudo` strips environment variables by default.

The harness will:
1. Parse and validate the scenario
2. Check adapter capabilities against the scenario requirements
3. Provision a monitor on each in-scope service (e.g. Jetmon)
4. Send an `ActivateRequest` to the target VM's control API to begin injecting 503s
5. Wait for the scenario duration
6. Send `DeactivateRequest`, record `failure_end`
7. Wait for the grace period
8. Call `Retrieve` on each adapter to collect detection data
9. Deprovision all monitors
10. Write derived metrics to MySQL
11. Print a summary

---

## Updating the fleet

### Deploy a new binary version

```sh
# Target VM
make deploy-target TARGET_HOST=203.0.113.20

# DNS VMs
make deploy-dns DNS_HOST=203.0.113.10
make deploy-dns DNS_HOST=203.0.113.11

# Harness
make deploy-harness HARNESS_HOST=203.0.113.5
```

### Apply a new database migration

```sh
scp schema/002_next_migration.sql ubuntu@203.0.113.5:/tmp/
ssh ubuntu@203.0.113.5 \
  "mysql -u uptime_bench -p uptime_bench < /tmp/002_next_migration.sql"
```

### Update fleet.toml

Edit `/etc/uptime-bench/fleet.toml` in place on the harness and on every DNS VM (use `sudoedit`). Each DNS binary re-reads the file at startup, so restart any DNS unit whose zones changed:

```sh
ssh <user>@dns-vm 'sudo systemctl restart uptime-bench-dns'
```

The harness reads `fleet.toml` fresh on each scenario invocation (Step 11), so it does not need a restart.

### Add a new target VM to the fleet

1. Provision: `make provision-target TARGET_HOST=NEW_IP HARNESS_IP=203.0.113.5`
2. Place credentials (Step 6)
3. Deploy binary: `make deploy-target TARGET_HOST=NEW_IP` (the deploy script restarts the systemd unit for you)
4. Add the new `[[targets]]` block to `fleet.toml` on the harness and on each DNS VM, then restart the DNS units (see "Update fleet.toml" above)

---

## Troubleshooting

### Service fails to start after deploy

Check the journal:
```sh
sudo journalctl -u uptime-bench-target -n 50 --no-pager
```

Common causes:
- Missing or misformatted credential file in `/etc/uptime-bench/`
- Binary lacks `cap_net_bind_service` — rerun `make deploy-target` which re-applies it
- Port already in use — check `sudo ss -tlnp | grep ':80\|:443\|:9000'`

### Control API connection refused from harness

- Verify UFW on the fleet member allows the harness IP on the control port:
  `sudo ufw status verbose`
- Verify `CONTROL_TOKEN` is identical on both the harness and the fleet member
- Verify the fleet member's service is running

### DNS not resolving

- Confirm NS propagation: `dig NS bench-example.com @8.8.8.8`
- Confirm the DNS VM service is running and port 53 is open:
  `sudo ufw status | grep 53`
- Confirm glue records at the registrar match the DNS VM IPs

### MySQL connection refused

- Confirm MySQL is running: `sudo systemctl status mysql`
- Confirm the `DB_DSN` in `harness.env` has the correct password and host
- If MySQL is on a remote host (not localhost), ensure MySQL is bound to the right interface and the harness IP is whitelisted in MySQL's `bind-address` and user grants
