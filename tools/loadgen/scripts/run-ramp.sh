#!/usr/bin/env bash
# run-ramp.sh — ramp the messaging-pipeline rate from FROM rps to TO rps.
#
# Useful for finding the SUT's saturation point: watch the consumer-lag
# table in the final summary and the live progress logs. When peak_pending
# stops returning to 0 between samples, you've found the rate beyond
# which the workers can't keep up.
#
# Usage:
#   ./up.sh
#   ./run-ramp.sh
#
# Knobs (env vars):
#   FROM=100         starting rps             (default 100)
#   TO=2000          ending rps               (default 2000)
#   RAMP_DUR=60s     time to climb            (default 60s)
#   DURATION=90s     total run duration       (default 90s; FROM held for the last 30s once TO is hit)
#   SHAPE=linear     linear|exponential       (default linear)
#   PRESET=realistic                          (default realistic)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

FROM="${FROM:-100}"
TO="${TO:-2000}"
RAMP_DUR="${RAMP_DUR:-60s}"
DURATION="${DURATION:-90s}"
SHAPE="${SHAPE:-linear}"
PRESET="${PRESET:-realistic}"

cd "$DEPLOY_DIR"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Ramping ${FROM} → ${TO} rps over $RAMP_DUR (${SHAPE}), total $DURATION"
echo "    Watch the per-tick rate_bucket label on loadgen_published_total"
echo "    Live: http://localhost:3000  (Grafana)"
echo
# Note: --rate must be 0 when using --ramp-from/--ramp-to (they are
# mutually exclusive; --rate is the default-500 single-rate knob).
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --rate=0 \
  --ramp-from="$FROM" \
  --ramp-to="$TO" \
  --ramp-duration="$RAMP_DUR" \
  --ramp-shape="$SHAPE" \
  --duration="$DURATION" \
  --warmup=5s
