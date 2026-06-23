# Data Migration Deployment Plan
# Rocket.Chat → tchat Nextgen

**Date:** 2026-06-23
**Author:** Engineering
**Status:** Draft — Pending Manager Review

---

## 1. Executive Summary

This plan covers the one-time bulk migration of all historical data from the existing
Rocket.Chat MongoDB instance into the tchat Nextgen platform (MongoDB + Cassandra),
followed by a real-time oplog connector that keeps the target in sync until cut-over.

The migration is split into two sequential phases:

1. **Bulk Migration** — copies all historical data in dependency order using independent
   Kubernetes Jobs, one per collection. Each job is checkpointed so it can be restarted
   from its last position on failure without re-doing completed work.

2. **Oplog Connector** — starts from a resume token captured before bulk migration begins,
   replaying every write that happened on the source during migration. Cut-over happens
   once connector lag reaches near-zero.

Estimated total wall-clock time (PROD): **6–10 hours end-to-end** including connector
catch-up, assuming no major failures.

---

## 2. Source & Target Systems

| | Source | Target |
|---|---|---|
| Platform | Rocket.Chat | tchat Nextgen |
| Databases | MongoDB (primary) | MongoDB + Cassandra |
| Environment | PROD MongoDB instance | Nextgen PROD cluster |

### Collection Mapping

| Source Collection | Target Collection / Table | Store |
|---|---|---|
| `users` | `users` | MongoDB |
| `rocketchat_room` | `rooms` | MongoDB |
| `rocketchat_avatars` | `rocketchat_avatars` | MongoDB |
| `rocketchat_subscription` | `subscriptions` | MongoDB |
| `tsmc_room_members` | `room_members` | MongoDB |
| `rocketchat_uploads` | `rocketchat_uploads` | MongoDB |
| `rocketchat_message` | `messages_by_id` | Cassandra |
| `rocketchat_message` | `messages_by_room` | Cassandra |
| `rocketchat_message` (pinned) | `pinned_messages_by_room` | Cassandra |
| `rocketchat_message` (thread roots) | `thread_rooms` *(derived)* | MongoDB |
| `rocketchat_message` (thread replies) | `thread_messages_by_thread` | Cassandra |
| `tsmc_thread_subscriptions` | `thread_subscriptions` | MongoDB |

> **Note:** `thread_rooms` has no direct source collection. It is derived by scanning
> `rocketchat_message` for documents where `tcount > 0` (thread root messages) and
> constructing a `thread_rooms` document for each.

---

## 3. Data Volumes

### TEST Environment (verified)

| Collection | Document Count |
|---|---|
| `rocketchat_message` | 6,901,901 |
| `users` | 106,884 |
| `rocketchat_subscription` | 51,214 |
| `tsmc_thread_subscriptions` | 536,057 |
| `tsmc_room_members` | 10,023 |
| `rocketchat_room` | 6,913 |
| `rocketchat_uploads` | 456 |
| `rocketchat_avatars` | 9 |

### PROD Environment (estimated from known message count)

Scale factor from TEST → PROD (messages): 216M / 6.9M ≈ **31×**

| Collection | Estimated PROD Count |
|---|---|
| `rocketchat_message` | **216,000,000** (confirmed) |
| `users` | ~3,300,000 |
| `rocketchat_subscription` | ~1,600,000 |
| `tsmc_thread_subscriptions` | ~16,600,000 |
| `tsmc_room_members` | ~310,000 |
| `rocketchat_room` | ~214,000 |
| `rocketchat_uploads` | ~14,000 |
| `rocketchat_avatars` | negligible |

> PROD counts for non-message collections are estimates. Exact counts should be
> captured in Stage 0 (Pre-Migration Baseline) before the migration begins.

---

## 4. Migration Architecture

### 4.1 Approach — One Kubernetes Job Per Collection

Each collection or Cassandra table is migrated by a dedicated Kubernetes Job.
Jobs are independent, checkpointed, and idempotent:

- **Checkpointing:** Each job records its last successfully written document ID
  in a `migration_checkpoints` collection. On restart, it resumes from that ID.
- **Idempotency:** All writes use upsert on the target primary key — restarting
  a job never creates duplicate documents.
- **Independence:** A failed job for one collection does not affect others. Only
  that job needs to be restarted.

### 4.2 Sequencing

Jobs are sequenced by a shell script (`run-migration.sh`) that:

1. Submits the current phase's jobs to Kubernetes
2. Waits for all jobs in the phase to reach `Complete`
3. Runs validation checks (document count comparison)
4. Proceeds to the next phase only if all checks pass

Phases where collections have no dependency on each other run their jobs in parallel
within that phase.

