#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  CONTROL_URL=http://TARGET_IP:9000 TARGET_HOST=bench-a.example.com CONTROL_TOKEN_FILE=/path/to/control-token deploy/target-smoke.sh

Required:
  CONTROL_URL         Target control API root, e.g. http://203.0.113.20:9000
  TARGET_HOST         HTTP Host / HTTPS SNI value to test, e.g. bench-a.example.com
  CONTROL_TOKEN       Bearer token for the control API, or
  CONTROL_TOKEN_FILE  File containing the bearer token

Optional:
  TARGET_IP           IP to connect to while sending TARGET_HOST as Host/SNI.
                      Defaults to TARGET_HOST.
  HTTP_PORT           HTTP data-plane port. Defaults to 80.
  HTTPS_PORT          HTTPS data-plane port. Defaults to 443.
  PATH_UNDER_TEST     Request path. Defaults to /.
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

control_get() {
  local path="$1"
  curl -fsS \
    -H "Authorization: Bearer ${TOKEN}" \
    "${CONTROL_URL}${path}"
}

activate() {
  local failure_type="$1"
  local params_json="${2:-{}}"
  local host_json path_json
  host_json="$(json_escape "$TARGET_HOST")"
  path_json="$(json_escape "$PATH_UNDER_TEST")"
  control_post "/activate" \
    "{\"run_id\":\"${RUN_ID}\",\"seed\":1,\"failure\":{\"type\":\"${failure_type}\",\"host\":\"${host_json}\",\"path\":\"${path_json}\",\"duration\":${DURATION_NS},\"rate\":1,\"params\":${params_json}}}"
}

activate_global() {
  local failure_type="$1"
  local params_json="${2:-{}}"
  control_post "/activate" \
    "{\"run_id\":\"${RUN_ID}\",\"seed\":1,\"failure\":{\"type\":\"${failure_type}\",\"duration\":${DURATION_NS},\"rate\":1,\"params\":${params_json}}}"
}

activate_hostwide() {
  local failure_type="$1"
  local params_json="${2:-{}}"
  local host_json
  host_json="$(json_escape "$TARGET_HOST")"
  control_post "/activate" \
    "{\"run_id\":\"${RUN_ID}\",\"seed\":1,\"failure\":{\"type\":\"${failure_type}\",\"host\":\"${host_json}\",\"duration\":${DURATION_NS},\"rate\":1,\"params\":${params_json}}}"
}

deactivate() {
  local failure_type="$1"
  local host_json path_json
  host_json="$(json_escape "$TARGET_HOST")"
  path_json="$(json_escape "$PATH_UNDER_TEST")"
  control_post "/deactivate" \
    "{\"run_id\":\"${RUN_ID}\",\"failure_type\":\"${failure_type}\",\"host\":\"${host_json}\",\"path\":\"${path_json}\"}" || true
}

deactivate_hostwide() {
  local failure_type="$1"
  local host_json
  host_json="$(json_escape "$TARGET_HOST")"
  control_post "/deactivate" \
    "{\"run_id\":\"${RUN_ID}\",\"failure_type\":\"${failure_type}\",\"host\":\"${host_json}\"}" || true
}

deactivate_global() {
  local failure_type="$1"
  control_post "/deactivate" \
    "{\"run_id\":\"${RUN_ID}\",\"failure_type\":\"${failure_type}\"}" || true
}

cleanup() {
  deactivate "http_status"
  deactivate "http_method_status"
  deactivate "http_timeout"
  deactivate "http_partial"
  deactivate "http_body"
  deactivate "http_redirect"
  deactivate_hostwide "tls_invalid"
  deactivate_hostwide "tls_handshake"
  deactivate_hostwide "tls_deprecated"
  deactivate_global "tcp_refused"
}

http_url() {
  printf 'http://%s:%s%s' "$TARGET_HOST" "$HTTP_PORT" "$PATH_UNDER_TEST"
}

https_url() {
  printf 'https://%s:%s%s' "$TARGET_HOST" "$HTTPS_PORT" "$PATH_UNDER_TEST"
}

curl_resolve_args() {
  local port="$1"
  if [ "$CONNECT_HOST" != "$TARGET_HOST" ]; then
    printf '%s\n' --resolve "${TARGET_HOST}:${port}:${CONNECT_HOST}"
  fi
}

