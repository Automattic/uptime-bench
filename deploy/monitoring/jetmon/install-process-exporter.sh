#!/usr/bin/env bash
set -euo pipefail

hosts=("$@")
if [[ "${#hosts[@]}" -eq 0 ]]; then
  hosts=(jetmon-service-host-1 jetmon-service-host-2)
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ssh_config="${SSH_CONFIG:-/home/gaarai/.ssh/config}"

for host in "${hosts[@]}"; do
  echo "Installing process exporter on ${host}"
  scp -O -F "$ssh_config" -o ControlMaster=no -o ControlPath=none \
    "${script_dir}/configs/process-exporter/jetmon-process-exporter.yml" \
    "${host}:/tmp/process-exporter.yml"
  scp -O -F "$ssh_config" -o ControlMaster=no -o ControlPath=none \
    "${script_dir}/configs/process-exporter/prometheus-process-exporter.default" \
    "${host}:/tmp/prometheus-process-exporter.default"
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo apt-get update
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo apt-get install -y prometheus-process-exporter
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo mkdir -p /etc/prometheus
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo install -m 0644 -o root -g root /tmp/process-exporter.yml /etc/prometheus/process-exporter.yml
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo install -m 0644 -o root -g root /tmp/prometheus-process-exporter.default /etc/default/prometheus-process-exporter
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo systemctl enable --now prometheus-process-exporter
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo systemctl restart prometheus-process-exporter
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo ufw allow from 10.0.0.0/24 to any port 9256 proto tcp
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" sudo ufw allow from 100.64.0.0/10 to any port 9256 proto tcp
  ssh -F "$ssh_config" -S none -o BatchMode=yes -o ControlMaster=no "$host" curl -fsS http://127.0.0.1:9256/metrics >/dev/null
done
