#!/usr/bin/env bash
# up.sh — bring up the loadgen compose stack and wait for readiness.
#
# Usage:
#   ./up.sh
#
# Idempotent. Safe to re-run; uses `docker compose up -d --build`.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"

echo "==> Bringing up loadgen stack from $DEPLOY_DIR"
cd "$DEPLOY_DIR"
docker compose -f docker-compose.loadtest.yml up -d --build

echo "==> Waiting for NATS (max 60s)..."
for i in $(seq 1 60); do
  if docker compose -f docker-compose.loadtest.yml exec -T nats nats-server --help >/dev/null 2>&1 \
     || nc -z localhost 4222 2>/dev/null; then
    break
  fi
  sleep 1
done

echo "==> Waiting for MongoDB (max 60s)..."
for i in $(seq 1 60); do
  if docker compose -f docker-compose.loadtest.yml exec -T mongodb mongosh --quiet --eval 'db.runCommand({ping:1})' >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

echo "==> Waiting for Cassandra (max 120s)..."
for i in $(seq 1 120); do
  if docker compose -f docker-compose.loadtest.yml exec -T cassandra cqlsh -e 'DESCRIBE KEYSPACES' >/dev/null 2>&1; then
    break
  fi
  sleep 1
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