### 4.3 Repository Structure

```
k8s/migration/
  jobs/
    01-users.yaml
    02-rooms.yaml
    02-avatars.yaml
    03-subscriptions.yaml
    03-room-members.yaml
    03-uploads.yaml
    03-cassandra-messages-by-id.yaml
    03-cassandra-messages-by-room.yaml
    03-cassandra-pinned-messages.yaml
    04-thread-rooms.yaml
    04-cassandra-thread-messages.yaml
    05-thread-subscriptions.yaml
    06-backfill-rooms-lastmsg.yaml
    06-backfill-thread-rooms-lastmsg.yaml
  run-migration.sh
  configmap.yaml       # source/target connection strings
```

Each Job manifest references a shared ConfigMap for source MongoDB URI, target
MongoDB URI, and target Cassandra endpoints. Credentials are injected via Kubernetes
Secrets.

---

## 5. Migration Phases

### Pre-Migration (Before Any Data Moves)

| Step | Action |
|---|---|
| P0.1 | **Capture oplog resume token** from source MongoDB. This is the connector's starting position. |
| P0.2 | Record exact document counts for all source collections (baseline). |
| P0.3 | Verify read access to source and write access to target from the migration namespace. |
| P0.4 | Run each job in **dry-run mode** (reads source, validates schema, writes nothing). Fix any schema mismatches before proceeding. |

---

### Phase 1 — Foundation (no dependencies)

**Estimated duration: ~15 minutes (PROD)**

| Job | Source | Target | Count (PROD est.) |
|---|---|---|---|
| `01-users` | `users` | `users` | ~3.3M |

`users` is the root entity referenced by nearly every other collection.
It runs alone and must complete before Phase 2 begins.

---

### Phase 2 — Room Shell (parallel)

**Estimated duration: ~5 minutes (PROD)**

| Job | Source | Target | Count (PROD est.) |
|---|---|---|---|
| `02-rooms` | `rocketchat_room` | `rooms` | ~214K |
| `02-avatars` | `rocketchat_avatars` | `rocketchat_avatars` | negligible |

`rooms` is migrated without `lastMsgId` / `lastMsgAt` — these denormalized
fields are backfilled in Phase 6 after messages exist.

---

### Phase 3 — Membership + Messages (parallel)

**Estimated duration: ~3–6 hours (PROD) — dominated by Cassandra message writes**

All Phase 3 jobs run in parallel:

| Job | Source | Target | Count (PROD est.) | Est. Duration |
|---|---|---|---|---|
| `03-subscriptions` | `rocketchat_subscription` | `subscriptions` | ~1.6M | ~5 min |
| `03-room-members` | `tsmc_room_members` | `room_members` | ~310K | ~1 min |
| `03-uploads` | `rocketchat_uploads` | `rocketchat_uploads` | ~14K | <1 min |
| `03-cassandra-messages-by-id` | `rocketchat_message` | `messages_by_id` | 216M | ~3 hours |
| `03-cassandra-messages-by-room` | `rocketchat_message` | `messages_by_room` | 216M | ~3 hours |
| `03-cassandra-pinned-messages` | `rocketchat_message` (pinned) | `pinned_messages_by_room` | small subset | <30 min |

> The two Cassandra message jobs (`messages_by_id` and `messages_by_room`) read the same
> source collection but write to different tables. They run in parallel — each writing
> a different Cassandra table — cutting the wall-clock time roughly in half vs sequential.
>
> Messages are inserted in ascending `created_at` order so that any self-referential
> quoted message IDs always point to an already-written row.

---

### Phase 4 — Thread Structure (parallel)

**Estimated duration: ~30–60 minutes (PROD)**

| Job | Source | Target | Notes |
|---|---|---|---|
| `04-thread-rooms` | `rocketchat_message` (where `tcount > 0`) | `thread_rooms` *(derived)* | One `thread_rooms` document per thread root message |
| `04-cassandra-thread-messages` | `rocketchat_message` (where `tmid` is set) | `thread_messages_by_thread` | Thread reply messages only |

`thread_rooms` skips `lastMsgId` / `lastMsgAt` — backfilled in Phase 6.

---

### Phase 5 — Thread Subscriptions

**Estimated duration: ~55 minutes (PROD)**

| Job | Source | Target | Count (PROD est.) |
|---|---|---|---|
| `05-thread-subscriptions` | `tsmc_thread_subscriptions` | `thread_subscriptions` | ~16.6M |

Deepest dependency node — requires `thread_rooms`, `rooms`, and `users` to all exist.

---

### Phase 6 — Denormalized Backfill (parallel)

**Estimated duration: ~10 minutes (PROD)**

