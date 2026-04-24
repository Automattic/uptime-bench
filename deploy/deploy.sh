#!/usr/bin/env bash
# deploy.sh — build and push an uptime-bench binary to a remote host.
#
# Usage:
#   ./deploy/deploy.sh <component> <host> [user]
#
# Components:  harness | target | dns
# host:        SSH-reachable hostname or IP
# user:        SSH/admin user with sudo access (default: ubuntu)
#              This is NOT the uptime-bench service account (which has no shell).
#              Run provision.sh first to set up the host.
#
# Examples:
#   ./deploy/deploy.sh harness harness.internal.example.com
#   ./deploy/deploy.sh target  203.0.113.20
#   ./deploy/deploy.sh dns     203.0.113.10
#
# Requirements:
#   - Go toolchain available locally
#   - SSH access to the remote host (key-based auth, sudo privileges)
#   - Host already provisioned via deploy/provision.sh
#   - The binary destination on the remote host is /usr/local/bin/

set -euo pipefail

# ---------------------------------------------------------------------------
# Output helpers (colour-aware, NO_COLOR-respecting)
# ---------------------------------------------------------------------------

if [[ "${NO_COLOR:-}" == "" ]] && { [[ -t 1 ]] || [[ "${FORCE_COLOR:-}" == "1" ]]; }; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_CYAN=$'\033[36m'
else
    C_RESET= C_BOLD= C_RED= C_GREEN= C_CYAN=
fi

section() { echo; echo "${C_BOLD}${C_CYAN}==>${C_RESET} ${C_BOLD}$*${C_RESET}"; }
ok()      { echo "    ${C_GREEN}[ok]${C_RESET} $*"; }
err()     { echo "    ${C_BOLD}${C_RED}[ERROR]${C_RESET} $*" >&2; }

COMPONENT="${1:-}"
HOST="${2:-}"
REMOTE_USER="${3:-ubuntu}"

if [[ -z "$COMPONENT" || -z "$HOST" ]]; then
    err "Usage: $0 <harness|target|dns> <host> [user]"
    exit 1
fi

case "$COMPONENT" in
    harness) CMD_PATH="./cmd/harness" ;;
    target)  CMD_PATH="./cmd/target"  ;;
    dns)     CMD_PATH="./cmd/dns"     ;;
    *)
        err "Unknown component: $COMPONENT. Must be one of: harness, target, dns"
        exit 1
        ;;
esac

BINARY="bin/uptime-bench-${COMPONENT}"
SERVICE="uptime-bench-${COMPONENT}"
REMOTE_BIN="/usr/local/bin/uptime-bench-${COMPONENT}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

section "Building $COMPONENT for linux/amd64"
GOOS=linux GOARCH=amd64 go build -o "$BINARY" "$CMD_PATH"

section "Copying binary to ${REMOTE_USER}@${HOST}:${REMOTE_BIN}"
# scp to a user-writable staging path; sudo install handles ownership and
# the atomic temp-file-plus-rename that avoids ETXTBSY when overwriting a
# running binary.
REMOTE_STAGING="/tmp/uptime-bench-${COMPONENT}.new"
scp "$BINARY" "${REMOTE_USER}@${HOST}:${REMOTE_STAGING}"

section "Installing binary and restarting service"
# shellcheck disable=SC2029
ssh "${REMOTE_USER}@${HOST}" "
    sudo install -m 755 -o root -g root ${REMOTE_STAGING} ${REMOTE_BIN}
    rm -f ${REMOTE_STAGING}
    # install replaces the binary, clearing any file capabilities. Re-apply
    # cap_net_bind_service for target and dns servers (needed to bind
    # ports 80/443 and 53 without running as root).
    if [[ '${COMPONENT}' == 'target' || '${COMPONENT}' == 'dns' ]]; then
        sudo setcap 'cap_net_bind_service=+ep' ${REMOTE_BIN}
        sudo systemctl restart ${SERVICE}
        sudo systemctl status ${SERVICE} --no-pager -l
    else
        echo 'Harness systemd unit is vestigial (binary requires -scenario per run); skipping restart.'
    fi
"

section "Done — ${SERVICE} on ${HOST}"
ok "Deploy finished."
