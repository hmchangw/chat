#!/usr/bin/env bash
# run-csv.sh — produce a CSV of per-sample latencies for offline analysis.
#
# Useful when you need to import the run's latency distribution into
# a notebook (pandas/matplotlib) or compare two runs by tail shape
# rather than just p50/p95/p99.
#
# CSV columns: run_id, row_index, request_id, metric, latency_ns.
# The run_id correlates the rows back to SUT-side logs/metrics that
# carry the X-Loadgen-Run-ID header.
#
# Usage:
#   ./up.sh
#   ./run-csv.sh
#
# Knobs (env vars):
#   RATE=200      rps                         (default 200)
#   DURATION=30s  measured duration           (default 30s)
#   PRESET=small  fixture preset              (default small)
#   CSV_OUT       host-side output path       (default /tmp/loadgen-<ts>.csv)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

RATE="${RATE:-200}"
DURATION="${DURATION:-30s}"
PRESET="${PRESET:-small}"
TS="$(date +%s)"
CSV_OUT="${CSV_OUT:-/tmp/loadgen-${TS}.csv}"
CSV_IN_CONTAINER="/tmp/loadgen-${TS}.csv"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running messaging-pipeline @ ${RATE} rps for $DURATION → CSV inside container"
echo "    Container path: $CSV_IN_CONTAINER"
echo "    Host path: $CSV_OUT"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup=5s \
  --csv="$CSV_IN_CONTAINER"

echo
echo "==> Copying CSV out of the container"
docker cp loadgen-loadgen-1:"$CSV_IN_CONTAINER" "$CSV_OUT"
echo "    rows: $(wc -l < "$CSV_OUT")"
head -3 "$CSV_OUT"
echo "    ..."
tail -3 "$CSV_OUT"
echo
echo "Next: load it in your notebook:"
echo "  import pandas as pd; df = pd.read_csv('$CSV_OUT')"
