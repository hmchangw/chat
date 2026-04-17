#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.test.yml"
COMPOSE=(docker compose -f "$COMPOSE_FILE")

wait_for_mongo() {
  local attempt
  for attempt in $(seq 1 10); do
    if "${COMPOSE[@]}" exec -T mongodb mongosh --quiet \
        --eval "db.adminCommand('ping').ok" \
        | grep -q "^1$"; then
      return 0
    fi
    echo "waiting for mongodb (attempt $attempt/10)..."
    sleep 1
  done
  echo "mongodb did not become ready in 10s" >&2
  return 1
}

insert_collection() {
  local collection="$1"
  local json_file="$2"

  # Ship the JSON file into the mongodb container, then let mongosh parse it.
  "${COMPOSE[@]}" cp "$json_file" "mongodb:/tmp/$collection.json"

  "${COMPOSE[@]}" exec -T mongodb mongosh mongodb://localhost:27017/chat --quiet --eval "
    const docs = JSON.parse(cat('/tmp/${collection}.json'));
    db.${collection}.drop();
    const res = db.${collection}.insertMany(docs);
    print('${collection}: inserted ' + Object.keys(res.insertedIds).length);
  "
}

echo "waiting for mongodb..."
wait_for_mongo

echo "seeding collections..."
insert_collection users "$SCRIPT_DIR/seed/users.json"
insert_collection rooms "$SCRIPT_DIR/seed/rooms.json"
insert_collection subscriptions "$SCRIPT_DIR/seed/subscriptions.json"

echo "seed complete"
