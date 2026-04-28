#!/usr/bin/env bash
# provision-server.sh — configure an uptime-bench fleet server.
#
# Runs ON the server as root. Idempotent — safe to run multiple times for
# updates and re-configuration. Targets Ubuntu Server 24.04 LTS.
#
# Usage (direct on the server):
#   sudo bash provision-server.sh --type <harness|target|dns|certmint> [options]
#
# Options:
#   --type TYPE         Server role: harness | target | dns | certmint  (required)
#   --harness-ip IP     Restrict the control API port to requests from this IP.
#                       Recommended for target and dns servers. If omitted, the
#                       control port is accessible from any source.
#   --deploy-user USER  The SSH/admin user to preserve in firewall and SSH config.
#                       (default: ubuntu)
#   --ssh-port PORT     SSH port to allow through the firewall (default: 22)
#   --skip-swap         Skip swap file creation. Use if the host already has swap.
#
# Hardening applied:
#   - All packages updated
#   - SSH: root login disabled, password auth disabled, max auth tries reduced
#   - UFW: default deny inbound; only required ports opened per server type
#   - fail2ban: SSH brute-force protection
#   - Unattended-upgrades: automatic security patch installation
#   - Sysctl: TCP SYN cookies, reverse-path filtering, ICMP redirect rejection
#   - NTP: systemd-timesyncd confirmed running (UTC clock; accurate time is
#          critical for benchmark latency measurements)
#   - Swap: 2 GB swap file created if no swap exists

set -euo pipefail

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------

TYPE=""
HARNESS_IP=""
DEPLOY_USER="ubuntu"
SSH_PORT="22"
SKIP_SWAP=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --type)         TYPE="$2";         shift 2 ;;
        --harness-ip)   HARNESS_IP="$2";   shift 2 ;;
        --deploy-user)  DEPLOY_USER="$2";  shift 2 ;;
        --ssh-port)     SSH_PORT="$2";     shift 2 ;;
        --skip-swap)    SKIP_SWAP=true;    shift ;;
        *)
            echo "Unknown argument: $1" >&2
            exit 1
            ;;
    esac
done

if [[ -z "$TYPE" ]]; then
    echo "Error: --type is required." >&2
    exit 1
fi

case "$TYPE" in
    harness|target|dns|certmint) ;;
    *)
        echo "Error: --type must be one of: harness, target, dns, certmint" >&2
        exit 1
        ;;
esac

if [[ $EUID -ne 0 ]]; then
    echo "Error: this script must be run as root (use sudo)." >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

# ANSI color setup. Honours NO_COLOR (https://no-color.org/) and inherits
# colour preference when called from a parent script that sets FORCE_COLOR=1
# (provision.sh forwards FORCE_COLOR over ssh so remote output stays coloured).
if [[ "${NO_COLOR:-}" == "" ]] && { [[ -t 1 ]] || [[ "${FORCE_COLOR:-}" == "1" ]]; }; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_DIM=$'\033[2m'
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_CYAN=$'\033[36m'
else
    C_RESET= C_BOLD= C_DIM= C_RED= C_GREEN= C_YELLOW= C_CYAN=
fi

section() { echo; echo "${C_BOLD}${C_CYAN}==>${C_RESET} ${C_BOLD}$*${C_RESET}"; }
ok()      { echo "    ${C_GREEN}[ok]${C_RESET} $*"; }
info()    { echo "    ${C_DIM}[--]${C_RESET} $*"; }
warn()    { echo "    ${C_YELLOW}[!!]${C_RESET} $*" >&2; }
err()     { echo "    ${C_BOLD}${C_RED}[ERROR]${C_RESET} $*" >&2; }

# ---------------------------------------------------------------------------
# Phase 1: System update
# ---------------------------------------------------------------------------

section "Updating system packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get upgrade -y -qq
ok "System packages up to date"

# ---------------------------------------------------------------------------
# Phase 2: Install required packages
# ---------------------------------------------------------------------------

section "Installing required packages"
apt-get install -y -qq \
    ufw \
    fail2ban \
    unattended-upgrades \
    apt-listchanges \
    curl \
    wget \
    htop \
    rsync \
    jq \
    ca-certificates \
    logrotate \
    chrony
