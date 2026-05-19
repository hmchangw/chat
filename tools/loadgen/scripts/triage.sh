#!/usr/bin/env bash
# triage.sh — gather diagnostic data for a run.
#
# Usage:
#   ./triage.sh RUN_ID
#
# Collects:
#   - Per-service docker logs (--since=30m)
#   - NATS varz + jsz via the monitoring port
#   - The run's metrics.prom file
# Bundles into runs/<run_id>/diagnostics.tgz.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"

usage() {
    cat <<EOF
Usage: $(basename "$0") RUN_ID

Gathers diagnostic data for a specific run and bundles into
runs/<run_id>/diagnostics.tgz.

Environment variables:
  RUNS_DIR       Base directory (default: tools/loadgen/runs).
  NATS_MON_URL   NATS monitoring endpoint (default: http://localhost:8222).
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }
if [ $# -ne 1 ]; then usage >&2; exit 2; fi

RUN_ID="$1"
RUNS_DIR="${RUNS_DIR:-$ROOT_DIR/tools/loadgen/runs}"
NATS_MON_URL="${NATS_MON_URL:-http://localhost:8222}"

BUNDLE_DIR="$RUNS_DIR/$RUN_ID"
[ -d "$BUNDLE_DIR" ] || { echo "run not found: $BUNDLE_DIR" >&2; exit 1; }

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

DEPLOY_DIR="$ROOT_DIR/tools/loadgen/deploy"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

echo "[triage] gathering docker logs..."
for svc in nats mongo cassandra message-gatekeeper message-worker broadcast-worker history-service search-service room-service; do
    $COMPOSE logs --no-color --since=30m "$svc" >"$TMPDIR/$svc.log" 2>/dev/null || true
done

echo "[triage] gathering NATS varz/jsz..."
curl -s "$NATS_MON_URL/varz" >"$TMPDIR/nats-varz.json" || true
curl -s "$NATS_MON_URL/jsz" >"$TMPDIR/nats-jsz.json" || true

echo "[triage] copying metrics.prom..."
cp "$BUNDLE_DIR/metrics.prom" "$TMPDIR/metrics.prom" 2>/dev/null || true

OUT="$BUNDLE_DIR/diagnostics.tgz"
tar -czf "$OUT" -C "$TMPDIR" .
echo "[triage] wrote $OUT"
ls -lh "$OUT"
