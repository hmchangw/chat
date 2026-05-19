#!/usr/bin/env bash
# compare-runs.sh — print a Markdown comparison of two artifact bundles.
#
# Usage:
#   ./compare-runs.sh OLD_RUN_ID NEW_RUN_ID
#
# Both run IDs must exist under RUNS_DIR (default: tools/loadgen/runs).
# Output is Markdown to stdout — pipe to a file or paste into a PR.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
    cat <<EOF
Usage: $(basename "$0") OLD_RUN_ID NEW_RUN_ID

Compares histograms.hlog from two artifact bundles and prints a Markdown
table of per-cell p50/p99 deltas.

Environment variables:
  RUNS_DIR  Base directory (default: tools/loadgen/runs).
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }
if [ $# -ne 2 ]; then
    usage >&2
    exit 2
fi

OLD_RUN_ID="$1"
NEW_RUN_ID="$2"
RUNS_DIR="${RUNS_DIR:-$ROOT_DIR/tools/loadgen/runs}"

OLD_PATH="$RUNS_DIR/$OLD_RUN_ID"
NEW_PATH="$RUNS_DIR/$NEW_RUN_ID"

[ -d "$OLD_PATH" ] || { echo "old run not found: $OLD_PATH" >&2; exit 1; }
[ -d "$NEW_PATH" ] || { echo "new run not found: $NEW_PATH" >&2; exit 1; }

cd "$ROOT_DIR"
go run ./tools/loadgen/cmd/compare-runs "$OLD_PATH" "$NEW_PATH"
