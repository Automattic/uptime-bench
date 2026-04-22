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

COMPONENT="${1:-}"
HOST="${2:-}"
REMOTE_USER="${3:-ubuntu}"

if [[ -z "$COMPONENT" || -z "$HOST" ]]; then
    echo "Usage: $0 <harness|target|dns> <host> [user]" >&2
    exit 1
fi

case "$COMPONENT" in
    harness) CMD_PATH="./cmd/harness" ;;
    target)  CMD_PATH="./cmd/target"  ;;
    dns)     CMD_PATH="./cmd/dns"     ;;
    *)
        echo "Unknown component: $COMPONENT. Must be one of: harness, target, dns" >&2
        exit 1
        ;;
esac

BINARY="bin/uptime-bench-${COMPONENT}"
SERVICE="uptime-bench-${COMPONENT}"
REMOTE_BIN="/usr/local/bin/uptime-bench-${COMPONENT}"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

echo "==> Building $COMPONENT for linux/amd64..."
GOOS=linux GOARCH=amd64 go build -o "$BINARY" "$CMD_PATH"

echo "==> Copying binary to ${REMOTE_USER}@${HOST}:${REMOTE_BIN}..."
# Copy to a temporary path first, then move atomically to avoid replacing the
# running binary in-place (which can cause "text file busy" on Linux).
REMOTE_TMP="${REMOTE_BIN}.new"
scp "$BINARY" "${REMOTE_USER}@${HOST}:${REMOTE_TMP}"

echo "==> Installing binary and restarting service..."
# shellcheck disable=SC2029
ssh "${REMOTE_USER}@${HOST}" "
    sudo mv ${REMOTE_TMP} ${REMOTE_BIN}
    sudo chmod 755 ${REMOTE_BIN}
    # Replacing the binary clears any file capabilities set by provision.sh.
    # Re-apply cap_net_bind_service for target and dns servers (needed to bind
    # ports 80/443 and 53 without running as root).
    if [[ '${COMPONENT}' == 'target' || '${COMPONENT}' == 'dns' ]]; then
        sudo setcap 'cap_net_bind_service=+ep' ${REMOTE_BIN}
    fi
    sudo systemctl restart ${SERVICE}
    sudo systemctl status ${SERVICE} --no-pager -l
"

echo "==> Done. ${SERVICE} restarted on ${HOST}."
