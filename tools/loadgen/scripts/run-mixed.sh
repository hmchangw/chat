#!/usr/bin/env bash
# run-mixed.sh — concurrent messaging-pipeline + read scenarios.
#
# Simulates a production-shaped workload: ~70% writes (messaging-pipeline
# at the realistic preset) running concurrently with ~30% reads
# (history + search + room RPCs at smaller rates) — all hitting the
# same NATS/SUT at the same time. Tests cross-scenario isolation and
# whether read latencies suffer under sustained write load.
#
# Bug 5 fix — three things the original script got wrong:
#
#   1. METRICS_ADDR ports: previously every subprocess inherited the
#      default :9099, so three of the four loadgen processes failed to
#      bind their /metrics endpoint and ran without Prometheus scrape
#      coverage. Each scenario now gets a distinct port (9091..9094).
#
#   2. RUN_ID correlation: each loadgen process generates its own UUIDv7
#      run_id and logs it at startup. We grep + echo them up-front so
#      operators can correlate Grafana dashboards / SUT-side traces
#      with each subprocess.
#
#   3. Shared abort: a sentinel file (`/tmp/loadgen-mixed/.abort-<ts>`)
#      is created by any subprocess that exits non-zero; a small watcher
#      kills the remaining children promptly so a saturation/liveness
#      trip in one scenario doesn't leave the others spinning for the
#      full duration.
#
# Usage:
#   ./up.sh
#   ./run-mixed.sh
#   ./run-mixed.sh --dry-run            # print env + commands, exit 0
#
# Knobs (env vars):
#   WRITE_RATE=500        messaging-pipeline rps   (default 500)
#   READ_RATE=100         each read-scenario rps   (default 100)
#   DURATION=2m           run duration             (default 2m)
#   PIPE_METRICS_ADDR     pipeline /metrics bind   (default :9091)
#   HIST_METRICS_ADDR     history  /metrics bind   (default :9092)
#   SRCH_METRICS_ADDR     search   /metrics bind   (default :9093)
#   ROOM_METRICS_ADDR     room-rpc /metrics bind   (default :9094)
#
# Manual verification:
#   shellcheck tools/loadgen/scripts/run-mixed.sh
#   ./run-mixed.sh --dry-run

set -euo pipefail

DRY_RUN=0
if [ "${1:-}" = "--dry-run" ]; then
  DRY_RUN=1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

WRITE_RATE="${WRITE_RATE:-500}"
READ_RATE="${READ_RATE:-100}"
DURATION="${DURATION:-2m}"

# Bug 5: distinct metrics ports so all four /metrics endpoints bind
# successfully. Operators must publish these in the loadgen container's
# port mapping (or rely on the in-cluster Prometheus scrape) for
# external visibility.
PIPE_METRICS_ADDR="${PIPE_METRICS_ADDR:-:9091}"
HIST_METRICS_ADDR="${HIST_METRICS_ADDR:-:9092}"
SRCH_METRICS_ADDR="${SRCH_METRICS_ADDR:-:9093}"
ROOM_METRICS_ADDR="${ROOM_METRICS_ADDR:-:9094}"

mkdir -p /tmp/loadgen-mixed
ts=$(date +%s)
ABORT_SENTINEL="/tmp/loadgen-mixed/.abort-${ts}"
rm -f "$ABORT_SENTINEL"

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> DRY RUN: env each subprocess would launch with"
  printf '  pipeline: METRICS_ADDR=%s WRITE_RATE=%s DURATION=%s\n' "$PIPE_METRICS_ADDR" "$WRITE_RATE" "$DURATION"
  printf '  history:  METRICS_ADDR=%s READ_RATE=%s  DURATION=%s\n' "$HIST_METRICS_ADDR" "$READ_RATE" "$DURATION"
  printf '  search:   METRICS_ADDR=%s READ_RATE=%s  DURATION=%s\n' "$SRCH_METRICS_ADDR" "$READ_RATE" "$DURATION"
  printf '  room:     METRICS_ADDR=%s READ_RATE=%s  DURATION=%s\n' "$ROOM_METRICS_ADDR" "$READ_RATE" "$DURATION"
  printf '  abort sentinel: %s\n' "$ABORT_SENTINEL"
  exit 0
fi

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
echo "    messaging-pipeline (realistic)  @ ${WRITE_RATE} rps  metrics=${PIPE_METRICS_ADDR}"
echo "    history-read                    @ ${READ_RATE} rps   metrics=${HIST_METRICS_ADDR}"
echo "    search-read                     @ ${READ_RATE} rps   metrics=${SRCH_METRICS_ADDR}"
echo "    room-rpc                        @ ${READ_RATE} rps   metrics=${ROOM_METRICS_ADDR}"
echo "    Output will interleave — Grafana at http://localhost:3000 is cleaner."
echo

