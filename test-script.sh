#!/usr/bin/env bash
#
# End-to-end test script for the message.thread.read RPC in room-service.
# Exercises every scenario listed in
# docs/superpowers/specs/2026-05-20-thread-read-rpc-design.md
# against a running local stack.
#
# This script is temporary — remove after manual verification.
#
# Prerequisites (run once before this script):
#   make deps-up
#   make up SERVICE=room-service    # in another terminal, leave running
#
# Usage:
#   ./test-script.sh                # run all scenarios
#   ./test-script.sh -v             # verbose (show raw NATS/mongo output too)
#   ./test-script.sh <scenario>     # run a single scenario by number, e.g. 3

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$SCRIPT_DIR"
readonly NATS_CREDS_HOST="$SCRIPT_DIR/docker-local/backend.creds"
readonly NETWORK="chat-local"
readonly NATS_URL="nats://chat-local-nats:4222"
readonly MONGO_CONTAINER="chat-local-mongodb"
readonly MONGO_DB="chat"
readonly SITE_ID="${SITE_ID:-site-local}"
readonly TEST_PREFIX="e2e_threadread"

# --- Site-b federation harness (used by scenarios 8 and 9) -----------------
readonly SITE_B="site-b"
readonly SITE_B_INBOX_STREAM="INBOX_${SITE_B}"
readonly SITE_B_OUTBOX_STREAM="OUTBOX_${SITE_ID}"
readonly SITE_B_MONGO_DB="chat_${SITE_B//-/_}"
readonly SITE_B_INBOX_CONTAINER="${TEST_PREFIX}_inbox_b"
readonly SITE_B_INBOX_IMAGE="inbox-worker-inbox-worker"
SITE_B_FEDERATION_READY=0

VERBOSE=0
RUN_ONLY=""
for arg in "$@"; do
  case "$arg" in
    -v|--verbose) VERBOSE=1 ;;
    [0-9]*) RUN_ONLY="$arg" ;;
    -h|--help)
      sed -n '2,/^$/p' "$0" | sed 's/^# \?//'
      exit 0 ;;
  esac
done

# --- output helpers ---------------------------------------------------------
RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BLUE=$'\033[0;34m'; CYAN=$'\033[0;36m'; DIM=$'\033[2m'; NC=$'\033[0m'
PASS_COUNT=0; FAIL_COUNT=0
declare -a FAILED_TESTS=()

