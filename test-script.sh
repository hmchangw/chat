#!/usr/bin/env bash
#
# End-to-end test script for the message.read-receipt RPC in room-service.
# Exercises every scenario listed in
# docs/superpowers/specs/2026-05-08-room-service-read-receipt-rpc-design.md
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
#   ./test-script.sh -v             # verbose (shows full NATS/mongo/cassandra output)
#   ./test-script.sh <scenario>     # run a single scenario by number, e.g. 3

set -euo pipefail

readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly NATS_CREDS_HOST="$SCRIPT_DIR/docker-local/backend.creds"
readonly NETWORK="chat-local"
readonly NATS_URL="nats://chat-local-nats:4222"
readonly MONGO_CONTAINER="chat-local-mongodb"
readonly MONGO_DB="chat"
readonly CASSANDRA_CONTAINER="chat-local-cassandra"
readonly CASSANDRA_KEYSPACE="chat"
readonly SITE_ID="${SITE_ID:-site-local}"
readonly TEST_PREFIX="e2e_readreceipt"

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

# Visible tagged logs (always shown, no -v required). Routed to stderr so they
# remain visible when callers use command substitution to capture stdout.
step()   { printf '  %s>%s %s\n' "$CYAN" "$NC" "$*" >&2; }
seeded() { printf '    %sseed%s %s\n' "$DIM" "$NC" "$*" >&2; }
sent()   { printf '    %s→ %s%s\n' "$YELLOW" "$NC" "$*" >&2; }
recv()   { printf '    %s← %s%s\n' "$GREEN" "$NC" "$*" >&2; }

