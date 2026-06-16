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
#   4. seeds a roster of demo users (so room-service can resolve members and you
#      can act as different accounts)
#   5. seeds custom emoji shortcodes so reactions validate (reactions require a
#      registered custom emoji; there is no built-in/unicode set)
#   6. mints a fresh room (owner + $MEMBERS) + message
#
# Writes the ids to /tmp/room_id.txt and /tmp/msg_id.txt and prints copy-paste
# commands. Pass --logs to stream the app logs live in this terminal once setup
# is done (otherwise run ./demo-logs.sh in a separate terminal).
#
# Env overrides: ACCOUNT, SITE_ID, MEMBERS, REACT_EMOJIS, NATS_BIN_DIR, NATS_URL,
# NATS_CREDS, VAULT_C, VAULT_TOKEN, CASS_C, MONGO_C.

set -euo pipefail

FOLLOW_LOGS=0
[ "${1:-}" = "--logs" ] && FOLLOW_LOGS=1

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ACCOUNT="${ACCOUNT:-alice}"
SITE_ID="${SITE_ID:-site-local}"
MEMBERS="${MEMBERS:-bob carol dave}"   # extra members added to the demo room
NATS_BIN_DIR="${NATS_BIN_DIR:-/home/codespace/.local/bin}"
NATS_URL="${NATS_URL:-nats://localhost:4222}"
NATS_CREDS="${NATS_CREDS:-$HERE/backend.creds}"
VAULT_C="${VAULT_C:-chat-local-services-vault-1}"
VAULT_TOKEN="${VAULT_TOKEN:-dev-only-token}"
CASS_C="${CASS_C:-chat-local-cassandra}"
MONGO_C="${MONGO_C:-chat-local-mongodb}"
REACT_EMOJIS="${REACT_EMOJIS:-thumbsup heart tada}"

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

# 4. Seed a roster of demo users. room-service resolves members against the users
#    collection, so these must exist before you create rooms or act as them
#    (ACCOUNT=erin ...). Idempotent upsert; safe to re-run. Includes a bot
#    (helper.bot) — bots bypass the large-room pin guard. Different departments
#    let you exercise org/dept scenarios.
log "seeding demo users"
docker exec "$MONGO_C" mongosh chat --quiet --eval "
  const roster = [
    ['alice','Alice','爱丽丝','Engineering'], ['bob','Bob','鲍勃','Engineering'],
    ['carol','Carol','卡罗尔','Product'],     ['dave','Dave','戴夫','Product'],
    ['erin','Erin','艾琳','Design'],          ['frank','Frank','弗兰克','Design'],
    ['grace','Grace','格蕾丝','Sales'],        ['heidi','Heidi','海蒂','Sales'],
    ['ivan','Ivan','伊万','Support'],          ['judy','Judy','朱迪','Support'],
    ['mallory','Mallory','玛洛里','Security'],  ['trent','Trent','特伦特','Security'],
    ['helper.bot','Helper Bot','助手','Bots'],
  ];
  roster.forEach(([acc,en,zh,dept]) =>
    db.users.updateOne(
      {_id:'u-'+acc},
      {\$set:{account:acc,siteId:'$SITE_ID',engName:en,chineseName:zh,
              deptId:dept.toLowerCase(),deptName:dept,sectId:'',sectName:'',employeeId:''}},
      {upsert:true}));
  print('users: ' + db.users.find({siteId:'$SITE_ID'},{_id:0,account:1}).toArray().map(u=>u.account).join(', '));
" 2>&1 | tail -1

# 5. Seed custom emoji shortcodes so reactions validate. history-service checks
#    the custom_emojis collection (no built-in set); seed BEFORE any react so the
#    60s negative-lookup cache is never poisoned. Upsert = idempotent.
log "seeding reaction emoji ($REACT_EMOJIS)"
docker exec "$MONGO_C" mongosh chat --quiet --eval "
  ['$(echo "$REACT_EMOJIS" | sed "s/ /','/g")'].forEach(sc =>
    db.custom_emojis.updateOne(
      {siteId:'$SITE_ID',shortcode:sc},
      {\$setOnInsert:{_id:'ce-'+sc+'-$SITE_ID',siteId:'$SITE_ID',shortcode:sc,
        imageUrl:'https://example.com/e/'+sc+'.png',createdBy:'u-$ACCOUNT',createdAt:Date.now()}},
      {upsert:true}));
  print('custom_emojis for $SITE_ID: ' +
    db.custom_emojis.find({siteId:'$SITE_ID'},{_id:0,shortcode:1}).toArray().map(e=>e.shortcode).join(', '));
" 2>&1 | tail -1

# 6. Mint a fresh room + message (creating a room doubles as a readiness probe).
#    The room includes $MEMBERS so you can act as any of them (ACCOUNT=bob ...);
#    $ACCOUNT is the owner. Users NOT listed here are non-members — act as one to
#    exercise the not_subscribed path.
log "minting a fresh room (owner=$ACCOUNT, members=$MEMBERS) + message"
MEMBERS_JSON="[\"$(echo "$MEMBERS" | sed 's/  */","/g')\"]"
NC=(nats --server "$NATS_URL" --creds "$NATS_CREDS")
RID=""
for _ in $(seq 1 30); do
  RID="$("${NC[@]}" req "chat.user.$ACCOUNT.request.room.$SITE_ID.create" \
        "{\"name\":\"pin-fav-demo\",\"users\":$MEMBERS_JSON}" --raw 2>/dev/null | jq -r '.roomId // empty' || true)"
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
./docker-local/demo-pin-fav.sh react $RID $MID thumbsup   # toggle; run twice to add then remove
./docker-local/demo-pin-fav.sh fav   $RID
./docker-local/demo-pin-fav.sh unfav $RID

# live logs (or run ./docker-local/demo-logs.sh in another terminal):
./docker-local/demo-setup.sh --logs        # re-run with --logs to stream here
EOF

if [ "$FOLLOW_LOGS" = "1" ]; then
  log "following app logs (Ctrl+C to stop) — drive the demo from another terminal"
  exec "$HERE/demo-logs.sh"
fi
