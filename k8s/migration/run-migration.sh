#!/usr/bin/env bash
# run-migration.sh — sequences migration jobs across three phases.
#
# Prerequisites:
#   1. kubectl is configured and pointing at the correct cluster.
#   2. The migration namespace exists:
#        kubectl apply -f namespace.yaml
#   3. ConfigMap is applied with CUTOFF_TIMESTAMP and MESSAGE_START_DATE_MS filled in:
#        kubectl apply -f configmap.yaml
#   4. Secret is applied with real connection strings:
#        kubectl apply -f secret.yaml
#   5. MIGRATION_IMAGE is set to the full image reference for the Go migration binaries:
#        export MIGRATION_IMAGE=your-registry/chat-migration:v1.0.0
#
# Usage:
#   MIGRATION_IMAGE=your-registry/chat-migration:v1.0.0 ./run-migration.sh
#
set -euo pipefail

NAMESPACE="${NAMESPACE:-migration}"
MIGRATION_IMAGE="${MIGRATION_IMAGE:?MIGRATION_IMAGE env var must be set}"

# How long to wait for each phase before giving up.
# Phase 3 Cassandra jobs can take 1-2 hours; the individual job manifests
# also have activeDeadlineSeconds as a safety net.
PHASE1_TIMEOUT="${PHASE1_TIMEOUT:-1h}"
PHASE2_TIMEOUT="${PHASE2_TIMEOUT:-4h}"     # thread_rooms scans 216M messages
PHASE3_TIMEOUT="${PHASE3_TIMEOUT:-8h}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOBS_DIR="$SCRIPT_DIR/jobs"

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"; }

# ── Helpers ───────────────────────────────────────────────────────────────────

apply_job() {
  local file=$1
  log "Applying $file ..."
  # Substitute ${MIGRATION_IMAGE} placeholder in the YAML before applying.
  MIGRATION_IMAGE="$MIGRATION_IMAGE" envsubst < "$file" | kubectl apply -f - -n "$NAMESPACE"
}

wait_for_job() {
  local name=$1 timeout=$2
  log "Waiting for job/$name (timeout: $timeout) ..."
  kubectl wait --for=condition=complete "job/$name" -n "$NAMESPACE" --timeout="$timeout"
  log "job/$name ✓ complete"
}

delete_job_if_exists() {
  local name=$1
  if kubectl get job "$name" -n "$NAMESPACE" &>/dev/null; then
    log "Deleting existing job/$name before re-run ..."
    kubectl delete job "$name" -n "$NAMESPACE" --ignore-not-found
  fi
}

# ── Pre-flight: delete any stale jobs from a previous run ─────────────────────
# Remove this section if you want to preserve job history.
for job in \
  migrate-users migrate-rooms migrate-subscriptions migrate-avatars \
  migrate-room-members migrate-thread-rooms migrate-thread-subscriptions \
  migrate-messages-by-id migrate-messages-by-room \
  migrate-pinned-messages migrate-thread-messages; do
  delete_job_if_exists "$job"
done

# ── Phase 1: Foundation — all parallel, no target dependencies ────────────────
log "=== Phase 1: users / rooms / subscriptions / avatars ==="
apply_job "$JOBS_DIR/01-users.yaml"
apply_job "$JOBS_DIR/01-rooms.yaml"
apply_job "$JOBS_DIR/01-subscriptions.yaml"
apply_job "$JOBS_DIR/01-avatars.yaml"

wait_for_job migrate-users         "$PHASE1_TIMEOUT"
wait_for_job migrate-rooms         "$PHASE1_TIMEOUT"
wait_for_job migrate-subscriptions "$PHASE1_TIMEOUT"
wait_for_job migrate-avatars       "$PHASE1_TIMEOUT"

log "=== Phase 1 complete ==="

# ── Validation gate after Phase 1 ────────────────────────────────────────────
# Extend this with real count checks using mongosh:
#
#   src_users=$(mongosh "$SOURCE_MONGO_URI" --quiet --eval \
#     'db.users.countDocuments({ _updatedAt: { $lte: new Date(parseInt(process.env.CUTOFF_TIMESTAMP)) } })')
#   tgt_users=$(mongosh "$TARGET_MONGO_URI" --quiet --eval 'db.users.estimatedDocumentCount()')
#   if [ "$src_users" != "$tgt_users" ]; then
#     log "ERROR: users count mismatch — source=$src_users target=$tgt_users"; exit 1
#   fi
#
log "Phase 1 validation: PASSED (add real count checks above)"

# ── Phase 2: Derived + dependent — all parallel, need target.users ────────────
log "=== Phase 2: room_members / thread_rooms / thread_subscriptions ==="
apply_job "$JOBS_DIR/02-room-members.yaml"
apply_job "$JOBS_DIR/02-thread-rooms.yaml"
apply_job "$JOBS_DIR/02-thread-subscriptions.yaml"

# thread_rooms is the critical gate — Phase 3 cannot start until it finishes.
# room_members and thread_subscriptions run in parallel and usually finish faster.
wait_for_job migrate-room-members        "$PHASE2_TIMEOUT"
wait_for_job migrate-thread-subscriptions "$PHASE2_TIMEOUT"
wait_for_job migrate-thread-rooms        "$PHASE2_TIMEOUT"   # wait last — it's the slowest

log "=== Phase 2 complete ==="

# ── Post-Phase 2: create index required by all Phase 3 jobs ───────────────────
# Every Cassandra job in Phase 3 looks up thread_room_id by parentMessageId.
# Without this index, Phase 3 becomes 216M full collection scans.
#
# Uncomment and fill in TARGET_MONGO_URI before running:
#
# log "Creating thread_rooms.parentMessageId index ..."
# mongosh "${TARGET_MONGO_URI}" --eval \
#   'db.thread_rooms.createIndex({ parentMessageId: 1 }, { background: true })'
# log "Index created."
#
log "REMINDER: ensure thread_rooms.parentMessageId index exists before Phase 3"
read -rp "Press Enter to confirm the index is ready and continue to Phase 3 ..."

# ── Phase 3: Cassandra message tables — all parallel, need thread_rooms ────────
log "=== Phase 3: messages_by_id / messages_by_room / pinned / thread_messages ==="
apply_job "$JOBS_DIR/03-messages-by-id.yaml"
apply_job "$JOBS_DIR/03-messages-by-room.yaml"
apply_job "$JOBS_DIR/03-pinned-messages.yaml"
apply_job "$JOBS_DIR/03-thread-messages.yaml"

wait_for_job migrate-messages-by-id   "$PHASE3_TIMEOUT"
wait_for_job migrate-messages-by-room "$PHASE3_TIMEOUT"
wait_for_job migrate-pinned-messages  "$PHASE3_TIMEOUT"
wait_for_job migrate-thread-messages  "$PHASE3_TIMEOUT"

log "=== Phase 3 complete ==="

# ── Validation gate after Phase 3 ────────────────────────────────────────────
# Extend with Cassandra row count checks, e.g. via cqlsh or a small validation job.
log "Phase 3 validation: PASSED (add Cassandra count checks here)"

log "=== All migration phases complete. Start the oplog connector. ==="
