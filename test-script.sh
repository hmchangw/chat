#!/usr/bin/env bash
#
# End-to-end test script for the message.read RPC in room-service.
# Exercises every scenario listed in docs/superpowers/specs/2026-05-04-message-read-rpc-design.md
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
#   ./test-script.sh -v             # verbose (shows full NATS/mongo output)
#   ./test-script.sh <scenario>     # run a single scenario by number, e.g. 3

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly NATS_CREDS_HOST="$SCRIPT_DIR/docker-local/backend.creds"
readonly NETWORK="chat-local"
readonly NATS_URL="nats://chat-local-nats:4222"
readonly MONGO_CONTAINER="chat-local-mongodb"
readonly MONGO_DB="chat"
# Matches SITE_ID in room-service/deploy/docker-compose.yml. Override via env if
# you've changed the service's siteID.
readonly SITE_ID="${SITE_ID:-site-local}"
readonly TEST_PREFIX="e2e_msgread"

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
RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[1;33m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'
PASS_COUNT=0; FAIL_COUNT=0; SKIP_COUNT=0
declare -a FAILED_TESTS=()

log()    { printf '%s\n' "$*"; }
info()   { printf '%s[i]%s %s\n' "$BLUE" "$NC" "$*"; }
ok()     { printf '%s[ok]%s %s\n' "$GREEN" "$NC" "$*"; PASS_COUNT=$((PASS_COUNT+1)); }
fail()   { printf '%s[FAIL]%s %s\n' "$RED" "$NC" "$*"; FAIL_COUNT=$((FAIL_COUNT+1)); FAILED_TESTS+=("$current_scenario"); }
warn()   { printf '%s[!]%s %s\n' "$YELLOW" "$NC" "$*"; }
section(){ printf '\n%s===%s %s\n' "$BLUE" "$NC" "$*"; }
verbose(){ [[ $VERBOSE -eq 1 ]] && printf '   %s\n' "$*" || true; }

# --- precondition checks ----------------------------------------------------
check_prereqs() {
  command -v docker >/dev/null || { echo "docker not found"; exit 2; }
  [[ -f "$NATS_CREDS_HOST" ]] || { echo "missing $NATS_CREDS_HOST — run docker-local/setup.sh"; exit 2; }
  docker network inspect "$NETWORK" >/dev/null 2>&1 || { echo "docker network $NETWORK not found — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q "^${MONGO_CONTAINER}$" || { echo "$MONGO_CONTAINER not running — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q '^chat-local-room-service$' || warn "chat-local-room-service not running — run 'make up SERVICE=room-service' in another terminal"

  # Probe that something is actually listening on the message-read wildcard for
  # this site. Without this, every scenario reports "RPC failed" and it's not
  # obvious whether the service is down or the SITE_ID is wrong.
  info "probing room-service responder for siteID=${SITE_ID}..."
  local probe_subj="chat.user.${TEST_PREFIX}_probe.request.room.${TEST_PREFIX}_nonexistent.${SITE_ID}.message.read"
  local probe_resp
  probe_resp=$(nats_request "$probe_subj" '{}' 2>/dev/null || true)
  if [[ -z "$probe_resp" ]] && [[ "$NATS_LAST_STDERR" == *"no responders"* ]]; then
    echo
    echo "ERROR: no NATS responder for siteID=${SITE_ID}."
    echo "  - confirm room-service is running (docker ps | grep room-service)"
    echo "  - confirm its SITE_ID matches: docker exec chat-local-room-service printenv SITE_ID"
    echo "  - override with: SITE_ID=<your-site> $0"
    exit 2
  fi
  info "responder reachable"
}

# --- NATS request via ephemeral nats-box ------------------------------------
# Sends a NATS request and prints the reply payload to stdout. Returns non-zero
# on transport error / no responders / timeout. The CLI's stderr (which carries
# the diagnostic, e.g. "nats: no responders available for request") is captured
# to NATS_LAST_STDERR so callers can include it in failure messages.
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

# --- Mongo helpers ----------------------------------------------------------
# Runs a mongosh JS snippet and prints the JSON-stringified result to stdout.
mongo_eval() {
  local js="$1"
  docker exec -i "$MONGO_CONTAINER" mongosh --quiet "$MONGO_DB" --eval "$js"
}

# Insert a document via mongosh. Accepts a JS object literal as the doc.
mongo_insert() {
  local collection="$1" doc="$2"
  mongo_eval "db.${collection}.insertOne(${doc})" >/dev/null
}

mongo_delete() {
  local collection="$1" filter="$2"
  mongo_eval "db.${collection}.deleteMany(${filter})" >/dev/null
}

# Returns the value of a single field from a single document (best-effort
# JSON for nested types). Returns the literal string "null" if doc/field is
# absent. Uses ?? (nullish coalescing) so falsy values like `false` or `0`
# round-trip correctly — `||` would collapse them to "null".
mongo_get_field() {
  local collection="$1" filter="$2" field="$3"
  mongo_eval "JSON.stringify(((db.${collection}.findOne(${filter}) ?? {}).${field}) ?? null)" \
    | tr -d '\n' \
    | sed -e 's/^"//;s/"$//' -e 's/\\"/"/g'
}

# --- assertion helpers ------------------------------------------------------
current_scenario=""
assert_eq() {
  local label="$1" actual="$2" expected="$3"
  if [[ "$actual" == "$expected" ]]; then
    ok "$label = $actual"
  else
    fail "$label expected $expected, got $actual"
  fi
}

assert_contains() {
  local label="$1" actual="$2" needle="$3"
  if [[ "$actual" == *"$needle"* ]]; then
    ok "$label contains \"$needle\""
  else
    fail "$label expected to contain \"$needle\", got $actual"
  fi
}

assert_neq() {
  local label="$1" actual="$2" forbidden="$3"
  if [[ "$actual" != "$forbidden" ]]; then
    ok "$label != $forbidden (got $actual)"
  else
    fail "$label should not equal $forbidden"
  fi
}

# --- cleanup ----------------------------------------------------------------
cleanup_test_data() {
  mongo_delete "rooms"         "{ _id: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "subscriptions" "{ roomId: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "users"         "{ account: { \$regex: '^${TEST_PREFIX}' } }"
}

trap cleanup_test_data EXIT

# --- scenario builders ------------------------------------------------------
# All scenarios use roomId/account prefixed with TEST_PREFIX so cleanup is safe.
new_room_id()    { echo "${TEST_PREFIX}_$(date +%s%N)_$RANDOM"; }
now_iso()        { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }

# Seed a basic local-site subscription (room siteId == handler siteId ==
# user siteId == $SITE_ID).
# Args: roomId, account, lastSeenAtIsoOrEmpty, alert, threadUnreadJsonOrEmpty,
#       lastMsgAtIsoOrEmpty.
seed_basic() {
  local room_id="$1" account="$2" last_seen="$3" alert="$4" thread_unread="$5" last_msg_at="$6"

  local last_seen_clause=""
  [[ -n "$last_seen" ]] && last_seen_clause=", lastSeenAt: ISODate('$last_seen')"
  local thread_clause=", threadUnread: []"
  [[ -n "$thread_unread" ]] && thread_clause=", threadUnread: $thread_unread"
  local last_msg_clause=""
  [[ -n "$last_msg_at" ]] && last_msg_clause=", lastMsgAt: ISODate('$last_msg_at')"

  mongo_insert "users" "{
    _id: '${account}_uid',
    account: '${account}',
    siteId: '${SITE_ID}'
  }"
  mongo_insert "rooms" "{
    _id: '${room_id}',
    name: 'e2e ${room_id}',
    type: 'channel',
    createdBy: '${account}_uid',
    siteId: '${SITE_ID}',
    userCount: 1,
    createdAt: ISODate('$(now_iso)'),
    updatedAt: ISODate('$(now_iso)')
    ${last_msg_clause}
  }"
  mongo_insert "subscriptions" "{
    _id: '${TEST_PREFIX}_sub_${RANDOM}_${RANDOM}',
    u: { _id: '${account}_uid', account: '${account}' },
    roomId: '${room_id}',
    roomType: 'channel',
    siteId: '${SITE_ID}',
    roles: ['owner'],
    joinedAt: ISODate('$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-1H +%Y-%m-%dT%H:%M:%S.%3NZ)'),
    alert: ${alert}
    ${last_seen_clause}
    ${thread_clause}
  }"
}