| Job | Updates |
|---|---|
| `06-backfill-rooms-lastmsg` | Sets `rooms.lastMsgId` and `rooms.lastMsgAt` from Cassandra `messages_by_room` |
| `06-backfill-thread-rooms-lastmsg` | Sets `thread_rooms.lastMsgId` and `thread_rooms.lastMsgAt` from Cassandra `thread_messages_by_thread` |

---

## 6. Timeline Summary (PROD)

| Phase | Jobs | Est. Duration | Running Total |
|---|---|---|---|
| Pre-Migration | Dry-run, baseline counts | ~1 hour | 1 hr |
| Phase 1 | users | ~15 min | 1 hr 15 min |
| Phase 2 | rooms, avatars | ~5 min | 1 hr 20 min |
| Phase 3 | subscriptions, room_members, uploads, Cassandra messages | **3–6 hours** | 4.5–7.5 hrs |
| Phase 4 | thread_rooms, Cassandra thread messages | ~30–60 min | 5–8.5 hrs |
| Phase 5 | thread_subscriptions | ~55 min | 6–9.5 hrs |
| Phase 6 | backfill | ~10 min | 6.5–10 hrs |
| Connector catch-up | Replay events since resume token | ~30–60 min | **7–11 hrs total** |

> **Note:** Throughput assumptions — MongoDB: ~5,000 docs/sec per job;
> Cassandra: ~20,000 rows/sec per job. Actual throughput depends on cluster
> sizing and network. A load test in TEST environment is recommended before
> the PROD run.

---

## 7. Validation Strategy

A validation gate runs after every phase before the next phase starts.
If any check fails, the migration halts for investigation.

| Check | Method | Threshold |
|---|---|---|
| Document count match | Source count vs target count per collection | Delta < 0.1% |
| Spot check integrity | Sample 1,000 random documents, verify key fields | 100% match |
| Reference integrity | Sample `subscriptions.roomId` values exist in `rooms` | 100% |
| Backfill correctness | `rooms.lastMsgId` populated and non-null | 100% of non-empty rooms |

---

## 8. Risk & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Job crashes mid-collection | Medium | Low | Checkpointing + idempotent upserts — restart from checkpoint |
| Schema mismatch (source vs target) | Medium | High | Dry-run in Pre-Migration catches this before real run |
| Cassandra throughput lower than estimated | Medium | Medium | Load test in TEST first; scale Cassandra write workers if needed |
| Connector misses events from before migration | Low | High | Resume token captured in P0.1 before any migration work begins |
| `thread_rooms` derivation is slow (full scan of 216M messages) | Medium | Medium | Run with an index on `tcount > 0`; parallelize by room |
| Silent data loss not caught | Low | High | Validation gate between every phase with count + spot checks |
| Cut-over happens before connector catches up | Low | High | Connector lag monitored; cut-over blocked until lag < 5 seconds for 10 min |

---

## 9. Rollback Plan

| Scenario | Action |
|---|---|
| Failure during bulk migration | Stop all jobs. Fix the issue. Restart only the failed job from its checkpoint. Other completed jobs are unaffected. |
| Failure discovered after cut-over | Switch application traffic back to source system (source was kept read-only, not decommissioned). |
| Connector falls behind and cannot catch up | Pause cut-over. Investigate write rate vs connector throughput. Scale connector workers if needed. |

**Source system decommission window:** The source system is kept available in
read-only mode for **48 hours** after successful cut-over. It is not shut down
until the new system is confirmed stable.

---

## 10. Success Criteria

Migration is considered complete when all of the following are true:

- [ ] All Phase 1–6 jobs have reached `Complete` status in Kubernetes
- [ ] All validation gates passed (no halts, no threshold violations)
- [ ] Backfill pass complete — `rooms.lastMsgId` and `thread_rooms.lastMsgId` populated
- [ ] Oplog connector running and lag is < 5 seconds, sustained for 10 minutes
- [ ] Application smoke test passed on nextgen (send a message, load history, search)
- [ ] Source system switched to read-only

---

## 11. Pre-Migration Checklist

- [ ] Oplog resume token captured and stored (P0.1)
- [ ] Baseline document counts recorded for all source collections
- [ ] Dry-run completed with no schema errors
- [ ] Target MongoDB and Cassandra clusters provisioned and sized for write load
- [ ] Kubernetes migration namespace created with ConfigMap and Secrets applied
- [ ] All Job manifests reviewed and tested in TEST environment
- [ ] On-call schedule confirmed for the migration window
- [ ] Rollback procedure reviewed with the team

---

*This document is a working design spec. Final timeline estimates should be
validated by running the full migration pipeline against the TEST environment
before scheduling the PROD window.*
