#!/usr/bin/env bash
# run-soak.sh — sustained long-duration soak test.
#
# Exercises the harness's bounded-RSS claim with an extended run and
# produces a complete artifact bundle in the RUNS_DIR of the loadgen
# container. Default duration is 8h; override with DURATION.
#
# Phase 1b §1.14
#
# Usage:
#   ./up.sh
#   ./run-soak.sh             # 8h soak at 500 rps, realistic preset
#   DURATION=30m RATE=200 ./run-soak.sh
#
# See --help for the full env-var reference.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

usage() {
    cat <<EOF
Usage: $(basename "$0") [--help]

Runs a sustained soak load test against the loadgen compose stack.
Requires the stack to be up (./up.sh).

Environment variables (with defaults):
  DURATION=8h             Total run duration.
  PRESET=realistic        Loadgen preset name.
  RATE=500                Target requests/second.
  PROGRESS_INTERVAL=60s   Live-progress log interval (0 disables).
  WARMUP=60s              Warm-up window (samples discarded from stats).
  RUNS_DIR=               Artifact bundle root inside the container.
                          When empty, artifact writing is disabled.
                          Set to /runs to enable (container path).

Examples:
  ./tools/loadgen/scripts/run-soak.sh                       # 8h default
  DURATION=30m RATE=200 ./tools/loadgen/scripts/run-soak.sh
  DURATION=2h RUNS_DIR=/runs ./tools/loadgen/scripts/run-soak.sh
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }

DURATION="${DURATION:-8h}"
PRESET="${PRESET:-realistic}"
RATE="${RATE:-500}"
PROGRESS_INTERVAL="${PROGRESS_INTERVAL:-60s}"
WARMUP="${WARMUP:-60s}"
RUNS_DIR="${RUNS_DIR:-}"

cd "$DEPLOY_DIR"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Starting soak: preset=$PRESET rate=${RATE} rps duration=$DURATION warmup=$WARMUP"
echo "    Live progress every $PROGRESS_INTERVAL"
echo "    Grafana: http://localhost:3000  (admin/admin)"
echo "    $(date -u +'%Y-%m-%dT%H:%M:%SZ')"
echo

# Build optional RUNS_DIR env injection for the container exec.
RUNS_DIR_ARG=""
if [ -n "$RUNS_DIR" ]; then
  RUNS_DIR_ARG="-e RUNS_DIR=${RUNS_DIR}"
fi

# Run inside a temp log file so we can extract the run_id after completion.
LOGFILE="$(mktemp /tmp/loadgen-soak.XXXXXX.log)"
trap 'rm -f "$LOGFILE"' EXIT

set +e
# shellcheck disable=SC2086
$COMPOSE exec -T $RUNS_DIR_ARG loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP" \
  --progress-interval="$PROGRESS_INTERVAL" \
  2>&1 | tee "$LOGFILE"
SOAK_EXIT=${PIPESTATUS[0]}
set -e

echo
echo "==> Soak complete. $(date -u +'%Y-%m-%dT%H:%M:%SZ')  exit=$SOAK_EXIT"

# Extract run_id: emitted as JSON slog {"msg":"run started","run_id":"<uuid>",...}
# and also printed as plain text "run_id: <uuid>" in the summary block.
RUN_ID=""
RUN_ID="$(grep -oE '"run_id":"[^"]+"' "$LOGFILE" | head -1 | grep -oE '[0-9a-f-]{32,}' || true)"
if [ -z "$RUN_ID" ]; then
  # Fallback: plain-text summary line
  RUN_ID="$(grep -oE 'run_id: [a-zA-Z0-9-]+' "$LOGFILE" | head -1 | awk '{print $2}' || true)"
fi

if [ -n "$RUN_ID" ]; then
  echo "    run_id=$RUN_ID"
else
  echo "    (run_id not captured — check log above)"
fi

if [ -n "$RUNS_DIR" ] && [ -n "$RUN_ID" ]; then
  echo
  echo "==> Artifact bundle (inside container at ${RUNS_DIR}/${RUN_ID}/):"
  $COMPOSE exec -T loadgen ls -1 "${RUNS_DIR}/${RUN_ID}/" 2>/dev/null || \
    echo "    (bundle listing unavailable — run may have been interrupted)"
fi

exit "$SOAK_EXIT"
