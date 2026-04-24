#!/usr/bin/env bash
# provision-server.sh — configure an uptime-bench fleet server.
#
# Runs ON the server as root. Idempotent — safe to run multiple times for
# updates and re-configuration. Targets Ubuntu Server 24.04 LTS.
#
# Usage (direct on the server):
#   sudo bash provision-server.sh --type <harness|target|dns> [options]
#
# Options:
#   --type TYPE         Server role: harness | target | dns  (required)
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
    harness|target|dns) ;;
    *)
        echo "Error: --type must be one of: harness, target, dns" >&2
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

section() { echo ""; echo "==> $*"; }
ok()      { echo "    [ok] $*"; }
info()    { echo "    [--] $*"; }

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
    echo "ERROR: no SSH service unit found (tried ssh.service and sshd.service)." >&2
    echo "Investigate with: systemctl list-units --type=service | grep -i ssh" >&2
    exit 1
fi

# Validate config before reloading to avoid locking ourselves out.
if sshd -t; then
    systemctl reload "$SSH_UNIT"
    ok "SSH hardening applied and ${SSH_UNIT} reloaded"
else
    echo "ERROR: sshd config test failed — NOT reloading ${SSH_UNIT}. Fix $SSHD_CONF." >&2
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
    systemctl enable "uptime-bench-${TYPE}"
    ok "Systemd unit installed and enabled (not started — binary not yet deployed)"
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
echo "  Next steps:"
echo "    1. Place credentials in /etc/uptime-bench/ (mode 0640, root:uptime-bench)"
echo "       - harness.env / target.env / dns.env  (DB_DSN, CONTROL_TOKEN)"
echo "       - control-token                        (shared fleet auth token)"
echo "    2. Deploy the binary:"
echo "       make deploy-${TYPE} ${TYPE^^}_HOST=$(hostname -I | awk '{print $1}')"
if [[ "$TYPE" == "target" || "$TYPE" == "dns" ]]; then
    echo "    3. After binary is deployed, verify capability:"
    echo "       sudo setcap 'cap_net_bind_service=+ep' ${BINARY}"
    echo "       getcap ${BINARY}"
fi
echo ""
