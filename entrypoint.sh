#!/bin/sh
set -e

CONFIG_FILE="/config/caddy.json"
TEMPLATE="/app/caddy.json.tmpl"

# On first boot, stamp the template into the config volume.
# On subsequent boots, --resume loads the autosaved config instead,
# which preserves the enrolled IP allowlist.
if [ ! -f "$CONFIG_FILE" ]; then
  echo "First boot: generating Caddy config from template..."
  sed \
    -e "s|\${CADDYGATE_BASE_DOMAIN}|${CADDYGATE_BASE_DOMAIN}|g" \
    -e "s|\${CADDYGATE_DNS_PROVIDER}|${CADDYGATE_DNS_PROVIDER}|g" \
    -e "s|\${CADDYGATE_DNS_API_TOKEN}|${CADDYGATE_DNS_API_TOKEN}|g" \
    -e "s|\${CADDYGATE_ENROLLMENT_UUID}|${CADDYGATE_ENROLLMENT_UUID}|g" \
    "$TEMPLATE" > "$CONFIG_FILE"
  echo "Config written to $CONFIG_FILE"
fi

# Ensure the admin socket directory exists
mkdir -p /run/caddy

# Start Caddy (--resume reloads autosaved config if present, preserving allowlist)
echo "Starting Caddy..."
caddy run --resume --config "$CONFIG_FILE" &
CADDY_PID=$!

# Start CaddyGate sidecar
echo "Starting CaddyGate sidecar..."
exec /app/caddygate