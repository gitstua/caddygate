#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Load .env if present (for IMAGE_NAME etc.) but don't require it
if [[ -f "$ROOT_DIR/.env" ]]; then
  set -a; source "$ROOT_DIR/.env"; set +a
fi

IMAGE_NAME="${IMAGE_NAME:-caddygate}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
DNS_MODULE="${DNS_MODULE:-github.com/caddy-dns/cloudflare}"

echo "Building $IMAGE_NAME:$IMAGE_TAG"
echo "  DNS module : $DNS_MODULE"
echo "  Context    : $ROOT_DIR"
echo ""

docker build \
  --build-arg DNS_MODULE="$DNS_MODULE" \
  --tag "$IMAGE_NAME:$IMAGE_TAG" \
  "$ROOT_DIR"

echo ""
echo "Done. Run with: docker compose up -d"
