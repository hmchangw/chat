#!/usr/bin/env bash
# run-realistic.sh — 2-minute run on the realistic preset (1000 users, 100 rooms).
#
# Usage:
#   ./up.sh              # one-time stack bring-up
#   ./run-realistic.sh   # this script
#
# Larger fixtures and higher rate than quickstart.sh; intended as the
# "what does a realistic load look like" experience. ~60k messages
# total, mention rate 10%, fan-out shaped like real chat traffic.
#
# Assumes ./up.sh has already brought the stack up. If you want a
# smaller / faster smoke test, use ./quickstart.sh instead.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.yml"

RATE="${RATE:-500}"
DURATION="${DURATION:-2m}"
WARMUP="${WARMUP:-15s}"
PRESET="${PRESET:-realistic}"


if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running messaging-pipeline preset=$PRESET @ ${RATE} rps for $DURATION"
echo "    Warm-up: $WARMUP   (samples discarded)"
echo "    Watch live: http://localhost:3000  (Grafana, admin/admin)"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP"

echo
echo "==> Run complete. Stack is still up."
echo "Tweak knobs:"
echo "  RATE=1000 DURATION=5m ./run-realistic.sh"
echo
echo "Or tear down:  ./down.sh"
