#!/usr/bin/env bash
#
# demo-state.sh — Snapshot the moving parts the pin/fav demo touches.
#
# Prints, with zero extra tooling (just curl + docker):
#   1. JetStream stream stats   — from the NATS HTTP monitor port (:8222)
#   2. Pinned rows for a room   — from Cassandra pinned_messages_by_room
#   3. The message's pin flag    — from Cassandra messages_by_id
#
# Run it before and after ./demo-pin-fav.sh to watch the numbers move.
#
# Usage:
#   ./docker-local/demo-state.sh [ROOM_ID] [MESSAGE_ID]
#
# Env overrides: NATS_MON_URL (default http://localhost:8222),
#                CASS_CONTAINER (default chat-local-cassandra),
#                KEYSPACE (default chat).

set -euo pipefail

ROOM_ID="${1:-mo65OxviiELm1VFW5}"
MSG_ID="${2:-mSHtGjzExHBVtlu90Qnb}"

NATS_MON_URL="${NATS_MON_URL:-http://localhost:8222}"
CASS_CONTAINER="${CASS_CONTAINER:-chat-local-cassandra}"
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
