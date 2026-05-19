#!/usr/bin/env bash
# run-chaos.sh — drive a chaos run.
#
# Brings up the toxiproxy overlay, starts a baseline messaging-pipeline run,
# applies latency + bandwidth toxics mid-run, and lets the operator observe
# how the SUT degrades. Use the loadgen verdict + Grafana dashboards to see
# the effect.
#
# Phase 3 §3.17
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"

usage() {
    cat <<EOF
Usage: $(basename "$0") [--help]

Environment variables (with defaults):
  DURATION=5m            Total run duration.
  PRESET=realistic       Loadgen preset.
  RATE=200               Target rps.
  TOXIPROXY_URL=http://localhost:8474
                         Toxiproxy admin endpoint.

The script:
  1. Brings up the chaos overlay (toxiproxy + toxiproxy-init).
  2. Starts loadgen run in the background.
  3. Waits 60s, then installs a latency toxic (200ms + 50ms jitter on nats_inbound).
  4. Waits 60s, adds a bandwidth limit (1 KB/s on nats_inbound).
  5. Waits 60s, removes the bandwidth limit.
  6. Lets the run complete and reports the verdict.

See tools/loadgen/docs/scenarios/chaos.md for the full toxic matrix.
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }

DURATION="${DURATION:-5m}"
PRESET="${PRESET:-realistic}"
RATE="${RATE:-200}"
TOXIPROXY_URL="${TOXIPROXY_URL:-http://localhost:8474}"

cd "$DEPLOY_DIR"
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') bringing up chaos overlay..."
$DC -f docker-compose.loadtest.yml -f docker-compose.chaos.yml --profile chaos up -d toxiproxy toxiproxy-init

# Wait for toxiproxy-init to finish creating the proxies.
sleep 8

cd "$SCRIPT_DIR"
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') starting loadgen baseline..."
go build -o /tmp/loadgen-chaos ../
LOADGEN=/tmp/loadgen-chaos
trap 'rm -f /tmp/loadgen-chaos; cd "$DEPLOY_DIR" && $DC --profile chaos stop toxiproxy toxiproxy-init' EXIT

"$LOADGEN" run \
    --scenario=messaging-pipeline \
    --preset="$PRESET" \
    --rate="$RATE" \
    --duration="$DURATION" \
    --warmup=30s &
LOADGEN_PID=$!

sleep 60
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') installing latency toxic (200ms +/- 50ms)..."
"$LOADGEN" chaos add nats_inbound latency-fault latency latency=200 jitter=50 || true

sleep 60
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') installing bandwidth limit (1 KB/s)..."
"$LOADGEN" chaos add nats_inbound bandwidth-fault bandwidth rate=1 || true

sleep 60
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') removing bandwidth fault..."
"$LOADGEN" chaos remove nats_inbound bandwidth-fault || true

wait $LOADGEN_PID
echo "[chaos] $(date -u +'%Y-%m-%dT%H:%M:%SZ') run complete. Check RUN QUALITY verdict and Grafana for degradation behavior."
