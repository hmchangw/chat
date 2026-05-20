#!/usr/bin/env bash
# down.sh — tear down the loadgen stack and remove its volumes.
#
# Usage:
#   ./down.sh
#
# Delegates to the Makefile's `down` target so the loadgen overlay,
# docker-local microservices, and docker-local deps come down in the
# correct reverse order. Volumes are removed (`down -v` inside the
# Makefile target), so seeded fixtures and Cassandra data do not
# persist between runs.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"

echo "==> Tearing down loadgen stack (delegates to Makefile)"
make -C "$DEPLOY_DIR" down

echo "==> Done."
