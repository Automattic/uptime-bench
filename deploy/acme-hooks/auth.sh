#!/usr/bin/env bash
# auth.sh — Certbot manual-auth-hook for uptime-bench DNS-01 challenges.
#
# Certmint runs certbot with `--manual --preferred-challenges dns
# --manual-auth-hook /path/to/auth.sh`. Certbot invokes this script
# once per identifier in the order, with these env vars set:
#
#   CERTBOT_IDENTIFIER             e.g. *.bench.example.com or bench.example.com
#   CERTBOT_VALIDATION             validation token to install at TXT
#   CERTBOT_TOKEN                  ACME challenge token (unused here)
#   CERTBOT_REMAINING_CHALLENGES   countdown, present until 0
#   CERTBOT_ALL_IDENTIFIERS        comma-separated list of every identifier
#
# This script writes the validation TXT to every authoritative DNS
# member listed in $UPTIME_BENCH_DNS_CONTROL_URLS, using the bearer
# token in $CONTROL_TOKEN. The wildcard prefix `*.` is
# stripped from the identifier — RFC 8555 §8.4 requires both apex and
# wildcard authorizations to validate at the same _acme-challenge name.
#
# Usage from certmint config:
#   "authenticator_args": [
#     "--manual",
#     "--preferred-challenges", "dns",
#     "--manual-auth-hook", "/usr/local/bin/uptime-bench-acme-auth",
#     "--manual-cleanup-hook", "/usr/local/bin/uptime-bench-acme-cleanup"
#   ]
#
# Required env:
#   UPTIME_BENCH_DNS_CONTROL_URLS  space-separated, e.g.
#                                  "http://dns-01.bench:9100 http://dns-02.bench:9100"
#   CONTROL_TOKEN     bearer token shared with the DNS members

set -euo pipefail

: "${CERTBOT_IDENTIFIER:?certbot env CERTBOT_IDENTIFIER not set}"
: "${CERTBOT_VALIDATION:?certbot env CERTBOT_VALIDATION not set}"
: "${UPTIME_BENCH_DNS_CONTROL_URLS:?set UPTIME_BENCH_DNS_CONTROL_URLS to your DNS member control URLs}"
: "${CONTROL_TOKEN:?set CONTROL_TOKEN to the DNS control token}"

identifier="${CERTBOT_IDENTIFIER#\*.}"
name="_acme-challenge.${identifier}"
value="${CERTBOT_VALIDATION}"

payload=$(printf '{"name":"%s","value":"%s","ttl":30}' "${name}" "${value}")

for dns_control_url in ${UPTIME_BENCH_DNS_CONTROL_URLS}; do
  echo "acme-auth: PUT ${dns_control_url}/acme/txt name=${name}" >&2
  curl -fsS -X PUT "${dns_control_url}/acme/txt" \
    -H "Authorization: Bearer ${CONTROL_TOKEN}" \
    -H "Content-Type: application/json" \
    --data "${payload}"
done
