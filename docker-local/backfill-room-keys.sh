#!/usr/bin/env bash
#
# Mint a P-256 room key in Valkey for any room without one. For rooms
# created before room-service started minting on create. Idempotent.

set -euo pipefail

MONGO_CONTAINER="${MONGO_CONTAINER:-chat-local-mongodb}"
VALKEY_CONTAINER="${VALKEY_CONTAINER:-chat-local-valkey}"
DB="${MONGO_DB:-chat}"

if ! docker container inspect -f '{{.State.Running}}' "$MONGO_CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $MONGO_CONTAINER not running" >&2; exit 1
fi
if ! docker container inspect -f '{{.State.Running}}' "$VALKEY_CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $VALKEY_CONTAINER not running" >&2; exit 1
fi

room_ids=$(docker exec "$MONGO_CONTAINER" mongosh "$DB" --quiet --eval 'db.rooms.find({}, {_id:1}).forEach(r => print(r._id))')

if [ -z "$room_ids" ]; then
  echo "(no rooms to check)"
  exit 0
fi

# Single shared P-256 key for all backfilled rooms; dev only.
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

openssl ecparam -name prime256v1 -genkey -noout -out "$tmpdir/priv.pem" 2>/dev/null
priv_b64=$(openssl ec -in "$tmpdir/priv.pem" -text -noout 2>/dev/null \
  | awk '/priv:/{flag=1; next} /pub:/{flag=0} flag' \
  | tr -d ' :\n' \
  | xxd -r -p \
  | base64)
pub_b64=$(openssl ec -in "$tmpdir/priv.pem" -text -noout 2>/dev/null \
  | awk '/pub:/{flag=1; next} /ASN1 OID:/{flag=0} flag' \
  | tr -d ' :\n' \
  | xxd -r -p \
  | base64)

if [ -z "$priv_b64" ] || [ -z "$pub_b64" ]; then
  echo "ERROR: failed to extract P-256 key bytes via openssl" >&2; exit 1
fi

added=0
skipped=0
for rid in $room_ids; do
  key="room:${rid}:key"
  exists=$(docker exec "$VALKEY_CONTAINER" valkey-cli exists "$key")
  if [ "$exists" = "1" ]; then
    skipped=$((skipped + 1))
    continue
  fi
  docker exec "$VALKEY_CONTAINER" valkey-cli hset "$key" pub "$pub_b64" priv "$priv_b64" ver 0 > /dev/null
  added=$((added + 1))
done

echo "rooms with new key: $added | already had key: $skipped"
