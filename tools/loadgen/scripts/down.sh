#!/usr/bin/env bash
# down.sh — tear down the loadgen stack and remove its volumes.
#
# Usage:
#   ./down.sh
#
# Equivalent to `docker compose down -v` plus the dashboards profile.
# Volumes are removed, so seeded fixtures and Cassandra data do not
# persist between runs.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"

cd "$DEPLOY_DIR"

echo "==> Tearing down loadgen stack"
docker compose -f docker-compose.loadtest.yml --profile dashboards down -v

echo "==> Done."
