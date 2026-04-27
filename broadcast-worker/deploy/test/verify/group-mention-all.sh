#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

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

exec "${COMPOSE[@]}" exec -T mongodb mongosh mongodb://localhost:27017/chat --quiet --eval "
  const doc = db.rooms.findOne({_id: 'group-1'}, {lastMentionAllAt: 1});
  printjson(doc);
  if (!doc) {
    print('FAIL: rooms.group-1 not found');
    quit(1);
  }
  const expectedPrefix = '2026-04-17T12:02:00';
  const lastMentionAllAt = doc.lastMentionAllAt ? doc.lastMentionAllAt.toISOString() : '';
  if (!lastMentionAllAt.startsWith(expectedPrefix)) {
    print('FAIL: expected lastMentionAllAt to start with ' + expectedPrefix + ', got ' + lastMentionAllAt);
    quit(1);
  }
  print('OK: rooms.group-1 lastMentionAllAt=' + lastMentionAllAt);
"
