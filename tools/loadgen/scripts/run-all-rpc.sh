#!/usr/bin/env bash
# run-all-rpc.sh — run the three request/reply scenarios back-to-back.
#
# Useful as a single-command "is everything still reachable?" sanity
# check after a deploy or schema change. Each scenario gets its own
# preset (history-read / search-read / room-rpc) and prints its own
# summary. Total wall-time ~3 minutes.
#
# Usage:
#   ./up.sh
#   ./run-all-rpc.sh
#
# Knobs (env vars):
#   RATE=150         per-scenario rps         (default 150)
#   DURATION=30s     per-scenario duration    (default 30s)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

RATE="${RATE:-150}"
DURATION="${DURATION:-30s}"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

run_one() {
  local preset="$1"
  local scenario="$2"
  local extra="${3:-}"
  echo
  echo "################################################################"
  echo "# $scenario (preset=$preset, rate=$RATE, duration=$DURATION)"
  echo "################################################################"
  $COMPOSE exec -T loadgen /loadgen seed --preset="$preset" >/dev/null
  # shellcheck disable=SC2086
  $COMPOSE exec -T loadgen /loadgen run \
    --preset="$preset" \
    --scenario="$scenario" \
    --rate="$RATE" \
    --duration="$DURATION" \
    --warmup=5s \
    $extra
}

run_one history-read history-read "--auto-warmup=true --auto-warmup-rate=200"
run_one search-read  search-read   ""
run_one room-rpc     room-rpc      ""

echo
echo "==> All three RPC scenarios complete."
