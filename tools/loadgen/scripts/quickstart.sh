#!/usr/bin/env bash
# quickstart.sh — quick 30-second smoke run on the smallest preset.
#
# Designed for a fresh clone: prove the stack works end-to-end in under
# a minute, leave the stack up so the user can poke around Grafana.
#
# Usage:
#   ./up.sh           # one-time stack bring-up
#   ./quickstart.sh   # this script
#
# Run sequence: seed -> run messaging-pipeline -> print summary.
# Expected outcome: ~3000 messages published with 0 errors and p99 < 100ms
# on a laptop. See README.md for what to do next.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.loadtest.yml"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=small: 10 users, 5 rooms)"
$COMPOSE exec -T loadgen /loadgen seed --preset=small

echo
echo "==> Running messaging-pipeline @ 100 rps for 30s (10s warm-up discarded)"
echo "    (a one-line summary will be printed when this completes)"
echo
$COMPOSE exec -T loadgen /loadgen run \
  --preset=small \
  --scenario=messaging-pipeline \
  --rate=100 \
  --duration=30s \
  --warmup=10s

echo
echo "==> Smoke run complete. Stack is still up."
echo "    Grafana: http://localhost:3000 (admin/admin)"
echo
echo "Try next:"
echo "  ./run-realistic.sh   2-minute run on the realistic preset"
echo "  ./down.sh            tear everything down"
