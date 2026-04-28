#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  CONTROL_URL=http://TARGET_IP:9000 TARGET_HOST=bench-a.example.com CONTROL_TOKEN_FILE=/path/to/control-token deploy/tls-smoke.sh

Required:
  CONTROL_URL         Target control API root, e.g. http://203.0.113.20:9000
  TARGET_HOST         HTTPS SNI/Host value to test, e.g. bench-a.example.com
  CONTROL_TOKEN       Bearer token for the control API, or
  CONTROL_TOKEN_FILE  File containing the bearer token

Optional:
  TARGET_IP           IP to connect to while sending TARGET_HOST as SNI/Host.
                      Defaults to TARGET_HOST.
  HTTPS_PORT          HTTPS data-plane port. Defaults to 443.
  DURATION_NS         Failure activation duration in nanoseconds. Defaults to 60000000000.
EOF
}

require() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
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

activate() {
  local failure_type="$1"
  local params_json="$2"
  local host_json
  host_json="$(json_escape "$TARGET_HOST")"
  control_post "/activate" \
    "{\"run_id\":\"${RUN_ID}\",\"seed\":1,\"failure\":{\"type\":\"${failure_type}\",\"host\":\"${host_json}\",\"duration\":${DURATION_NS},\"rate\":1,\"params\":${params_json}}}"
}

deactivate() {
  local failure_type="$1"
  local host_json
  host_json="$(json_escape "$TARGET_HOST")"
  control_post "/deactivate" \
    "{\"run_id\":\"${RUN_ID}\",\"failure_type\":\"${failure_type}\",\"host\":\"${host_json}\"}" || true
}

healthy_https() {
  local resolve_args=()
  if [ "$CONNECT_HOST" != "$TARGET_HOST" ]; then
    resolve_args=(--resolve "${TARGET_HOST}:${HTTPS_PORT}:${CONNECT_HOST}")
  fi
  curl -fsSk "${resolve_args[@]}" "https://${TARGET_HOST}:${HTTPS_PORT}/" >/dev/null
}

openssl_s_client() {
  openssl s_client \
    -connect "${CONNECT_HOST}:${HTTPS_PORT}" \
    -servername "$TARGET_HOST" \
    "$@"
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

require curl
require openssl

: "${CONTROL_URL:?set CONTROL_URL}"
: "${TARGET_HOST:?set TARGET_HOST}"

TOKEN="$(control_token)"
TARGET_IP="${TARGET_IP:-}"
CONNECT_HOST="${TARGET_IP:-$TARGET_HOST}"
HTTPS_PORT="${HTTPS_PORT:-443}"
DURATION_NS="${DURATION_NS:-60000000000}"
RUN_ID="tls-smoke-$(date +%s)"

echo "== healthy HTTPS"
healthy_https

echo "== tls_handshake should fail"
activate "tls_handshake" '{"reason":"version_mismatch"}'
trap 'deactivate "tls_handshake"; deactivate "tls_deprecated"' EXIT
set +e
handshake_output="$(openssl_s_client -tls1_3 -brief </dev/null 2>&1)"
handshake_status=$?
set -e
deactivate "tls_handshake"
if [ "$handshake_status" -eq 0 ]; then
  echo "tls_handshake unexpectedly succeeded" >&2
  echo "$handshake_output" >&2
  exit 1
fi

echo "== tls_deprecated should negotiate TLSv1.1"
activate "tls_deprecated" '{"variant":"TLS11"}'
deprecated_output="$(openssl_s_client -tls1_1 -cipher 'DEFAULT:@SECLEVEL=0' -brief </dev/null 2>&1)"
deactivate "tls_deprecated"
if ! printf '%s\n' "$deprecated_output" | grep -q 'TLSv1.1'; then
  echo "tls_deprecated did not report TLSv1.1" >&2
  echo "$deprecated_output" >&2
  exit 1
fi

echo "TLS deployed-target smoke passed for ${TARGET_HOST} via ${CONNECT_HOST}:${HTTPS_PORT}"