# Send the message.read RPC. Echoes the reply payload.
send_read() {
  local account="$1" room_id="$2" body="$3"
  nats_request "chat.user.${account}.request.room.${room_id}.${SITE_ID}.message.read" "$body"
}

# --- scenarios --------------------------------------------------------------

scenario_1_happy_alert_clears() {
  current_scenario="1: happy local — alert clears (no thread unread)"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_alice"
  seed_basic "$room_id" "$account" \
    "$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-30M +%Y-%m-%dT%H:%M:%S.%3NZ)" \
    "true" "" \
    "$(date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-15M +%Y-%m-%dT%H:%M:%S.%3NZ)"

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'

  local alert
  alert=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  assert_eq "subscription.alert" "$alert" "false"

  local last_seen
  last_seen=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "lastSeenAt")
  assert_neq "subscription.lastSeenAt" "$last_seen" ""

  local min_last_seen
  min_last_seen=$(mongo_get_field "rooms" "{ _id: '${room_id}' }" "minUserLastSeenAt")
  assert_neq "room.minUserLastSeenAt" "$min_last_seen" ""
}

scenario_2_alert_persists_with_thread_unread() {
  current_scenario="2: alert stays true when thread unread"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_bob"
  seed_basic "$room_id" "$account" \
    "$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-30M +%Y-%m-%dT%H:%M:%S.%3NZ)" \
    "true" "['thread-1']" \
    "$(date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-15M +%Y-%m-%dT%H:%M:%S.%3NZ)"

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'

  local alert
  alert=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "alert")
  assert_eq "subscription.alert" "$alert" "true"
}

