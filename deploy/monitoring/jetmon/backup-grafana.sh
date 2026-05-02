#!/usr/bin/env bash
set -euo pipefail

stack_dir="${STACK_DIR:-/home/jetmon/jetmon-monitoring}"
backup_dir="${BACKUP_DIR:-${stack_dir}/backups/grafana}"
retention_days="${RETENTION_DAYS:-30}"
db_path="${GRAFANA_DB:-${stack_dir}/dbs/grafana/grafana.db}"

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "sqlite3 is required for a consistent Grafana backup" >&2
  exit 1
fi

if [[ ! -f "$db_path" ]]; then
  echo "Grafana database not found: $db_path" >&2
  exit 1
fi

umask 077
mkdir -p "$backup_dir"

timestamp="$(date -u +%Y%m%d-%H%M%SZ)"
backup_path="${backup_dir}/grafana-${timestamp}.db"

sqlite3 "$db_path" ".backup ${backup_path}"
gzip -f "$backup_path"
find "$backup_dir" -name 'grafana-*.db.gz' -type f -mtime +"$retention_days" -delete

echo "${backup_path}.gz"
