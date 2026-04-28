#!/usr/bin/env bash
# auth.sh — Certbot manual-auth-hook for uptime-bench DNS-01 challenges.
#
# Certmint runs certbot with `--manual --preferred-challenges dns
# --manual-auth-hook /path/to/auth.sh`. Certbot invokes this script
# once per identifier in the order, with these env vars set:
#
#   CERTBOT_DOMAIN                 e.g. bench.example.com (the apex; certbot
#                                  has already stripped any leading "*.")
#   CERTBOT_VALIDATION             validation token to install at TXT
#   CERTBOT_TOKEN                  ACME challenge token (unused here)
#   CERTBOT_REMAINING_CHALLENGES   countdown, present until 0
#   CERTBOT_ALL_DOMAINS            comma-separated list of every domain
#
# This script writes the validation TXT to every authoritative DNS
# member listed in $UPTIME_BENCH_DNS_CONTROL_URLS, using the bearer
# token in $CONTROL_TOKEN. We still defensively strip a leading "*."
# from CERTBOT_DOMAIN — certbot strips it for the manual plugin today,
# but the safety strip means a future certbot change won't silently
# encode a wildcard prefix into the TXT owner name.
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

: "${CERTBOT_DOMAIN:?certbot env CERTBOT_DOMAIN not set}"
: "${CERTBOT_VALIDATION:?certbot env CERTBOT_VALIDATION not set}"
: "${UPTIME_BENCH_DNS_CONTROL_URLS:?set UPTIME_BENCH_DNS_CONTROL_URLS to your DNS member control URLs}"
: "${CONTROL_TOKEN:?set CONTROL_TOKEN to the DNS control token}"

identifier="${CERTBOT_DOMAIN#\*.}"
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