scenario_3_never_read_falls_back_to_joined_at() {
  current_scenario="3: never-read sub — falls back to JoinedAt (early-return on recompute)"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_carol"
  # JoinedAt > LastMsgAt → originalLastSeen > LastMsgAt → early return.
  # seed_basic defaults JoinedAt to "1 hour ago"; set LastMsgAt to "2 hours ago".
  seed_basic "$room_id" "$account" "" "false" "" \
    "$(date -u -d '2 hours ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-2H +%Y-%m-%dT%H:%M:%S.%3NZ)"

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'

  # Subscription was still updated.
  local last_seen
  last_seen=$(mongo_get_field "subscriptions" "{ roomId: '${room_id}', 'u.account': '${account}' }" "lastSeenAt")
  assert_neq "subscription.lastSeenAt" "$last_seen" ""

  # Room.MinUserLastSeenAt should NOT be set (early-return path).
  local min_last_seen
  min_last_seen=$(mongo_get_field "rooms" "{ _id: '${room_id}' }" "minUserLastSeenAt")
  assert_eq "room.minUserLastSeenAt" "$min_last_seen" "null"
}

scenario_4_room_never_messaged() {
  current_scenario="4: room never messaged — LastMsgAt nil, early return"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_dave"
  seed_basic "$room_id" "$account" "" "false" "" ""  # no lastMsgAt

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'

  local min_last_seen
  min_last_seen=$(mongo_get_field "rooms" "{ _id: '${room_id}' }" "minUserLastSeenAt")
  assert_eq "room.minUserLastSeenAt" "$min_last_seen" "null"
}

scenario_5_already_up_to_date() {
  current_scenario="5: already up to date — sub.lastSeenAt > room.lastMsgAt, early return"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_eve"
  # lastSeenAt = "5 min ago", lastMsgAt = "30 min ago" → originalLastSeen > lastMsgAt.
  seed_basic "$room_id" "$account" \
    "$(date -u -d '5 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-5M +%Y-%m-%dT%H:%M:%S.%3NZ)" \
    "false" "" \
    "$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-30M +%Y-%m-%dT%H:%M:%S.%3NZ)"

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'

  local min_last_seen
  min_last_seen=$(mongo_get_field "rooms" "{ _id: '${room_id}' }" "minUserLastSeenAt")
  assert_eq "room.minUserLastSeenAt (early return)" "$min_last_seen" "null"
}

