#!/bin/sh
# tools/loadgen/scripts/lib/compose.sh
#
# Detects docker compose v2 (plugin) or docker-compose v1 (standalone),
# exports $DC for the chosen command and $DC_KIND ("v1" or "v2") for callers
# that need to branch. Also exports COMPOSE_PROJECT_NAME=loadgen so the
# project naming is consistent regardless of which directory you launch from.
#
# Print-cmd mode: if invoked as `lib/compose.sh print-cmd`, prints $DC and exits.
# Used by the Makefile to embed the right command in target invocations.
#
# Phase 1b/X.3 (loadgen v2 plan, Docker Compose v1+v2 compatibility).

# Try v2 plugin first.
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    v=$(docker compose version --short 2>/dev/null || echo 0)
    case "$v" in
        2.*|[3-9].*)
            DC="docker compose"
            DC_KIND="v2"
            ;;
    esac
fi

# Fall back to v1 standalone if v2 wasn't usable.
if [ -z "${DC:-}" ] && command -v docker-compose >/dev/null 2>&1; then
    v=$(docker-compose version --short 2>/dev/null || echo 0)
    case "$v" in
        1.29.*|1.[3-9]*|[2-9].*)
            DC="docker-compose"
            DC_KIND="v1"
            ;;
    esac
fi

if [ -z "${DC:-}" ]; then
    echo "ERROR: need docker compose (v2.0+) or docker-compose (v1.29.2+)" >&2
    exit 2
fi

export DC
export DC_KIND
export COMPOSE_PROJECT_NAME=loadgen

# Convenience function: callers can run 'dc up -d' instead of '$DC up -d'.
dc() { $DC "$@"; }

# print-cmd mode for Makefile consumers.
if [ "${1:-}" = "print-cmd" ]; then
    printf '%s\n' "$DC"
fi
