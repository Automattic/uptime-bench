# Certmint Operator Guide

`cmd/certmint` mints publicly-trusted Let's Encrypt certificates that the
target binary's TLS listener consumes for `tls_expired` and `tls_expiring`
scenarios. It runs as a separate fleet member because cert minting is a
long-running, low-cadence background workload that should not contend with
benchmark runs for CPU, network, or process state.

This guide covers a conservative single-host install. Keep staging enabled
until DNS-01 automation has been verified end-to-end.

## How it fits

- The `[certmint]` section in `fleet.toml` tells the harness where the cert
  library HTTP API lives. The harness forwards that URL to targets at
  scenario provision time.
- The certbot manual hooks in [`deploy/acme-hooks/`](../deploy/acme-hooks/)
  install ACME DNS-01 challenge records on every `uptime-bench-dns` member,
  using the shared CONTROL_TOKEN. No managed-DNS provider integration is
  needed for the runtime domains.
- Certmint writes an immutable library directory and an append-only
  `manifest.json`; the target's `internal/certlibrary` package selects from
  it by host coverage and `NotAfter` window.

## Prerequisites

- A provisioned certmint VM (see "Provisioning" below).
- DNS-01 reachability: the certmint host must be able to PUT/DELETE on every
  uptime-bench-dns member's `:9100` control port. Provisioning's UFW rules
  cover the inbound side on the dns members; outbound from certmint is
  unrestricted by default.
- A registered domain delegated to the uptime-bench DNS members (see
  [`OPERATIONS.md`](../OPERATIONS.md)).

The example config drives certbot's `--manual` plugin via the hook scripts
in `deploy/acme-hooks/`. To swap in a managed-DNS plugin (e.g.
`certbot-dns-rfc2136`), replace `certbot.authenticator_args` in the config
and skip the hooks.

## Provisioning

From your local checkout of `uptime-bench`, with the certmint VM standing
up at e.g. `203.0.113.30` and your target VM at `203.0.113.20`:

```sh
make provision-certmint CERTMINT_HOST=203.0.113.30 TARGET_IPS=203.0.113.20
```

This:
- Creates `/etc/uptime-bench/`, `/var/lib/uptime-bench-certmint/`, and
  `/var/lib/uptime-bench/certs/` with the right ownership.
- Installs `certbot` from apt.
- Installs the systemd unit at `/etc/systemd/system/uptime-bench-certmint.service`
  and enables it (the daemon won't start until the binary is deployed).
- Installs `/etc/uptime-bench/certmint.example.json` and the RFC2136 example.
- Configures UFW: `:9200` (cert-library HTTP API) is restricted to
  `TARGET_IPS`. Omit `TARGET_IPS` to leave the port open to any source —
  not recommended in production.

Edit the env file and the certmint config in-place on the host:

```sh
sudo cp /etc/uptime-bench/certmint.env.example /etc/uptime-bench/certmint.env
sudoedit /etc/uptime-bench/certmint.env
sudoedit /etc/uptime-bench/certmint.json
```

Set:
- `CONTROL_TOKEN` and `UPTIME_BENCH_CONTROL_TOKEN` to the same value used on
  every other fleet member.
- `UPTIME_BENCH_DNS_CONTROL_URLS` to the space-separated control base URLs
  of every dns member, e.g. `"http://203.0.113.10:9100 http://203.0.113.11:9100"`.
- The `domains[]`, `profiles[]`, and ACME settings in `certmint.json` per
  your fleet.

## Deploy the binary

Same as the rest of the fleet:

```sh
make deploy-certmint CERTMINT_HOST=203.0.113.30
```

The binary cross-compiles for `linux/amd64`, ships, atomically replaces, and
restarts the systemd unit.

## Verify with staging

Keep `"staging": true` in `certmint.json` while testing. Confirm the planned
orders and certbot commands:

```sh
sudo uptime-bench-certmint plan -config /etc/uptime-bench/certmint.json
sudo uptime-bench-certmint once -config /etc/uptime-bench/certmint.json -dry-run
```

Run one staging issuance pass:

```sh
sudo uptime-bench-certmint once -config /etc/uptime-bench/certmint.json
```

Inspect the resulting manifest:

```sh
sudo uptime-bench-certmint inspect -library /var/lib/uptime-bench/certs/staging
```

Staging output is written under `<library_dir>/staging` with
staging-specific certbot lineage names. A later production run cannot reuse
or archive a staging lineage.

### Inter-order quiet period

When two orders for the same domain run back-to-back — common when one
profile's wildcard SAN and another profile's wildcard SAN both land on the
same `_acme-challenge.<domain>` TXT name — the second order's auth-hook can
race Let's Encrypt's recursive resolver caching the first order's old TXT
value, and validation fails on a freshly installed but cache-shadowed
record.

`inter_order_quiet` (default `60s`, twice the canonical 30s TTL on
`uptime-bench-dns`) tells certmint to wait that long between two orders
that share a domain. Cross-domain throughput is unaffected. Set to `"0s"`
to disable if you control the recursive cache or run all orders single-shot.

## Switch to production

After staging issuance and archive behavior are verified:

1. Review `profiles[].per_day` across every configured domain.
2. Confirm the generated SAN template keeps orders unique.
3. Set `"staging": false` in `/etc/uptime-bench/certmint.json`.
4. Confirm short-lived profiles use `"required_profile": "shortlived"` and a
   conservative `"max_lifetime"` such as `"168h"`.
5. Run `plan` and `once -dry-run` again.
6. Run one production `once` manually before letting the daemon take over.

The daemon uses `lock_path` to prevent overlapping `once` and `daemon`
invocations. The lock is advisory and tied to the running process; a stale
lock file from a crash does not keep the next process locked.

Each certbot invocation runs under `certbot.issuance_timeout` (default
`10m`). Increase this if slow DNS-01 propagation legitimately needs more
time; otherwise the daemon will surface a stuck issuance rather than hang.

The daemon does not reload `certmint.json` while running. To pick up config
edits:

```sh
sudo systemctl restart uptime-bench-certmint.service
```

## systemd

Provisioning installs and enables the unit. Check logs with:

```sh
sudo journalctl -u uptime-bench-certmint.service
```

If you need to install or update the unit manually (e.g. while iterating on
the unit file in this repo without a full deploy):

```sh
sudo install -o root -g root -m 0644 deploy/systemd/uptime-bench-certmint.service \
    /etc/systemd/system/uptime-bench-certmint.service
sudo systemctl daemon-reload
sudo systemctl restart uptime-bench-certmint.service
```

## Security notes

- Keep `/etc/uptime-bench/certmint.json` mode `0640` (root:uptime-bench).
  Provisioning sets this; the `ensure_config_file` helper re-asserts it on
  re-runs.
- Keep DNS API or TSIG credentials mode `0600`.
- Keep `/var/lib/uptime-bench/certs/` mode `0700`; it contains private keys.
- Keep certbot account and lineage state under
  `/var/lib/uptime-bench-certmint/letsencrypt/` (the default `config_dir`).
- Avoid copying `manifest.json` between hosts without the matching immutable
  PEM files; the target's library loader will reject any entry whose
  fingerprint doesn't match the on-disk PEM.
