#!/usr/bin/env bash
set -euo pipefail

host="${1:-monitoring.example.com}"
dest="${DEST:-/opt/jetmon-monitoring}"
ssh_config="${SSH_CONFIG:-$HOME/.ssh/config}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

tar -C "$script_dir" \
  --exclude=.env \
  --exclude=backups \
  --exclude=dbs \
  -czf - . \
  | ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" "mkdir -p '$dest' && tar -xzf - -C '$dest'"

ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" "cd '$dest' && sudo docker compose -p jetmon-monitoring up -d"
ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" "cd '$dest' && sudo docker compose -p jetmon-monitoring up -d --force-recreate prometheus"
ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" "cd '$dest' && sudo docker compose -p jetmon-monitoring restart grafana"
