#!/usr/bin/env bash
#
# demo-logs.sh — Follow the app service logs live (run in its own terminal).
#
# Multiplexes the running services' logs with per-service prefixes, like
# `docker compose logs -f`. Pin/fav requests show up as they happen, so keep
# this open in one terminal while you drive demo-pin-fav.sh in another.
#
# Usage:
#   ./docker-local/demo-logs.sh                 # the pin/fav-relevant services
#   ./docker-local/demo-logs.sh all             # every service
#   ./docker-local/demo-logs.sh room-service …  # only the named services
#
# Env overrides: PROJECT (default chat-local-services),
#                COMPOSE_FILE (default <script-dir>/compose.services.yaml),
#                TAIL (default 20).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT="${PROJECT:-chat-local-services}"
COMPOSE_FILE="${COMPOSE_FILE:-$HERE/compose.services.yaml}"
TAIL="${TAIL:-20}"

case "${1:-}" in
  "")  set -- room-service history-service message-gatekeeper message-worker broadcast-worker ;;
  all) shift ;; # no service filter -> follow them all
esac

exec docker compose -p "$PROJECT" -f "$COMPOSE_FILE" logs -f --tail="$TAIL" "$@"
