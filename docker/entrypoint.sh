#!/bin/sh
set -e

# Write the control token from env to the file path the binaries expect.
if [ -n "$CONTROL_TOKEN" ]; then
    printf '%s' "$CONTROL_TOKEN" > /etc/uptime-bench/control-token
fi

# Write the harness env file if this is the harness container.
if [ -n "$DB_DSN" ]; then
    cat > /etc/uptime-bench/harness.env <<EOF
DB_DSN=${DB_DSN}
CONTROL_TOKEN=${CONTROL_TOKEN}
EOF
fi

exec "$@"