ok "Packages installed"

# Certmint role needs certbot for ACME issuance. Installed only when the
# role calls for it so other fleet members don't carry an unused
# Python/letsencrypt-client footprint.
if [[ "$TYPE" == "certmint" ]]; then
    apt-get install -y -qq certbot
    ok "Installed certbot for certmint role"
fi

# ---------------------------------------------------------------------------
# Phase 3: Time synchronisation (critical for benchmark accuracy)
# ---------------------------------------------------------------------------

section "Configuring time synchronisation"

# Use chrony for more accurate NTP than systemd-timesyncd.
# Disable timesyncd to avoid conflicts.
systemctl disable --now systemd-timesyncd 2>/dev/null || true
systemctl enable --now chrony
timedatectl set-timezone UTC
ok "chrony enabled, timezone set to UTC"

# ---------------------------------------------------------------------------
# Phase 4: Create service user and directories
# ---------------------------------------------------------------------------

section "Creating uptime-bench service user"

if ! id -u uptime-bench &>/dev/null; then
    useradd \
        --system \
        --no-create-home \
        --shell /usr/sbin/nologin \
        --comment "uptime-bench service account" \
        uptime-bench
    ok "Created uptime-bench system user"
else
    ok "uptime-bench user already exists"
fi

# Configuration directory: owned by root, readable by uptime-bench.
# Credential files inside should be 0640 root:uptime-bench.
install -d -m 750 -o root -g uptime-bench /etc/uptime-bench
ok "Created /etc/uptime-bench (750 root:uptime-bench)"

# Ensure the current hostname resolves locally via /etc/hosts. Without this,
# disabling systemd-resolved's stub listener on DNS hosts leaves sudo and
# other tools unable to self-resolve the hostname, which produces noisy
# "unable to resolve host" warnings even though operations still succeed.
# 127.0.1.1 is Ubuntu's convention for the local hostname (distinct from
# 127.0.0.1, which is reserved for localhost).
HOSTNAME_SHORT="$(hostname -s)"
if [[ -n "$HOSTNAME_SHORT" ]] && ! grep -qE "[[:space:]]${HOSTNAME_SHORT}([[:space:]]|$)" /etc/hosts; then
    echo "127.0.1.1 ${HOSTNAME_SHORT}" >> /etc/hosts
    ok "Added 127.0.1.1 ${HOSTNAME_SHORT} to /etc/hosts"
else
    info "/etc/hosts already resolves ${HOSTNAME_SHORT}"
fi

# ---------------------------------------------------------------------------
# Phase 4b: Skeleton credential and config files
# ---------------------------------------------------------------------------
# .example files are always overwritten so they track the latest documented
# format. The operator copies them to the real names and fills in secrets.
# Real credential files (without the .example suffix) are never touched here.

section "Writing skeleton credential and config files"

case "$TYPE" in
    harness)
        cat > /etc/uptime-bench/harness.env.example <<'EOF'
# uptime-bench harness environment file.
#
# To use:
#   sudo cp harness.env.example harness.env
#   sudo chown root:uptime-bench harness.env
#   sudo chmod 640 harness.env
#   sudoedit harness.env
#
# DB_DSN: MySQL connection string for the harness's event log database.
#   Format: user:password@tcp(host:3306)/uptime_bench?parseTime=true
#
# CONTROL_TOKEN: shared bearer token used for control-plane requests to
#   every fleet member. Generate once with `openssl rand -hex 32` and
#   reuse the exact same value on every fleet VM.
# Values are quoted so this file is valid both for systemd's
# EnvironmentFile= parser and for shell sourcing (the parens in tcp(...)
# would otherwise be a bash syntax error during `. harness.env`).
DB_DSN="uptime_bench:CHANGE_ME@tcp(127.0.0.1:3306)/uptime_bench?parseTime=true"
CONTROL_TOKEN="CHANGE_ME"
EOF
        ;;
    target)
        cat > /etc/uptime-bench/target.env.example <<'EOF'
