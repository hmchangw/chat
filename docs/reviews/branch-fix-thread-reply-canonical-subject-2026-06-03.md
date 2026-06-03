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

---

## Service: history-service

**Overall assessment:** Sound implementation. The `SoftDeleteMessage` signature change (`→ (*int, error)` for tcount) is correctly propagated through all call sites. `publishCanonicalBestEffort` correctly swallows publish errors with a log line, satisfying the best-effort contract. The new test `TestHistoryService_DeleteMessage_ThreadReply_PublishFailsButDeleteSucceeds` verifies this contract.

**Findings:**

- `medium` — `internal/service/messages.go` — `publishCanonicalBestEffort` logs the publish error with `slog.Error` but does not include the `requestID` from context. All log lines in new code must include the correlation ID (see Observability chapter).

- `medium` — `internal/service/messages.go` — The `EventThreadReplyAdded` path in `DeleteMessage` constructs the canonical event with `NewTCount` from `SoftDeleteMessage`'s return. If `SoftDeleteMessage` returns a non-nil tcount and a non-nil error simultaneously (an edge case that should not happen but is not contractually excluded), the code publishes the event with potentially stale data. A comment asserting the mutual exclusivity would clarify intent.

- `medium` — `internal/cassrepo/write.go` — `SoftDeleteMessage`'s tcount decrement is a Cassandra counter decrement. If the service crashes after decrement but before returning, the canonical delete event will never be published on retry (the LWT `IF deleted_at = null` guard prevents re-deletion). The tcount will be decremented twice on the next delivery attempt if the message is still `deleted_at = null`. This is the same partial-write concern as message-worker; a comment noting the retry behaviour would be appropriate.

- `medium` — `internal/service/integration_test.go` — Integration tests for the `SoftDeleteMessage → publishCanonicalBestEffort` path exist but do not inject a failing publisher to exercise the best-effort swallow; they rely on the unit test. Given the best-effort contract is critical for consistency, adding an integration-level check would raise confidence.

- `low` — `internal/publisher/publisher.go` — The `Publisher` interface is minimal and correct. The one export `Publish` is the same signature as `nc.Publish`. No issues.

---

## Service: room-service

**Overall assessment:** Correct patterns followed throughout. The `UpdateSubscriptionThreadRead` pipeline addition correctly uses a MongoDB aggregation pipeline to update `threadUnread` and recompute `alert` atomically from the post-update state.

**Findings:**

- `medium` — `store_mongo.go:1010–1030` — `UpdateSubscriptionThreadRead` uses a three-stage aggregation pipeline (`$set` → `$set` → `$set`). On concurrent requests for the same `(account, roomID)`, both requests read the same pre-update `threadUnread`, and the second write's `$filter` operates on the pre-first-update state. The result is that one concurrent read "wins" and the other's alert recomputation is based on stale data. **Impact:** Incorrect unread badge until the next read event. **Mitigation:** The outbox event uses the returned value so cross-site consistency is preserved; this is a local-only display glitch. The existing design accepts best-effort badge accuracy for concurrent read-marks, consistent with the room-level unread counters. Document this in a comment.

- `medium` — `handler.go` — `handleThreadRead` and `handleSubscriptionRead` are new in this diff. Both are missing OTel spans (see Observability chapter for full details).

- `nitpick` — `store_mongo.go` — Variable name `threadUnreadEntry` is used in 3 aggregation pipeline stages but represents a different computed value in each. Renaming intermediate values to `threadUnreadFiltered` / `threadUnreadAfterFilter` would aid readability.

---

## Service: search-sync-worker / notification-worker / inbox-worker

### search-sync-worker

**Findings:**

- `medium` — `messages.go` — The new `handleThreadReplyAdded` handler correctly skips indexing for `EventThreadReplyAdded` events (badge-only events carry no new message content to index). The guard is present and correct. No issues beyond missing request-ID in log lines (see Observability chapter).

### notification-worker

**Findings:**

