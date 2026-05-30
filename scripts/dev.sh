#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ ! -f "$ROOT_DIR/.env" ]]; then
  echo "No .env found. Copying .env.example → .env"
  cp "$ROOT_DIR/.env.example" "$ROOT_DIR/.env"
  echo "Edit .env before continuing, then re-run this script."
  exit 1
fi

echo "Building and starting stack..."
docker compose -f "$ROOT_DIR/docker-compose.yml" up --build -d

echo ""
echo "Stack is up. Tailing logs (Ctrl+C to stop):"
docker compose -f "$ROOT_DIR/docker-compose.yml" logs -f
