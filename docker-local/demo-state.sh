#!/usr/bin/env bash
#
# demo-state.sh — Snapshot the moving parts the pin/fav demo touches.
#
# Prints, with zero extra tooling (just curl + docker):
#   1. JetStream stream stats     — from the NATS HTTP monitor port (:8222)
#   2. Pinned rows for a room     — from Cassandra pinned_messages_by_room
#   3. The message's pin flag     — from Cassandra messages_by_id
#   4. The message's reactions    — the reactions map on messages_by_id
#   5. Per-member favorites        — from Mongo subscriptions (the `fav` toggle)
#
# Run it before and after ./demo-pin-fav.sh to watch the numbers move.
#
# Usage:
#   ./docker-local/demo-state.sh [ROOM_ID] [MESSAGE_ID]
#
# Env overrides: NATS_MON_URL (default http://localhost:8222),
#                CASS_CONTAINER (default chat-local-cassandra),
#                MONGO_CONTAINER (default chat-local-mongodb), KEYSPACE (default chat).

set -euo pipefail

# Default to the last ids minted by demo-setup.sh (so a no-arg run targets the
# current room/message, not a stale hardcoded one).
ROOM_ID="${1:-$(cat /tmp/room_id.txt 2>/dev/null || true)}"
MSG_ID="${2:-$(cat /tmp/msg_id.txt 2>/dev/null || true)}"
if [ -z "$ROOM_ID" ] || [ -z "$MSG_ID" ]; then
  echo "usage: demo-state.sh <ROOM_ID> <MESSAGE_ID>   (or run demo-setup.sh first to populate /tmp)" >&2
  exit 1
fi

NATS_MON_URL="${NATS_MON_URL:-http://localhost:8222}"
CASS_CONTAINER="${CASS_CONTAINER:-chat-local-cassandra}"
MONGO_CONTAINER="${MONGO_CONTAINER:-chat-local-mongodb}"
KEYSPACE="${KEYSPACE:-chat}"

cql() { docker exec "$CASS_CONTAINER" cqlsh -e "$1"; }

echo "================ NATS JetStream streams ($NATS_MON_URL) ================"
curl -s "$NATS_MON_URL/jsz?streams=1" \
  | jq -r '.account_details[]?.stream_detail[]?
           | "  \(.name)\tmsgs=\(.state.messages)\tbytes=\(.state.bytes)\tlast_seq=\(.state.last_seq)"' \
  | sort | column -t -s $'\t'

echo
echo "================ Cassandra: pinned_messages_by_room (room=$ROOM_ID) ====="
cql "SELECT message_id, pinned_at, pinned_by FROM ${KEYSPACE}.pinned_messages_by_room WHERE room_id='${ROOM_ID}';"

echo "================ Cassandra: messages_by_id pin flag (msg=$MSG_ID) ======="
cql "SELECT message_id, pinned_at, pinned_by FROM ${KEYSPACE}.messages_by_id WHERE message_id='${MSG_ID}';"

echo "================ Cassandra: reactions on the message (msg=$MSG_ID) ======"
cql "SELECT reactions FROM ${KEYSPACE}.messages_by_id WHERE message_id='${MSG_ID}';"

echo "================ Mongo: per-member favorites (room=$ROOM_ID) ============"
docker exec "$MONGO_CONTAINER" mongosh "$KEYSPACE" --quiet --eval "
  const subs = db.subscriptions.find({roomId:'${ROOM_ID}'},{_id:0,'u.account':1,roles:1,favorite:1})
                 .sort({'u.account':1}).toArray();
  if (!subs.length) { print('  (no subscriptions for this room)'); }
  else subs.forEach(s => print('  '.concat((s.u.account||'?').padEnd(10),
        'favorite=', String(s.favorite||false).padEnd(6),
        'roles=', JSON.stringify(s.roles||[]))));
" 2>&1 | grep -vE '^\s*$'