# uptime-bench target environment file.
#
# To use:
#   sudo cp target.env.example target.env
#   sudo chown root:uptime-bench target.env
#   sudo chmod 640 target.env
#   sudoedit target.env
#
# CONTROL_TOKEN: shared bearer token for control-plane requests.
#   Must match the value in /etc/uptime-bench/harness.env on the harness VM.
#
# MEMBER_ID: this VM's id field from the [[targets]] block in fleet.toml.
#   The target binary reports this id in control responses so the harness
#   can correlate results across a multi-target fleet.
CONTROL_TOKEN=CHANGE_ME
MEMBER_ID=target-XX
EOF
        ;;
    dns)
        cat > /etc/uptime-bench/dns.env.example <<'EOF'
# uptime-bench DNS environment file.
#
# To use:
#   sudo cp dns.env.example dns.env
#   sudo chown root:uptime-bench dns.env
#   sudo chmod 640 dns.env
#   sudoedit dns.env
#
# CONTROL_TOKEN: shared bearer token for control-plane requests.
#   Must match the value in /etc/uptime-bench/harness.env on the harness VM.
#
# MEMBER_ID: this VM's id field from the [[nameservers]] block in fleet.toml.
#   The DNS binary uses it to look up which zones it should serve.
CONTROL_TOKEN=CHANGE_ME
MEMBER_ID=ns-XX
EOF
        ;;
    certmint)
        cat > /etc/uptime-bench/certmint.env.example <<'EOF'
# uptime-bench certmint environment file.
#
# To use:
#   sudo cp certmint.env.example certmint.env
#   sudo chown root:uptime-bench certmint.env
#   sudo chmod 640 certmint.env
#   sudoedit certmint.env
#
# CONTROL_TOKEN: shared bearer token for control-plane requests. The
#   certbot manual hooks use this to PUT/DELETE TXT records on every
#   uptime-bench-dns member, and (Phase B) the cert-library HTTP server
#   uses it to authenticate target polls.
#   Must match the value in /etc/uptime-bench/harness.env on the harness VM.
#
# UPTIME_BENCH_DNS_CONTROL_URLS: space-separated control base URLs of every
#   uptime-bench-dns member. The certbot manual-auth and manual-cleanup hooks
#   read this to fan out TXT challenge records.
#   Example: "http://203.0.113.10:9100 http://203.0.113.11:9100"
CONTROL_TOKEN=CHANGE_ME
UPTIME_BENCH_CONTROL_TOKEN=CHANGE_ME
UPTIME_BENCH_DNS_CONTROL_URLS="http://CHANGE_ME:9100"
EOF
        ;;
esac
chmod 640 "/etc/uptime-bench/${TYPE}.env.example"
chown root:uptime-bench "/etc/uptime-bench/${TYPE}.env.example"
ok "Wrote /etc/uptime-bench/${TYPE}.env.example"

cat > /etc/uptime-bench/control-token.example <<'EOF'
# Replace this entire file with the same hex string used for CONTROL_TOKEN
# in /etc/uptime-bench/harness.env on the harness VM.
#
# To use:
#   sudo cp control-token.example control-token
#   sudo chown root:uptime-bench control-token
#   sudo chmod 640 control-token
#   sudoedit control-token   # delete these comments and paste the token
EOF
chmod 640 /etc/uptime-bench/control-token.example
chown root:uptime-bench /etc/uptime-bench/control-token.example
ok "Wrote /etc/uptime-bench/control-token.example"

# Move uploaded example config files into place when present.
for ex in fleet.example.toml services.example.toml; do
    if [[ -f "/tmp/$ex" ]]; then
        install -m 640 -o root -g uptime-bench "/tmp/$ex" "/etc/uptime-bench/$ex"
        rm -f "/tmp/$ex"
        ok "Installed /etc/uptime-bench/$ex"
    fi
done