log()    { printf '%s\n' "$*"; }
info()   { printf '%s[i]%s %s\n' "$BLUE" "$NC" "$*"; }
ok()     { printf '%s[ok]%s %s\n' "$GREEN" "$NC" "$*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail()   { printf '%s[FAIL]%s %s\n' "$RED" "$NC" "$*"; FAIL_COUNT=$((FAIL_COUNT+1)); FAILED_TESTS+=("$current_scenario"); }
warn()   { printf '%s[!]%s %s\n' "$YELLOW" "$NC" "$*"; }
section(){ printf '\n%s===%s %s\n' "$BLUE" "$NC" "$*"; }
verbose(){ [[ $VERBOSE -eq 1 ]] && printf '   %s\n' "$*" || true; }

step()   { printf '  %s>%s %s\n' "$CYAN" "$NC" "$*" >&2; }
seeded() { printf '    %sseed%s %s\n' "$DIM" "$NC" "$*" >&2; }
sent()   { printf '    %s→ %s%s\n' "$YELLOW" "$NC" "$*" >&2; }
recv()   { printf '    %s← %s%s\n' "$GREEN" "$NC" "$*" >&2; }
state()  { printf '    %s· %s%s\n' "$DIM" "$NC" "$*" >&2; }

# --- precondition checks ----------------------------------------------------
check_prereqs() {
  command -v docker >/dev/null || { echo "docker not found"; exit 2; }
  [[ -f "$NATS_CREDS_HOST" ]] || { echo "missing $NATS_CREDS_HOST — run docker-local/setup.sh"; exit 2; }
  docker network inspect "$NETWORK" >/dev/null 2>&1 || { echo "docker network $NETWORK not found — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q "^${MONGO_CONTAINER}$" || { echo "$MONGO_CONTAINER not running — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q '^chat-local-room-service$' || warn "chat-local-room-service not running — run 'make up SERVICE=room-service' in another terminal"

  info "probing room-service responder for siteID=${SITE_ID}..."
  local probe_subj="chat.user.${TEST_PREFIX}_probe.request.room.${TEST_PREFIX}_nonexistent.${SITE_ID}.message.thread.read"
  local probe_resp
  probe_resp=$(nats_request "$probe_subj" '{"threadId":"x"}' 2>/dev/null || true)
  if [[ -z "$probe_resp" ]] && [[ "$NATS_LAST_STDERR" == *"no responders"* ]]; then
    echo
    echo "ERROR: no NATS responder for siteID=${SITE_ID} on message.thread.read."
    echo "  - confirm room-service is running (docker ps | grep room-service)"
    echo "  - confirm its SITE_ID matches: docker exec chat-local-room-service printenv SITE_ID"
    echo "  - override with: SITE_ID=<your-site> $0"
    exit 2
  fi
  info "responder reachable"
}

# --- NATS request via ephemeral nats-box ------------------------------------
NATS_LAST_STDERR=""
nats_request() {
  local subject="$1" payload="$2"
  local stderr_file rc
  stderr_file="$(mktemp)"
  local out
  out=$(docker run --rm -i \
        --network "$NETWORK" \
        -v "$NATS_CREDS_HOST:/creds:ro" \
        natsio/nats-box:latest \
        nats --server "$NATS_URL" --creds /creds req \
             --raw --timeout 5s "$subject" "$payload" 2>"$stderr_file")
  rc=$?
  NATS_LAST_STDERR="$(cat "$stderr_file")"
  rm -f "$stderr_file"
  [[ $rc -ne 0 ]] && return $rc
  printf '%s' "$out"
}

nats_cmd() {
  docker run --rm -i \
    --network "$NETWORK" \
    -v "$NATS_CREDS_HOST:/creds:ro" \
    natsio/nats-box:latest \
    nats --server "$NATS_URL" --creds /creds "$@"
}

# --- Mongo helpers ----------------------------------------------------------
mongo_eval()   { docker exec -i "$MONGO_CONTAINER" mongosh --quiet "$MONGO_DB" --eval "$1"; }
mongo_b_eval() { docker exec -i "$MONGO_CONTAINER" mongosh --quiet "$SITE_B_MONGO_DB" --eval "$1"; }

mongo_insert() { mongo_eval "db.${1}.insertOne(${2})" >/dev/null; }
mongo_delete() { mongo_eval "db.${1}.deleteMany(${2})" >/dev/null; }

# Returns a single field's value from a single document (?? guards against
# falsy values like false/0 collapsing to "null").
mongo_get_field() {
  local collection="$1" filter="$2" field="$3"
  mongo_eval "JSON.stringify(((db.${collection}.findOne(${filter}) ?? {}).${field}) ?? null)" \
    | tr -d '\n' \
    | sed -e 's/^"//;s/"$//' -e 's/\\"/"/g'
}

mongo_b_get_field() {
  local collection="$1" filter="$2" field="$3"
  mongo_b_eval "JSON.stringify(((db.${collection}.findOne(${filter}) ?? {}).${field}) ?? null)" \
    | tr -d '\n' \
    | sed -e 's/^"//;s/"$//' -e 's/\\"/"/g'
}

# Pretty-print the current Subscription doc relevant to thread-read.
dump_sub() {
  local label="$1" filter="$2"
  local alert tu ls
  alert=$(mongo_get_field "subscriptions" "$filter" "alert")
  tu=$(mongo_get_field    "subscriptions" "$filter" "threadUnread")
  ls=$(mongo_get_field    "subscriptions" "$filter" "lastSeenAt")
  state "${label} Subscription:    alert=${alert} threadUnread=${tu} lastSeenAt=${ls}"
}

dump_sub_b() {
  local label="$1" filter="$2"
  local alert tu
  alert=$(mongo_b_get_field "subscriptions" "$filter" "alert")
  tu=$(mongo_b_get_field    "subscriptions" "$filter" "threadUnread")
  state "${label} Subscription@${SITE_B}: alert=${alert} threadUnread=${tu}"
}

dump_threadsub() {
  local label="$1" filter="$2"
  local ls ua hm
  ls=$(mongo_get_field "thread_subscriptions" "$filter" "lastSeenAt")
  ua=$(mongo_get_field "thread_subscriptions" "$filter" "updatedAt")
  hm=$(mongo_get_field "thread_subscriptions" "$filter" "hasMention")
  state "${label} ThreadSubscription:   lastSeenAt=${ls} updatedAt=${ua} hasMention=${hm}"
}

dump_threadsub_b() {
  local label="$1" filter="$2"
  local ls hm
  ls=$(mongo_b_get_field "thread_subscriptions" "$filter" "lastSeenAt")
  hm=$(mongo_b_get_field "thread_subscriptions" "$filter" "hasMention")
  state "${label} ThreadSubscription@${SITE_B}: lastSeenAt=${ls} hasMention=${hm}"
}

# --- assertion helpers ------------------------------------------------------
current_scenario=""
assert_contains() {
  local label="$1" actual="$2" needle="$3"
  if [[ "$actual" == *"$needle"* ]]; then ok "$label contains \"$needle\""; else fail "$label expected to contain \"$needle\", got: $actual"; fi
}
assert_not_contains() {
  local label="$1" actual="$2" forbidden="$3"
  if [[ "$actual" != *"$forbidden"* ]]; then ok "$label does not contain \"$forbidden\""; else fail "$label should not contain \"$forbidden\", got: $actual"; fi
}
assert_eq() {
  local label="$1" actual="$2" expected="$3"
  if [[ "$actual" == "$expected" ]]; then ok "$label = $actual"; else fail "$label expected $expected, got $actual"; fi
}
assert_neq() {
  local label="$1" actual="$2" forbidden="$3"
  if [[ "$actual" != "$forbidden" ]]; then ok "$label != $forbidden (got $actual)"; else fail "$label should not equal $forbidden"; fi
}

# --- cleanup ----------------------------------------------------------------
cleanup_test_data() {
  mongo_delete "rooms"                "{ _id: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "subscriptions"        "{ roomId: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "thread_subscriptions" "{ roomId: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "users"                "{ account: { \$regex: '^${TEST_PREFIX}' } }"
}

# --- site-b federation harness ---------------------------------------------
build_inbox_worker_image() {
  if docker image inspect "$SITE_B_INBOX_IMAGE" >/dev/null 2>&1; then return; fi
  info "building $SITE_B_INBOX_IMAGE (one-time)..."
  (cd "$REPO_ROOT" && docker compose -f inbox-worker/deploy/docker-compose.yml build) >/dev/null
}

# Ensure OUTBOX_<SITE_ID> exists; idempotent re-add returns err_code 10058.
ensure_outbox_stream() {
  local cfg
  cfg=$(cat <<EOF
{
  "name": "${SITE_B_OUTBOX_STREAM}",
  "subjects": ["outbox.${SITE_ID}.>"],
  "retention": "limits",
  "storage": "file",
  "max_consumers": -1,
  "max_msgs": -1,
  "max_bytes": -1,
  "max_age": 0,
  "discard": "old",
  "num_replicas": 1
}
EOF
)
  local resp
  resp=$(nats_request '$JS.API.STREAM.CREATE.'"${SITE_B_OUTBOX_STREAM}" "$cfg") || {
    fail "ensure OUTBOX request failed: ${NATS_LAST_STDERR}"; return 1; }
  if [[ "$resp" == *'"err_code":10058'* ]] || [[ "$resp" == *"stream name already in use"* ]]; then
    verbose "${SITE_B_OUTBOX_STREAM} already exists — reusing"; return 0
  fi
  if [[ "$resp" == *'"error"'* ]]; then fail "JetStream rejected OUTBOX config: $resp"; return 1; fi
  info "created ${SITE_B_OUTBOX_STREAM}"
}

# Create INBOX_site-b with Sources that transform outbox.<src>.to.site-b.<event>
# into chat.inbox.site-b.aggregate.<event> (inbox-worker's filter shape).
create_inbox_site_b_stream() {
  local cfg
  cfg=$(cat <<EOF
{
  "name": "${SITE_B_INBOX_STREAM}",
  "subjects": ["chat.inbox.${SITE_B}.*", "chat.inbox.${SITE_B}.aggregate.>"],
  "retention": "limits",
  "storage": "file",
  "max_consumers": -1,
  "max_msgs": -1,
  "max_bytes": -1,
  "max_age": 0,
  "discard": "old",
  "num_replicas": 1,
  "sources": [
    {
      "name": "${SITE_B_OUTBOX_STREAM}",
      "subject_transforms": [
        { "src": "outbox.${SITE_ID}.to.${SITE_B}.>", "dest": "chat.inbox.${SITE_B}.aggregate.>" }
      ]
    }
  ]
}
EOF
)
  nats_cmd stream rm --force "$SITE_B_INBOX_STREAM" >/dev/null 2>&1 || true
  local resp
  resp=$(nats_request '$JS.API.STREAM.CREATE.'"${SITE_B_INBOX_STREAM}" "$cfg") || {
    fail "JetStream create-stream request failed: ${NATS_LAST_STDERR}"; return 1; }
  if [[ "$resp" == *'"error"'* ]]; then fail "JetStream rejected stream config: $resp"; return 1; fi
  if [[ "$resp" != *'"type"'*'stream_create_response'* ]]; then fail "unexpected stream-create reply: $resp"; return 1; fi
}

start_site_b_inbox_worker() {
  docker rm -f "$SITE_B_INBOX_CONTAINER" >/dev/null 2>&1 || true
  docker run -d \
    --name "$SITE_B_INBOX_CONTAINER" \
    --network "$NETWORK" \
    -e NATS_URL=nats://chat-local-nats:4222 \
    -e NATS_CREDS_FILE=/etc/nats/backend.creds \
    -e SITE_ID="$SITE_B" \
    -e MONGO_URI=mongodb://chat-local-mongodb:27017 \
    -e MONGO_DB="$SITE_B_MONGO_DB" \
    -e BOOTSTRAP_STREAMS=false \
    -v "$NATS_CREDS_HOST:/etc/nats/backend.creds:ro" \
    "$SITE_B_INBOX_IMAGE" >/dev/null
}

wait_for_site_b_inbox_worker() {
  local i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 1
    if docker logs "$SITE_B_INBOX_CONTAINER" 2>&1 | grep -qE 'inbox-worker started|consumer.*created'; then
      return 0
    fi
    if ! docker ps --format '{{.Names}}' | grep -q "^${SITE_B_INBOX_CONTAINER}$"; then
      warn "site-b inbox-worker exited early; tail of logs:"
      docker logs --tail 30 "$SITE_B_INBOX_CONTAINER" 2>&1 | sed 's/^/    /' | head -40 || true
      return 1
    fi
  done
  warn "site-b inbox-worker didn't log readiness within 10s; continuing anyway"
}

setup_site_b_federation() {
  if [[ $SITE_B_FEDERATION_READY -eq 1 ]]; then return 0; fi
  build_inbox_worker_image
  ensure_outbox_stream || return 1
  create_inbox_site_b_stream || return 1
  start_site_b_inbox_worker || return 1
  wait_for_site_b_inbox_worker || return 1
  SITE_B_FEDERATION_READY=1
  info "site-b federation harness ready (stream=${SITE_B_INBOX_STREAM}, db=${SITE_B_MONGO_DB})"
}

teardown_site_b_federation() {
  if [[ $SITE_B_FEDERATION_READY -eq 1 ]] || docker ps -a --format '{{.Names}}' | grep -q "^${SITE_B_INBOX_CONTAINER}$"; then
    docker rm -f "$SITE_B_INBOX_CONTAINER" >/dev/null 2>&1 || true
    nats_cmd stream rm --force "$SITE_B_INBOX_STREAM" >/dev/null 2>&1 || true
    mongo_b_eval 'db.dropDatabase()' >/dev/null 2>&1 || true
  fi
}

trap 'cleanup_test_data; teardown_site_b_federation' EXIT

# --- seeding helpers --------------------------------------------------------
new_room_id()        { echo "${TEST_PREFIX}_r_$(date +%s%N)_$RANDOM"; }
new_thread_room_id() { echo "${TEST_PREFIX}_tr_$(date +%s%N)_$RANDOM"; }
new_parent_id()      { echo "${TEST_PREFIX}_p_$(date +%s%N)_$RANDOM"; }
now_iso()            { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }
iso_offset() {
  local off="$1"
  date -u -d "${off} minutes" +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null \
    || date -u -v"${off}M" +%Y-%m-%dT%H:%M:%S.%3NZ
}

seed_user() {
  local account="$1" site_id="${2:-$SITE_ID}"
  mongo_insert "users" "{
    _id: '${account}_uid', account: '${account}', siteId: '${site_id}'
  }"
  seeded "user account=${account} siteId=${site_id}"
}

seed_room() {
  local room_id="$1" creator="$2"
  mongo_insert "rooms" "{
    _id: '${room_id}', name: 'e2e ${room_id}', type: 'channel',
    createdBy: '${creator}_uid', siteId: '${SITE_ID}', userCount: 1,
    createdAt: ISODate('$(now_iso)'), updatedAt: ISODate('$(now_iso)')
  }"
  seeded "room id=${room_id} type=channel"
}

# seed_sub <roomId> <account> <threadUnreadJson> <alert>
seed_sub() {
  local room_id="$1" account="$2" thread_unread="$3" alert="$4"
  local tu_clause=", threadUnread: []"
  [[ -n "$thread_unread" ]] && tu_clause=", threadUnread: $thread_unread"
  mongo_insert "subscriptions" "{
    _id: '${TEST_PREFIX}_sub_${RANDOM}_${RANDOM}',
    u: { _id: '${account}_uid', account: '${account}' },
    roomId: '${room_id}', roomType: 'channel', siteId: '${SITE_ID}',
    roles: ['member'],
    joinedAt: ISODate('$(iso_offset -60)'),
    alert: ${alert}
    ${tu_clause}
  }"
  seeded "subscription account=${account} roomId=${room_id} alert=${alert} threadUnread=${thread_unread:-[]}"
}

