#!/usr/bin/env bash
# run-history.sh — exercise the history-service RPCs.
#
# Drives LoadHistory / GetMessageByID / LoadSurrounding / GetThreadMessages
# at the weighted mix declared by the `history-read` preset. Auto-warmup
# is enabled by default so the message-ID pool gets populated before the
# history queries start; otherwise GetMessageByID/LoadSurrounding/
# GetThreadMessages would have no IDs to query.
#
# Usage:
#   ./up.sh
#   ./run-history.sh
#
# Knobs (env vars):
#   RATE=200      target rps                  (default 200)
#   DURATION=60s  measured duration           (default 60s)
#   WARMUP=10s    warmup discarded            (default 10s)
#   PRESET=history-read                       (default history-read)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.yml"

RATE="${RATE:-200}"
DURATION="${DURATION:-60s}"
WARMUP="${WARMUP:-10s}"
PRESET="${PRESET:-history-read}"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running history-read @ ${RATE} rps for $DURATION (warm-up: $WARMUP)"
echo "    Mix: LoadHistory 60% / GetMessageByID 20% / LoadSurrounding 10% / GetThreadMessages 10%"
echo "    Auto-warmup populates the message-ID pool first (~20s of canonical inject)"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=history-read \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP" \
  --auto-warmup=true \
  --auto-warmup-rate=200
