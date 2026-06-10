#!/usr/bin/env bash
#
# demo-setup.sh — Get the local stack ready for the pin/fav demo and mint fresh IDs.
#
# Idempotent. Assumes `make deps-up && make up` has been run once so the
# containers exist; this script then heals the two things that break re-runs and
# hands you a working room + message id:
#   1. installs the `nats` CLI to a persistent path (if missing)
#   2. re-creates Vault's `chat-kek` transit key (dev-mode Vault loses it on restart)
#   3. restarts any encryption-dependent service that died on a Vault bounce
#   4. mints a fresh room + message (old rooms are poisoned if Vault lost its key)
#
# Writes the ids to /tmp/room_id.txt and /tmp/msg_id.txt and prints copy-paste
# commands. Pass --logs to stream the app logs live in this terminal once setup
# is done (otherwise run ./demo-logs.sh in a separate terminal).
#
# Env overrides: ACCOUNT, SITE_ID, MEMBER, NATS_BIN_DIR, NATS_URL,
# NATS_CREDS, VAULT_C, VAULT_TOKEN, CASS_C.

set -euo pipefail

FOLLOW_LOGS=0
[ "${1:-}" = "--logs" ] && FOLLOW_LOGS=1

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCOUNT="${ACCOUNT:-alice}"
SITE_ID="${SITE_ID:-site-local}"
MEMBER="${MEMBER:-bob}"
NATS_BIN_DIR="${NATS_BIN_DIR:-/home/codespace/.local/bin}"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
NATS_CREDS="${NATS_CREDS:-$HERE/backend.creds}"
VAULT_C="${VAULT_C:-chat-local-services-vault-1}"
VAULT_TOKEN="${VAULT_TOKEN:-dev-only-token}"
CASS_C="${CASS_C:-chat-local-cassandra}"

log() { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
running() { [ "$(docker inspect -f '{{.State.Running}}' "$1" 2>/dev/null)" = "true" ]; }

# 1. nats CLI on a persistent path (/tmp gets wiped by the dev sandbox).
log "nats CLI"
if ! command -v nats >/dev/null 2>&1 && [ ! -x "$NATS_BIN_DIR/nats" ]; then
  echo "installing nats CLI -> $NATS_BIN_DIR"
  mkdir -p "$NATS_BIN_DIR"
  GOBIN="$NATS_BIN_DIR" go install github.com/nats-io/natscli/nats@latest
fi
export PATH="$NATS_BIN_DIR:$PATH"
echo "using $(command -v nats) ($(nats --version 2>&1))"

# 2. Vault transit key. Dev-mode Vault is in-memory, so a restart drops chat-kek.
log "Vault transit key chat-kek"
if running "$VAULT_C"; then
  docker exec -e VAULT_ADDR=http://127.0.0.1:8200 -e VAULT_TOKEN="$VAULT_TOKEN" "$VAULT_C" sh -c '
    vault secrets enable -path=transit transit 2>/dev/null
    vault write -f transit/keys/chat-kek >/dev/null
    vault read transit/keys/chat-kek' | grep -E '^name|^type' || true
else
  echo "WARNING: $VAULT_C is not running — start the stack first (make deps-up && make up)"
fi

# 3. Restart any service that may have crashed when Vault lost its key.
log "ensure services running"
for c in chat-local-services-{auth-service,room-service,history-service,message-gatekeeper,message-worker,broadcast-worker,room-worker}-1; do
  docker inspect "$c" >/dev/null 2>&1 || continue
  if ! running "$c"; then echo "starting $c"; docker start "$c" >/dev/null; fi
done

# 4. Mint a fresh room + message (creating a room doubles as a readiness probe).
log "minting a fresh room + message"
NC=(nats --server "$NATS_URL" --creds "$NATS_CREDS")
RID=""
for _ in $(seq 1 30); do
  RID="$("${NC[@]}" req "chat.user.$ACCOUNT.request.room.$SITE_ID.create" \
        "{\"name\":\"pin-fav-demo\",\"users\":[\"$MEMBER\"]}" --raw 2>/dev/null | jq -r '.roomId // empty' || true)"
  [ -n "$RID" ] && break
  sleep 2
done
[ -n "$RID" ] || { echo "ERROR: room-service not responding — check 'docker ps' and service logs"; exit 1; }

# `|| true` swallows the SIGPIPE `head` sends `tr` (would trip pipefail otherwise).
MID="$(tr -dc '0-9A-Za-z' </dev/urandom | head -c 20 || true)"
"${NC[@]}" pub "chat.user.$ACCOUNT.room.$RID.$SITE_ID.msg.send" \
  "{\"id\":\"$MID\",\"content\":\"Pin me!\",\"requestId\":\"$(cat /proc/sys/kernel/random/uuid)\"}" >/dev/null

# Wait for the message to land in Cassandra before declaring success.
for _ in $(seq 1 15); do
  found="$(docker exec "$CASS_C" cqlsh -e \
    "SELECT message_id FROM chat.messages_by_id WHERE message_id='$MID';" 2>/dev/null || true)"
  case "$found" in *"$MID"*) break;; esac
  sleep 1
done

echo "$RID" >/tmp/room_id.txt
echo "$MID" >/tmp/msg_id.txt

log "ready"
cat <<EOF
ROOM_ID = $RID
MSG_ID  = $MID   (also saved to /tmp/room_id.txt and /tmp/msg_id.txt)

export PATH=$NATS_BIN_DIR:\$PATH

./docker-local/demo-state.sh   $RID $MID     # snapshot state
./docker-local/demo-pin-fav.sh pin   $RID $MID
./docker-local/demo-pin-fav.sh list  $RID
./docker-local/demo-pin-fav.sh unpin $RID $MID
./docker-local/demo-pin-fav.sh fav   $RID
./docker-local/demo-pin-fav.sh unfav $RID

# live logs (or run ./docker-local/demo-logs.sh in another terminal):
./docker-local/demo-setup.sh --logs        # re-run with --logs to stream here
EOF

if [ "$FOLLOW_LOGS" = "1" ]; then
  log "following app logs (Ctrl+C to stop) — drive the demo from another terminal"
  exec "$HERE/demo-logs.sh"
fi
