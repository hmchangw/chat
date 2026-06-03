# Branch Review: fix/thread-reply-canonical-subject

**Date:** 2026-06-03  
**Base branch:** main  
**Services touched (7):** broadcast-worker, history-service, inbox-worker, message-worker, notification-worker, room-service, search-sync-worker  
**Shared packages touched:** pkg/model  

---

## Executive Summary

This branch implements real-time thread-reply fan-out in broadcast-worker, the reply-count badge pipeline (tcount CAS → canonical event → client), and migrates broadcast-worker's follower lookup from `thread_subscriptions` to `thread_rooms.replyAccounts` (matching PR #237). The scope touches 7 services plus `pkg/model`.

**Finding counts:**

| Severity  | Count |
|-----------|-------|
| critical  | 6     |
| high      | 7     |
| medium    | 8     |
| low       | 3     |
| nitpick   | 2     |

**Top-line risk assessment:** **DO NOT MERGE** in current state. Two blocking issues:

1. `notification-worker` has **no event-type guard** — all 4 canonical event types (including `thread_reply_added`) trigger push notifications with empty payloads. This is a user-visible regression that spams every room member on every thread reply.
2. `pkg/model.ThreadMetadataUpdatedEvent` is missing all `bson` tags. Any MongoDB serialisation round-trip (outbox, replay) will silently drop all fields.

The rest of the implementation is solid: idempotent Cassandra LWT writes, correct dedup-ID generation, proper tcount propagation, well-structured handler decomposition. Fix the two blockers and the high-severity findings before merge.
