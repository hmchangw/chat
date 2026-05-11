#!/usr/bin/env bash
# run-dashboards.sh — bring up Grafana + Prometheus then start a long run.
#
# Same as run-realistic.sh but additionally enables the `dashboards`
# compose profile (Grafana + Prometheus). Useful when you want to watch
# the metrics live rather than just read the final summary.
#
# Usage:
#   ./up.sh
#   ./run-dashboards.sh
#
# Once running, open:
#   http://localhost:3000   Grafana (admin/admin)  — preloaded dashboard
#   http://localhost:9090   Prometheus
#   http://localhost:9099/metrics   raw scrape (only alive during the run)
#
# Knobs (env vars):
#   RATE=500      target rps                  (default 500)
#   DURATION=5m   long run for nicer graphs   (default 5m)
#   WARMUP=15s    warmup discarded            (default 15s)
#   PRESET=realistic                          (default realistic)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

RATE="${RATE:-500}"
DURATION="${DURATION:-5m}"
WARMUP="${WARMUP:-15s}"
PRESET="${PRESET:-realistic}"

cd "$DEPLOY_DIR"
if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Bringing up dashboards profile (Grafana + Prometheus)"
$COMPOSE --profile dashboards up -d

echo
echo "==> Open in another tab while this runs:"
echo "    http://localhost:3000   (admin/admin)"
echo

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

echo
echo "==> Running messaging-pipeline @ ${RATE} rps for $DURATION  (warm-up: $WARMUP)"
$COMPOSE exec -T loadgen /loadgen run \
  --preset="$PRESET" \
  --scenario=messaging-pipeline \
  --rate="$RATE" \
  --duration="$DURATION" \
  --warmup="$WARMUP"

echo
echo "==> Run complete. Grafana + Prometheus remain up. Tear down:"
echo "    ./down.sh   (also stops the dashboards)"