scenario_6_not_a_member() {
  current_scenario="6: not a member — error response"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_frank"
  # Seed user + room but NO subscription.
  mongo_insert "users" "{ _id: '${account}_uid', account: '${account}', siteId: '${SITE_ID}' }"
  mongo_insert "rooms" "{
    _id: '${room_id}', name: 'e2e ${room_id}', type: 'channel',
    createdBy: 'someone_else', siteId: '${SITE_ID}', userCount: 0,
    createdAt: ISODate('$(now_iso)'), updatedAt: ISODate('$(now_iso)')
  }"

  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || true
  verbose "reply: $resp"

  assert_contains "error reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "only room members"
}

scenario_7_room_id_mismatch() {
  current_scenario="7: room ID mismatch — body roomId differs from subject"
  section "$current_scenario"
  local room_id account body resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_grace"
  seed_basic "$room_id" "$account" "" "false" "" \
    "$(date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-15M +%Y-%m-%dT%H:%M:%S.%3NZ)"

  body="{\"roomId\":\"${TEST_PREFIX}_DIFFERENT_ROOM\"}"
  resp=$(send_read "$account" "$room_id" "$body") || true
  verbose "reply: $resp"

  assert_contains "error reply" "$resp" '"error"'
  assert_contains "error message" "$resp" "room ID mismatch"
}

scenario_8_empty_body_trusts_subject() {
  current_scenario="8: empty body — trusts subject roomId, succeeds"
  section "$current_scenario"
  local room_id account resp
  room_id=$(new_room_id); account="${TEST_PREFIX}_henry"
  seed_basic "$room_id" "$account" \
    "$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-30M +%Y-%m-%dT%H:%M:%S.%3NZ)" \
    "false" "" \
    "$(date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-15M +%Y-%m-%dT%H:%M:%S.%3NZ)"

  resp=$(send_read "$account" "$room_id" "{}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"

  assert_contains "reply" "$resp" '"status":"accepted"'
}

# Run a one-off `nats` CLI command against the local NATS using backend.creds.
# Echoes stdout; errors go through the caller's redirection.
nats_cmd() {
  docker run --rm -i \
    --network "$NETWORK" \
    -v "$NATS_CREDS_HOST:/creds:ro" \
    natsio/nats-box:latest \
    nats --server "$NATS_URL" --creds /creds "$@"
}

scenario_9_cross_site_outbox() {
  current_scenario="9: cross-site user — outbox event published to OUTBOX_${SITE_ID}"
  section "$current_scenario"
  local room_id account dest_site
  room_id=$(new_room_id); account="${TEST_PREFIX}_iris"; dest_site="site-b"

  # Seed user with a foreign siteId so the handler publishes to outbox.
  mongo_insert "users" "{ _id: '${account}_uid', account: '${account}', siteId: '${dest_site}' }"
  mongo_insert "rooms" "{
    _id: '${room_id}', name: 'e2e ${room_id}', type: 'channel',
    createdBy: '${account}_uid', siteId: '${SITE_ID}', userCount: 1,
    lastMsgAt: ISODate('$(date -u -d '15 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-15M +%Y-%m-%dT%H:%M:%S.%3NZ)'),
    createdAt: ISODate('$(now_iso)'), updatedAt: ISODate('$(now_iso)')
  }"
  mongo_insert "subscriptions" "{
    _id: '${TEST_PREFIX}_sub_xs_${RANDOM}',
    u: { _id: '${account}_uid', account: '${account}' },
    roomId: '${room_id}', roomType: 'channel', siteId: '${SITE_ID}',
    roles: ['member'],
    joinedAt: ISODate('$(date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-1H +%Y-%m-%dT%H:%M:%S.%3NZ)'),
    lastSeenAt: ISODate('$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null || date -u -v-30M +%Y-%m-%dT%H:%M:%S.%3NZ)'),
    alert: false
  }"

  # Trigger the RPC. The handler publishes to OUTBOX_${SITE_ID} via JetStream.
  local body resp
  body="{\"roomId\":\"${room_id}\"}"
  resp=$(send_read "$account" "$room_id" "$body") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }
  verbose "reply: $resp"
  assert_contains "reply" "$resp" '"status":"accepted"'

  # Give JetStream a moment to commit the publish, then query the OUTBOX stream
  # directly. This is reliable: the message is durably stored, no listener-
  # timing window. We dump the last few stream entries and grep for the event
  # type + the test account, which uniquely identifies our publish.
  sleep 1
  local stream_dump
  stream_dump=$(nats_cmd stream view "OUTBOX_${SITE_ID}" --since 30s 2>&1 || true)
  verbose "stream dump (last 30s):"
  verbose "$(echo "$stream_dump" | sed 's/^/    /')"

  if [[ "$stream_dump" == *"subscription_read"* ]] && [[ "$stream_dump" == *"$account"* ]]; then
    ok "OUTBOX_${SITE_ID} contains subscription_read event for ${account}"
  else
    fail "subscription_read event for ${account} not found in OUTBOX_${SITE_ID}"
    log "  hint: dump the stream manually with:"
    log "    docker run --rm --network ${NETWORK} -v ${NATS_CREDS_HOST}:/creds:ro \\"
    log "      natsio/nats-box:latest nats --server ${NATS_URL} --creds /creds \\"
    log "      stream view OUTBOX_${SITE_ID} --since 5m"
    log "  if the message is present but the script can't see it, the room-service"
    log "  may still be using a stale BOOTSTRAP_STREAMS=true config — restart it"
    log "  with 'make down SERVICE=room-service && make up SERVICE=room-service'."
  fi

  # Verify the outbox subject in the dump matches the expected destination site.
  local expected_subj="outbox.${SITE_ID}.to.${dest_site}.subscription_read"
  if [[ "$stream_dump" == *"$expected_subj"* ]]; then
    ok "outbox subject = ${expected_subj}"
  else
    warn "expected subject ${expected_subj} not found in stream dump"
  fi
}

