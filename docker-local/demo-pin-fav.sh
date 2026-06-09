#!/usr/bin/env bash
#
# demo-pin-fav.sh — End-to-end demo of the pin/unpin and favorite-toggle
# features against the local-dev stack, driven entirely by the `nats` CLI.
#
# What it does (all over NATS request/reply, except the message send which is
# a JetStream publish):
#   1. Creates a room               (room-service, async — provisioned by room-worker)
#   2. Waits until the room is live  (polls member.list)
#   3. Sends a message               (JetStream publish → gatekeeper → Cassandra)
#   4. Pins it                       (history-service)   chat...msg.pin
#   5. Lists pinned messages         (history-service)   chat...msg.pinned.list
#   6. Unpins it                     (history-service)   chat...msg.unpin
#   7. Toggles favorite on, then off (room-service)      chat...favorite.toggle
#   ...while a background subscriber prints the room's pin/unpin events live.
#
# Prerequisites (run from the repo root):
#   ./docker-local/setup.sh   # one-time: generates backend.creds + nats.conf
#   make deps-up              # NATS, Mongo, Cassandra, ...
#   make up                   # auth-service, room-service, history-service, workers, ...
#   nats CLI + jq installed   # https://github.com/nats-io/natscli
#
# Auth note: this uses docker-local/backend.creds, which setup.sh provisions
# with full pub/sub. The acting user account is taken from the SUBJECT token
# (natsrouter derives {account} from the subject, not the connection), so no
# per-user JWT is needed for a local demo. Override ACCOUNT/SITE_ID/etc. via env.

set -euo pipefail

# ---- Config (override via environment) -------------------------------------
NATS_URL="${NATS_URL:-nats://localhost:4222}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NATS_CREDS="${NATS_CREDS:-$SCRIPT_DIR/backend.creds}"
ACCOUNT="${ACCOUNT:-alice}"
SITE_ID="${SITE_ID:-site-local}"
ROOM_NAME="${ROOM_NAME:-pin-fav-demo}"

# ---- Pretty output ---------------------------------------------------------
if [[ -t 1 ]]; then
  BOLD=$'\033[1m'; DIM=$'\033[2m'; GREEN=$'\033[32m'; CYAN=$'\033[36m'; RED=$'\033[31m'; RESET=$'\033[0m'
else
  BOLD=""; DIM=""; GREEN=""; CYAN=""; RED=""; RESET=""
fi
step()  { echo; echo "${BOLD}${CYAN}==> $*${RESET}"; }
info()  { echo "    $*"; }
ok()    { echo "    ${GREEN}✓ $*${RESET}"; }
die()   { echo "${RED}ERROR: $*${RESET}" >&2; exit 1; }

# ---- Preflight -------------------------------------------------------------
command -v nats >/dev/null 2>&1 || die "the 'nats' CLI is not installed (https://github.com/nats-io/natscli)"
command -v jq   >/dev/null 2>&1 || die "'jq' is not installed"
[[ -f "$NATS_CREDS" ]] || die "creds file not found at $NATS_CREDS — run ./docker-local/setup.sh first"
nats --server "$NATS_URL" --creds "$NATS_CREDS" server check connection >/dev/null 2>&1 \
  || die "cannot reach NATS at $NATS_URL with $NATS_CREDS — is 'make deps-up' running?"

# nats req wrapper: prints the raw reply body to stdout.
req() {
  local subject="$1" payload="${2:-}"
  nats --server "$NATS_URL" --creds "$NATS_CREDS" req "$subject" "$payload" --raw 2>/dev/null
}

# Returns 0 if the reply JSON is an error envelope (has a "code" field), else 1.
is_err() { jq -e 'has("code")' >/dev/null 2>&1 <<<"$1"; }

# A short, valid 20-char base62 message id (idgen.IsValidMessageID accepts 17 or 20).
gen_msg_id() { LC_ALL=C tr -dc '0-9A-Za-z' < /dev/urandom | head -c 20; }
# A UUID for request dedup (v4 hyphenated is accepted as a request id).
gen_uuid()   { if command -v uuidgen >/dev/null 2>&1; then uuidgen | tr 'A-Z' 'a-z'; else
  printf '%s-%s-4%s-%s-%s\n' \
    "$(tr -dc 'a-f0-9' </dev/urandom | head -c 8)" \
    "$(tr -dc 'a-f0-9' </dev/urandom | head -c 4)" \
    "$(tr -dc 'a-f0-9' </dev/urandom | head -c 3)" \
    "$(tr -dc 'a-f0-9' </dev/urandom | head -c 4)" \
    "$(tr -dc 'a-f0-9' </dev/urandom | head -c 12)"; fi; }

# Retry an RPC until it returns a non-error reply (or time out). Echoes the reply.
retry_until_ok() {
  local subject="$1" payload="$2" tries="${3:-20}" delay="${4:-1}" reply
  for ((i = 1; i <= tries; i++)); do
    reply="$(req "$subject" "$payload")" || true
    if [[ -n "$reply" ]] && ! is_err "$reply"; then echo "$reply"; return 0; fi
    sleep "$delay"
  done
  echo "$reply"; return 1
}

echo "${BOLD}Pin / Unpin / Favorite demo${RESET}  ${DIM}(account=$ACCOUNT site=$SITE_ID)${RESET}"

