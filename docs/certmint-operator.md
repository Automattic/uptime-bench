# Operator Setup

This guide covers a conservative single-host install. Keep staging enabled until
DNS-01 automation has been verified end to end.

## Prerequisites

- `uptime-bench-certmint` built from this repo
- `certbot`
- a DNS-01 certbot plugin for the configured provider
- DNS credentials allowed to create and remove `_acme-challenge` TXT records

The example config uses `certbot-dns-rfc2136`. Other DNS plugins can work by
replacing `certbot.authenticator_args` in the config.

## Install Files

Build the binary:

```sh
make build
```

Install the binary and protected directories:

```sh
sudo install -o root -g root -m 0755 bin/uptime-bench-certmint /usr/local/bin/uptime-bench-certmint
sudo install -o root -g root -m 0700 -d /etc/uptime-bench-certmint
sudo install -o root -g root -m 0700 -d /var/lib/uptime-bench-certmint
sudo install -o root -g root -m 0700 -d /var/lib/uptime-bench/certs
sudo install -o root -g root -m 0700 -d /var/log/uptime-bench-certmint
```

Install and edit the config:

```sh
sudo install -o root -g root -m 0600 configs/example.json /etc/uptime-bench-certmint/config.json
sudo editor /etc/uptime-bench-certmint/config.json
```

Install and edit the RFC2136 credentials template if using the example plugin:

```sh
sudo install -o root -g root -m 0600 configs/rfc2136.ini.example /etc/uptime-bench-certmint/rfc2136.ini
sudo editor /etc/uptime-bench-certmint/rfc2136.ini
```

## Verify With Staging

Keep `"staging": true` while testing. Confirm the planned orders and certbot
commands:

```sh
sudo uptime-bench-certmint plan -config /etc/uptime-bench-certmint/config.json
sudo uptime-bench-certmint once -config /etc/uptime-bench-certmint/config.json -dry-run
```

Run one staging issuance pass:

```sh
sudo uptime-bench-certmint once -config /etc/uptime-bench-certmint/config.json
```

Inspect the resulting manifest:

```sh
sudo uptime-bench-certmint inspect -library /var/lib/uptime-bench/certs/staging
```

Staging output is intentionally written below `<library_dir>/staging` and uses
staging-specific certbot lineage names. This prevents a later production run
from reusing or archiving a staging lineage.

## Switch To Production

After staging issuance and archive behavior are verified:

1. Review `profiles[].per_day` across every configured domain.
2. Confirm the generated SAN template keeps orders unique.
3. Set `"staging": false` in `/etc/uptime-bench-certmint/config.json`.
4. Confirm short-lived profiles use `"required_profile": "shortlived"` and a
   conservative `"max_lifetime"` such as `"168h"`.
5. Run another `plan` and `once -dry-run`.
6. Run one production `once` manually before enabling the daemon.

The daemon uses `lock_path` to prevent overlapping `once` and `daemon`
invocations. The lock is advisory and tied to the running process; the lock file
may remain after a crash, but it does not keep the next process locked.

## systemd

Install the unit:

```sh
sudo install -o root -g root -m 0644 deploy/systemd/uptime-bench-certmint.service /etc/systemd/system/uptime-bench-certmint.service
sudo systemctl daemon-reload
sudo systemctl enable --now uptime-bench-certmint.service
```

Check logs:

```sh
sudo journalctl -u uptime-bench-certmint.service
```

## Security Notes

- Keep `/etc/uptime-bench-certmint/config.json` mode `0600` if it contains
  sensitive provider arguments.
- Keep DNS API or TSIG credentials mode `0600`.
- Keep the library directory mode `0700`; it contains private keys.
- Keep certbot account and lineage state under a protected directory.
- Avoid copying `manifest.json` without the matching immutable PEM files.
