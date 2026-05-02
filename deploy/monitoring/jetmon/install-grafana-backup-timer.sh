#!/usr/bin/env bash
set -euo pipefail

stack_dir="${STACK_DIR:-/home/jetmon/jetmon-monitoring}"
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

if ! command -v sqlite3 >/dev/null 2>&1; then
  sudo apt-get update
  sudo apt-get install -y sqlite3
fi

if [[ "${script_dir}/backup-grafana.sh" != "${stack_dir}/backup-grafana.sh" ]]; then
  sudo install -m 0755 -o root -g root "${script_dir}/backup-grafana.sh" "${stack_dir}/backup-grafana.sh"
fi

tmp_service="$(mktemp)"
tmp_timer="$(mktemp)"
trap 'rm -f "$tmp_service" "$tmp_timer"' EXIT

cat >"$tmp_service" <<SERVICE
[Unit]
Description=Back up Jetmon Grafana SQLite database

[Service]
Type=oneshot
ExecStart=${stack_dir}/backup-grafana.sh
SERVICE

cat >"$tmp_timer" <<TIMER
[Unit]
Description=Run Jetmon Grafana backup daily

[Timer]
OnCalendar=daily
Persistent=true
RandomizedDelaySec=15m

[Install]
WantedBy=timers.target
TIMER

sudo install -m 0644 -o root -g root "$tmp_service" /etc/systemd/system/jetmon-grafana-backup.service
sudo install -m 0644 -o root -g root "$tmp_timer" /etc/systemd/system/jetmon-grafana-backup.timer
sudo systemctl daemon-reload
sudo systemctl enable --now jetmon-grafana-backup.timer
sudo systemctl start jetmon-grafana-backup.service
systemctl list-timers jetmon-grafana-backup.timer