# ---- 1. Create a room ------------------------------------------------------
step "Creating room \"$ROOM_NAME\""
CREATE_SUBJECT="chat.user.$ACCOUNT.request.room.$SITE_ID.create"
CREATE_REPLY="$(req "$CREATE_SUBJECT" "{\"name\":\"$ROOM_NAME\",\"users\":[],\"orgs\":[],\"channels\":[]}")"
is_err "$CREATE_REPLY" && die "room create failed: $CREATE_REPLY"
ROOM_ID="$(jq -r '.roomId' <<<"$CREATE_REPLY")"
[[ -n "$ROOM_ID" && "$ROOM_ID" != "null" ]] || die "no roomId in reply: $CREATE_REPLY"
info "reply: $CREATE_REPLY"
ok "room queued: ${BOLD}$ROOM_ID${RESET} (status=$(jq -r '.status' <<<"$CREATE_REPLY"))"

# ---- 2. Wait for room-worker to provision it -------------------------------
step "Waiting for room to be provisioned (room create is async)"
MEMBER_LIST_SUBJECT="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID.member.list"
retry_until_ok "$MEMBER_LIST_SUBJECT" "" 30 1 >/dev/null \
  || die "room $ROOM_ID never became ready (member.list kept failing)"
ok "room is live and $ACCOUNT is a member"

# ---- 3. Start a live listener for room pin/unpin events ---------------------
step "Subscribing to room events: chat.room.$ROOM_ID.event"
EVENTS_FILE="$(mktemp)"
nats --server "$NATS_URL" --creds "$NATS_CREDS" sub "chat.room.$ROOM_ID.event" --raw \
  >"$EVENTS_FILE" 2>/dev/null &
SUB_PID=$!
cleanup() { kill "$SUB_PID" >/dev/null 2>&1 || true; rm -f "$EVENTS_FILE"; }
trap cleanup EXIT
sleep 1
ok "listening (pid $SUB_PID)"

# ---- 4. Send a message -----------------------------------------------------
step "Sending a message (JetStream publish — async, no sync reply)"
MSG_ID="$(gen_msg_id)"
REQ_ID="$(gen_uuid)"
SEND_SUBJECT="chat.user.$ACCOUNT.room.$ROOM_ID.$SITE_ID.msg.send"
nats --server "$NATS_URL" --creds "$NATS_CREDS" pub "$SEND_SUBJECT" \
  "{\"id\":\"$MSG_ID\",\"content\":\"Pin me!\",\"requestId\":\"$REQ_ID\"}" >/dev/null 2>&1
ok "published message ${BOLD}$MSG_ID${RESET}"

# ---- 5. Pin the message ----------------------------------------------------
step "Pinning the message"
PIN_SUBJECT="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID.msg.pin"
# Retry: the message must propagate gatekeeper → canonical → Cassandra first.
PIN_REPLY="$(retry_until_ok "$PIN_SUBJECT" "{\"messageId\":\"$MSG_ID\"}" 20 1)" \
  || die "pin failed (message never became pinnable): $PIN_REPLY"
info "reply: $PIN_REPLY"
ok "pinned at $(jq -r '.pinnedAt' <<<"$PIN_REPLY") (UTC millis)"

# ---- 6. List pinned messages ----------------------------------------------
step "Listing pinned messages"
PINNED_LIST_SUBJECT="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID.msg.pinned.list"
LIST_REPLY="$(req "$PINNED_LIST_SUBJECT" "")"
is_err "$LIST_REPLY" && die "pinned.list failed: $LIST_REPLY"
echo "$LIST_REPLY" | jq .
ok "$(jq '[.. | objects | select(has("id")) | .id] | length' <<<"$LIST_REPLY" 2>/dev/null || echo '?') message(s) pinned"

# ---- 7. Unpin the message --------------------------------------------------
step "Unpinning the message"
UNPIN_SUBJECT="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID.msg.unpin"
UNPIN_REPLY="$(req "$UNPIN_SUBJECT" "{\"messageId\":\"$MSG_ID\"}")"
is_err "$UNPIN_REPLY" && die "unpin failed: $UNPIN_REPLY"
info "reply: $UNPIN_REPLY"
LIST_AFTER="$(req "$PINNED_LIST_SUBJECT" "")"
ok "unpinned — pinned list is now: $LIST_AFTER"

# ---- 8. Favorite toggle (on, then off) -------------------------------------
step "Toggling favorite (room-service — flips the bit each call)"
FAV_SUBJECT="chat.user.$ACCOUNT.request.room.$ROOM_ID.$SITE_ID.favorite.toggle"
FAV1="$(req "$FAV_SUBJECT" "")"
is_err "$FAV1" && die "favorite.toggle failed: $FAV1"
ok "toggle #1 → favorite=$(jq -r '.favorite' <<<"$FAV1")  ${DIM}($FAV1)${RESET}"
FAV2="$(req "$FAV_SUBJECT" "")"
is_err "$FAV2" && die "favorite.toggle failed: $FAV2"
ok "toggle #2 → favorite=$(jq -r '.favorite' <<<"$FAV2")  ${DIM}($FAV2)${RESET}"

# ---- 9. Show the live room events we captured ------------------------------
step "Room events captured during the demo"
sleep 1  # let the last events land
if [[ -s "$EVENTS_FILE" ]]; then
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    echo "    ${DIM}$(jq -c '{event: (.eventType // .event // .type), messageId: (.messageId // .message.id)}' <<<"$line" 2>/dev/null || echo "$line")${RESET}"
  done <"$EVENTS_FILE"
else
  info "(no room events captured — broadcast-worker may not be running)"
fi

echo
echo "${BOLD}${GREEN}Demo complete.${RESET}  room=$ROOM_ID  message=$MSG_ID"