# Variant for site-b (user's home site). Cross-site case requires it.
seed_sub_b() {
  local room_id="$1" account="$2" thread_unread="$3" alert="$4"
  local tu_clause=", threadUnread: []"
  [[ -n "$thread_unread" ]] && tu_clause=", threadUnread: $thread_unread"
  mongo_b_eval "db.subscriptions.insertOne({
    _id: '${TEST_PREFIX}_subb_${RANDOM}_${RANDOM}',
    u: { _id: '${account}_uid', account: '${account}' },
    roomId: '${room_id}', roomType: 'channel', siteId: '${SITE_ID}',
    roles: ['member'],
    joinedAt: ISODate('$(iso_offset -60)'),
    alert: ${alert}
    ${tu_clause}
  })" >/dev/null
  seeded "subscription@${SITE_B} account=${account} roomId=${room_id} alert=${alert} threadUnread=${thread_unread:-[]}"
}

# seed_thread_sub <roomId> <threadRoomId> <parentMsgId> <account> <hasMention> [lastSeenAtIso]
seed_thread_sub() {
  local room_id="$1" thread_room_id="$2" parent_id="$3" account="$4" has_mention="$5" last_seen="${6:-}"
  local ls_clause=""
  if [[ -n "$last_seen" ]]; then
    ls_clause=", lastSeenAt: ISODate('$last_seen')"
  else
    ls_clause=", lastSeenAt: null"
  fi
  mongo_insert "thread_subscriptions" "{
    _id: '${TEST_PREFIX}_ts_${RANDOM}_${RANDOM}',
    parentMessageId: '${parent_id}',
    roomId: '${room_id}',
    threadRoomId: '${thread_room_id}',
    userId: '${account}_uid',
    userAccount: '${account}',
    siteId: '${SITE_ID}',
    hasMention: ${has_mention},
    createdAt: ISODate('$(iso_offset -60)'),
    updatedAt: ISODate('$(iso_offset -60)')
    ${ls_clause}
  }"
  seeded "threadSubscription account=${account} parent=${parent_id} threadRoomId=${thread_room_id} hasMention=${has_mention} lastSeenAt=${last_seen:-null}"
}