http_code() {
  local method="$1"
  local max_time="${2:-5}"
  local body_file="$3"
  local args=()
  mapfile -t args < <(curl_resolve_args "$HTTP_PORT")
  if [ "$method" = "HEAD" ]; then
    curl -sS --max-time "$max_time" "${args[@]}" --head -o "$body_file" -w '%{http_code}' "$(http_url)"
    return
  fi
  curl -sS --max-time "$max_time" "${args[@]}" -X "$method" -o "$body_file" -w '%{http_code}' "$(http_url)"
}

https_code() {
  local method="$1"
  local max_time="${2:-5}"
  local body_file="$3"
  local insecure="${4:-}"
  local args=()
  mapfile -t args < <(curl_resolve_args "$HTTPS_PORT")
  if [ "$method" = "HEAD" ]; then
    curl -sS --max-time "$max_time" "${args[@]}" ${insecure:+-k} --head -o "$body_file" -w '%{http_code}' "$(https_url)"
    return
  fi
  curl -sS --max-time "$max_time" "${args[@]}" ${insecure:+-k} -X "$method" -o "$body_file" -w '%{http_code}' "$(https_url)"
}

expect_http_code() {
  local label="$1"
  local method="$2"
  local want="$3"
  local body_file
  body_file="$(mktemp)"
  local got
  got="$(http_code "$method" 5 "$body_file")"
  rm -f "$body_file"
  if [ "$got" != "$want" ]; then
    echo "${label}: got HTTP ${got}, want ${want}" >&2
    exit 1
  fi
}

expect_https_code_insecure() {
  local label="$1"
  local method="$2"
  local want="$3"
  local body_file
  body_file="$(mktemp)"
  local got
  got="$(https_code "$method" 5 "$body_file" "insecure")"
  rm -f "$body_file"
  if [ "$got" != "$want" ]; then
    echo "${label}: got HTTPS ${got}, want ${want}" >&2
    exit 1
  fi
}

expect_https_code_verified() {
  local label="$1"
  local method="$2"
  local want="$3"
  local body_file
  body_file="$(mktemp)"
  local got
  got="$(https_code "$method" 5 "$body_file")"
  rm -f "$body_file"
  if [ "$got" != "$want" ]; then
    echo "${label}: got HTTPS ${got}, want ${want}" >&2
    exit 1
  fi
}

expect_body_contains() {
  local label="$1"
  local needle="$2"
  local body_file
  body_file="$(mktemp)"
  local got
  got="$(http_code "GET" 5 "$body_file")"
  if [ "$got" != "200" ]; then
    echo "${label}: got HTTP ${got}, want 200" >&2
    cat "$body_file" >&2 || true
    rm -f "$body_file"
    exit 1
  fi
  if ! grep -q "$needle" "$body_file"; then
    echo "${label}: body missing ${needle}" >&2
    cat "$body_file" >&2 || true
    rm -f "$body_file"
    exit 1
  fi
  rm -f "$body_file"
}

expect_http_curl_failure() {
  local label="$1"
  local max_time="${2:-2}"
  local args=()
  mapfile -t args < <(curl_resolve_args "$HTTP_PORT")
  set +e
  curl -fsS --max-time "$max_time" "${args[@]}" "$(http_url)" >/tmp/uptime-bench-target-smoke-http.out 2>/tmp/uptime-bench-target-smoke-http.err
  local status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    echo "${label}: curl unexpectedly succeeded" >&2
    cat /tmp/uptime-bench-target-smoke-http.out >&2 || true
    exit 1
  fi
}

