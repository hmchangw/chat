#!/usr/bin/env bash
# run-saturation.sh — push past the SUT's p99 threshold and let the
# abort watcher trip the run.
#
# IMPORTANT: the abort watcher reads its latency samples from the
# `RecordRequest` path, which is fed by the READ scenarios
# (history-read / search-read / room-rpc). It is NOT wired to
# messaging-pipeline's E1/E2 correlator. Therefore this script
# defaults to SCENARIO=history-read, where p99 over the limit
# actually triggers the watcher.
#
# Exit codes:
#   0  ran the full duration cleanly (SUT survived; try higher RATE)
#   1  errors above tolerance (regression; investigate)
#   2  saturation watcher fired (p99 stayed over P99_MS for SUSTAIN) ←
#      the success case for this script
#   3  liveness watcher fired (SUT became unreachable; lower RATE)
#
# Usage:
#   ./up.sh
#   ./run-saturation.sh
#
# Knobs (env vars):
#   SCENARIO=history-read                     (default history-read; or search-read, room-rpc)
#   PRESET=history-read                       (default $SCENARIO)
#   RATE=1500        target rps               (default 1500)
#   DURATION=45s     measured duration        (default 45s)
#   P99_MS=15        p99 limit in ms          (default 15)
#   SUSTAIN=5s       sustain window           (default 5s)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

SCENARIO="${SCENARIO:-history-read}"
PRESET="${PRESET:-$SCENARIO}"
RATE="${RATE:-1500}"
DURATION="${DURATION:-45s}"
P99_MS="${P99_MS:-15}"
SUSTAIN="${SUSTAIN:-5s}"

cd "$DEPLOY_DIR"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

# history-read benefits from a populated message-ID pool; auto-warmup
# does that automatically. Other scenarios don't need it.
AUTO=""
if [ "$SCENARIO" = "history-read" ]; then
  AUTO="--auto-warmup=true --auto-warmup-rate=200"
fi

echo
echo "==> Running $SCENARIO @ ${RATE} rps for $DURATION"
echo "    Abort if p99 > ${P99_MS}ms sustained for $SUSTAIN  (expect exit 2)"
echo "    Liveness watcher disabled — only the saturation knob should trip"
echo

set +e
# shellcheck disable=SC2086
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario="$SCENARIO" \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup=5s \
  --abort-on-p99-ms="$P99_MS" \
  --abort-p99-sustain="$SUSTAIN" \
  --liveness-interval=0 \
  $AUTO
CODE=$?
set -e

echo
case $CODE in
  0) echo "==> SUT survived ${RATE} rps cleanly. Increase RATE to find the saturation point." ;;
  1) echo "==> Errors above tolerance (exit 1). Investigate the consumer-lag table above." ;;
  2) echo "==> Saturation watcher fired (exit 2). p99 stayed over ${P99_MS}ms for ${SUSTAIN}." ;;
  3) echo "==> Liveness watcher fired (exit 3). SUT became unreachable — lower RATE." ;;
  *) echo "==> Unexpected exit code $CODE" ;;
esac
exit $CODE
