#!/usr/bin/env bash
# cleanup.sh — Certbot manual-cleanup-hook for uptime-bench DNS-01.
#
# Certbot invokes this after each identifier finishes validating (or
# fails), with the same env vars as the auth hook. This script removes
# the matching (name, value) from every DNS member, leaving any
# sibling values for the same name in place — apex and wildcard share
# the _acme-challenge name and are torn down in separate hook calls.
#
# See auth.sh for the full env-var list and config example.

set -euo pipefail

: "${CERTBOT_IDENTIFIER:?certbot env CERTBOT_IDENTIFIER not set}"
: "${CERTBOT_VALIDATION:?certbot env CERTBOT_VALIDATION not set}"
: "${UPTIME_BENCH_DNS_CONTROL_URLS:?set UPTIME_BENCH_DNS_CONTROL_URLS to your DNS member control URLs}"
: "${UPTIME_BENCH_CONTROL_TOKEN:?set UPTIME_BENCH_CONTROL_TOKEN to the DNS control token}"

identifier="${CERTBOT_IDENTIFIER#\*.}"
name="_acme-challenge.${identifier}"
value="${CERTBOT_VALIDATION}"

payload=$(printf '{"name":"%s","value":"%s"}' "${name}" "${value}")

for dns_control_url in ${UPTIME_BENCH_DNS_CONTROL_URLS}; do
  echo "acme-cleanup: DELETE ${dns_control_url}/acme/txt name=${name}" >&2
  curl -fsS -X DELETE "${dns_control_url}/acme/txt" \
    -H "Authorization: Bearer ${UPTIME_BENCH_CONTROL_TOKEN}" \
    -H "Content-Type: application/json" \
    --data "${payload}"
done