seed_thread_sub_b() {
  local room_id="$1" thread_room_id="$2" parent_id="$3" account="$4" has_mention="$5" last_seen="${6:-}"
  local ls_clause=""
  if [[ -n "$last_seen" ]]; then
    ls_clause=", lastSeenAt: ISODate('$last_seen')"
  else
    ls_clause=", lastSeenAt: null"
  fi
  mongo_b_eval "db.thread_subscriptions.insertOne({
    _id: '${TEST_PREFIX}_tsb_${RANDOM}_${RANDOM}',
    parentMessageId: '${parent_id}',
    roomId: '${room_id}',
    threadRoomId: '${thread_room_id}',
    userId: '${account}_uid',
    userAccount: '${account}',
    siteId: '${SITE_ID}',
    hasMention: ${has_mention},
    createdAt: ISODate('$(iso_offset -60)'),
    updatedAt: ISODate('$(iso_offset -60)')
    ${ls_clause}
  })" >/dev/null
  seeded "threadSubscription@${SITE_B} account=${account} parent=${parent_id} hasMention=${has_mention} lastSeenAt=${last_seen:-null}"
}

# Send the thread.read RPC, echoing subject + payload + response.
send_thread_read() {
  local account="$1" room_id="$2" body="$3"
  local subject="chat.user.${account}.request.room.${room_id}.${SITE_ID}.message.thread.read"
  step "REQUEST"
  sent "subject: ${subject}"
  sent "payload: ${body}"
  local resp
  resp=$(nats_request "$subject" "$body") || {
    recv "<no response — ${NATS_LAST_STDERR}>"
    return 1
  }
  step "RESPONSE"
  recv "${resp:-<empty>}"
  printf '%s' "$resp"
}