expect_https_curl_failure() {
  local label="$1"
  local max_time="${2:-5}"
  local args=()
  mapfile -t args < <(curl_resolve_args "$HTTPS_PORT")
  set +e
  curl -fsS --max-time "$max_time" "${args[@]}" "$(https_url)" >/tmp/uptime-bench-target-smoke-https.out 2>/tmp/uptime-bench-target-smoke-https.err
  local status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    echo "${label}: curl unexpectedly succeeded" >&2
    cat /tmp/uptime-bench-target-smoke-https.out >&2 || true
    exit 1
  fi
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
require grep
require mktemp
require openssl
require sed

: "${CONTROL_URL:?set CONTROL_URL}"
: "${TARGET_HOST:?set TARGET_HOST}"

TOKEN="$(control_token)"
TARGET_IP="${TARGET_IP:-}"
CONNECT_HOST="${TARGET_IP:-$TARGET_HOST}"
HTTP_PORT="${HTTP_PORT:-80}"
HTTPS_PORT="${HTTPS_PORT:-443}"
PATH_UNDER_TEST="${PATH_UNDER_TEST:-/}"
DURATION_NS="${DURATION_NS:-60000000000}"
RUN_ID="target-smoke-$(date +%s)"

trap cleanup EXIT

echo "== healthy HTTP baseline"
expect_http_code "healthy GET" "GET" "200"
expect_http_code "healthy HEAD" "HEAD" "200"
expect_body_contains "healthy body" "uptime-bench-canary"

echo "== http_status should return configured status"
activate "http_status" '{"status_code":503}'
expect_http_code "http_status GET" "GET" "503"
deactivate "http_status"
expect_http_code "http_status recovery" "GET" "200"

echo "== http_method_status HEAD failure with GET still healthy"
activate "http_method_status" '{"method":"HEAD","status_code":405}'
expect_http_code "http_method_status HEAD" "HEAD" "405"
expect_http_code "http_method_status GET unaffected" "GET" "200"
deactivate "http_method_status"

echo "== http_method_status GET failure with HEAD still healthy"
activate "http_method_status" '{"method":"GET","status_code":503}'
expect_http_code "http_method_status GET" "GET" "503"
expect_http_code "http_method_status HEAD unaffected" "HEAD" "200"
deactivate "http_method_status"

echo "== http_body should silently alter content"
activate "http_body" '{"content":"defacement"}'
expect_body_contains "http_body defacement" "H4CK3D"
deactivate "http_body"
expect_body_contains "http_body recovery" "uptime-bench-canary"

echo "== http_redirect should return redirect"
activate "http_redirect" '{"variant":"loop"}'
expect_http_code "http_redirect loop" "GET" "302"
deactivate "http_redirect"

echo "== http_partial should fail body transfer"
activate "http_partial" '{"truncate_after_bytes":32}'
expect_http_curl_failure "http_partial" 5
deactivate "http_partial"

echo "== http_timeout should exceed client timeout"
activate "http_timeout" '{"delay":"2s"}'
expect_http_curl_failure "http_timeout" 1
deactivate "http_timeout"

echo "== tcp_refused should close the HTTP connection"
activate_global "tcp_refused" '{}'
expect_http_curl_failure "tcp_refused" 2
deactivate_global "tcp_refused"

echo "== healthy HTTPS baseline"
expect_https_code_verified "healthy HTTPS GET" "GET" "200"
set +e
healthy_tls_output="$(openssl_s_client -brief </dev/null 2>&1)"
healthy_tls_status=$?
set -e
if [ "$healthy_tls_status" -ne 0 ]; then
  echo "healthy TLS handshake failed" >&2
  echo "$healthy_tls_output" >&2
  exit 1
fi

echo "== tls_invalid self_signed should fail normal verification but serve with -k"
activate_hostwide "tls_invalid" '{"variant":"self_signed"}'
expect_https_curl_failure "tls_invalid self_signed" 5
expect_https_code_insecure "tls_invalid self_signed insecure" "GET" "200"
deactivate_hostwide "tls_invalid"

echo "== tls_invalid hostname_mismatch should fail normal verification but serve with -k"
activate_hostwide "tls_invalid" '{"variant":"hostname_mismatch"}'
expect_https_curl_failure "tls_invalid hostname_mismatch" 5
expect_https_code_insecure "tls_invalid hostname_mismatch insecure" "GET" "200"
deactivate_hostwide "tls_invalid"

echo "== tls_handshake should fail the TLS handshake"
activate_hostwide "tls_handshake" '{"reason":"version_mismatch"}'
set +e
handshake_output="$(openssl_s_client -tls1_3 -brief </dev/null 2>&1)"
handshake_status=$?
set -e
deactivate_hostwide "tls_handshake"
if [ "$handshake_status" -eq 0 ]; then
  echo "tls_handshake unexpectedly succeeded" >&2
  echo "$handshake_output" >&2
  exit 1
fi

echo "== tls_deprecated should negotiate TLSv1.1"
activate_hostwide "tls_deprecated" '{"variant":"TLS11"}'
deprecated_output="$(openssl_s_client -tls1_1 -cipher 'DEFAULT:@SECLEVEL=0' -brief </dev/null 2>&1)"
deactivate_hostwide "tls_deprecated"
if ! printf '%s\n' "$deprecated_output" | grep -q 'TLSv1.1'; then
  echo "tls_deprecated did not report TLSv1.1" >&2
  echo "$deprecated_output" >&2
  exit 1
fi

echo "== control status should be clean"
status_json="$(control_get "/status")"
if ! printf '%s\n' "$status_json" | grep -q '"active_failures":\[\]'; then
  echo "target still has active failures after smoke run" >&2
  printf '%s\n' "$status_json" >&2
  exit 1
fi

echo "Target deployed-fleet smoke passed for ${TARGET_HOST} via ${CONNECT_HOST}"
