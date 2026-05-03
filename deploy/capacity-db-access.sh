#!/usr/bin/env bash
# Provision stable DB access for Jetmon capacity runs.
#
# The script keeps database ports private by creating systemd-managed SSH
# tunnels from the orchestrator host to each Jetmon service host. It creates a
# scoped MySQL user on each service DB and writes 0600 DSN files on the
# orchestrator host for uptime-bench capacity runs.

set -euo pipefail

SSH_CONFIG="${SSH_CONFIG:-$HOME/.ssh/config}"
ORCHESTRATOR="${ORCHESTRATOR:-jetmon-vm-host-3}"
ORCHESTRATOR_ADDR="${ORCHESTRATOR_ADDR:-10.0.0.67}"
SERVICE_V1_HOST="${SERVICE_V1_HOST:-jetmon-service-host-1}"
SERVICE_V2_HOST="${SERVICE_V2_HOST:-jetmon-service-host-2}"
SERVICE_V1_ADDR="${SERVICE_V1_ADDR:-10.0.0.170}"
SERVICE_V2_ADDR="${SERVICE_V2_ADDR:-10.0.0.171}"
REMOTE_DB_PORT="${REMOTE_DB_PORT:-3307}"
LOCAL_V1_PORT="${LOCAL_V1_PORT:-13307}"
LOCAL_V2_PORT="${LOCAL_V2_PORT:-23307}"
DB_USER="${DB_USER:-uptime_bench_capacity}"
INSTALL_ROOT="${INSTALL_ROOT:-/home/jetmon/uptime-bench-capacity}"
SECRET_DIR="${SECRET_DIR:-${INSTALL_ROOT}/secrets}"
TUNNEL_KEY="${TUNNEL_KEY:-/home/jetmon/.ssh/id_uptime_bench_capacity_ed25519}"

ssh_base=(-F "$SSH_CONFIG" -o BatchMode=yes -o ControlMaster=no -S none)

run_ssh() {
  ssh "${ssh_base[@]}" "$@"
}

run_scp() {
  scp -O -F "$SSH_CONFIG" -o BatchMode=yes -o ControlMaster=no "$@"
}

sql_string() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\'/\'\'}"
  printf "%s" "$value"
}

sql_ident() {
  local value="$1"
  value="${value//\`/\`\`}"
  printf "%s" "$value"
}

shell_quote() {
  printf "%q" "$1"
}

new_password() {
  openssl rand -hex 24
}