# --- scenarios --------------------------------------------------------------

scenario_1_happy_alert_clears() {
  current_scenario="1: happy local — alert clears (only one thread unread)"
  section "$current_scenario"
  step "SEEDING"
  local room_id tr_id parent account resp
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id)
  account="${TEST_PREFIX}_alice"

  seed_user "$account"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "['${parent}']" "true"
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$account" "true"

  step "BEFORE"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || { fail "RPC failed"; return; }
  assert_contains "reply" "$resp" '"status":"accepted"'

  step "AFTER"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local alert tu has_mention
  alert=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  tu=$(mongo_get_field    "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "threadUnread")
  has_mention=$(mongo_get_field "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "hasMention")
  assert_eq "subscription.alert"        "$alert"       "false"
  assert_eq "subscription.threadUnread" "$tu"          "null"   # omitempty -> $unset -> absent
  assert_eq "threadSubscription.hasMention" "$has_mention" "false"
}

scenario_2_happy_alert_stays() {
  current_scenario="2: happy local — alert stays (more thread unread remain)"
  section "$current_scenario"
  step "SEEDING"
  local room_id tr_id parent other account resp
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id); other=$(new_parent_id)
  account="${TEST_PREFIX}_bob"

  seed_user "$account"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "['${parent}','${other}']" "true"
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$account" "true"

  step "BEFORE"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || { fail "RPC failed"; return; }
  assert_contains "reply" "$resp" '"status":"accepted"'

  step "AFTER"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local alert tu
  alert=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  tu=$(mongo_get_field    "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "threadUnread")
  assert_eq "subscription.alert"        "$alert" "true"
  assert_eq "subscription.threadUnread" "$tu"    "[\"${other}\"]"
}

scenario_3_idempotent_not_in_array() {
  current_scenario="3: idempotent — threadId not in ThreadUnread (no-op pull)"
  section "$current_scenario"
  step "SEEDING"
  local room_id tr_id parent other account resp
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id); other=$(new_parent_id)
  account="${TEST_PREFIX}_carol"

  seed_user "$account"
  seed_room "$room_id" "$account"
  # Subscription's threadUnread contains a different parent, NOT the one we're reading.
  seed_sub "$room_id" "$account" "['${other}']" "true"
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$account" "true"

  step "BEFORE"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || { fail "RPC failed"; return; }
  assert_contains "reply" "$resp" '"status":"accepted"'

  step "AFTER"
  dump_sub        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local alert tu has_mention
  alert=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  tu=$(mongo_get_field    "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "threadUnread")
  has_mention=$(mongo_get_field "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "hasMention")
  # Pull is a no-op; alert stays true (other parent still unread); thread-sub hasMention still clears.
  assert_eq "subscription.alert"        "$alert"       "true"
  assert_eq "subscription.threadUnread" "$tu"          "[\"${other}\"]"
  assert_eq "threadSubscription.hasMention" "$has_mention" "false"
}

scenario_4_empty_threadId() {
  current_scenario="4: empty threadId — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id account resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_dave"
  seed_user "$account"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "" "false"

  resp=$(send_thread_read "$account" "$room_id" '{"threadId":""}') || true
  assert_contains "reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "threadId is required"
}

scenario_5_not_room_member() {
  current_scenario="5: not a room member — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id tr_id parent owner outsider resp
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id)
  owner="${TEST_PREFIX}_owner"; outsider="${TEST_PREFIX}_eve"
  seed_user "$owner"
  seed_user "$outsider"
  seed_room "$room_id" "$owner"
  seed_sub "$room_id" "$owner" "" "false"
  # The outsider HAS a thread subscription (somehow) but NOT a room subscription.
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$outsider" "true"

  resp=$(send_thread_read "$outsider" "$room_id" "{\"threadId\":\"${parent}\"}") || true
  assert_contains "reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "only room members"
}

scenario_6_thread_sub_not_found() {
  current_scenario="6: thread subscription not found — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id parent account resp
  room_id=$(new_room_id); parent=$(new_parent_id)
  account="${TEST_PREFIX}_frank"

  seed_user "$account"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "['${parent}']" "true"
  # No thread_subscriptions row for (parent, account).

  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || true
  assert_contains "reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "thread subscription not found"
}

scenario_7_cross_room_defensive_filter() {
  current_scenario="7: cross-room defensive filter — threadId belongs to a different room"
  section "$current_scenario"
  step "SEEDING"
  local room_a room_b tr_id parent account resp
  room_a=$(new_room_id); room_b=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id)
  account="${TEST_PREFIX}_grace"

  seed_user "$account"
  seed_room "$room_a" "$account"
  seed_room "$room_b" "$account"
  seed_sub  "$room_a" "$account" "" "false"
  seed_sub  "$room_b" "$account" "" "false"
  # ThreadSubscription's roomId is room_b. Request will arrive on room_a's subject.
  mongo_insert "thread_subscriptions" "{
    _id: '${TEST_PREFIX}_ts_${RANDOM}',
    parentMessageId: '${parent}',
    roomId: '${room_b}',
    threadRoomId: '${tr_id}',
    userId: '${account}_uid',
    userAccount: '${account}',
    siteId: '${SITE_ID}',
    hasMention: false,
    createdAt: ISODate('$(now_iso)'),
    updatedAt: ISODate('$(now_iso)')
  }"
  seeded "threadSubscription parent=${parent} BUT roomId=${room_b} (request will target room=${room_a})"

  resp=$(send_thread_read "$account" "$room_a" "{\"threadId\":\"${parent}\"}") || true
  assert_contains "reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "thread subscription not found"
}

