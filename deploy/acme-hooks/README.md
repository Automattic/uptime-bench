# Certbot manual hooks for uptime-bench DNS-01

These scripts wire `certbot --manual --preferred-challenges dns` to the
authoritative `uptime-bench-dns` fleet so `cmd/certmint` can
mint Let's Encrypt certificates whose domains resolve through the
benchmark's own nameservers. They install the validation TXT record on
every DNS member before Let's Encrypt queries it, then remove it once
the order finishes.

## Files

- `auth.sh` — `--manual-auth-hook`. Installs the challenge TXT.
- `cleanup.sh` — `--manual-cleanup-hook`. Removes the challenge TXT.

## Required environment

Both hooks read:

| Variable                          | Purpose                                                                 |
|-----------------------------------|-------------------------------------------------------------------------|
| `UPTIME_BENCH_DNS_CONTROL_URLS`   | Space-separated control base URLs, e.g. `http://dns-01.bench:9100 http://dns-02.bench:9100`. |
| `UPTIME_BENCH_CONTROL_TOKEN`      | Bearer token configured on the DNS members.                             |

Certbot itself populates `CERTBOT_IDENTIFIER`, `CERTBOT_VALIDATION`, and
the other `CERTBOT_*` variables.

## Use from certmint

In your certmint config, point certbot at these scripts:

```json
"authenticator_args": [
  "--manual",
  "--preferred-challenges", "dns",
  "--manual-auth-hook", "/usr/local/bin/uptime-bench-acme-auth",
  "--manual-cleanup-hook", "/usr/local/bin/uptime-bench-acme-cleanup"
]
```

Certmint already adds `--preferred-challenges dns`, so do not duplicate
that flag if certmint stops being authoritative for it.

## How challenge names are derived

Both hooks strip a leading `*.` from `CERTBOT_IDENTIFIER` and prepend
`_acme-challenge.`:

```
identifier:  *.bench.example.com   →  _acme-challenge.bench.example.com
identifier:  bench.example.com     →  _acme-challenge.bench.example.com
```

When an order contains both the apex and its wildcard, certbot calls
the auth hook twice with the same challenge name and two different
validation values; the DNS server preserves both because the TXT store
keys on (name, value) pairs.

## What the DNS server does with the request

Each `PUT /acme/txt` writes to an in-memory TXT store and returns 204.
DNS queries for `_acme-challenge.<name>` answer from that store and
bypass any active failure scenario, so cert minting can run while a
benchmark is in flight. `DELETE /acme/txt` removes only the matching
(name, value) pair, leaving sibling values intact. See
[`docs/certmint-operator.md`](../../docs/certmint-operator.md)
for the full design.

## Manual smoke test

With a DNS member running locally on port 9100:

```sh
export UPTIME_BENCH_DNS_CONTROL_URLS="http://127.0.0.1:9100"
export UPTIME_BENCH_CONTROL_TOKEN="$(cat /etc/uptime-bench/control-token)"
CERTBOT_IDENTIFIER="bench.example.com" \
CERTBOT_VALIDATION="manual-smoke-token" \
  ./deploy/acme-hooks/auth.sh

dig @127.0.0.1 -p 53 TXT _acme-challenge.bench.example.com +short
# → "manual-smoke-token"

CERTBOT_IDENTIFIER="bench.example.com" \
CERTBOT_VALIDATION="manual-smoke-token" \
  ./deploy/acme-hooks/cleanup.sh

dig @127.0.0.1 -p 53 TXT _acme-challenge.bench.example.com +short
# → (empty; DNS returns NXDOMAIN)
```
