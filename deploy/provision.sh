#!/usr/bin/env bash
# provision.sh — provision or re-provision an uptime-bench fleet server.
#
# Copies the provisioning script to the remote host and runs it as root.
# Safe to run multiple times; the server-side script is idempotent.
#
# Usage:
#   ./deploy/provision.sh --type <type> --host <host> [options]
#
# Required:
#   --type TYPE       Server role: harness | target | dns
#   --host HOST       SSH-reachable hostname or IP
#
# Optional:
#   --user USER       SSH login user with sudo access (default: ubuntu)
#   --harness-ip IP   Restrict control port to this source IP (recommended for
#                     target and dns servers; omit to allow from any IP)
#   --ssh-port PORT   SSH port on the remote host (default: 22)
#   --skip-swap       Do not create a swap file (if the host already has swap)
#
# Examples:
#   ./deploy/provision.sh --type target --host 203.0.113.20 --harness-ip 203.0.113.5
#   ./deploy/provision.sh --type dns    --host 203.0.113.10 --harness-ip 203.0.113.5
#   ./deploy/provision.sh --type harness --host 203.0.113.5

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

TYPE=""
HOST=""
SSH_USER="ubuntu"
HARNESS_IP=""
SSH_PORT="22"
EXTRA_ARGS=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --type)        TYPE="$2";        shift 2 ;;
        --host)        HOST="$2";        shift 2 ;;
        --user)        SSH_USER="$2";    shift 2 ;;
        --harness-ip)  HARNESS_IP="$2";  shift 2 ;;
        --ssh-port)    SSH_PORT="$2";    shift 2 ;;
        --skip-swap)   EXTRA_ARGS="$EXTRA_ARGS --skip-swap"; shift ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: $0 --type <harness|target|dns> --host <host> [--user USER] [--harness-ip IP] [--ssh-port PORT] [--skip-swap]" >&2
            exit 1
            ;;
    esac
done

if [[ -z "$TYPE" || -z "$HOST" ]]; then
    echo "Error: --type and --host are required." >&2
    exit 1
fi

case "$TYPE" in
    harness|target|dns) ;;
    *)
        echo "Error: --type must be one of: harness, target, dns" >&2
        exit 1
        ;;
esac

SSH_OPTS="-p $SSH_PORT -o StrictHostKeyChecking=accept-new"
# scp uses -P for port (lowercase -p means preserve times/modes).
SCP_OPTS="-P $SSH_PORT -o StrictHostKeyChecking=accept-new"

# ---------------------------------------------------------------------------
# Upload scripts and systemd unit, then run provisioning
# ---------------------------------------------------------------------------

echo "==> Uploading provisioning script to ${SSH_USER}@${HOST}..."
scp $SCP_OPTS \
    "${REPO_ROOT}/deploy/provision-server.sh" \
    "${SSH_USER}@${HOST}:/tmp/provision-server.sh"

echo "==> Uploading systemd unit for ${TYPE}..."
scp $SCP_OPTS \
    "${REPO_ROOT}/deploy/systemd/uptime-bench-${TYPE}.service" \
    "${SSH_USER}@${HOST}:/tmp/uptime-bench-${TYPE}.service"

# Upload example config files for the roles that consume them.
# Harness reads both; DNS reads fleet.toml for zone records; target needs neither.
if [[ "$TYPE" == "harness" || "$TYPE" == "dns" ]]; then
    echo "==> Uploading fleet.example.toml..."
    scp $SCP_OPTS \
        "${REPO_ROOT}/fleet.example.toml" \
        "${SSH_USER}@${HOST}:/tmp/fleet.example.toml"
fi
if [[ "$TYPE" == "harness" ]]; then
    echo "==> Uploading services.example.toml..."
    scp $SCP_OPTS \
        "${REPO_ROOT}/services.example.toml" \
        "${SSH_USER}@${HOST}:/tmp/services.example.toml"
fi

echo "==> Running provisioning on ${HOST} (type: ${TYPE})..."

PROVISION_CMD="sudo bash /tmp/provision-server.sh --type ${TYPE}"
[[ -n "$HARNESS_IP" ]]  && PROVISION_CMD="$PROVISION_CMD --harness-ip $HARNESS_IP"
[[ -n "$EXTRA_ARGS" ]]  && PROVISION_CMD="$PROVISION_CMD $EXTRA_ARGS"

# shellcheck disable=SC2029
ssh $SSH_OPTS "${SSH_USER}@${HOST}" "$PROVISION_CMD"

echo ""
echo "==> Provisioning complete on ${HOST}."
echo "    Next step: deploy the binary with:"
echo "      make deploy-${TYPE} ${TYPE^^}_HOST=${HOST}"
