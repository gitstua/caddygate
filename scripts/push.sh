#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ -f "$ROOT_DIR/.env" ]]; then
  set -a; source "$ROOT_DIR/.env"; set +a
fi

IMAGE_NAME="${IMAGE_NAME:-caddygate}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
REGISTRY="${REGISTRY:-}"  # e.g. ghcr.io/yourorg

if [[ -z "$REGISTRY" ]]; then
  echo "Error: REGISTRY is not set."
  echo "Set it in .env or: REGISTRY=ghcr.io/yourorg ./scripts/push.sh"
  exit 1
fi

FULL_IMAGE="$REGISTRY/$IMAGE_NAME:$IMAGE_TAG"

echo "Tagging $IMAGE_NAME:$IMAGE_TAG → $FULL_IMAGE"
docker tag "$IMAGE_NAME:$IMAGE_TAG" "$FULL_IMAGE"

echo "Pushing $FULL_IMAGE"
docker push "$FULL_IMAGE"

echo ""
echo "Done: $FULL_IMAGE"
