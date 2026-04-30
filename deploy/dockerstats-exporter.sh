#!/usr/bin/env bash
# Build and deploy the Docker stats Prometheus exporter to a Jetmon host.
#
# Usage:
#   deploy/dockerstats-exporter.sh <host> [user]
#
# The exporter is installed at /usr/local/bin and run in a small Docker
# container so Docker publishes the scrape port consistently with cAdvisor.

set -euo pipefail

HOST="${1:-}"
REMOTE_USER="${2:-jetmon}"
PORT="${PORT:-9103}"
IMAGE="${IMAGE:-alpine:3.20}"
CONTAINER_NAME="${CONTAINER_NAME:-uptime-bench-dockerstats-exporter}"

if [[ -z "$HOST" ]]; then
    echo "Usage: $0 <host> [user]" >&2
    exit 1
fi

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

BINARY="bin/uptime-bench-dockerstats-exporter"
REMOTE_STAGING="/tmp/uptime-bench-dockerstats-exporter.new"
REMOTE_BIN="/usr/local/bin/uptime-bench-dockerstats-exporter"

echo "==> Building dockerstats exporter for linux/amd64"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o "$BINARY" ./cmd/uptime-bench-dockerstats-exporter

echo "==> Copying exporter to ${REMOTE_USER}@${HOST}"
scp "$BINARY" "${REMOTE_USER}@${HOST}:${REMOTE_STAGING}"

echo "==> Installing and starting Docker-published exporter on ${HOST}:${PORT}"
# shellcheck disable=SC2029
ssh "${REMOTE_USER}@${HOST}" "
    set -euo pipefail
    sudo install -m 755 -o root -g root ${REMOTE_STAGING} ${REMOTE_BIN}
    rm -f ${REMOTE_STAGING}
    sudo systemctl disable --now uptime-bench-dockerstats-exporter.service >/dev/null 2>&1 || true
    docker rm -f ${CONTAINER_NAME} >/dev/null 2>&1 || true
    docker run -d \
      --name ${CONTAINER_NAME} \
      --restart unless-stopped \
      -p ${PORT}:9103 \
      -v ${REMOTE_BIN}:${REMOTE_BIN}:ro \
      -v /var/run/docker.sock:/var/run/docker.sock:ro \
      --memory=64m \
      --cpus=0.25 \
      --read-only \
      --security-opt no-new-privileges:true \
      --cap-drop=ALL \
      ${IMAGE} \
      ${REMOTE_BIN} -listen=:9103
    docker ps --filter name=${CONTAINER_NAME} --format '{{.Names}} {{.Status}} {{.Ports}}'
"

echo "==> Done"