- `critical` — `handler.go:46` — **`HandleMessage` has no event-type guard.** The handler fires `"new_message"` push notifications for **all 4 canonical event types** including `EventThreadReplyAdded`, `EventUpdated`, and `EventDeleted`. Thread-reply badge events carry `Content=""` and `UserID=""` in the message payload. Every thread reply will spam **all room members** with an empty push notification. This is a user-visible regression that must be fixed before merge.

  **Required fix:**
  ```go
  // At the top of HandleMessage, after unmarshalling evt:
  if evt.Event != model.EventCreated {
      return nil
  }
  ```

  This matches the intent described in `docs/thread-reply-notifications.md` (priority #1: only notify on new messages, not on badge-only events).

### inbox-worker

**Findings:**

- `critical` — `handler.go:245–246` — The error log for a failed `OutboxThreadSubscriptionUpserted` handler references the wrong field. The log emits `slog.String("account", event.RoomID)` (using `RoomID` for the `account` key). This silently records the room ID in place of the account, making the log useless for debugging.

  **Required fix:** `slog.String("account", event.Account)`

---

## Go Expert

**Scope:** Full branch diff reviewed for Go idioms, naming, error wrapping, struct tags, and CLAUDE.md Section 3 compliance.

### Critical

- `critical` — `pkg/model/event.go` — **`ThreadMetadataUpdatedEvent` is missing all `bson` tags.** CLAUDE.md Section 3 mandates: "All model structs get both `json` and `bson` tags." Every field — `Type`, `RoomID`, `SiteID`, `ParentMessageID`, `NewTCount`, `Action`, `ReplyMessageID`, `Timestamp` — has only a `json` tag. Any MongoDB round-trip (outbox serialisation, replay, storage) will silently lose all field values. Fix required before merge.

  ```go
  type ThreadMetadataUpdatedEvent struct {
      Type            RoomEventType `json:"type"            bson:"type"`
      RoomID          string        `json:"roomId"          bson:"roomId"`
      SiteID          string        `json:"siteId"          bson:"siteId"`
      ParentMessageID string        `json:"parentMessageId" bson:"parentMessageId"`
      NewTCount       int           `json:"newTcount"       bson:"newTcount"`
      Action          ThreadAction  `json:"action"          bson:"action"`
      ReplyMessageID  string        `json:"replyMessageId"  bson:"replyMessageId"`
      Timestamp       int64         `json:"timestamp"       bson:"timestamp"`
  }
  ```

### High

- `high` — `broadcast-worker/handler.go:79,111,191,200,245,253,261,430,476` — **12 bare `return err` statements** violate CLAUDE.md Section 3 ("Always wrap with context: `fmt.Errorf("short description: %w", err)`"). Examples of required fixes:
  - Line 79: `return fmt.Errorf("channel thread fan-out for parent %s: %w", parentMsgID, err)`
  - Line 111: `return fmt.Errorf("encrypt thread created event for parent %s: %w", parentMsgID, err)`
  - Line 191: `return fmt.Errorf("fan-out thread updated event for parent %s: %w", parentMsgID, err)`
  - Line 245: `return fmt.Errorf("fan-out thread deleted event for parent %s: %w", parentMsgID, err)`
  - Line 430: `return fmt.Errorf("get current room key for room %s: %w", roomID, err)`
  - Line 476: `return fmt.Errorf("encrypt room event for room %s: %w", roomID, err)`

  The pattern is consistent: whenever a helper call's error is returned directly, it needs wrapping to locate the failure in logs.

### Low / nitpick

- `low` — `broadcast-worker/handler.go` — `threadFanOutAccounts` iterates a `map[string]struct{}` and appends to a `[]string`. The helper is correct but a comment noting why duplicate subscribers are already excluded (the map key uniqueness guarantee) would aid future readers.

- `nitpick` — `pkg/model/event.go` — `ThreadAction` type defined adjacent to `RoomEventType` constants is fine idiomatically, but the `ThreadActionReplyAdded` / `ThreadActionReplyDeleted` constants are separated from the `ThreadMetadataUpdatedEvent` struct they exclusively serve. Co-locating them (or grouping with a blank-line separator + comment) would improve discoverability.

---

## Test-Automation

**TDD compliance:** New code follows the Red-Green-Refactor cycle. Table-driven tests are used throughout. The `-race` flag is covered by the Makefile. Mocks are regenerated (mock_store_test.go updated with `GetThreadFollowers`).

### High

- `high` — `broadcast-worker/handler.go` — **No unit test added for `publishThreadMetadata`.** `publishThreadMetadata` is a new exported-equivalent function (called from all three thread handlers) that constructs and publishes a `ThreadMetadataUpdatedEvent`. There is no `Test…_publishThreadMetadata` test case exercising: happy path, publish failure, and zero tcount edge case. CLAUDE.md Section 4 requires every new function to have a corresponding test. **TDD violation.**

- `high` — `room-service/handler.go` — **No unit test added for `usersByAccount`** (new helper introduced to build the `Participant` slice for `Mentions`). The helper is called from `buildRoomEvent`; its logic (account→participant mapping, deduplication) is untested in isolation. Omitting a test for a non-trivial data-transformation helper is a TDD violation per CLAUDE.md Section 4.

### Medium

- `medium` — `broadcast-worker/handler_test.go` — The error-path test cases for `GetThreadFollowers` returning an error cover the `channelThreadFanOut` wrapper but do not test what happens when `GetThreadFollowers` returns an **empty map** (no followers). The fan-out should be a no-op; verifying the downstream publish call is NOT made in that case would complete the boundary coverage.

- `medium` — `notification-worker/handler_test.go` — Once the `EventCreated`-only guard is added (critical fix), a test case for `evt.Event = model.EventThreadReplyAdded` must be added to verify it is silently dropped (no publish). Without this test the guard can be accidentally removed without a failing test.

### Low

- `low` — `message-worker/handler_test.go` — The `incrementParentTcount` failure path (where the tcount call fails) is not tested. The test should verify that the canonical publish is skipped and the error is returned (not swallowed).

- `low` — `mock_store_test.go` (broadcast-worker) — Mocks are up-to-date. No staleness issues detected (`make generate` matches committed mock).

---

## Bug & Security

**SAST (gosec + govulncheck + semgrep):** PASS — no medium+ findings introduced by this branch.

### Critical

- `critical` — `notification-worker/handler.go:46` — **Regression: push notifications fired for all 4 event types.** The branch introduces `EventThreadReplyAdded` events on `MESSAGES_CANONICAL`, but `notification-worker.HandleMessage` has no event-type guard. It fires `"new_message"` push notifications for every canonical event (created, updated, deleted, thread_reply_added) including badge-only events that carry `Content=""` and `UserID=""`. Every thread reply will spam all room members with an empty notification. **Fix (one line at handler entry):**
  ```go
  if evt.Event != model.EventCreated {
      return nil
  }
  ```

### High

- `high` — `room-service/store_mongo.go:1010–1030` — MongoDB aggregation pipeline in `UpdateSubscriptionThreadRead` has a race window. Two concurrent read-marks for the same `(account, roomID)` both read the same pre-update `threadUnread`. Each stages its `$filter` against stale data; the loser's recomputed `alert` is wrong. The `alert` value in the outbox event (cross-site) is derived from the pipeline return, so cross-site consistency is preserved — only the local badge is incorrect until the next read. This is an accepted best-effort trade-off but the race window should be documented.

### Medium

- `medium` — `broadcast-worker/handler.go:371–375` — **Nil pointer dereference risk in `handleThreadDeleted`.** `msg.UpdatedAt` is dereferenced on line 372 without a nil check. `handleThreadCreated` (line 132) guards against this; `handleThreadDeleted` does not. A malformed event with `UpdatedAt == nil` will panic the worker.

  **Fix:**
  ```go
  if msg.UpdatedAt == nil {
      return fmt.Errorf("thread deleted event missing UpdatedAt for message %s", msg.ID)
  }
  ```

- `medium` — `message-worker/store_cassandra.go:108–120` — `SaveThreadMessage` two-phase write (`messages_by_id` LWT then `thread_messages_by_thread` non-LWT) has partial-write risk on crash-between-statements. The idempotent INSERT on redelivery mitigates data loss, but the tcount may be decremented one too few times. This is a known Cassandra limitation; a comment explaining the invariant and trade-off is required.

### Low

- `low` — `broadcast-worker/handler.go:492–510` — `buildEditRoomEvent` and `buildDeleteRoomEvent` now correctly use `evt.Timestamp` from the source event rather than `time.Now()`. This is a correctness fix (edit/delete timestamps reflect event time, not processing time). No security issue; noted as a positive correctness change.

- `low` — `broadcast-worker/store.go` — `GetThreadFollowers` integration test against a live MongoDB collection is absent. Unit mock coverage is adequate but a store-level integration test would verify the projection and index usage end-to-end.

---

## Performance

### Critical

- `critical` — `broadcast-worker/handler.go` — **N+1 NATS publish in `publishToThreadAccounts`.** For each account in the follower set, `publishToThreadAccounts` calls `nc.Publish` individually (one NATS publish per follower). For a thread with 200 followers this is 200 sequential publish calls per event. NATS publish is fast but not free at scale; more critically this is 200 round-trips through the NATS subscription dispatch layer. **Recommended fix:** batch into a single publish to a room-scoped subject (`chat.room.{roomID}.thread.{parentID}.event`) with subscriber-side filtering, or use a fan-out subject pattern already established by broadcast-worker's room-level events. If per-account delivery is a hard requirement, use `nc.PublishMsg` with a pre-allocated `nats.Msg` to avoid per-call allocation.

### High

- `high` — `broadcast-worker/handler.go` — `channelThreadFanOut` calls `h.store.GetThreadFollowers` on every canonical event — including `EventUpdated` and `EventDeleted` events that do not change the thread follower set. For high-traffic threads, every edit/delete triggers an unnecessary MongoDB round-trip. Cache the follower set per `parentMessageID` with a short TTL (e.g. 30s) or skip the lookup for non-follower-modifying events.

- `high` — `message-worker/store_cassandra.go` — `incrementParentTcount` issues a Cassandra `UPDATE` (counter increment) followed by a separate `SELECT` to read the post-increment value. Cassandra counter tables do not return the post-update value in the write response. The two-query approach is correct but adds one extra round-trip per thread-reply insertion. Consider whether the tcount can be derived from a `SELECT count(*)` on `thread_messages_by_thread` (cheaper for small threads) or accepted as approximate (skip the SELECT, derive client-side from the event sequence number).

### Medium

- `medium` — `broadcast-worker/handler.go` — `threadFanOutAccounts` allocates a new `[]string` on every call by iterating the map. For empty maps (no followers) this allocates a non-nil slice. A `if len(followers) == 0 { return nil, nil }` early-return avoids the allocation and makes the no-follower fast path explicit.
