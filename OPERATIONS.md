# uptime-bench — Operations Guide

This guide covers everything needed to stand up a working uptime-bench fleet: VPS requirements, domain configuration, provisioning, credential setup, and starting the service.

> **Implementation status:** The target server, DNS server, and harness binaries are currently stubs under active development. This guide documents the intended full setup. Steps that depend on unfinished code are marked **[pending implementation]**.

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

From your local machine (with the repo checked out), run the provisioning script for each VM. The `--harness-ip` flag restricts the control port to accept connections only from the harness VM — always set this in production.

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
- Installs and enables the systemd unit (not yet started)

After provisioning completes, re-authentication as root is disabled. All subsequent SSH must use the `ubuntu` user (or whichever `--deploy-user` you specified).

---

## Step 6 — Place credential files on each VM

Each VM needs credential files in `/etc/uptime-bench/`. These are created manually — they contain secrets and are never committed to the repo.

### On the harness VM

```sh
ssh ubuntu@203.0.113.5
sudo bash -c 'cat > /etc/uptime-bench/harness.env' <<'EOF'
DB_DSN=uptime_bench:CHOOSE_A_STRONG_PASSWORD@tcp(127.0.0.1:3306)/uptime_bench?parseTime=true
CONTROL_TOKEN=YOUR_GENERATED_TOKEN_HERE
EOF
sudo chmod 640 /etc/uptime-bench/harness.env
sudo chown root:uptime-bench /etc/uptime-bench/harness.env

sudo bash -c 'echo "YOUR_GENERATED_TOKEN_HERE" > /etc/uptime-bench/control-token'
sudo chmod 640 /etc/uptime-bench/control-token
sudo chown root:uptime-bench /etc/uptime-bench/control-token
```

### On each target VM

```sh
ssh ubuntu@203.0.113.20
sudo bash -c 'cat > /etc/uptime-bench/target.env' <<'EOF'
CONTROL_TOKEN=YOUR_GENERATED_TOKEN_HERE
EOF
sudo chmod 640 /etc/uptime-bench/target.env
sudo chown root:uptime-bench /etc/uptime-bench/target.env

sudo bash -c 'echo "YOUR_GENERATED_TOKEN_HERE" > /etc/uptime-bench/control-token'
sudo chmod 640 /etc/uptime-bench/control-token
sudo chown root:uptime-bench /etc/uptime-bench/control-token
```

### On each DNS VM

```sh
ssh ubuntu@203.0.113.10  # repeat for 203.0.113.11
sudo bash -c 'cat > /etc/uptime-bench/dns.env' <<'EOF'
CONTROL_TOKEN=YOUR_GENERATED_TOKEN_HERE
EOF
sudo chmod 640 /etc/uptime-bench/dns.env
sudo chown root:uptime-bench /etc/uptime-bench/dns.env

sudo bash -c 'echo "YOUR_GENERATED_TOKEN_HERE" > /etc/uptime-bench/control-token'
sudo chmod 640 /etc/uptime-bench/control-token
sudo chown root:uptime-bench /etc/uptime-bench/control-token
```

The `CONTROL_TOKEN` value must be identical on every VM.

---

## Step 7 — Create fleet.toml on the harness VM

`fleet.toml` is never committed. Create it directly on the harness VM at `/etc/uptime-bench/fleet.toml` (or a path of your choosing, referenced by the `--fleet` flag).

The simplest approach: edit `fleet.example.toml` locally, then copy it:

```sh
cp fleet.example.toml fleet.toml
# edit fleet.toml with your real IPs and hostnames
scp fleet.toml ubuntu@203.0.113.5:/tmp/fleet.toml
ssh ubuntu@203.0.113.5 'sudo mv /tmp/fleet.toml /etc/uptime-bench/fleet.toml && \
  sudo chmod 640 /etc/uptime-bench/fleet.toml && \
  sudo chown root:uptime-bench /etc/uptime-bench/fleet.toml'
rm fleet.toml  # remove from local machine; it is git-ignored but clean up anyway
```

A minimal `fleet.toml` for the example fleet:

