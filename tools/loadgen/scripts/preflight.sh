#!/usr/bin/env bash
# preflight.sh — host readiness check before running loadgen.
# Delegates to 'loadgen doctor'.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
    cat <<EOF
Usage: $(basename "$0") [--help]

Runs 'loadgen doctor' to check host readiness. Exit 0 if all checks pass.
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }

cd "$ROOT_DIR"
go run ./tools/loadgen doctor