# --- precondition checks ----------------------------------------------------
check_prereqs() {
  command -v docker >/dev/null || { echo "docker not found"; exit 2; }
  [[ -f "$NATS_CREDS_HOST" ]] || { echo "missing $NATS_CREDS_HOST — run docker-local/setup.sh"; exit 2; }
  docker network inspect "$NETWORK" >/dev/null 2>&1 || { echo "docker network $NETWORK not found — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q "^${MONGO_CONTAINER}$" || { echo "$MONGO_CONTAINER not running — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q "^${CASSANDRA_CONTAINER}$" || { echo "$CASSANDRA_CONTAINER not running — run 'make deps-up'"; exit 2; }
  docker ps --format '{{.Names}}' | grep -q '^chat-local-room-service$' || warn "chat-local-room-service not running — run 'make up SERVICE=room-service' in another terminal"

  info "probing room-service responder for siteID=${SITE_ID}..."
  local probe_subj="chat.user.${TEST_PREFIX}_probe.request.room.${TEST_PREFIX}_nonexistent.${SITE_ID}.message.read-receipt"
  local probe_resp
  probe_resp=$(nats_request "$probe_subj" '{"messageId":"x"}' 2>/dev/null || true)
  if [[ -z "$probe_resp" ]] && [[ "$NATS_LAST_STDERR" == *"no responders"* ]]; then
    echo
    echo "ERROR: no NATS responder for siteID=${SITE_ID} on message.read-receipt."
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

# --- Mongo helpers ----------------------------------------------------------
mongo_eval() {
  local js="$1"
  docker exec -i "$MONGO_CONTAINER" mongosh --quiet "$MONGO_DB" --eval "$js"
}

mongo_insert() {
  local collection="$1" doc="$2"
  mongo_eval "db.${collection}.insertOne(${doc})" >/dev/null
}

mongo_delete() {
  local collection="$1" filter="$2"
  mongo_eval "db.${collection}.deleteMany(${filter})" >/dev/null
}

# --- Cassandra helpers ------------------------------------------------------
cqlsh_exec() {
  local cql="$1"
  docker exec -i "$CASSANDRA_CONTAINER" cqlsh -k "$CASSANDRA_KEYSPACE" -e "$cql"
}

# Insert a row into messages_by_id with the minimum columns the handler reads
# (message_id, room_id, created_at, sender). Uses INSERT...JSON instead of the
# inline UDT literal form ({account: 'x', id: 'y'}) — the latter trips a
# segfault in cqlsh's Python parser on some hosts. With JSON, cqlsh hands the
# raw JSON to the server which parses the UDT, bypassing the crashing path.
seed_message() {
  local message_id="$1" room_id="$2" created_at_iso="$3" sender_account="$4"
  local json="{\"message_id\":\"${message_id}\",\"room_id\":\"${room_id}\",\"created_at\":\"${created_at_iso}\",\"sender\":{\"account\":\"${sender_account}\",\"id\":\"${sender_account}_uid\"}}"
  cqlsh_exec "INSERT INTO messages_by_id JSON '${json}'" >/dev/null
}

# --- assertion helpers ------------------------------------------------------
current_scenario=""
assert_contains() {
  local label="$1" actual="$2" needle="$3"
  if [[ "$actual" == *"$needle"* ]]; then
    ok "$label contains \"$needle\""
  else
    fail "$label expected to contain \"$needle\", got: $actual"
  fi
}

assert_not_contains() {
  local label="$1" actual="$2" forbidden="$3"
  if [[ "$actual" != *"$forbidden"* ]]; then
    ok "$label does not contain \"$forbidden\""
  else
    fail "$label should not contain \"$forbidden\", got: $actual"
  fi
}

# --- cleanup ----------------------------------------------------------------
cleanup_test_data() {
  mongo_delete "rooms"         "{ _id: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "subscriptions" "{ roomId: { \$regex: '^${TEST_PREFIX}' } }"
  mongo_delete "users"         "{ account: { \$regex: '^${TEST_PREFIX}' } }"
  cqlsh_exec "DELETE FROM messages_by_id WHERE message_id IN ('${TEST_PREFIX}_msg1','${TEST_PREFIX}_msg2','${TEST_PREFIX}_msg3','${TEST_PREFIX}_msg4','${TEST_PREFIX}_msg5','${TEST_PREFIX}_msg_other_room','${TEST_PREFIX}_msg_notsender')" >/dev/null 2>&1 || true
}

trap 'cleanup_test_data' EXIT

# --- scenario builders ------------------------------------------------------
new_room_id() { echo "${TEST_PREFIX}_$(date +%s%N)_$RANDOM"; }
now_iso()     { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }
iso_offset()  {
  # Args: signed minutes offset, e.g. "-30" or "+5". Prints UTC ISO8601.
  local off="$1"
  date -u -d "${off} minutes" +%Y-%m-%dT%H:%M:%S.%3NZ 2>/dev/null \
    || date -u -v"${off}M" +%Y-%m-%dT%H:%M:%S.%3NZ
}

# Seed a user with display fields populated.
seed_user() {
  local account="$1" eng="$2" zh="$3"
  mongo_insert "users" "{
    _id: '${account}_uid',
    account: '${account}',
    siteId: '${SITE_ID}',
    engName: '${eng}',
    chineseName: '${zh}'
  }"
  seeded "user account=${account} engName=${eng} chineseName=${zh}"
}

# Seed a channel room owned by the sender.
seed_room() {
  local room_id="$1" creator_account="$2"
  mongo_insert "rooms" "{
    _id: '${room_id}',
    name: 'e2e ${room_id}',
    type: 'channel',
    createdBy: '${creator_account}_uid',
    siteId: '${SITE_ID}',
    userCount: 1,
    createdAt: ISODate('$(now_iso)'),
    updatedAt: ISODate('$(now_iso)')
  }"
  seeded "room id=${room_id} type=channel createdBy=${creator_account}"
}

# Seed a subscription. lastSeenAtIso may be empty (sub never read).
seed_sub() {
  local room_id="$1" account="$2" last_seen_iso="$3"
  local last_seen_clause=""
  [[ -n "$last_seen_iso" ]] && last_seen_clause=", lastSeenAt: ISODate('$last_seen_iso')"
  mongo_insert "subscriptions" "{
    _id: '${TEST_PREFIX}_sub_${RANDOM}_${RANDOM}',
    u: { _id: '${account}_uid', account: '${account}' },
    roomId: '${room_id}',
    roomType: 'channel',
    siteId: '${SITE_ID}',
    roles: ['member'],
    joinedAt: ISODate('$(iso_offset -60)'),
    alert: false,
    threadUnread: []
    ${last_seen_clause}
  }"
  seeded "subscription account=${account} roomId=${room_id} lastSeenAt=${last_seen_iso:-<none>}"
}

# Echo the seeded message after writing the row, so the log reads chronologically.
seed_message_logged() {
  local message_id="$1" room_id="$2" created_at_iso="$3" sender_account="$4"
  seed_message_logged "$message_id" "$room_id" "$created_at_iso" "$sender_account"
  seeded "message id=${message_id} roomId=${room_id} createdAt=${created_at_iso} sender=${sender_account}"
}