# Certmint role: install its own example config files + writable state
# dirs. The library and certbot account material live under
# /var/lib/uptime-bench-certmint so they survive package upgrades and
# accidental /etc edits; they're owned by the uptime-bench service
# account so the daemon can write without elevated privileges.
if [[ "$TYPE" == "certmint" ]]; then
    install -d -m 700 -o uptime-bench -g uptime-bench /var/lib/uptime-bench-certmint
    install -d -m 700 -o uptime-bench -g uptime-bench /var/lib/uptime-bench/certs
    ok "Created /var/lib/uptime-bench-certmint and /var/lib/uptime-bench/certs"

    if [[ -f /tmp/certmint.example.json ]]; then
        install -m 640 -o root -g uptime-bench /tmp/certmint.example.json \
            /etc/uptime-bench/certmint.example.json
        rm -f /tmp/certmint.example.json
        ok "Installed /etc/uptime-bench/certmint.example.json"
    fi
    if [[ -f /tmp/rfc2136.ini.example ]]; then
        install -m 640 -o root -g uptime-bench /tmp/rfc2136.ini.example \
            /etc/uptime-bench/rfc2136.ini.example
        rm -f /tmp/rfc2136.ini.example
        ok "Installed /etc/uptime-bench/rfc2136.ini.example"
    fi
fi

# ---------------------------------------------------------------------------
# Phase 4c: Create operator config files from skeletons (non-destructive)
# ---------------------------------------------------------------------------
# Create each real config file the first time from its skeleton with the
# correct ownership and mode, so a forgotten chown/chmod after provisioning
# cannot leave the service unable to read its config. Existing real files
# are left untouched except that their ownership and mode are re-asserted.
#
# control-token is intentionally NOT auto-created on the harness: its
# skeleton is pure instruction text, and auto-creating it would let the
# harness start and then fail opaquely on the first control-plane call with
# an invalid bearer token. The operator still sees cp/chown/chmod/edit
# instructions for that one file below.

section "Creating operator config files from skeletons"

ensure_config_file() {
    local src="$1"
    local dst="$2"
    if [[ -f "$dst" ]]; then
        chown root:uptime-bench "$dst"
        chmod 640 "$dst"
        info "$(basename "$dst") exists — ownership/mode re-asserted"
    elif [[ -f "$src" ]]; then
        install -m 640 -o root -g uptime-bench "$src" "$dst"
        ok "Created $dst from $(basename "$src")"
    else
        warn "Cannot create $dst: skeleton $src is missing"
    fi
}

ensure_config_file "/etc/uptime-bench/${TYPE}.env.example" "/etc/uptime-bench/${TYPE}.env"

case "$TYPE" in
    harness)
        ensure_config_file "/etc/uptime-bench/fleet.example.toml"    "/etc/uptime-bench/fleet.toml"
        ensure_config_file "/etc/uptime-bench/services.example.toml" "/etc/uptime-bench/services.toml"
        ;;
    dns)
        ensure_config_file "/etc/uptime-bench/fleet.example.toml"    "/etc/uptime-bench/fleet.toml"
        ;;
    certmint)
        ensure_config_file "/etc/uptime-bench/certmint.example.json" "/etc/uptime-bench/certmint.json"
        ;;
esac

# ---------------------------------------------------------------------------
# Phase 5: SSH hardening
# ---------------------------------------------------------------------------

section "Hardening SSH configuration"

SSHD_CONF="/etc/ssh/sshd_config.d/99-uptime-bench.conf"
cat > "$SSHD_CONF" << EOF
# uptime-bench hardening — managed by provision-server.sh
PermitRootLogin no
PasswordAuthentication no
ChallengeResponseAuthentication no
PubkeyAuthentication yes
X11Forwarding no
MaxAuthTries 3
LoginGraceTime 30
ClientAliveInterval 300
ClientAliveCountMax 2
AllowUsers ${DEPLOY_USER}
EOF

chmod 600 "$SSHD_CONF"

# The systemd unit is named ssh.service on Debian/Ubuntu and sshd.service on
# RHEL/Fedora. Probe each candidate directly with systemctl cat.
SSH_UNIT=""
for candidate in ssh sshd; do
    if systemctl cat "${candidate}.service" >/dev/null 2>&1; then
        SSH_UNIT="$candidate"
        break
    fi
done

if [[ -z "$SSH_UNIT" ]]; then
    err "No SSH service unit found (tried ssh.service and sshd.service)."
    err "Investigate with: systemctl list-units --type=service | grep -i ssh"
    exit 1
fi

