#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if docker compose version >/dev/null 2>&1; then
  COMPOSE_CMD=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  COMPOSE_CMD=(docker-compose)
else
  echo "error: neither 'docker compose' nor 'docker-compose' is available" >&2
  exit 1
fi

COMPOSE_FILES=(-f "$DEPLOY_DIR/docker-compose.test.yml")
if [[ -n "${COMPOSE_OVERRIDE:-}" ]]; then
  COMPOSE_FILES+=(-f "$COMPOSE_OVERRIDE")
fi
COMPOSE=("${COMPOSE_CMD[@]}" "${COMPOSE_FILES[@]}")

wait_for_mongo() {
  local attempt
  for attempt in $(seq 1 10); do
    if "${COMPOSE[@]}" exec -T mongodb mongosh --quiet \
        --eval "db.adminCommand('ping').ok" \
        2>/dev/null | grep -q "^1$"; then
      return 0
    fi
    echo "waiting for mongodb (attempt $attempt/10)..."
    sleep 1
  done
  echo "mongodb did not become ready in 10s" >&2
  return 1
}

load_collection() {
  local collection="$1"
  local json_file="$2"

  "${COMPOSE[@]}" exec -T mongodb mongoimport \
    --uri="mongodb://localhost:27017/chat" \
    --collection="$collection" \
    --jsonArray \
    --drop \
    < "$json_file" 2>&1 \
    | grep -E "imported|failed|error" || true
}

echo "waiting for mongodb..."
wait_for_mongo

echo "seeding collections..."
load_collection users "$SCRIPT_DIR/seed/users.json"
load_collection rooms "$SCRIPT_DIR/seed/rooms.json"
load_collection subscriptions "$SCRIPT_DIR/seed/subscriptions.json"

echo "seed complete"
