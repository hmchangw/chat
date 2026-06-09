#!/usr/bin/env bash
#
# demo-pin-fav.sh — Quick demo that pin/unpin and favorite/unfavorite work.
#
# Assumes the local stack is up (make deps-up && make up) and you already have
# a room with a message (e.g. created in the app). Just runs the RPCs and
# prints each reply so you can see it working.
#
# Usage:
#   ./docker-local/demo-pin-fav.sh <ROOM_ID> <MESSAGE_ID>
#
# Env overrides: ACCOUNT (default alice), SITE_ID (default site-local),
#                NATS_URL, NATS_CREDS.

set -euo pipefail

ROOM_ID="${1:?usage: demo-pin-fav.sh <ROOM_ID> <MESSAGE_ID>}"
MSG_ID="${2:?usage: demo-pin-fav.sh <ROOM_ID> <MESSAGE_ID>}"

ACCOUNT="${ACCOUNT:-alice}"
SITE_ID="${SITE_ID:-site-local}"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
NATS_CREDS="${NATS_CREDS:-$(dirname "${BASH_SOURCE[0]}")/backend.creds}"

# nats req wrapper — prints the raw JSON reply.
req() { nats --server "$NATS_URL" --creds "$NATS_CREDS" req "$1" "${2:-}" --raw; }

base="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID"

echo "### PIN"
req "$base.msg.pin"         "{\"messageId\":\"$MSG_ID\"}"

echo; echo "### PINNED LIST (should contain $MSG_ID)"
req "$base.msg.pinned.list" ""

echo; echo "### UNPIN"
req "$base.msg.unpin"       "{\"messageId\":\"$MSG_ID\"}"

echo; echo "### PINNED LIST (should be empty now)"
req "$base.msg.pinned.list" ""

echo; echo "### FAVORITE (toggle on)"
req "$base.favorite.toggle" ""

echo; echo "### UNFAVORITE (toggle off)"
req "$base.favorite.toggle" ""

echo; echo "Done."