# Validate config before reloading to avoid locking ourselves out.
if sshd -t; then
    systemctl reload "$SSH_UNIT"
    ok "SSH hardening applied and ${SSH_UNIT} reloaded"
else
    err "sshd config test failed — NOT reloading ${SSH_UNIT}. Fix $SSHD_CONF."
    exit 1
fi

# ---------------------------------------------------------------------------
# Phase 6: Sysctl network hardening
# ---------------------------------------------------------------------------

section "Applying sysctl hardening"

cat > /etc/sysctl.d/99-uptime-bench.conf << 'EOF'
# TCP SYN flood protection
net.ipv4.tcp_syncookies = 1

# Reverse-path filtering (block spoofed packets)
net.ipv4.conf.default.rp_filter = 1
net.ipv4.conf.all.rp_filter = 1

# Reject ICMP redirects
net.ipv4.conf.all.accept_redirects = 0
net.ipv4.conf.default.accept_redirects = 0
net.ipv6.conf.all.accept_redirects = 0
net.ipv6.conf.default.accept_redirects = 0

# Do not send ICMP redirects
net.ipv4.conf.all.send_redirects = 0
net.ipv4.conf.default.send_redirects = 0

# Reject source-routed packets
net.ipv4.conf.all.accept_source_route = 0
net.ipv4.conf.default.accept_source_route = 0
net.ipv6.conf.all.accept_source_route = 0

# Log packets with impossible source addresses
net.ipv4.conf.all.log_martians = 1
net.ipv4.conf.default.log_martians = 1

# Restrict kernel pointer exposure
kernel.kptr_restrict = 2

# Restrict dmesg to root
kernel.dmesg_restrict = 1
EOF

sysctl --system -q
ok "Sysctl hardening applied"

# ---------------------------------------------------------------------------
# Phase 7: Firewall (UFW)
# ---------------------------------------------------------------------------

section "Configuring firewall (UFW)"

ufw --force reset

ufw default deny incoming
ufw default allow outgoing
ufw default deny forward

# SSH — always allowed from any source so we don't lock ourselves out.
ufw allow "${SSH_PORT}/tcp" comment "SSH"

# Type-specific inbound rules.
case "$TYPE" in
    target)
        ufw allow 80/tcp  comment "HTTP (monitor probes)"
        ufw allow 443/tcp comment "HTTPS (monitor probes)"

        if [[ -n "$HARNESS_IP" ]]; then
            ufw allow from "$HARNESS_IP" to any port 9000 proto tcp \
                comment "Control API (harness only)"
        else
            ufw allow 9000/tcp comment "Control API (any source — set --harness-ip to restrict)"
        fi
        ;;

    dns)
        ufw allow 53/udp comment "DNS (UDP)"
        ufw allow 53/tcp comment "DNS (TCP)"

        if [[ -n "$HARNESS_IP" ]]; then
            ufw allow from "$HARNESS_IP" to any port 9100 proto tcp \
                comment "Control API (harness only)"
        else
            ufw allow 9100/tcp comment "Control API (any source — set --harness-ip to restrict)"
        fi
        ;;

    certmint)
        # The cert-library HTTP API is read-only and gated by the same
        # bearer-token auth the rest of the fleet uses. The certmint
        # host deliberately doesn't carry a list of target IPs —
        # certmint shouldn't know about targets at all; that knowledge
        # lives in fleet.toml on the harness, and targets learn the
        # certmint URL from there. Operators who want belt-and-
        # suspenders firewall scoping should layer on DigitalOcean
        # Cloud Firewall (or equivalent) externally, where the target
        # list is naturally maintained alongside the rest of the fleet
        # topology.
        ufw allow 9200/tcp comment "Cert-library HTTP API (bearer-token authed)"
        ;;

    harness)
        # Harness initiates all connections outbound. No inbound data-plane ports.
        # Control plane connections go outbound to fleet members.
        info "Harness type: no inbound data-plane ports opened"
        ;;
esac

ufw --force enable
ufw status verbose
ok "UFW configured and enabled"

# ---------------------------------------------------------------------------
# Phase 8: fail2ban (SSH brute-force protection)
# ---------------------------------------------------------------------------

section "Configuring fail2ban"

