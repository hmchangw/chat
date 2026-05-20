#!/usr/bin/env bash
# run-campaign.sh — four-phase campaign: baseline → ramp → sustained → soak.
#
# Fails fast on any UNTRUSTED verdict. Prints a rolled-up verdict at the end.
#
# Phase 1b §1.14
#
# Usage:
#   ./up.sh
#   ./run-campaign.sh
#
# See --help for the full env-var reference.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(cd "$SCRIPT_DIR/../deploy" && pwd)"
. "$SCRIPT_DIR/lib/compose.sh"
COMPOSE="dc -f $DEPLOY_DIR/docker-compose.yml"

usage() {
    cat <<EOF
Usage: $(basename "$0") [--help]

Runs a four-phase campaign against the loadgen compose stack.
Requires the stack to be up (./up.sh).

Phases (each runs as a separate loadgen invocation):
  1. baseline   30s at BASELINE_RATE (smoke test)
  2. ramp        5m linear ramp from BASELINE_RATE to PEAK_RATE
  3. sustained  60m at PEAK_RATE
  4. soak       SOAK_DURATION at half of PEAK_RATE

Environment variables (with defaults):
  PRESET=realistic       Loadgen preset name.
  BASELINE_RATE=50       Starting rate for baseline + ramp phases (rps).
  PEAK_RATE=500          Target rate for sustained phase (rps).
  SOAK_DURATION=2h       Soak phase duration.
  PROGRESS_INTERVAL=60s  Live-progress log interval (0 disables).

Behavior:
  - If ANY phase prints an UNTRUSTED verdict, the campaign aborts with exit 4.
  - If the stack is not running, the script exits 1 immediately.
  - The final summary lists per-phase verdicts and a rolled-up campaign verdict
    (worst-of: UNTRUSTED > DEGRADED > TRUSTED).

Examples:
  ./tools/loadgen/scripts/run-campaign.sh
  SOAK_DURATION=30m PEAK_RATE=200 ./tools/loadgen/scripts/run-campaign.sh
EOF
}

[ "${1:-}" = "--help" ] && { usage; exit 0; }

PRESET="${PRESET:-realistic}"
BASELINE_RATE="${BASELINE_RATE:-50}"
PEAK_RATE="${PEAK_RATE:-500}"
SOAK_DURATION="${SOAK_DURATION:-2h}"
PROGRESS_INTERVAL="${PROGRESS_INTERVAL:-60s}"

cd "$DEPLOY_DIR"

if [ "$(docker inspect -f '{{.State.Status}}' loadgen-loadgen-1 2>/dev/null || echo missing)" != "running" ]; then
  echo "ERROR: loadgen container is not running. Run ./up.sh first." >&2
  exit 1
fi

echo "==> Seeding fixtures (preset=$PRESET)"
$COMPOSE exec -T loadgen /loadgen seed --preset="$PRESET"

CAMPAIGN_START="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
echo
echo "==> Campaign start: $CAMPAIGN_START"
echo "    preset=$PRESET  baseline_rate=${BASELINE_RATE}  peak_rate=${PEAK_RATE}  soak=${SOAK_DURATION}"

# Per-phase verdict accumulator (populated by run_phase).
VERDICTS=()

# Half of PEAK_RATE for the soak phase (integer division).
SOAK_RATE="$((PEAK_RATE / 2))"

# run_phase NAME [loadgen-run-args...]
# Runs a single phase, captures its verdict, and fails fast on UNTRUSTED.
run_phase() {
    local name="$1"; shift
    local logfile
    logfile="$(mktemp /tmp/loadgen-campaign-${name}.XXXXXX.log)"
    # shellcheck disable=SC2064
    trap "rm -f '$logfile'" EXIT

    echo
    echo "==== [campaign] PHASE: $name $(date -u +'%Y-%m-%dT%H:%M:%SZ') ===="

    local exit_code=0
    set +e
    $COMPOSE exec -T loadgen /loadgen run "$@" 2>&1 | tee "$logfile"
    exit_code="${PIPESTATUS[0]}"
    set -e

    local verdict
    verdict="$(grep -oE 'RUN QUALITY: (TRUSTED|DEGRADED|UNTRUSTED)' "$logfile" | tail -1 | awk '{print $NF}')"
    verdict="${verdict:-UNKNOWN}"

    VERDICTS+=("$name=$verdict")
    echo "[campaign] $name verdict=$verdict (exit=$exit_code)"

    if [ "$verdict" = "UNTRUSTED" ]; then
        echo "[campaign] aborting: UNTRUSTED verdict in phase $name" >&2
        exit 4
    fi
}

# Phase 1: baseline (30s smoke test at low rate).
run_phase baseline \
    --scenario=messaging-pipeline \
    --preset="$PRESET" \
    --rate="$BASELINE_RATE" \
    --duration=30s \
    --warmup=10s \
    --progress-interval="$PROGRESS_INTERVAL"

# Phase 2: ramp (5m linear ramp from BASELINE_RATE to PEAK_RATE).
# --rate=0 is required when using --ramp-from/--ramp-to (they are mutually
# exclusive with the single-rate knob).
run_phase ramp \
    --scenario=messaging-pipeline \
    --preset="$PRESET" \
    --rate=0 \
    --ramp-from="$BASELINE_RATE" \
    --ramp-to="$PEAK_RATE" \
    --ramp-duration=5m \
    --ramp-shape=linear \
    --duration=5m \
    --warmup=15s \
    --progress-interval="$PROGRESS_INTERVAL"

# Phase 3: sustained (60m at PEAK_RATE).
run_phase sustained \
    --scenario=messaging-pipeline \
    --preset="$PRESET" \
    --rate="$PEAK_RATE" \
    --duration=60m \
    --warmup=60s \
    --progress-interval="$PROGRESS_INTERVAL"

# Phase 4: soak (SOAK_DURATION at half of PEAK_RATE).
run_phase soak \
    --scenario=messaging-pipeline \
    --preset="$PRESET" \
    --rate="$SOAK_RATE" \
    --duration="$SOAK_DURATION" \
    --warmup=60s \
    --progress-interval="$PROGRESS_INTERVAL"

# Rolled-up worst-of verdict (UNTRUSTED > DEGRADED > TRUSTED > UNKNOWN).
echo
echo "==== [campaign] SUMMARY $(date -u +'%Y-%m-%dT%H:%M:%SZ') ===="
echo "    Started:  $CAMPAIGN_START"
WORST="TRUSTED"
for v in "${VERDICTS[@]}"; do
    echo "    $v"
    case "$v" in
        *=UNTRUSTED) WORST="UNTRUSTED" ;;
        *=DEGRADED)  [ "$WORST" != "UNTRUSTED" ] && WORST="DEGRADED" ;;
        *=UNKNOWN)   [ "$WORST" = "TRUSTED" ] && WORST="UNKNOWN" ;;
    esac
done
echo "    ----"
echo "    CAMPAIGN VERDICT: $WORST"

case "$WORST" in
    UNTRUSTED) exit 4 ;;
    *)         exit 0 ;;
esac
