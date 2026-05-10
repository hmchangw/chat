#!/usr/bin/env bash
# up.sh — bring up the loadgen compose stack and wait for readiness.
#
# Usage:
#   ./up.sh
#
# Idempotent. Safe to re-run; uses `docker compose up -d --build`.
# Waits for both infra (NATS / MongoDB / Cassandra / Elasticsearch /
# Valkey) AND for the request/reply services-under-test (room-service,
# history-service, search-service) so subsequent ./quickstart.sh or
# ./run-realistic.sh don't see first-second errors from services that
# are still warming up.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
COMPOSE="docker compose -f docker-compose.loadtest.yml"

echo "==> Bringing up loadgen stack from $DEPLOY_DIR"
cd "$DEPLOY_DIR"
$COMPOSE up -d --build

# Helper: poll until a docker container reports "running" + healthy.
# Honors compose-defined healthchecks; falls back to "running" if no
# healthcheck is configured.
wait_for_container() {
  local name="$1"
  local max="$2"
  local i
  for ((i = 0; i < max; i++)); do
    state=$(docker inspect -f '{{.State.Status}}' "$name" 2>/dev/null || echo missing)
    if [ "$state" = "running" ]; then
      health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$name" 2>/dev/null || echo none)
      if [ "$health" = "healthy" ] || [ "$health" = "none" ]; then
        return 0
      fi
    fi
    sleep 1
  done
  echo "WARN: $name not ready after ${max}s (state=$state, health=${health:-?})" >&2
  return 1
}

echo "==> Waiting for NATS (max 60s)..."
wait_for_container loadgen-nats-1 60 || true
echo "==> Waiting for MongoDB (max 60s)..."
wait_for_container loadgen-mongodb-1 60 || true
echo "==> Waiting for Cassandra (max 120s)..."
wait_for_container loadgen-cassandra-1 120 || true
echo "==> Waiting for Elasticsearch (max 90s)..."
wait_for_container loadgen-elasticsearch-1 90 || true

# F7: gate on the request/reply services being NATS-subscribed by
# delegating to the loadgen binary's own readiness probe. Each scenario
# probes the service it'll talk to. --skip-readiness=false is the
# default; --readiness-timeout caps wait.
echo "==> Waiting for services to accept RPCs (max 30s each)..."
$COMPOSE exec -T loadgen /loadgen seed --preset=small >/dev/null
for scenario in history-read search-read room-rpc; do
  if $COMPOSE exec -T loadgen /loadgen run \
      --preset=small \
      --scenario="$scenario" \
      --rate=1 \
      --duration=1ms \
      --warmup=0s \
      --auto-warmup=false \
      --readiness-timeout=30s \
      --progress-interval=0 \
      >/dev/null 2>&1; then
    echo "    $scenario service: ready"
  else
    echo "    $scenario service: NOT READY (continuing anyway)" >&2
  fi
done

echo
echo "==> Stack is up."
echo "    Grafana:     http://localhost:3000  (admin/admin)"
echo "    Prometheus:  http://localhost:9090"
echo "    NATS HTTP:   http://localhost:8222"
echo "    Loadgen /metrics: http://localhost:9099/metrics"
echo
echo "Next: ./quickstart.sh   (30s smoke run)"
echo "  or: ./run-realistic.sh (2min realistic run)"
echo "  or: ./down.sh           (tear it all down)"
