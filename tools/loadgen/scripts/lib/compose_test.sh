#!/usr/bin/env bash
set -euo pipefail
. "$(dirname "$0")/compose.sh"
[ -n "$DC" ] || { echo "DC not set"; exit 1; }
[ -n "$DC_KIND" ] || { echo "DC_KIND not set"; exit 1; }
[ "$DC_KIND" = "v1" ] || [ "$DC_KIND" = "v2" ] || { echo "unexpected DC_KIND=$DC_KIND"; exit 1; }
[ "$COMPOSE_PROJECT_NAME" = "loadgen" ] || { echo "expected COMPOSE_PROJECT_NAME=loadgen, got '$COMPOSE_PROJECT_NAME'"; exit 1; }
echo "compose.sh OK with DC=$DC ($DC_KIND)"
