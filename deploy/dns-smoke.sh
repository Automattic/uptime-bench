#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  CONTROL_URL=http://DNS_IP:9100 DNS_SERVER=DNS_IP TARGET_HOST=bench-a.example.com CONTROL_TOKEN_FILE=/path/to/control-token deploy/dns-smoke.sh

Required:
  CONTROL_URL         DNS member control API root, e.g. http://203.0.113.53:9100
  DNS_SERVER          DNS server address for direct queries
  TARGET_HOST         A record to query through the member
  CONTROL_TOKEN       Bearer token for the control API, or
  CONTROL_TOKEN_FILE  File containing the bearer token

Optional:
  EXPECTED_A          Expected A response. If unset, the baseline answer is used.
  DURATION_NS         Failure activation duration in nanoseconds. Defaults to 10000000000.
EOF
}

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

control_token() {
  if [ "${CONTROL_TOKEN:-}" != "" ]; then
    printf '%s' "$CONTROL_TOKEN"
    return
  fi
  if [ "${CONTROL_TOKEN_FILE:-}" != "" ]; then
    tr -d '\r\n' < "$CONTROL_TOKEN_FILE"
    return
  fi
  echo "set CONTROL_TOKEN or CONTROL_TOKEN_FILE" >&2
  exit 1
}

control_post() {
  local path="$1"
  local body="$2"
  curl -fsS \
    -H "Authorization: Bearer ${TOKEN}" \
    -H "Content-Type: application/json" \
    -X POST \
    --data "$body" \
    "${CONTROL_URL}${path}" >/dev/null
}

control_get() {
  local path="$1"
  curl -fsS \
    -H "Authorization: Bearer ${TOKEN}" \
    "${CONTROL_URL}${path}"
}

activate() {
  local failure_type="$1"
  local params_json="${2:-{}}"
  control_post "/activate" \
    "{\"run_id\":\"${RUN_ID}\",\"seed\":1,\"failure\":{\"type\":\"${failure_type}\",\"duration\":${DURATION_NS},\"rate\":1,\"params\":${params_json}}}"
}

deactivate() {
  local failure_type="$1"
  control_post "/deactivate" \
    "{\"run_id\":\"${RUN_ID}\",\"failure_type\":\"${failure_type}\"}" || true
}

cleanup() {
  deactivate "dns_nxdomain"
  deactivate "dns_servfail"
  deactivate "dns_timeout"
  deactivate "dns_cname_nxdomain"
  deactivate "dns_latency"
  deactivate "dns_ns_unavailable"
}

dig_short() {
  dig "@${DNS_SERVER}" "$TARGET_HOST" A +time=1 +tries=1 +short
}

dig_status() {
  dig "@${DNS_SERVER}" "$TARGET_HOST" A +time=1 +tries=1
}

expect_a() {
  local label="$1"
  local got
  got="$(dig_short)"
  if [ "$got" != "$EXPECTED_A" ]; then
    echo "${label}: got A ${got:-<empty>}, want ${EXPECTED_A}" >&2
    exit 1
  fi
}

expect_dig_contains() {
  local label="$1"
  local needle="$2"
  local got
  got="$(dig_status)"
  if ! printf '%s\n' "$got" | grep -q "$needle"; then
    echo "${label}: dig output missing ${needle}" >&2
    printf '%s\n' "$got" >&2
    exit 1
  fi
}

expect_timeout() {
  local label="$1"
  set +e
  output="$(dig_status 2>&1)"
  set -e
  if printf '%s\n' "$output" | grep -q 'no servers could be reached'; then
    return
  fi
  echo "${label}: query unexpectedly produced a response" >&2
  printf '%s\n' "$output" >&2
  exit 1
}

expect_latency() {
  local label="$1"
  local start end elapsed_ms
  start="$(date +%s%3N)"
  expect_a "$label"
  end="$(date +%s%3N)"
  elapsed_ms=$((end - start))
  if [ "$elapsed_ms" -lt 250 ]; then
    echo "${label}: elapsed ${elapsed_ms}ms, want at least 250ms" >&2
    exit 1
  fi
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

require curl
require date
require dig
require grep

: "${CONTROL_URL:?set CONTROL_URL}"
: "${DNS_SERVER:?set DNS_SERVER}"
: "${TARGET_HOST:?set TARGET_HOST}"

TOKEN="$(control_token)"
DURATION_NS="${DURATION_NS:-10000000000}"
RUN_ID="dns-smoke-$(date +%s)"

trap cleanup EXIT

if [ "${EXPECTED_A:-}" = "" ]; then
  EXPECTED_A="$(dig_short)"
fi
if [ "$EXPECTED_A" = "" ]; then
  echo "baseline A lookup returned no answer" >&2
  exit 1
fi

echo "== healthy DNS baseline"
expect_a "healthy A"

echo "== dns_nxdomain should return NXDOMAIN"
activate "dns_nxdomain" '{}'
expect_dig_contains "dns_nxdomain" 'status: NXDOMAIN'
deactivate "dns_nxdomain"
expect_a "dns_nxdomain recovery"

echo "== dns_servfail should return SERVFAIL"
activate "dns_servfail" '{}'
expect_dig_contains "dns_servfail" 'status: SERVFAIL'
deactivate "dns_servfail"

echo "== dns_timeout should drop the response"
activate "dns_timeout" '{}'
expect_timeout "dns_timeout"
deactivate "dns_timeout"

echo "== dns_cname_nxdomain should return a broken CNAME"
activate "dns_cname_nxdomain" '{}'
expect_dig_contains "dns_cname_nxdomain" 'CNAME'
deactivate "dns_cname_nxdomain"

echo "== dns_latency should delay a valid response"
activate "dns_latency" '{"added_latency":"300ms"}'
expect_latency "dns_latency"
deactivate "dns_latency"

echo "== dns_ns_unavailable servfail mode should return SERVFAIL"
activate "dns_ns_unavailable" '{"mode":"servfail"}'
expect_dig_contains "dns_ns_unavailable servfail" 'status: SERVFAIL'
deactivate "dns_ns_unavailable"

echo "== dns_ns_unavailable silent mode should drop the response"
activate "dns_ns_unavailable" '{"mode":"silent"}'
expect_timeout "dns_ns_unavailable silent"
deactivate "dns_ns_unavailable"

echo "== control status should be clean"
status_json="$(control_get "/status")"
if ! printf '%s\n' "$status_json" | grep -q '"active_failures":\[\]'; then
  echo "dns member still has active failures after smoke run" >&2
  printf '%s\n' "$status_json" >&2
  exit 1
fi

echo "DNS deployed-fleet smoke passed for ${TARGET_HOST} via ${DNS_SERVER}"