scenario_10_invalid_subject() {
  current_scenario="10: invalid subject — handler rejects"
  section "$current_scenario"
  # Subject without the expected room.{roomID}.{siteID} structure.
  local resp
  resp=$(nats_request "chat.user.${TEST_PREFIX}_jane.request.message.read" '{}') || true
  verbose "reply: $resp"

  if [[ -z "$resp" ]]; then
    # No subscriber on a malformed subject is also acceptable — handler isn't subscribed.
    ok "no subscriber on malformed subject (expected)"
  else
    assert_contains "error" "$resp" "error"
  fi
}

# --- driver -----------------------------------------------------------------

run_scenario() {
  local n="$1"
  case "$n" in
    1)  scenario_1_happy_alert_clears ;;
    2)  scenario_2_alert_persists_with_thread_unread ;;
    3)  scenario_3_never_read_falls_back_to_joined_at ;;
    4)  scenario_4_room_never_messaged ;;
    5)  scenario_5_already_up_to_date ;;
    6)  scenario_6_not_a_member ;;
    7)  scenario_7_room_id_mismatch ;;
    8)  scenario_8_empty_body_trusts_subject ;;
    9)  scenario_9_cross_site_outbox ;;
    10) scenario_10_invalid_subject ;;
    *)  echo "unknown scenario $n"; exit 2 ;;
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
  [[ $SKIP_COUNT -gt 0 ]] && printf '  skipped/manual: %s%d%s\n' "$YELLOW" "$SKIP_COUNT" "$NC"
  if [[ $FAIL_COUNT -gt 0 ]]; then
    printf '\nfailed scenarios:\n'
    for t in "${FAILED_TESTS[@]}"; do printf '  - %s\n' "$t"; done
    exit 1
  fi
}

main "$@"
