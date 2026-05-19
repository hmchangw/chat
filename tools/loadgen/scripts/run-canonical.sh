#!/usr/bin/env bash
# run-canonical.sh — bypass message-gatekeeper and inject directly into
# MESSAGES_CANONICAL_<siteID> via JetStream PublishAsync (S5 path).
#
# Use this when you want to:
#   - benchmark just the message-worker / broadcast-worker /
#     notification-worker downstream of the gatekeeper (gatekeeper
#     is the bottleneck otherwise)
#   - exercise the S5 async-publish path in isolation
#   - validate that loadgen_publish_errors_total{reason="async_ack"}
#     stays at 0 under load
#
# Usage:
#   ./up.sh
#   ./run-canonical.sh
#
# Knobs (env vars):
#   RATE=1000     target rps                  (default 1000)
#   DURATION=30s  measured duration           (default 30s)
#   WARMUP=5s     warmup discarded            (default 5s)
#   PRESET=small  fixture preset              (default small)
#   ASYNC_MAX=4096  --js-async-max-pending    (default 4096)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

RATE="${RATE:-1000}"
DURATION="${DURATION:-30s}"
WARMUP="${WARMUP:-5s}"
PRESET="${PRESET:-small}"
ASYNC_MAX="${ASYNC_MAX:-4096}"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running messaging-pipeline canonical-inject @ ${RATE} rps for $DURATION"
echo "    --js-async-max-pending=$ASYNC_MAX  (S5 path; backpressure at the JS ring)"
echo "    No gatekeeper round-trip — direct publish to MESSAGES_CANONICAL_site-local"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --inject=canonical \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP" \
  --skip-readiness \
  --js-async-max-pending="$ASYNC_MAX"