remote_env_value() {
  local host="$1"
  local env_path="$2"
  local var_name="$3"
  run_ssh "$host" "bash -s" -- "$env_path" "$var_name" <<'REMOTE'
set -euo pipefail
env_path="$1"
key="$2"
line="$(grep -E "^${key}=" "$env_path" | tail -n 1 || true)"
value="${line#*=}"
value="${value%$'\r'}"
case "$value" in
  \"*\") value="${value#\"}"; value="${value%\"}" ;;
  \'*\') value="${value#\'}"; value="${value%\'}" ;;
esac
printf '%s' "$value"
REMOTE
}

ensure_orchestrator_key() {
  echo "==> Ensuring orchestrator SSH tunnel key on ${ORCHESTRATOR}"
  run_ssh "$ORCHESTRATOR" "umask 077; mkdir -p /home/jetmon/.ssh; test -f '$TUNNEL_KEY' || ssh-keygen -t ed25519 -N '' -C 'uptime-bench-capacity@'\"\$(hostname)\" -f '$TUNNEL_KEY' >/dev/null; chmod 600 '$TUNNEL_KEY'; chmod 644 '${TUNNEL_KEY}.pub'"
}

install_tunnel_key() {
  local host="$1"
  local public_key="$2"
  local restricted
  local encoded
  restricted="${public_key}"
  encoded="$(printf '%s' "$restricted" | base64 | tr -d '\n')"
  echo "==> Authorizing tunnel key on ${host}"
  run_ssh "$host" "bash -s" -- "$encoded" "$REMOTE_DB_PORT" <<'REMOTE'
set -euo pipefail
line="$(printf '%s' "$1" | base64 -d)"
remote_db_port="$2"
mkdir -p "$HOME/.ssh"
chmod 700 "$HOME/.ssh"
touch "$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"
tmp="$(mktemp)"
grep -vxF "restrict,port-forwarding,permitopen=127.0.0.1:${remote_db_port}" "$HOME/.ssh/authorized_keys" > "$tmp" || true
grep -vxF "restrict,port-forwarding,permitopen=\"127.0.0.1:${remote_db_port}\" ssh-ed25519" "$tmp" > "${tmp}.next" || true
mv "${tmp}.next" "$tmp"
grep -v "permitopen=\"127.0.0.1:${remote_db_port}\" .*uptime-bench-capacity@" "$tmp" > "${tmp}.next" || true
mv "${tmp}.next" "$tmp"
grep -v "uptime-bench-capacity@" "$tmp" > "${tmp}.next" || true
mv "${tmp}.next" "$tmp"
cat "$tmp" > "$HOME/.ssh/authorized_keys"
rm -f "$tmp"
grep -qxF "$line" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$line" >> "$HOME/.ssh/authorized_keys"
REMOTE
}

install_sshd_tunnel_policy() {
  local host="$1"
  echo "==> Allowing scoped SSH local forwarding on ${host}"
  run_ssh "$host" "sudo tee /etc/ssh/sshd_config.d/60-uptime-bench-capacity-tunnels.conf >/dev/null" <<CONF
Match User jetmon Address ${ORCHESTRATOR_ADDR}
    AllowTcpForwarding local
    PermitOpen 127.0.0.1:${REMOTE_DB_PORT} localhost:${REMOTE_DB_PORT}
CONF
  run_ssh "$host" "sudo sshd -t && sudo systemctl reload ssh"
}

mysql_exec() {
  local host="$1"
  local env_path="$2"
  local root_var="$3"
  local db_var="$4"
  local container="$5"
  local sql="$6"
  local root_password
  local database
  root_password="$(remote_env_value "$host" "$env_path" "$root_var")"
  database="$(remote_env_value "$host" "$env_path" "$db_var")"
  if [[ -z "$root_password" || -z "$database" ]]; then
    echo "Missing ${root_var} or ${db_var} on ${host}:${env_path}" >&2
    exit 1
  fi
  printf '%s\n' "$sql" | run_ssh "$host" "sudo docker exec -i -e MYSQL_PWD=$(shell_quote "$root_password") $(shell_quote "$container") mysql --protocol=tcp -uroot $(shell_quote "$database")"
}

provision_db_user() {
  local label="$1"
  local host="$2"
  local env_path="$3"
  local root_var="$4"
  local db_var="$5"
  local container="$6"
  local password="$7"
  local database
  local escaped_user
  local escaped_password
  local escaped_database

  database="$(remote_env_value "$host" "$env_path" "$db_var")"
  if [[ -z "$database" ]]; then
    echo "Missing ${db_var} on ${host}:${env_path}" >&2
    exit 1
  fi

  escaped_user="$(sql_string "$DB_USER")"
  escaped_password="$(sql_string "$password")"
  escaped_database="$(sql_ident "$database")"

  echo "==> Creating scoped ${label} MySQL user on ${host}" >&2
  mysql_exec "$host" "$env_path" "$root_var" "$db_var" "$container" "
CREATE USER IF NOT EXISTS '${escaped_user}'@'localhost' IDENTIFIED BY '${escaped_password}';
ALTER USER '${escaped_user}'@'localhost' IDENTIFIED BY '${escaped_password}';
CREATE USER IF NOT EXISTS '${escaped_user}'@'127.0.0.1' IDENTIFIED BY '${escaped_password}';
ALTER USER '${escaped_user}'@'127.0.0.1' IDENTIFIED BY '${escaped_password}';
CREATE USER IF NOT EXISTS '${escaped_user}'@'172.%' IDENTIFIED BY '${escaped_password}';
ALTER USER '${escaped_user}'@'172.%' IDENTIFIED BY '${escaped_password}';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE TEMPORARY TABLES ON \`${escaped_database}\`.* TO '${escaped_user}'@'localhost';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE TEMPORARY TABLES ON \`${escaped_database}\`.* TO '${escaped_user}'@'127.0.0.1';
GRANT SELECT, INSERT, UPDATE, DELETE, CREATE TEMPORARY TABLES ON \`${escaped_database}\`.* TO '${escaped_user}'@'172.%';
FLUSH PRIVILEGES;
"
  printf '%s' "$database"
}

write_dsn_file() {
  local service="$1"
  local path="$2"
  local dsn="$3"
  echo "==> Writing ${service} DSN file on ${ORCHESTRATOR}: ${path}"
  printf '%s\n' "$dsn" | run_ssh "$ORCHESTRATOR" "umask 077; mkdir -p '$SECRET_DIR'; cat > '$path'; chmod 600 '$path'"
}

install_tunnel_service() {
  local service="$1"
  local remote_addr="$2"
  local local_port="$3"
  local unit_name="uptime-bench-capacity-tunnel-${service}.service"

  echo "==> Installing ${unit_name} on ${ORCHESTRATOR}"
  run_ssh "$ORCHESTRATOR" "sudo tee '/etc/systemd/system/${unit_name}' >/dev/null" <<UNIT
[Unit]
Description=uptime-bench capacity ${service} MySQL SSH tunnel
After=network-online.target
Wants=network-online.target

[Service]
User=jetmon
ExecStart=/usr/bin/ssh -i ${TUNNEL_KEY} -o BatchMode=yes -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o StrictHostKeyChecking=accept-new -N -L 127.0.0.1:${local_port}:127.0.0.1:${REMOTE_DB_PORT} jetmon@${remote_addr}
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  run_ssh "$ORCHESTRATOR" "sudo systemctl daemon-reload && sudo systemctl enable --now '${unit_name}' && sudo systemctl restart '${unit_name}'"
}

install_runner() {
  echo "==> Building and installing capacity runner on ${ORCHESTRATOR}"
  GOOS=linux GOARCH=amd64 go build -o bin/uptime-bench-jetmon-capacity-run ./cmd/uptime-bench-jetmon-capacity-run
  run_ssh "$ORCHESTRATOR" "mkdir -p '${INSTALL_ROOT}/bin' '${INSTALL_ROOT}/configs' '${INSTALL_ROOT}/reports'"
  run_scp bin/uptime-bench-jetmon-capacity-run "${ORCHESTRATOR}:${INSTALL_ROOT}/bin/uptime-bench-jetmon-capacity-run"
  run_scp configs/capacity/jetmon.fleet.toml "${ORCHESTRATOR}:${INSTALL_ROOT}/configs/jetmon.fleet.toml"
  run_ssh "$ORCHESTRATOR" "chmod 755 '${INSTALL_ROOT}/bin/uptime-bench-jetmon-capacity-run'"
}

verify_tunnel() {
  local service="$1"
  local port="$2"
  echo "==> Verifying ${service} tunnel on ${ORCHESTRATOR}:127.0.0.1:${port}"
  run_ssh "$ORCHESTRATOR" "timeout 5 bash -lc '</dev/tcp/127.0.0.1/${port}'"
}

main() {
  ensure_orchestrator_key
  public_key="$(run_ssh "$ORCHESTRATOR" "cat '${TUNNEL_KEY}.pub'")"
  install_tunnel_key "$SERVICE_V1_HOST" "$public_key"
  install_tunnel_key "$SERVICE_V2_HOST" "$public_key"
  install_sshd_tunnel_policy "$SERVICE_V1_HOST"
  install_sshd_tunnel_policy "$SERVICE_V2_HOST"

  v1_password="$(new_password)"
  v2_password="$(new_password)"
  v1_database="$(provision_db_user "v1" "$SERVICE_V1_HOST" "/home/jetmon/jetmon/docker/.env" "MYSQLDB_ROOT_PASSWORD" "MYSQLDB_DATABASE" "jetmon-mysqldb-1" "$v1_password")"
  v2_database="$(provision_db_user "v2" "$SERVICE_V2_HOST" "/home/jetmon/jetmon/docker/.env" "MYSQL_ROOT_PASSWORD" "MYSQL_DATABASE" "docker-mysqldb-1" "$v2_password")"

  install_tunnel_service "v1" "$SERVICE_V1_ADDR" "$LOCAL_V1_PORT"
  install_tunnel_service "v2" "$SERVICE_V2_ADDR" "$LOCAL_V2_PORT"

  v1_dsn="${DB_USER}:${v1_password}@tcp(127.0.0.1:${LOCAL_V1_PORT})/${v1_database}?parseTime=true&loc=UTC"
  v2_dsn="${DB_USER}:${v2_password}@tcp(127.0.0.1:${LOCAL_V2_PORT})/${v2_database}?parseTime=true&loc=UTC"
  write_dsn_file "v1" "${SECRET_DIR}/jetmon-v1-capacity.dsn" "$v1_dsn"
  write_dsn_file "v2" "${SECRET_DIR}/jetmon-v2-capacity.dsn" "$v2_dsn"

  install_runner
  verify_tunnel "v1" "$LOCAL_V1_PORT"
  verify_tunnel "v2" "$LOCAL_V2_PORT"

  echo "==> Capacity DB access is ready on ${ORCHESTRATOR}"
  echo "Run from ${ORCHESTRATOR}:"
  echo "  cd ${INSTALL_ROOT}"
  echo "  ./bin/uptime-bench-jetmon-capacity-run -config=configs/jetmon.fleet.toml -mode=verify -apply"
}

main "$@"
