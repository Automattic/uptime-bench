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
            err "Unknown argument: $1"
            err "Usage: $0 --type <harness|target|dns> --host <host> [--user USER] [--harness-ip IP] [--ssh-port PORT] [--skip-swap]"
            exit 1
            ;;
    esac
done

if [[ -z "$TYPE" || -z "$HOST" ]]; then
    err "--type and --host are required."
    exit 1
fi

case "$TYPE" in
    harness|target|dns) ;;
    *)
        err "--type must be one of: harness, target, dns"
        exit 1
        ;;
esac

SSH_OPTS="-p $SSH_PORT -o StrictHostKeyChecking=accept-new"
# scp uses -P for port (lowercase -p means preserve times/modes).
SCP_OPTS="-P $SSH_PORT -o StrictHostKeyChecking=accept-new"

# ---------------------------------------------------------------------------
# Upload scripts and systemd unit, then run provisioning
# ---------------------------------------------------------------------------

section "Uploading provisioning script to ${SSH_USER}@${HOST}"
scp $SCP_OPTS \
    "${REPO_ROOT}/deploy/provision-server.sh" \
    "${SSH_USER}@${HOST}:/tmp/provision-server.sh"

section "Uploading systemd unit for ${TYPE}"
scp $SCP_OPTS \
    "${REPO_ROOT}/deploy/systemd/uptime-bench-${TYPE}.service" \
    "${SSH_USER}@${HOST}:/tmp/uptime-bench-${TYPE}.service"

# Upload example config files for the roles that consume them.
# Harness reads both; DNS reads fleet.toml for zone records; target needs neither.
if [[ "$TYPE" == "harness" || "$TYPE" == "dns" ]]; then
    section "Uploading fleet.example.toml"
    scp $SCP_OPTS \
        "${REPO_ROOT}/fleet.example.toml" \
        "${SSH_USER}@${HOST}:/tmp/fleet.example.toml"
fi
if [[ "$TYPE" == "harness" ]]; then
    section "Uploading services.example.toml"
    scp $SCP_OPTS \
        "${REPO_ROOT}/services.example.toml" \
        "${SSH_USER}@${HOST}:/tmp/services.example.toml"
fi

section "Running provisioning on ${HOST} (type: ${TYPE})"

# Forward colour preference to the remote provision-server.sh. Its stdout
# isn't a terminal over ssh, so it needs FORCE_COLOR. sudo strips the
# environment by default, so the variable is passed as a sudo argument
# (sudo accepts NAME=value pairs before the command).
SUDO_ENV=""
[[ -n "$C_RESET" ]] && SUDO_ENV="FORCE_COLOR=1 "

PROVISION_CMD="sudo ${SUDO_ENV}bash /tmp/provision-server.sh --type ${TYPE} --deploy-user ${SSH_USER} --ssh-port ${SSH_PORT}"
[[ -n "$HARNESS_IP" ]]  && PROVISION_CMD="$PROVISION_CMD --harness-ip $HARNESS_IP"
[[ -n "$EXTRA_ARGS" ]]  && PROVISION_CMD="$PROVISION_CMD $EXTRA_ARGS"

# shellcheck disable=SC2029
ssh $SSH_OPTS "${SSH_USER}@${HOST}" "$PROVISION_CMD"

section "Provisioning complete on ${HOST}"
ok "Next step: deploy the binary with:"
echo "      ${C_BOLD}make deploy-${TYPE} ${TYPE^^}_HOST=${HOST} DEPLOY_USER=${SSH_USER}${C_RESET}"