# Helper: run-and-trip — invokes loadgen and writes the abort sentinel
# on non-zero exit so the watcher can fan-out kill the siblings.
run_scenario() {
  local label="$1"
  local logfile="$2"
  shift 2
  if "$@" >"$logfile" 2>&1; then
    return 0
  else
    local rc=$?
    echo "$label exited rc=$rc" >>"$ABORT_SENTINEL"
    return "$rc"
  fi
}

run_scenario pipeline "/tmp/loadgen-mixed/pipeline-${ts}.log" \
  $COMPOSE exec -T -e "METRICS_ADDR=${PIPE_METRICS_ADDR}" loadgen \
  /loadgen run --preset=realistic    --scenario=messaging-pipeline --rate="$WRITE_RATE" --duration="$DURATION" --warmup=10s &
PIPE_PID=$!
run_scenario history "/tmp/loadgen-mixed/history-${ts}.log" \
  $COMPOSE exec -T -e "METRICS_ADDR=${HIST_METRICS_ADDR}" loadgen \
  /loadgen run --preset=history-read --scenario=history-read       --rate="$READ_RATE"  --duration="$DURATION" --warmup=10s --auto-warmup=false &
HIST_PID=$!
run_scenario search "/tmp/loadgen-mixed/search-${ts}.log" \
  $COMPOSE exec -T -e "METRICS_ADDR=${SRCH_METRICS_ADDR}" loadgen \
  /loadgen run --preset=search-read  --scenario=search-read        --rate="$READ_RATE"  --duration="$DURATION" --warmup=5s &
SRCH_PID=$!
run_scenario room "/tmp/loadgen-mixed/room-${ts}.log" \
  $COMPOSE exec -T -e "METRICS_ADDR=${ROOM_METRICS_ADDR}" loadgen \
  /loadgen run --preset=room-rpc     --scenario=room-rpc           --rate="$READ_RATE"  --duration="$DURATION" --warmup=5s &
ROOM_PID=$!

ALL_PIDS=("$PIPE_PID" "$HIST_PID" "$SRCH_PID" "$ROOM_PID")
echo "    pipeline pid=$PIPE_PID history pid=$HIST_PID search pid=$SRCH_PID room pid=$ROOM_PID"
echo "    abort sentinel: $ABORT_SENTINEL"
echo

# Pull the run_id each subprocess logged at startup. Each loadgen Run
# emits a structured "run started" line including run_id; surface them
# now so operators can correlate dashboards/traces.
echo "==> run_id correlation (sampled from per-scenario logs):"
sleep 2
for pair in "pipeline:/tmp/loadgen-mixed/pipeline-${ts}.log" \
            "history:/tmp/loadgen-mixed/history-${ts}.log" \
            "search:/tmp/loadgen-mixed/search-${ts}.log" \
            "room:/tmp/loadgen-mixed/room-${ts}.log"; do
  label="${pair%%:*}"
  path="${pair#*:}"
  # JSON slog line; tolerate it not being there yet (very fast exits).
  rid=$(grep -m1 -oE '"run_id":"[^"]+"' "$path" 2>/dev/null | head -1 || true)
  printf '  %-9s %s  (%s)\n' "$label" "${rid:-<not-yet-logged>}" "$path"
done
echo

# Watcher: poll the sentinel; if any scenario tripped (non-zero exit),
# kill the remaining children promptly. Bounded by the DURATION (the
# children exit on their own then), so the watcher self-terminates
# once all child processes have ended.
(
  while kill -0 "$PIPE_PID" 2>/dev/null \
     || kill -0 "$HIST_PID" 2>/dev/null \
     || kill -0 "$SRCH_PID" 2>/dev/null \
     || kill -0 "$ROOM_PID" 2>/dev/null; do
    if [ -f "$ABORT_SENTINEL" ]; then
      echo "!! scenario tripped — terminating siblings:" >&2
      cat "$ABORT_SENTINEL" >&2
      for pid in "${ALL_PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
      done
      exit 0
    fi
    sleep 1
  done
) &
WATCHER_PID=$!

# `wait` returns the exit code of each child individually; preserve the
# worst-case so the script's own exit reflects a tripped run.
exit_code=0
for pid in "${ALL_PIDS[@]}"; do
  if ! wait "$pid"; then
    rc=$?
    if [ "$rc" -gt "$exit_code" ]; then
      exit_code=$rc
    fi
  fi
done
kill "$WATCHER_PID" 2>/dev/null || true
wait "$WATCHER_PID" 2>/dev/null || true

echo
echo "==> All 4 runs complete. Per-run summaries:"
echo "    /tmp/loadgen-mixed/pipeline-${ts}.log"
echo "    /tmp/loadgen-mixed/history-${ts}.log"
echo "    /tmp/loadgen-mixed/search-${ts}.log"
echo "    /tmp/loadgen-mixed/room-${ts}.log"
if [ -f "$ABORT_SENTINEL" ]; then
  echo
  echo "!! one or more scenarios tripped (see $ABORT_SENTINEL):"
  cat "$ABORT_SENTINEL"
fi
echo
echo "Tail one with:  tail -n 40 /tmp/loadgen-mixed/pipeline-${ts}.log"

exit "$exit_code"