scenario_8_cross_site_outbox_inbox() {
  current_scenario="8: cross-site federation — outbox publish + site-b inbox-worker applies"
  section "$current_scenario"

  if ! setup_site_b_federation; then fail "site-b federation harness failed to start"; return; fi

  step "SEEDING"
  local room_id tr_id parent other account old_lastseen
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id); other=$(new_parent_id)
  account="${TEST_PREFIX}_iris"
  old_lastseen=$(iso_offset -60)

  # Site A (room's home site, authoritative): user lives on site-b.
  seed_user "$account" "$SITE_B"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "['${parent}','${other}']" "true"
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$account" "true" "$old_lastseen"

  # Site B (user's home site, cache): pre-seed parallel rows so inbox-worker has
  # documents to mutate. Without these, the inbox-worker's UpdateOne is a silent
  # no-op (correct — replays may arrive before member_added).
  seed_sub_b        "$room_id" "$account" "['${parent}','${other}']" "true"
  seed_thread_sub_b "$room_id" "$tr_id" "$parent" "$account" "true" "$old_lastseen"

  step "BEFORE"
  dump_sub          "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub    "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"
  dump_sub_b        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub_b  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local resp
  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || { fail "RPC failed"; return; }
  assert_contains "reply" "$resp" '"status":"accepted"'

  step "OUTBOX (site-a → site-b)"
  sleep 1
  local outbox_subject="outbox.${SITE_ID}.to.${SITE_B}.thread_read"
  local outbox_msg
  outbox_msg=$(nats_request '$JS.API.STREAM.MSG.GET.'"${SITE_B_OUTBOX_STREAM}" \
              "{\"last_by_subj\":\"${outbox_subject}\"}" 2>&1 || true)
  if [[ "$outbox_msg" == *'"message"'* ]] && [[ "$outbox_msg" == *"$outbox_subject"* ]]; then
    ok "${SITE_B_OUTBOX_STREAM} contains a thread_read message at ${outbox_subject}"
  else
    fail "no thread_read message at ${outbox_subject} in ${SITE_B_OUTBOX_STREAM}"
    log "$(echo "$outbox_msg" | sed 's/^/    /')"
    return
  fi

  step "INBOX (sourced into INBOX_site-b)"
  local inbox_subject="chat.inbox.${SITE_B}.aggregate.thread_read"
  local inbox_msg="" i
  for i in 1 2 3 4 5 6 7 8 9 10; do
    sleep 0.5
    inbox_msg=$(nats_request '$JS.API.STREAM.MSG.GET.'"${SITE_B_INBOX_STREAM}" \
                "{\"last_by_subj\":\"${inbox_subject}\"}" 2>&1 || true)
    [[ "$inbox_msg" == *'"message"'* ]] && [[ "$inbox_msg" == *"$inbox_subject"* ]] && break
  done
  if [[ "$inbox_msg" == *'"message"'* ]] && [[ "$inbox_msg" == *"$inbox_subject"* ]]; then
    ok "${SITE_B_INBOX_STREAM} sourced a thread_read message at ${inbox_subject}"
  else
    fail "${SITE_B_INBOX_STREAM} did not source a thread_read message within 5s"
    log "$(echo "$inbox_msg" | sed 's/^/    /')"
    return
  fi

  step "AFTER (poll site-b until inbox-worker applies)"
  local applied=0 b_tu b_alert b_hm b_ls
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20; do
    sleep 0.5
    b_hm=$(mongo_b_get_field "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "hasMention")
    if [[ "$b_hm" == "false" ]]; then applied=1; break; fi
  done

  dump_sub          "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub    "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"
  dump_sub_b        "  " "{ roomId: '${room_id}', 'u.account': '${account}' }"
  dump_threadsub_b  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  if [[ $applied -eq 1 ]]; then
    ok "site-b ThreadSubscription.hasMention cleared by inbox-worker"
  else
    fail "site-b ThreadSubscription.hasMention did not clear within 10s"
    log "  inbox-worker logs (tail):"
    docker logs --tail 40 "$SITE_B_INBOX_CONTAINER" 2>&1 | sed 's/^/    /' || true
    return
  fi

  # Authoritative-mirror checks: site-b ends up with the source-computed state.
  b_alert=$(mongo_b_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  b_tu=$(mongo_b_get_field    "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "threadUnread")
  b_ls=$(mongo_b_get_field    "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "lastSeenAt")
  assert_eq "site-b subscription.alert"        "$b_alert" "true"
  assert_eq "site-b subscription.threadUnread" "$b_tu"    "[\"${other}\"]"
  assert_neq "site-b threadSubscription.lastSeenAt" "$b_ls" "null"
}

scenario_9_cross_site_out_of_order_guard() {
  current_scenario="9: cross-site out-of-order — \$lt guard prevents ThreadSubscription regression"
  section "$current_scenario"

  if ! setup_site_b_federation; then fail "site-b federation harness failed to start"; return; fi

  step "SEEDING"
  local room_id tr_id parent account future_ls
  room_id=$(new_room_id); tr_id=$(new_thread_room_id); parent=$(new_parent_id)
  account="${TEST_PREFIX}_jane"
  # Future lastSeenAt on site-b — simulates a newer read having already arrived.
  future_ls=$(iso_offset 60)

  # Site A: as if this user just read the thread (their actual time.Now() is "now", which is < future_ls on site-b).
  seed_user "$account" "$SITE_B"
  seed_room "$room_id" "$account"
  seed_sub "$room_id" "$account" "['${parent}']" "true"
  seed_thread_sub "$room_id" "$tr_id" "$parent" "$account" "true"

  # Site B: ThreadSubscription is stamped in the FUTURE. The inbox-worker's
  # $lt guard MUST prevent the older event from regressing this.
  seed_sub_b        "$room_id" "$account" "['${parent}']" "true"
  seed_thread_sub_b "$room_id" "$tr_id" "$parent" "$account" "false" "$future_ls"

  step "BEFORE"
  dump_threadsub_b  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local resp
  resp=$(send_thread_read "$account" "$room_id" "{\"threadId\":\"${parent}\"}") || { fail "RPC failed"; return; }
  assert_contains "reply" "$resp" '"status":"accepted"'

  step "AFTER (allow time for inbox-worker; expect no regression)"
  # Give inbox-worker time to receive + try-and-skip the update.
  sleep 3
  dump_threadsub_b  "  " "{ threadRoomId: '${tr_id}', userAccount: '${account}' }"

  local ls hm
  ls=$(mongo_b_get_field "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "lastSeenAt")
  hm=$(mongo_b_get_field "thread_subscriptions" "{ threadRoomId: '${tr_id}', userAccount: '${account}' }" "hasMention")
  # $lt guard — both lastSeenAt AND hasMention come from the same $set; guard rejects the whole update.
  assert_contains "site-b lastSeenAt (unchanged)" "$ls" "${future_ls%.*}"
  assert_eq "site-b hasMention (unchanged)" "$hm" "false"
}

scenario_10_invalid_subject() {
  current_scenario="10: invalid subject — no responder or rejected"
  section "$current_scenario"
  local resp
  resp=$(nats_request "chat.user.${TEST_PREFIX}_kate.request.message.thread.read" '{"threadId":"x"}') || true
  if [[ -z "$resp" ]]; then
    ok "no subscriber on malformed subject (expected)"
  else
    assert_contains "error" "$resp" "error"
  fi
}

# --- driver -----------------------------------------------------------------

run_scenario() {
  case "$1" in
    1)  scenario_1_happy_alert_clears ;;
    2)  scenario_2_happy_alert_stays ;;
    3)  scenario_3_idempotent_not_in_array ;;
    4)  scenario_4_empty_threadId ;;
    5)  scenario_5_not_room_member ;;
    6)  scenario_6_thread_sub_not_found ;;
    7)  scenario_7_cross_room_defensive_filter ;;
    8)  scenario_8_cross_site_outbox_inbox ;;
    9)  scenario_9_cross_site_out_of_order_guard ;;
    10) scenario_10_invalid_subject ;;
    *)  echo "unknown scenario $1"; exit 2 ;;
  esac
}

main() {
  check_prereqs
  info "cleaning any leftover ${TEST_PREFIX}_* test data..."
  cleanup_test_data

  if [[ -n "$RUN_ONLY" ]]; then
    run_scenario "$RUN_ONLY"
  else
    for n in 1 2 3 4 5 6 7 8 9 10; do
      run_scenario "$n"
    done
  fi

  section "summary"
  printf '  passed: %s%d%s\n' "$GREEN" "$PASS_COUNT" "$NC"
  printf '  failed: %s%d%s\n' "$RED" "$FAIL_COUNT" "$NC"
  if [[ $FAIL_COUNT -gt 0 ]]; then
    printf '\nfailed scenarios:\n'
    for t in "${FAILED_TESTS[@]}"; do printf '  - %s\n' "$t"; done
    exit 1
  fi
}

main "$@"
