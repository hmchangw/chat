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

---

## Service: broadcast-worker

**Diff correctness:** The migration from `thread_subscriptions` to `thread_rooms.replyAccounts` is correctly implemented. `GetThreadFollowers` (store.go) uses a `FindOne` projection `{"replyAccounts":1,"_id":0}` on `parentMessageId`, mirrors `notification-worker`'s `mongoThreadFollowers.Followers()` pattern from PR #237, and correctly returns an empty `map[string]struct{}` (not an error) when the thread room document is absent (`mongo.ErrNoDocuments`). The collection wire-up in `main.go` (line 75) is correct.

**Scope / design coherence:** The handler decomposition into `handleThreadCreated`, `handleThreadUpdated`, `handleThreadDeleted` with a shared `channelThreadFanOut` helper is well-structured. The `siteID` parameter correctly dropped from `channelThreadFanOut` — broadcast-worker is single-site, the parameter was vestigial.

**Project-pattern adherence:**
- `idgen.GenerateUUIDv7()` used for dedup IDs ✓
- `pkg/subject` builders used for subject construction ✓
- Consumer pattern (`cons.Messages()` + semaphore) matches existing high-throughput services ✓

**Findings:**

- `nitpick` — `handler.go:79,111,191,200,245,253` — These are bare `return err` statements. Not in isolation severe (Go expert cross-cuts this), but the pattern is inconsistent with neighbouring wrapped returns in the same file.

- `low` — `store_mongo.go` `EnsureIndexes` creates an index on `thread_rooms(parentMessageId)` but there is no integration test verifying that `GetThreadFollowers` works correctly against a live MongoDB collection. The unit-test mock covers the happy path and `ErrNoDocuments`; a store integration test would complete coverage.

**Overall:** No critical or high findings in broadcast-worker specifically. The core fan-out logic is correct and the PR #237 migration pattern is faithfully applied.

---

## Service: message-worker

**Diff correctness:** The tcount CAS pipeline is the key addition: `SaveThreadMessage` performs an LWT insert (`IF NOT EXISTS`) into `messages_by_id`, increments the parent's `tcount` with `incrementParentTcount` (Cassandra LWT counter), and publishes the authoritative post-CAS value to the canonical stream as `EventThreadReplyAdded`. The flow is correct and idempotent — duplicate NATS deliveries are safely handled.

**Findings:**

- `critical` — `handler.go` — The canonical event published after `SaveThreadMessage` sets `NewTCount` from `incrementParentTcount`'s return, but if `incrementParentTcount` returns an error, the handler currently logs and **continues to publish** the canonical event with a nil `NewTCount`. Downstream consumers (`broadcast-worker`, history-service) rely on `NewTCount` being non-nil for `EventThreadReplyAdded` events. Publishing a badge event with a nil count will cause clients to display a stale badge until the next event. The handler must **not** publish if `incrementParentTcount` fails.

- `high` — `store_cassandra.go:108–120` — `SaveThreadMessage` writes `messages_by_id` (LWT) and `thread_messages_by_thread` (non-LWT) in separate statements with no transaction. On redelivery (`applied=false`), the code correctly skips `incrementParentTcount`, but the non-LWT `thread_messages_by_thread` INSERT is always re-executed. If the first attempt crashed between the two writes, the thread row would be missing. The second delivery writes it correctly (idempotent INSERT), but the tcount is already one too low from the first attempt's partial success. This is a known limitation of multi-statement Cassandra writes; it should be documented in a comment.

- `medium` — `handler_test.go` — Error path for `incrementParentTcount` failure is not tested. The test coverage for the `SaveThreadMessage → publish` pipeline covers the happy path but does not verify the "publish must be skipped on tcount failure" invariant.

- `medium` — `store_cassandra.go` — `incrementParentTcount` uses a Cassandra lightweight transaction but does not validate `applied=false` (concurrent identical increment is impossible by design, so this is low risk — but a comment explaining why the LWT result is not checked would prevent future confusion).