cat > /etc/fail2ban/jail.d/uptime-bench.conf << EOF
[sshd]
enabled  = true
port     = ${SSH_PORT}
maxretry = 5
bantime  = 3600
findtime = 600
EOF

systemctl enable --now fail2ban
systemctl reload fail2ban
ok "fail2ban configured for SSH on port ${SSH_PORT}"

# ---------------------------------------------------------------------------
# Phase 9: Unattended security upgrades
# ---------------------------------------------------------------------------

section "Configuring unattended security upgrades"

cat > /etc/apt/apt.conf.d/50unattended-upgrades-uptime-bench << 'EOF'
Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
};
Unattended-Upgrade::AutoFixInterruptedDpkg "true";
Unattended-Upgrade::MinimalSteps "true";
Unattended-Upgrade::Remove-Unused-Kernel-Packages "true";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::Automatic-Reboot-Time "03:00";
EOF

cat > /etc/apt/apt.conf.d/20auto-upgrades-uptime-bench << 'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Download-Upgradeable-Packages "1";
APT::Periodic::AutocleanInterval "7";
APT::Periodic::Unattended-Upgrade "1";
EOF

systemctl enable --now unattended-upgrades
ok "Unattended security upgrades enabled"

# ---------------------------------------------------------------------------
# Phase 10: Swap file
# ---------------------------------------------------------------------------

section "Checking swap"

if $SKIP_SWAP; then
    info "Skipping swap file creation (--skip-swap)"
elif swapon --show | grep -q .; then
    info "Swap already configured — skipping"
else
    SWAP_FILE="/swapfile"
    SWAP_SIZE="2G"
    info "No swap detected — creating ${SWAP_SIZE} swap file at ${SWAP_FILE}"
    fallocate -l "$SWAP_SIZE" "$SWAP_FILE"
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE"
    swapon "$SWAP_FILE"
    # Persist across reboots
    if ! grep -q "$SWAP_FILE" /etc/fstab; then
        echo "${SWAP_FILE} none swap sw 0 0" >> /etc/fstab
    fi
    # Reduce swappiness — prefer RAM, use swap only as a last resort.
    echo "vm.swappiness=10" > /etc/sysctl.d/99-swappiness.conf
    sysctl -w vm.swappiness=10 -q
    ok "Swap file created and activated (${SWAP_SIZE})"
fi

# ---------------------------------------------------------------------------
# Phase 11: Type-specific capability grants
# ---------------------------------------------------------------------------

section "Applying type-specific configuration"

BINARY="/usr/local/bin/uptime-bench-${TYPE}"

case "$TYPE" in
    target|dns)
        # Grant the binary permission to bind privileged ports (80, 443, 53)
        # without running as root. Applied at binary installation time if the
        # binary exists; re-applied on each provision run.
        if [[ -f "$BINARY" ]]; then
            setcap 'cap_net_bind_service=+ep' "$BINARY"
            ok "cap_net_bind_service granted to ${BINARY}"
        else
            info "Binary not yet deployed — capability will be set on first deploy"
            info "Run: sudo setcap 'cap_net_bind_service=+ep' ${BINARY}"
        fi
        ;;
    harness)
        ok "No special capabilities required for harness"
        ;;
esac

# DNS servers: uptime-bench-dns binds 0.0.0.0:53, which collides with
# systemd-resolved's stub resolver on 127.0.0.53:53. Disable the stub so the
# binary can bind; relink /etc/resolv.conf to systemd-resolved's real output
# so the host itself still resolves names.
if [[ "$TYPE" == "dns" ]]; then
    RESOLVED_CONF="/etc/systemd/resolved.conf"
    if grep -qE '^\s*DNSStubListener\s*=\s*no\b' "$RESOLVED_CONF"; then
        ok "DNSStubListener already disabled in ${RESOLVED_CONF}"
    else
        sed -i -E '/^\s*#?\s*DNSStubListener\s*=/d' "$RESOLVED_CONF"
        printf '\n# Disabled by uptime-bench provision-server.sh — port 53 is bound by uptime-bench-dns\nDNSStubListener=no\n' >> "$RESOLVED_CONF"
        systemctl restart systemd-resolved
        ok "DNSStubListener=no set and systemd-resolved restarted"
    fi
    if [[ -L /etc/resolv.conf && "$(readlink /etc/resolv.conf)" == "/run/systemd/resolve/resolv.conf" ]]; then
        ok "/etc/resolv.conf already points at the real resolver"
    else
        ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
        ok "/etc/resolv.conf relinked to /run/systemd/resolve/resolv.conf"
    fi
