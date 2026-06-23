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
catch-up, assuming no major failures. The dominant factor is the 216M message write
to Cassandra (Phase 3) — all other collections are comparatively small.

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

### PROD Environment

Scale factors are **not uniform** across collections — each collection grows independently:

| Collection | PROD Count | Source | Scale vs TEST |
|---|---|---|---|
| `rocketchat_message` | **216,000,000** | Confirmed | 31× |
| `rocketchat_room` | **~1,000,000** | Confirmed | ~145× |
| `users` | **~90,000** | Confirmed | ~0.8× (same order as TEST) |
| `rocketchat_subscription` | ~7,400,000 | Estimated (7.4 subs/room × 1M rooms) | — |
| `tsmc_thread_subscriptions` | ~2,400,000 | Estimated (31× TEST) | ~31× |
| `tsmc_room_members` | ~1,450,000 | Estimated (1.45 members/room × 1M rooms) | — |
| `rocketchat_uploads` | ~14,000 | Estimated (31× TEST) | ~31× |
| `rocketchat_avatars` | negligible | — | — |

> Subscription and room_member counts are estimated by multiplying the TEST ratio
> (subs per room, members per room) by the confirmed PROD room count.
> Exact counts must be captured in P0.2 (Pre-Migration Baseline) before the run.

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
    01-rooms.yaml
    01-subscriptions.yaml
    01-avatars.yaml
    02-room-members.yaml
    02-thread-subscriptions.yaml
    02-thread-rooms.yaml
    03-cassandra-messages-by-id.yaml
    03-cassandra-messages-by-room.yaml
    03-cassandra-pinned-messages.yaml
    03-cassandra-thread-messages.yaml
    04-backfill-rooms-lastmsg.yaml
    04-backfill-thread-rooms-lastmsg.yaml
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

### Phase 1 — Foundation (all parallel, source deps only)

**Estimated duration: ~25 minutes (PROD) — dominated by subscriptions**

None of these jobs have a target dependency. They all read from source collections only
and can run fully in parallel.

| Job | Source Collections Read | Target | Count (PROD est.) | Est. Duration |
|---|---|---|---|---|
| `01-users` | `users` | `users` | ~90K | <1 min |
| `01-rooms` | `rocketchat_room` + `rocketchat_subscription` | `rooms` | ~1,000,000 | ~3–4 min |
| `01-subscriptions` | `rocketchat_subscription` + `rocketchat_room` | `subscriptions` | ~7,400,000 | ~25 min |
| `01-avatars` | `rocketchat_avatars` | `rocketchat_avatars` | negligible | <1 min |

`rooms` is migrated without `lastMsgId` / `lastMsgAt` — backfilled in Phase 4.

---

### Phase 2 — Derived + Dependent Collections (all parallel, need target `users`)

**Estimated duration: ~30–60 minutes (PROD) — dominated by `thread_rooms` derivation**

All three jobs need `users` from Phase 1. They run in parallel with each other.
`thread_rooms` is the critical gate: Phase 3 cannot start until it completes.

| Job | Source Collections Read | Target | Target Dep | Count (PROD est.) | Est. Duration |
|---|---|---|---|---|---|
| `02-room-members` | `tsmc_room_members` | `room_members` | `users` | ~1,450,000 | ~5 min |
| `02-thread-subscriptions` | `tsmc_thread_subscriptions` + `rocketchat_room` | `thread_subscriptions` | `users` | ~2,400,000 | ~8 min |
| `02-thread-rooms` | `rocketchat_message` + `tsmc_thread_subscriptions` + `rocketchat_room` | `thread_rooms` *(derived)* | `users` | scan of 216M | ~30–60 min |

**`thread_rooms` derivation:** Scans all 216M source messages where `tcount > 0`
(thread root messages) and constructs one `thread_rooms` document per root.
Skips `lastMsgId` / `lastMsgAt` — backfilled in Phase 4.

Before Phase 3 starts: create an index on `thread_rooms.parentMessageId` in the
target — every Phase 3 Cassandra write for a thread message hits this lookup.

---

### Phase 3 — Cassandra Message Tables (all parallel, need target `thread_rooms`)

**Estimated duration: ~3–6 hours (PROD) — the dominant phase**

All four Cassandra tables need `thread_room_id`, which is resolved by looking up
`thread_rooms.parentMessageId` in the target for each thread-related message.
All four jobs run in parallel.

| Job | Source | Target | Count (PROD est.) | Est. Duration |
|---|---|---|---|---|
| `03-cassandra-messages-by-id` | `rocketchat_message` | `messages_by_id` | 216M | ~3 hrs |
| `03-cassandra-messages-by-room` | `rocketchat_message` | `messages_by_room` | 216M | ~3 hrs |
| `03-cassandra-pinned-messages` | `rocketchat_message` (pinned) | `pinned_messages_by_room` | small subset | <30 min |
| `03-cassandra-thread-messages` | `rocketchat_message` (where `tmid` set) | `thread_messages_by_thread` | subset of 216M | ~1–2 hrs |

> `messages_by_id` and `messages_by_room` read the same source but write to
> different Cassandra tables — parallel jobs halve the wall-clock time.
>
> All jobs insert messages in ascending `created_at` order.

---

### Phase 4 — Denormalized Backfill (parallel)

**Estimated duration: ~10 minutes (PROD)**

| Job | Updates |
|---|---|
| `04-backfill-rooms-lastmsg` | Sets `rooms.lastMsgId` and `rooms.lastMsgAt` from Cassandra `messages_by_room` |
| `04-backfill-thread-rooms-lastmsg` | Sets `thread_rooms.lastMsgId` and `thread_rooms.lastMsgAt` from Cassandra `thread_messages_by_thread` |

---

## 6. Timeline Summary (PROD)

| Phase | Jobs | Est. Duration | Running Total |
|---|---|---|---|
| Pre-Migration | Dry-run, baseline counts | ~1 hour | 1 hr |
| Phase 1 | users, rooms, subscriptions, avatars | ~25 min | ~1 hr 25 min |
| Phase 2 | room_members, thread_subscriptions, **thread_rooms** (critical gate) | ~30–60 min | ~2–2.5 hrs |
| Phase 3 | all 4 Cassandra tables (216M messages) | **~3–6 hours** | ~5–8.5 hrs |
| Phase 4 | backfill rooms + thread_rooms | ~10 min | ~5–9 hrs |
| Connector catch-up | Replay events since resume token | ~30–60 min | **~5.5–10 hrs total** |

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
| `thread_rooms` derivation is slow — it gates all 216M Cassandra writes | Medium | **High** | Index `rocketchat_message` on `tcount` before running; parallelize scan by date range if needed |
| `thread_room_id` lookup slow in Phase 3 (216M hits on thread_rooms) | Medium | Medium | Index `thread_rooms.parentMessageId` before Phase 3 starts |
| Cassandra throughput lower than estimated | Medium | Medium | Load test in TEST first; scale Cassandra write workers if needed |
| Connector misses events from before migration | Low | High | Resume token captured in P0.1 before any migration work begins |
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

- [ ] All Phase 1–4 jobs have reached `Complete` status in Kubernetes
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