# Send the read-receipt RPC, echoing subject, payload, and response. Echoes the
# reply payload to stdout (last line) so callers can capture it via $(...).
send_receipt() {
  local account="$1" room_id="$2" body="$3"
  local subject="chat.user.${account}.request.room.${room_id}.${SITE_ID}.message.read-receipt"
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

scenario_1_happy_two_readers() {
  current_scenario="1: happy path — two readers returned, sender excluded"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender msg_id resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"
  msg_id="${TEST_PREFIX}_msg1"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_user "${TEST_PREFIX}_bob" "Bob" "鮑勃"
  seed_user "${TEST_PREFIX}_carol" "Carol" "卡羅"
  seed_user "${TEST_PREFIX}_dave" "Dave" "戴夫"
  seed_room "$room_id" "$sender"

  local msg_time read_after read_before
  msg_time=$(iso_offset -30)
  read_after=$(iso_offset -10)
  read_before=$(iso_offset -45)

  # Sender read after the message — should be excluded by the handler.
  seed_sub "$room_id" "$sender" "$read_after"
  # Bob read after — should appear.
  seed_sub "$room_id" "${TEST_PREFIX}_bob" "$read_after"
  # Carol read after — should appear.
  seed_sub "$room_id" "${TEST_PREFIX}_carol" "$read_after"
  # Dave read before the message — should not appear.
  seed_sub "$room_id" "${TEST_PREFIX}_dave" "$read_before"

  seed_message_logged "$msg_id" "$room_id" "$msg_time" "$sender"

  resp=$(send_receipt "$sender" "$room_id" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"readers"'
  assert_contains "reply" "$resp" '"account":"'"${TEST_PREFIX}"'_bob"'
  assert_contains "reply" "$resp" '"account":"'"${TEST_PREFIX}"'_carol"'
  assert_contains "reply" "$resp" '"engName":"Bob"'
  assert_contains "reply" "$resp" '"chineseName":"鮑勃"'
  assert_not_contains "reply" "$resp" '"account":"'"${sender}"'"'
  assert_not_contains "reply" "$resp" '"account":"'"${TEST_PREFIX}"'_dave"'
}

scenario_2_empty_readers() {
  current_scenario="2: empty readers — no subscription matches lastSeenAt >= createdAt"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender msg_id resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"
  msg_id="${TEST_PREFIX}_msg2"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_user "${TEST_PREFIX}_dave" "Dave" "戴夫"
  seed_room "$room_id" "$sender"

  local msg_time
  msg_time=$(iso_offset -1)
  # Both subs read well before the message — nothing should match.
  seed_sub "$room_id" "$sender" "$(iso_offset -60)"
  seed_sub "$room_id" "${TEST_PREFIX}_dave" "$(iso_offset -60)"

  seed_message_logged "$msg_id" "$room_id" "$msg_time" "$sender"

  resp=$(send_receipt "$sender" "$room_id" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"readers":[]'
}

scenario_3_not_a_room_member() {
  current_scenario="3: not a room member — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender outsider msg_id resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"
  outsider="${TEST_PREFIX}_eve"
  msg_id="${TEST_PREFIX}_msg3"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_user "$outsider" "Eve" "夏娃"
  seed_room "$room_id" "$sender"
  seed_sub "$room_id" "$sender" "$(iso_offset -10)"
  seed_message_logged "$msg_id" "$room_id" "$(iso_offset -30)" "$sender"

  # Outsider has no subscription in the room.
  resp=$(send_receipt "$outsider" "$room_id" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"error"'
  assert_contains "reply" "$resp" 'only room members'
}

scenario_4_message_not_found() {
  current_scenario="4: message not found — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_room "$room_id" "$sender"
  seed_sub "$room_id" "$sender" "$(iso_offset -10)"

  resp=$(send_receipt "$sender" "$room_id" "{\"messageId\":\"${TEST_PREFIX}_does_not_exist\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"error"'
  assert_contains "reply" "$resp" 'message not found'
}

scenario_5_message_in_another_room() {
  current_scenario="5: cross-room guard — message belongs to a different room"
  section "$current_scenario"
  step "SEEDING"
  local room_a room_b sender msg_id resp
  room_a=$(new_room_id)
  room_b=$(new_room_id)
  sender="${TEST_PREFIX}_alice"
  msg_id="${TEST_PREFIX}_msg_other_room"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_room "$room_a" "$sender"
  seed_room "$room_b" "$sender"
  seed_sub "$room_a" "$sender" "$(iso_offset -10)"
  seed_sub "$room_b" "$sender" "$(iso_offset -10)"

  # Message lives in room_b.
  seed_message_logged "$msg_id" "$room_b" "$(iso_offset -30)" "$sender"

  # Query against room_a should be rejected.
  resp=$(send_receipt "$sender" "$room_a" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"error"'
  assert_contains "reply" "$resp" 'does not belong to this room'
}

scenario_6_not_the_sender() {
  current_scenario="6: not the sender — handler rejects"
  section "$current_scenario"
  step "SEEDING"
  local room_id author requester msg_id resp
  room_id=$(new_room_id)
  author="${TEST_PREFIX}_alice"
  requester="${TEST_PREFIX}_bob"
  msg_id="${TEST_PREFIX}_msg_notsender"

  seed_user "$author" "Alice" "愛麗絲"
  seed_user "$requester" "Bob" "鮑勃"
  seed_room "$room_id" "$author"
  seed_sub "$room_id" "$author" "$(iso_offset -10)"
  seed_sub "$room_id" "$requester" "$(iso_offset -10)"

  seed_message_logged "$msg_id" "$room_id" "$(iso_offset -30)" "$author"

  resp=$(send_receipt "$requester" "$room_id" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"error"'
  assert_contains "reply" "$resp" 'only the message sender'
}

scenario_7_empty_message_id() {
  current_scenario="7: empty messageId — validation error"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_room "$room_id" "$sender"
  seed_sub "$room_id" "$sender" "$(iso_offset -10)"

  resp=$(send_receipt "$sender" "$room_id" '{}') || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"error"'
}

scenario_8_invalid_subject() {
  current_scenario="8: invalid subject — handler not subscribed"
  section "$current_scenario"
  step "SEEDING"
  local resp
  resp=$(nats_request "chat.user.${TEST_PREFIX}_jane.request.message.read-receipt" '{"messageId":"x"}') || true

  if [[ -z "$resp" ]]; then
    ok "no subscriber on malformed subject (expected)"
  else
    assert_contains "reply" "$resp" '"error"'
  fi
}

scenario_9_user_missing_display_fields() {
  current_scenario="9: reader with missing user record is dropped from result"
  section "$current_scenario"
  step "SEEDING"
  local room_id sender msg_id resp
  room_id=$(new_room_id)
  sender="${TEST_PREFIX}_alice"
  msg_id="${TEST_PREFIX}_msg5"

  seed_user "$sender" "Alice" "愛麗絲"
  seed_user "${TEST_PREFIX}_bob" "Bob" "鮑勃"
  # Carol's subscription is created but no users-collection record exists —
  # she should be dropped by the $unwind (preserveNullAndEmptyArrays: false).
  seed_room "$room_id" "$sender"
  seed_sub "$room_id" "$sender" "$(iso_offset -10)"
  seed_sub "$room_id" "${TEST_PREFIX}_bob" "$(iso_offset -10)"
  seed_sub "$room_id" "${TEST_PREFIX}_ghost" "$(iso_offset -10)"

  seed_message_logged "$msg_id" "$room_id" "$(iso_offset -30)" "$sender"

  resp=$(send_receipt "$sender" "$room_id" "{\"messageId\":\"${msg_id}\"}") || { fail "RPC failed: ${NATS_LAST_STDERR}"; return; }

  assert_contains "reply" "$resp" '"account":"'"${TEST_PREFIX}"'_bob"'
  assert_not_contains "reply" "$resp" '"account":"'"${TEST_PREFIX}"'_ghost"'
}

# --- driver -----------------------------------------------------------------

run_scenario() {
  local n="$1"
  case "$n" in
    1) scenario_1_happy_two_readers ;;
    2) scenario_2_empty_readers ;;
    3) scenario_3_not_a_room_member ;;
    4) scenario_4_message_not_found ;;
    5) scenario_5_message_in_another_room ;;
    6) scenario_6_not_the_sender ;;
    7) scenario_7_empty_message_id ;;
    8) scenario_8_invalid_subject ;;
    9) scenario_9_user_missing_display_fields ;;
    *) echo "unknown scenario $n"; exit 2 ;;
  esac
}

main() {
  check_prereqs
  info "cleaning any leftover ${TEST_PREFIX}_* test data..."
  cleanup_test_data

  if [[ -n "$RUN_ONLY" ]]; then
    run_scenario "$RUN_ONLY"
  else
    for n in 1 2 3 4 5 6 7 8 9; do
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
