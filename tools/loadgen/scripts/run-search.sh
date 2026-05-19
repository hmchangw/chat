#!/usr/bin/env bash
# run-search.sh — exercise the search-service RPCs.
#
# Drives SearchMessages / SearchRooms at the weighted mix declared by
# the `search-read` preset, drawing queries uniformly from a 10-token
# bag of generic English so seeded message content yields realistic
# hit rates.
#
# Usage:
#   ./up.sh
#   ./run-search.sh
#
# Knobs (env vars):
#   RATE=100      target rps                  (default 100)
#   DURATION=60s  measured duration           (default 60s)
#   WARMUP=5s     warmup discarded            (default 5s)
#   PRESET=search-read                        (default search-read)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

RATE="${RATE:-100}"
DURATION="${DURATION:-60s}"
WARMUP="${WARMUP:-5s}"
PRESET="${PRESET:-search-read}"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running search-read @ ${RATE} rps for $DURATION (warm-up: $WARMUP)"
echo "    Mix: SearchMessages 50% / SearchRooms 50%"
echo "    Query bag: hello/thanks/team/review/deploy/meeting/update/today/release/status"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=search-read \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP"
