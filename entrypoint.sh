#!/bin/sh
set -e

CONFIG_FILE="/config/caddy.json"
AUTOSAVE="/config/caddy/autosave.json"
TEMPLATE="/app/caddy.json.tmpl"

# Always regenerate config from template so the correct base domain and
# wildcard TLS subject are present from the moment Caddy starts.
# Without this, subjects patched via the Admin API after startup don't
# trigger certificate management.
echo "Generating Caddy config from template..."
sed \
  -e "s|\${CADDYGATE_BASE_DOMAIN}|${CADDYGATE_BASE_DOMAIN}|g" \
  -e "s|\${CADDYGATE_DNS_PROVIDER}|${CADDYGATE_DNS_PROVIDER}|g" \
  -e "s|\${CADDYGATE_DNS_API_TOKEN}|${CADDYGATE_DNS_API_TOKEN}|g" \
  -e "s|\${CADDYGATE_ENROLLMENT_UUID}|${CADDYGATE_ENROLLMENT_UUID}|g" \
  "$TEMPLATE" > "$CONFIG_FILE"

# Remove autosave so Caddy uses our freshly generated config.
# The sidecar restores the enrolled IP allowlist from its own persistence file.
rm -f "$AUTOSAVE"

mkdir -p /run/caddy

echo "Starting Caddy..."
caddy run --config "$CONFIG_FILE" &
CADDY_PID=$!

echo "Starting CaddyGate sidecar..."
exec /app/caddygate
