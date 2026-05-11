#!/usr/bin/env bash
# run-mixed.sh — concurrent messaging-pipeline + read scenarios.
#
# Simulates a production-shaped workload: ~70% writes (messaging-pipeline
# at the realistic preset) running concurrently with ~30% reads
# (history + search + room RPCs at smaller rates) — all hitting the
# same NATS/SUT at the same time. Tests cross-scenario isolation and
# whether read latencies suffer under sustained write load.
#
# Each scenario runs in its own background subprocess so the loadgen
# binary handles them as independent runs. Outputs interleave; use
# Grafana for cleaner observation.
#
# Usage:
#   ./up.sh
#   ./run-mixed.sh
#
# Knobs (env vars):
#   WRITE_RATE=500    messaging-pipeline rps   (default 500)
#   READ_RATE=100     each read-scenario rps   (default 100)
#   DURATION=2m       run duration             (default 2m)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

WRITE_RATE="${WRITE_RATE:-500}"
READ_RATE="${READ_RATE:-100}"
DURATION="${DURATION:-2m}"

cd "$DEPLOY_DIR"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures for each preset"
$COMPOSE exec -T loadgen /loadgen seed --preset=realistic    >/dev/null
$COMPOSE exec -T loadgen /loadgen seed --preset=history-read >/dev/null
$COMPOSE exec -T loadgen /loadgen seed --preset=search-read  >/dev/null
$COMPOSE exec -T loadgen /loadgen seed --preset=room-rpc     >/dev/null

echo
echo "==> Launching 4 concurrent runs for $DURATION:"
echo "    messaging-pipeline (realistic)  @ ${WRITE_RATE} rps"
echo "    history-read                    @ ${READ_RATE} rps"
echo "    search-read                     @ ${READ_RATE} rps"
echo "    room-rpc                        @ ${READ_RATE} rps"
echo "    Output will interleave — Grafana at http://localhost:3000 is cleaner."
echo

mkdir -p /tmp/loadgen-mixed
ts=$(date +%s)

$COMPOSE exec -T loadgen /loadgen run --preset=realistic    --scenario=messaging-pipeline --rate="$WRITE_RATE" --duration="$DURATION" --warmup=10s                            >"/tmp/loadgen-mixed/pipeline-${ts}.log" 2>&1 &
PIPE_PID=$!
$COMPOSE exec -T loadgen /loadgen run --preset=history-read --scenario=history-read       --rate="$READ_RATE"  --duration="$DURATION" --warmup=10s --auto-warmup=false        >"/tmp/loadgen-mixed/history-${ts}.log"  2>&1 &
HIST_PID=$!
$COMPOSE exec -T loadgen /loadgen run --preset=search-read  --scenario=search-read        --rate="$READ_RATE"  --duration="$DURATION" --warmup=5s                             >"/tmp/loadgen-mixed/search-${ts}.log"   2>&1 &
SRCH_PID=$!
$COMPOSE exec -T loadgen /loadgen run --preset=room-rpc     --scenario=room-rpc           --rate="$READ_RATE"  --duration="$DURATION" --warmup=5s                             >"/tmp/loadgen-mixed/room-${ts}.log"     2>&1 &
ROOM_PID=$!

echo "    pipeline pid=$PIPE_PID history pid=$HIST_PID search pid=$SRCH_PID room pid=$ROOM_PID"
echo
wait $PIPE_PID $HIST_PID $SRCH_PID $ROOM_PID
echo
echo "==> All 4 runs complete. Per-run summaries:"
echo "    /tmp/loadgen-mixed/pipeline-${ts}.log"
echo "    /tmp/loadgen-mixed/history-${ts}.log"
echo "    /tmp/loadgen-mixed/search-${ts}.log"
echo "    /tmp/loadgen-mixed/room-${ts}.log"
echo
echo "Tail one with:  tail -n 40 /tmp/loadgen-mixed/pipeline-${ts}.log"