fi

# ---------------------------------------------------------------------------
# Phase 12: Install systemd unit
# ---------------------------------------------------------------------------

section "Installing systemd unit"

UNIT_SRC="/tmp/uptime-bench-${TYPE}.service"
UNIT_DST="/etc/systemd/system/uptime-bench-${TYPE}.service"

if [[ -f "$UNIT_SRC" ]]; then
    cp "$UNIT_SRC" "$UNIT_DST"
    chmod 644 "$UNIT_DST"
    systemctl daemon-reload
    if [[ "$TYPE" == "harness" ]]; then
        # The harness binary requires -scenario per invocation; running it
        # via systemd would loop in failure-restart. Install the unit so it
        # is available for future use, but do not enable or start it.
        systemctl disable "uptime-bench-${TYPE}" 2>/dev/null || true
        systemctl stop    "uptime-bench-${TYPE}" 2>/dev/null || true
        ok "Systemd unit installed but not enabled (harness runs per-scenario)"
    else
        systemctl enable "uptime-bench-${TYPE}"
        ok "Systemd unit installed and enabled (not started — binary not yet deployed)"
    fi
else
    info "Unit file not found at ${UNIT_SRC} — skipping"
    info "Expected: deploy/systemd/uptime-bench-${TYPE}.service copied to /tmp/ by provision.sh"
fi

# ---------------------------------------------------------------------------
# Phase 13: Log rotation
# ---------------------------------------------------------------------------

section "Configuring log rotation"

cat > /etc/logrotate.d/uptime-bench << 'EOF'
/var/log/uptime-bench/*.log {
    daily
    rotate 30
    compress
    delaycompress
    missingok
    notifempty
    create 0640 uptime-bench uptime-bench
    sharedscripts
    postrotate
        systemctl reload-or-restart uptime-bench-* 2>/dev/null || true
    endscript
}
EOF

install -d -m 750 -o uptime-bench -g uptime-bench /var/log/uptime-bench
ok "Log rotation configured (/var/log/uptime-bench, 30-day retention)"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

section "Provisioning complete"
echo ""
echo "  Server type : ${TYPE}"
echo "  Deploy user : ${DEPLOY_USER}"
echo "  SSH port    : ${SSH_PORT}"
[[ -n "$HARNESS_IP" ]] && echo "  Control IP  : ${HARNESS_IP} (control port restricted)"
echo ""
PRIMARY_IP=$(hostname -I | awk '{print $1}')

echo "  Next steps:"
echo "    1. Edit the auto-created config files (ownership/mode already correct):"
echo "       sudoedit /etc/uptime-bench/${TYPE}.env"
case "$TYPE" in
    harness)
        echo "       sudoedit /etc/uptime-bench/fleet.toml"
        echo "       sudoedit /etc/uptime-bench/services.toml"
        echo ""
        echo "       # control-token is NOT auto-created (skeleton is instructional only);"
        echo "       # create it, paste the shared bearer token, and save:"
        echo "       sudo install -m 640 -o root -g uptime-bench /dev/null /etc/uptime-bench/control-token"
        echo "       sudoedit /etc/uptime-bench/control-token"
        ;;
    dns)
        echo "       sudoedit /etc/uptime-bench/fleet.toml"
        ;;
esac
echo ""
echo "    2. Deploy the binary (run on your local machine, in the repo):"
echo "       make deploy-${TYPE} ${TYPE^^}_HOST=${PRIMARY_IP} DEPLOY_USER=${DEPLOY_USER}"
if [[ "$TYPE" == "target" || "$TYPE" == "dns" ]]; then
    echo ""
    echo "    3. After the binary is deployed, verify the privileged-port capability:"
    echo "       getcap ${BINARY}"
    echo "       (deploy.sh re-applies it on every deploy; this is just a sanity check.)"
fi
echo ""
