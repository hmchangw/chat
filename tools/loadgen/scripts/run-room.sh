#!/usr/bin/env bash
# run-room.sh — exercise the room-service RPCs.
#
# Drives 5 room-service RPCs at the weighted mix from the `room-rpc`
# preset: RoomsList (60%), RoomsGet (20%), MemberList (10%),
# RoomCreate (8%), MemberAdd (2%). RoomCreate/MemberAdd are
# state-mutating; their generated IDs/names carry the WriteIDPrefix
# `loadgen-` so forensic checks of the loadgen Mongo DB are trivial.
#
# Usage:
#   ./up.sh
#   ./run-room.sh
#
# Knobs (env vars):
#   RATE=200      target rps                  (default 200)
#   DURATION=60s  measured duration           (default 60s)
#   WARMUP=5s     warmup discarded            (default 5s)
#   PRESET=room-rpc                           (default room-rpc)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

RATE="${RATE:-200}"
DURATION="${DURATION:-60s}"
WARMUP="${WARMUP:-5s}"
PRESET="${PRESET:-room-rpc}"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET — 1000 users, 100 rooms)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running room-rpc @ ${RATE} rps for $DURATION (warm-up: $WARMUP)"
echo "    Mix: RoomsList 60% / RoomsGet 20% / MemberList 10% / RoomCreate 8% / MemberAdd 2%"
echo "    Writes carry WriteIDPrefix='loadgen-' for cleanup forensics"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=room-rpc \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP"
