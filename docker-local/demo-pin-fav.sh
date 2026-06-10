#!/usr/bin/env bash
#
# demo-pin-fav.sh — Discrete pin/unpin/list + favorite/unfavorite driver.
#
# Each action is its own subcommand so you can fire exactly one RPC and see its
# reply. Assumes the local stack is up (./docker-local/demo-setup.sh) and you
# have a room + message id.
#
# Message actions (history-service):
#   pin    <ROOM_ID> <MESSAGE_ID>   pin a message
#   unpin  <ROOM_ID> <MESSAGE_ID>   unpin a message
#   list   <ROOM_ID>               list a room's pinned messages
#
# Room actions (room-service):
#   fav    <ROOM_ID>               mark room favorite   (idempotent -> favorite=true)
#   unfav  <ROOM_ID>               unmark room favorite (idempotent -> favorite=false)
#   toggle <ROOM_ID>               flip favorite once, print the new state
#
# Combo:
#   all    <ROOM_ID> <MESSAGE_ID>   pin -> list -> unpin -> list -> fav -> unfav
#
# Env overrides: ACCOUNT (default alice), SITE_ID (default site-local),
#                NATS_URL, NATS_CREDS.

set -euo pipefail

ACCOUNT="${ACCOUNT:-alice}"
SITE_ID="${SITE_ID:-site-local}"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
NATS_CREDS="${NATS_CREDS:-$(dirname "${BASH_SOURCE[0]}")/backend.creds}"

usage() {
  sed -n '5,21p' "${BASH_SOURCE[0]}" | sed 's/^#\s\{0,1\}//'
  exit "${1:-1}"
}

# nats req wrapper — prints the raw JSON reply.
req() { nats --server "$NATS_URL" --creds "$NATS_CREDS" req "$1" "${2:-}" --raw; }

# base <ROOM_ID> -> the room-scoped request subject prefix.
base() { echo "chat.user.$ACCOUNT.request.room.$1.$SITE_ID"; }

# favorite.toggle is a pure server-side flip (no target state), so converge to the
# desired value: toggle, read the reply, toggle once more only if needed (<=2 calls).
set_favorite() { # <ROOM_ID> <true|false>
  local room="$1" want="$2" resp cur
  for _ in 1 2; do
    resp="$(req "$(base "$room").favorite.toggle" "")"
    cur="$(printf '%s' "$resp" | jq -r '.favorite')"
    if [ "$cur" = "$want" ]; then printf '%s\n' "$resp"; return 0; fi
  done
  printf '%s\n' "$resp" # best effort; print whatever we last got
}

run_all() { # <ROOM_ID> <MESSAGE_ID>
  local room="$1" msg="$2"
  echo "### PIN";                           req "$(base "$room").msg.pin"         "{\"messageId\":\"$msg\"}"
  echo; echo "### PINNED LIST (has $msg)";  req "$(base "$room").msg.pinned.list" "{}"
  echo; echo "### UNPIN";                   req "$(base "$room").msg.unpin"       "{\"messageId\":\"$msg\"}"
  echo; echo "### PINNED LIST (empty now)"; req "$(base "$room").msg.pinned.list" "{}"
  echo; echo "### FAVORITE (-> true)";      set_favorite "$room" true
  echo; echo "### UNFAVORITE (-> false)";   set_favorite "$room" false
  echo; echo "Done."
}

action="${1:-}"; shift || true
case "$action" in
  pin)    room="${1:?pin needs <ROOM_ID> <MESSAGE_ID>}";   msg="${2:?pin needs <ROOM_ID> <MESSAGE_ID>}"
          req "$(base "$room").msg.pin"   "{\"messageId\":\"$msg\"}";;
  unpin)  room="${1:?unpin needs <ROOM_ID> <MESSAGE_ID>}"; msg="${2:?unpin needs <ROOM_ID> <MESSAGE_ID>}"
          req "$(base "$room").msg.unpin" "{\"messageId\":\"$msg\"}";;
  list)   room="${1:?list needs <ROOM_ID>}"
          req "$(base "$room").msg.pinned.list" "{}";;
  fav)    room="${1:?fav needs <ROOM_ID>}";   set_favorite "$room" true;;
  unfav)  room="${1:?unfav needs <ROOM_ID>}"; set_favorite "$room" false;;
  toggle) room="${1:?toggle needs <ROOM_ID>}"
          req "$(base "$room").favorite.toggle" "";;
  all)    room="${1:?all needs <ROOM_ID> <MESSAGE_ID>}";   msg="${2:?all needs <ROOM_ID> <MESSAGE_ID>}"
          run_all "$room" "$msg";;
  -h|--help|help) usage 0;;
  *)      usage 1;;
esac
