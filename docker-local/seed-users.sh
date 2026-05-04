#!/usr/bin/env bash
#
# Seed the `users` collection with dev fixtures (alice, bob). dev-auth
# accepts any account at login but doesn't insert into Mongo, so workers
# crash with "user not found". _id == account matches frontend convention.
# Idempotent.

set -euo pipefail

CONTAINER="${MONGO_CONTAINER:-chat-local-mongodb}"
DB="${MONGO_DB:-chat}"
SITE_ID="${SITE_ID:-site-local}"

if ! docker container inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "ERROR: $CONTAINER is not running. Run 'make deps-up' first." >&2
  exit 1
fi

echo "Seeding users into $CONTAINER/$DB (siteId=$SITE_ID)..."

docker exec "$CONTAINER" mongosh --quiet "$DB" --eval "
  const users = [
    { _id: 'alice', account: 'alice', siteId: '$SITE_ID', engName: 'Alice', chineseName: 'Alice', employeeId: 'E0001', sectId: 'dev', sectName: 'Dev' },
    { _id: 'bob',   account: 'bob',   siteId: '$SITE_ID', engName: 'Bob',   chineseName: 'Bob',   employeeId: 'E0002', sectId: 'dev', sectName: 'Dev' }
  ];
  const ops = users.map(u => ({
    updateOne: { filter: { _id: u._id }, update: { \$set: u }, upsert: true }
  }));
  const res = db.users.bulkWrite(ops);
  print('upserted: ' + res.upsertedCount + ', modified: ' + res.modifiedCount + ', matched: ' + res.matchedCount);
"

echo "Done. Login as 'alice' or 'bob' (siteId=$SITE_ID) in dev mode."
