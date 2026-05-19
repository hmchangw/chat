#!/usr/bin/env bash
# bisect.sh — git-checkout two SHAs and compare loadgen runs at each.
#
# Usage:
#   ./bisect.sh OLD_SHA NEW_SHA SCENARIO
#
# Workflow:
#   1. git checkout OLD_SHA; build loadgen; run for 2m; save run_id.
#   2. git checkout NEW_SHA; build loadgen; run for 2m; save run_id.
#   3. git checkout original branch.
#   4. compare-runs.sh OLD_RUN_ID NEW_RUN_ID
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

usage() {
    cat <<EOF
Usage: $(basename "$0") OLD_SHA NEW_SHA SCENARIO

Runs loadgen at two git SHAs (2m each), then prints a comparison.

Environment variables:
  PRESET           Loadgen preset (default: medium).
  RATE             Target rps (default: 200).
  RUN_DURATION     Per-SHA run duration (default: 2m).

Caveats:
  - Uses 'git checkout', which fails if the working tree is dirty.
  - Restores the original branch on exit (best-effort; on hard failure, you
    may need to 'git checkout <branch>' manually).
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }
if [ $# -ne 3 ]; then usage >&2; exit 2; fi

OLD_SHA="$1"; NEW_SHA="$2"; SCENARIO="$3"
PRESET="${PRESET:-medium}"
RATE="${RATE:-200}"
RUN_DURATION="${RUN_DURATION:-2m}"

ORIG_BRANCH="$(cd "$ROOT_DIR" && git branch --show-current 2>/dev/null || git rev-parse HEAD)"
trap 'cd "$ROOT_DIR" && git checkout "$ORIG_BRANCH" 2>/dev/null || true' EXIT

cd "$ROOT_DIR"
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "error: working tree is dirty; commit or stash before running bisect.sh" >&2
    exit 2
fi

run_at_sha() {
    local sha="$1"
    cd "$ROOT_DIR"
    echo "[bisect] checkout $sha"
    git checkout "$sha" >/dev/null
    echo "[bisect] building loadgen..."
    go build -o /tmp/loadgen-bisect ./tools/loadgen

    echo "[bisect] running for $RUN_DURATION at $RATE rps..."
    /tmp/loadgen-bisect run \
        --scenario="$SCENARIO" \
        --preset="$PRESET" \
        --rate="$RATE" \
        --duration="$RUN_DURATION" \
        --warmup=15s 2>&1 | tee "/tmp/bisect-$sha.log" >&2

    # Capture run_id from log.
    grep -oE 'run_id[=:][ ]*[a-zA-Z0-9-]+' "/tmp/bisect-$sha.log" | head -1 | tr -d ' ' | awk -F'[=:]' '{print $NF}'
}

OLD_RUN_ID="$(run_at_sha "$OLD_SHA")"
NEW_RUN_ID="$(run_at_sha "$NEW_SHA")"

cd "$ROOT_DIR" && git checkout "$ORIG_BRANCH"

echo
echo "[bisect] OLD: $OLD_SHA → run_id=$OLD_RUN_ID"
echo "[bisect] NEW: $NEW_SHA → run_id=$NEW_RUN_ID"
echo
"$SCRIPT_DIR/compare-runs.sh" "$OLD_RUN_ID" "$NEW_RUN_ID"