```toml
[control]
timeout         = "10s"
auth_token_file = "/etc/uptime-bench/control-token"

[adapters.jetmon]
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

`services.toml` declares which monitoring services to evaluate and their credentials. It is never committed — it contains secrets and is specific to your deployment.

Edit `services.example.toml` locally, then copy it to the harness VM:

```sh
cp services.example.toml services.toml
# Edit services.toml: set enabled = true and fill in url and auth for each service
scp services.toml ubuntu@203.0.113.5:/tmp/services.toml
ssh ubuntu@203.0.113.5 'sudo mv /tmp/services.toml /etc/uptime-bench/services.toml && \
  sudo chmod 640 /etc/uptime-bench/services.toml && \
  sudo chown root:uptime-bench /etc/uptime-bench/services.toml'
rm services.toml
```

See `services.example.toml` for the format and the required `auth` keys for each service type. The `id` field in each block must match the IDs used in scenario `monitors` lists.

---

## Step 8 — Deploy binaries

Build and push all three binaries from your local machine. The `deploy-*` targets cross-compile for `linux/amd64`:

```sh
make deploy-dns     DNS_HOST=203.0.113.10
make deploy-dns     DNS_HOST=203.0.113.11
make deploy-target  TARGET_HOST=203.0.113.20
make deploy-harness HARNESS_HOST=203.0.113.5
```

Each deploy:
1. Builds the binary for `linux/amd64`
2. Copies it atomically to `/usr/local/bin/`
3. Re-applies `cap_net_bind_service` (target and DNS only)
4. Restarts the systemd service

---

## Step 9 — Start and verify services

Services are enabled by the provisioning script but not started (the binary was not present yet). After deploying, start them:

```sh
# DNS VMs
ssh ubuntu@203.0.113.10 'sudo systemctl start uptime-bench-dns'
ssh ubuntu@203.0.113.11 'sudo systemctl start uptime-bench-dns'

# Target VM
ssh ubuntu@203.0.113.20 'sudo systemctl start uptime-bench-target'

# Harness VM (start last — it connects to fleet members on startup)
ssh ubuntu@203.0.113.5  'sudo systemctl start uptime-bench-harness'
```

Check that each service is running:

```sh
ssh ubuntu@203.0.113.10 'sudo systemctl status uptime-bench-dns --no-pager'
ssh ubuntu@203.0.113.20 'sudo systemctl status uptime-bench-target --no-pager'
ssh ubuntu@203.0.113.5  'sudo systemctl status uptime-bench-harness --no-pager'
```

Tail logs if needed:

```sh
ssh ubuntu@203.0.113.5 'sudo journalctl -u uptime-bench-harness -f'
```

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

**[Pending implementation]** The DNS VM binary does not yet serve DNS records — it is currently a stub. Once implemented, it will serve A records for all site hostnames configured in `fleet.toml`, pointing to the appropriate target VM IPs, with the TTL specified in the `[[domains]]` block.

---

## Step 11 — Run a scenario

Run a scenario on the harness VM:

```sh
ssh ubuntu@203.0.113.5
uptime-bench-harness \
  -fleet=/etc/uptime-bench/fleet.toml \
  -services=/etc/uptime-bench/services.toml \
  -scenario=/path/to/scenarios/http-503.toml
```

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

Edit locally, copy to the harness VM, then restart the harness:

```sh
scp fleet.toml ubuntu@203.0.113.5:/tmp/fleet.toml
ssh ubuntu@203.0.113.5 'sudo mv /tmp/fleet.toml /etc/uptime-bench/fleet.toml && \
  sudo chmod 640 /etc/uptime-bench/fleet.toml && \
  sudo chown root:uptime-bench /etc/uptime-bench/fleet.toml && \
  sudo systemctl restart uptime-bench-harness'
rm fleet.toml
```

### Add a new target VM to the fleet

1. Provision: `make provision-target TARGET_HOST=NEW_IP HARNESS_IP=203.0.113.5`
2. Place credentials (Step 6)
3. Deploy binary: `make deploy-target TARGET_HOST=NEW_IP`
4. Start service: `ssh ubuntu@NEW_IP 'sudo systemctl start uptime-bench-target'`
5. Add the new `[[targets]]` block to `fleet.toml` and redeploy it (see above)

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
