#!/usr/bin/env bash
# Temporary end-to-end test harness for the Room Info Batch RPC.
# Safe to delete after manual testing is complete.
#
# Usage:
#   ./test-script.sh up     # build + run stack, seed data, fire RPC, print response
#   ./test-script.sh down   # tear down stack and remove volumes
#   ./test-script.sh logs   # tail room-service logs
#   ./test-script.sh rpc    # re-fire the RPC against an already-running stack
#
# Requires: docker (with compose plugin). Works in github.dev / Codespaces.

set -euo pipefail
cd "$(dirname "$0")"

export COMPOSE_PROJECT_NAME=roomrpc-test
COMPOSE_FILE=room-service/deploy/docker-compose.yml
NETWORK="${COMPOSE_PROJECT_NAME}_default"

# Fixed base64 strings — content doesn't matter, only presence in response does.
PUB_KEY_B64='BAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8gISIjJCUmJygpKissLS4vMDEyMzQ1Njc4OTo7PD0+Pw=='
PRIV1_B64='AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8='
PRIV2_B64='/v38+/r5+Pf29fTz8vHw7+7t7Ovq6ejn5uXk4+Lh4N8='

SUBJECT='chat.user.tester.request.rooms.site-local.info.batch'
PAYLOAD='{"roomIds":["r1","r2","r3","missing"]}'

section() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
ok()      { printf '\033[1;32m✓\033[0m %s\n' "$*"; }
fail()    { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

require_docker() {
  command -v docker >/dev/null 2>&1 || fail "docker not installed"
  docker info >/dev/null 2>&1 || fail "docker daemon not running"
}

wait_for() {
  local desc="$1"; shift
  local i
  for i in $(seq 1 60); do
    if "$@" >/dev/null 2>&1; then ok "$desc"; return; fi
    sleep 1
  done
  fail "timeout waiting for $desc"
}

cmd_up() {
  require_docker

  section "Building and starting stack (nats, mongodb, valkey, room-service)"
  docker compose -f "$COMPOSE_FILE" up -d --build

  wait_for "MongoDB ready" \
    docker compose -f "$COMPOSE_FILE" exec -T mongodb \
      mongosh --quiet --eval 'db.runCommand({ping:1}).ok'

  wait_for "Valkey ready" \
    docker compose -f "$COMPOSE_FILE" exec -T valkey valkey-cli ping

  wait_for "room-service subscribed" \
    bash -c "docker compose -f '$COMPOSE_FILE' logs room-service 2>&1 | grep -q 'room-service running'"

  seed_data
  cmd_rpc
  echo
  echo "Next:"
  echo "  $0 logs    # tail room-service logs"
  echo "  $0 rpc     # re-fire the RPC"
  echo "  $0 down    # tear down"
}

seed_data() {
  section "Seeding MongoDB rooms (r1 with lastMsgAt, r2 without, r3 with lastMsgAt)"
  docker compose -f "$COMPOSE_FILE" exec -T mongodb mongosh chat --quiet --eval '
    db.rooms.deleteMany({_id:{$in:["r1","r2","r3"]}});
    db.rooms.insertMany([
      {_id:"r1", name:"room-1", type:"group", siteId:"site-local", createdBy:"tester",
       userCount:1, lastMsgAt:new Date("2026-04-10T12:00:00Z"),
       createdAt:new Date(), updatedAt:new Date()},
      {_id:"r2", name:"room-2", type:"group", siteId:"site-local", createdBy:"tester",
       userCount:1, lastMsgAt:new Date(0),
       createdAt:new Date(), updatedAt:new Date()},
      {_id:"r3", name:"room-3", type:"group", siteId:"site-local", createdBy:"tester",
       userCount:1, lastMsgAt:new Date("2026-04-10T11:00:00Z"),
       createdAt:new Date(), updatedAt:new Date()}
    ]);
    print("seeded " + db.rooms.countDocuments({_id:{$in:["r1","r2","r3"]}}) + " rooms");
  '

  section "Seeding Valkey keys (r1 and r2 only; r3 intentionally has no key)"
  docker compose -f "$COMPOSE_FILE" exec -T valkey \
    valkey-cli HSET room:r1:key pub "$PUB_KEY_B64" priv "$PRIV1_B64" ver 0 >/dev/null
  docker compose -f "$COMPOSE_FILE" exec -T valkey \
    valkey-cli HSET room:r2:key pub "$PUB_KEY_B64" priv "$PRIV2_B64" ver 0 >/dev/null
  docker compose -f "$COMPOSE_FILE" exec -T valkey valkey-cli DEL room:r3:key >/dev/null
  ok "keys set for r1, r2; r3 has none"
}

cmd_rpc() {
  section "Firing batch info RPC"
  echo "  subject: $SUBJECT"
  echo "  payload: $PAYLOAD"
  echo
  echo "Response:"

  docker run --rm --network="$NETWORK" natsio/nats-box:latest \
    nats --server=nats://nats:4222 req --raw --timeout=5s "$SUBJECT" "$PAYLOAD" \
    | (jq . 2>/dev/null || cat)
}

cmd_down() {
  section "Tearing down stack (removing volumes)"
  docker compose -f "$COMPOSE_FILE" down -v
  ok "stopped"
}

cmd_logs() {
  docker compose -f "$COMPOSE_FILE" logs -f room-service
}

case "${1:-up}" in
  up)   cmd_up ;;
  down) cmd_down ;;
  logs) cmd_logs ;;
  rpc)  cmd_rpc ;;
  *)    echo "Usage: $0 [up|down|logs|rpc]"; exit 1 ;;
esac
