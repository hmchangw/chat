# Message Edit & Delete Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add message edit and soft-delete operations end-to-end. Clients can request edits/deletes via NATS; the change is validated, persisted to Cassandra across all denormalized tables, and broadcast to room members. `history-service` remains read-only — reads naturally reflect edits/deletes via existing schema columns.

**Architecture:** Follows existing CQRS pattern. Client request → `message-gatekeeper` (validates + authorizes via Cassandra read) → publishes canonical event to `MESSAGES_CANONICAL` → `message-worker` applies UPDATEs to all tables that hold the row → `broadcast-worker` fans out to room members. `history-service` requires no code changes.

**Tech Stack:** Go 1.25, NATS JetStream, Cassandra (gocql), MongoDB, go.uber.org/mock, testify, testcontainers-go

**Decisions (locked):**
- **Delete semantics:** Soft delete via `UPDATE … SET deleted = true, updated_at = now()`. `msg` content retained. No Cassandra `DELETE`.
- **Edit semantics:** `UPDATE … SET msg = ?, edited_at = now(), updated_at = now()`. Only `msg` editable.
- **Authorization:** actor is the message sender (`actor.account == message.sender.account`) OR actor has `RoleOwner` in their subscription for the room. No separate admin role exists today.
- **Message length:** Reuse existing `maxContentBytes = 20 * 1024` (20 KB) from `message-gatekeeper/handler.go:19`. Extract into a shared helper used by create + edit.
- **Event payload includes `createdAt`:** Cassandra PK is `(room_id, created_at, message_id)`. Including `createdAt` avoids an extra lookup in the worker.
- **Naming:** Event payload uses `editedBy` / `deletedBy` (the account performing the action), matching the codebase convention of using `account` for user identifiers.
- **Idempotency:** All worker handlers must be idempotent — JetStream retry converges on the same state.

**Deferred / out of scope:**
- Thread `QuotedParentMessage` snapshot updates in child rows (accepted eventual-consistency gap)
- Audit / edit-history table (overwrites `msg` in place)
- Cross-site federation via outbox (event shape designed to be forward-compatible)
- Extending the role model (owner-as-admin for now)

**Known eventual-consistency gaps (documented, not fixed):**
- Multi-table fan-out partial failure: if one UPDATE succeeds and another fails, the room is temporarily inconsistent. Mitigation: JetStream retry + idempotent handlers converge on success.
- Thread parent snapshot drift: edited/deleted parent messages won't update `QuotedParentMessage` embedded in child rows.

---

## Section A: Model & Subject Changes

### Task 1: Add canonical event payload types

**Files:**
- Modify: `pkg/model/event.go` (or appropriate message-event file)
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests for `MessageEditedEvent` and `MessageDeletedEvent`**

Add round-trip JSON tests (both `json` and `bson` tags) using the existing `roundTrip` helper in `model_test.go`. Cover all fields present, missing-optional handling, and time-field marshaling.

- [ ] **Step 2: Define the event structs**

```go
type MessageEditedEvent struct {
    SiteID    string    `json:"siteId"    bson:"siteId"`
    RoomID    string    `json:"roomId"    bson:"roomId"`
    MessageID string    `json:"messageId" bson:"messageId"`
    CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
    NewMsg    string    `json:"newMsg"    bson:"newMsg"`
    EditedBy  string    `json:"editedBy"  bson:"editedBy"`
    EditedAt  time.Time `json:"editedAt"  bson:"editedAt"`
    Timestamp int64     `json:"timestamp" bson:"timestamp"`
}

type MessageDeletedEvent struct {
    SiteID    string    `json:"siteId"    bson:"siteId"`
    RoomID    string    `json:"roomId"    bson:"roomId"`
    MessageID string    `json:"messageId" bson:"messageId"`
    CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
    DeletedBy string    `json:"deletedBy" bson:"deletedBy"`
    DeletedAt time.Time `json:"deletedAt" bson:"deletedAt"`
    Timestamp int64     `json:"timestamp" bson:"timestamp"`
}
```

`Timestamp` follows `CLAUDE.md §Event Timestamps` (publish time, UnixMilli UTC).

- [ ] **Step 3: Also add client-facing request payload types**

```go
type EditMessageRequest struct {
    MessageID string `json:"messageId"`
    NewMsg    string `json:"newMsg"`
    RequestID string `json:"requestId,omitempty"`
}

type DeleteMessageRequest struct {
    MessageID string `json:"messageId"`
    RequestID string `json:"requestId,omitempty"`
}
```

Response types match the existing pattern (success ack / error via `natsutil.ReplyError`).

---

### Task 2: Add NATS subject builders for edit/delete

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests** for `MsgEditPattern` and `MsgDeletePattern` producing the expected strings.

- [ ] **Step 2: Add the builders**

```go
func MsgEditPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.edit", siteID)
}

func MsgDeletePattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.delete", siteID)
}
```

(Canonical subjects `MsgCanonicalUpdated` and `MsgCanonicalDeleted` already exist.)

---

## Section B: message-worker — Cassandra Fan-out

### Task 3: Add Cassandra UPDATE methods to the worker store

**Files:**
- Modify: `message-worker/store.go` (interface)
- Modify: `message-worker/store_cassandra.go` (implementation)
- Test: `message-worker/store_cassandra_test.go` (integration, `//go:build integration`)

- [ ] **Step 1: Write failing integration tests** using testcontainers-go:
  - Edit updates `msg`, `edited_at`, `updated_at` in `messages_by_room`, `messages_by_id`
  - Edit also updates `thread_messages_by_room` when the message was threaded
  - Edit also updates `pinned_messages_by_room` when the message was pinned
  - Edit on a non-existent row is a safe no-op
  - Delete sets `deleted = true` + `updated_at` across the same tables
  - Idempotency: running the same UPDATE twice yields identical state

- [ ] **Step 2: Extend the `Store` interface**

```go
type Store interface {
    // existing methods...
    UpdateMessageContent(ctx context.Context, roomID, messageID string, createdAt time.Time, newMsg string, editedAt time.Time) error
    SoftDeleteMessage(ctx context.Context, roomID, messageID string, createdAt time.Time, deletedAt time.Time) error
}
```

- [ ] **Step 3: Implement the Cassandra UPDATEs** — fan out to all four tables (`messages_by_room`, `thread_messages_by_room`, `messages_by_id`, `pinned_messages_by_room`). Always-issue; UPDATE is a no-op if the row doesn't exist. Collect errors; return first failure.

- [ ] **Step 4: Run `make generate`** to regenerate `mock_store_test.go`.

---

### Task 4: Route consumer by subject suffix and dispatch edit/delete handlers

**Files:**
- Modify: `message-worker/handler.go`
- Test: `message-worker/handler_test.go` (unit tests with mocked store)

- [ ] **Step 1: Write failing unit tests** for the routing logic:
  - `.created` subject → existing create path
  - `.edited` subject → new edit handler, invokes `UpdateMessageContent` with PK-bearing event fields
  - `.deleted` subject → new delete handler, invokes `SoftDeleteMessage`
  - Unknown suffix → log + ack (don't infinitely redeliver)
  - Store error on edit/delete → Nak for retry
  - Repeated delivery of the same event is safe (idempotency)

- [ ] **Step 2: Implement dispatch** inside `HandleJetStreamMsg` (or split into separate methods called by a dispatcher): parse the subject, unmarshal the right event struct, call the right store method.

- [ ] **Step 3: Ensure the existing `MaxRedeliver` / Nak path covers transient Cassandra errors.** No new retry logic needed.

---

## Section C: message-gatekeeper — Validation & Authorization

### Task 5: Add a slim Cassandra read repo to gatekeeper

**Files:**
- Create: `message-gatekeeper/store_cassandra.go`
- Modify: `message-gatekeeper/store.go` (interface)
- Modify: `message-gatekeeper/main.go` (wire dependency)
- Test: `message-gatekeeper/store_cassandra_test.go` (integration)

- [ ] **Step 1: Write failing integration tests** for a `GetMessageMeta(roomID, messageID)` method returning `sender account`, `createdAt`, and a not-found error when missing.

- [ ] **Step 2: Extend `Store` interface** with `GetMessageMeta`. Implement in a new Cassandra store file (mirror `history-service/internal/cassrepo` but slimmed). Query `messages_by_id` for PK + sender — minimum columns only.

- [ ] **Step 3: Wire Cassandra session** in `main.go` using `pkg/cassutil`. Add `CASSANDRA_HOSTS` and `CASSANDRA_KEYSPACE` env vars to config.

- [ ] **Step 4: Run `make generate`** for updated mocks.

---

### Task 6: Add `HandleEdit` and `HandleDelete` request handlers

**Files:**
- Modify: `message-gatekeeper/handler.go`
- Modify: `message-gatekeeper/main.go` (subscribe new subjects)
- Test: `message-gatekeeper/handler_test.go`

- [ ] **Step 1: Extract the length validator into a shared helper**

Move the `maxContentBytes` check from `processMessage` into a private helper `validateContent(s string) error`. Reuse for create and edit.

- [ ] **Step 2: Write failing table-driven unit tests for `HandleEdit`** covering:
  - Happy path: sender edits own message → publishes `MsgCanonicalUpdated` with correct payload, replies success
  - Happy path: room owner edits someone else's message → same publish + reply
  - Forbidden: non-sender non-owner → reply with sanitized error, no publish
  - Message not found → reply with sanitized error, no publish
  - Empty `newMsg` → reply with error, no publish
  - `newMsg` exceeds 20 KB → reply with error, no publish
  - Store error on `GetMessageMeta` → infraError / Nak path
  - Publish error → infraError / Nak path

- [ ] **Step 3: Write failing table-driven unit tests for `HandleDelete`** covering equivalent scenarios (no length check).

- [ ] **Step 4: Implement `HandleEdit`**

1. Parse subject params `account`, `roomID`, `siteID`
2. Unmarshal `EditMessageRequest`
3. Validate `messageID` present; `newMsg` non-empty; `validateContent(newMsg)` passes
4. `meta, err := store.GetMessageMeta(roomID, messageID)` — not-found → sanitized error
5. Authorization: `meta.Sender == account` OR subscription role check via existing `GetSubscription` (check `RoleOwner` in `sub.Roles`)
6. Build `MessageEditedEvent` with `CreatedAt = meta.CreatedAt`, `EditedBy = account`, `EditedAt = now().UTC()`, `Timestamp = now().UnixMilli()`
7. Publish to `subject.MsgCanonicalUpdated(siteID)` with `jetstream.WithMsgID(messageID + ":edit:" + timestamp)` for dedupe
8. Reply success

- [ ] **Step 5: Implement `HandleDelete`** — same flow, no length check, builds `MessageDeletedEvent`, publishes to `MsgCanonicalDeleted`.

- [ ] **Step 6: Subscribe new subjects in `main.go`**

Use `nc.QueueSubscribe` with service queue group on `subject.MsgEditPattern(siteID)` and `MsgDeletePattern(siteID)` (converted from the `{param}` pattern to a NATS `*` wildcard where needed).

---

## Section D: broadcast-worker — Fan-out

### Task 7: Handle edit/delete canonical events in broadcast-worker

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/main.go` (subscription config if needed)
- Test: `broadcast-worker/handler_test.go`

- [ ] **Step 1: Write failing unit tests:**
  - `.edited` canonical event → per-member user-facing event published to each room member
  - `.deleted` canonical event → per-member event published
  - Event payload includes `messageId`, `roomId`, new `msg` (for edit), `deleted=true` (for delete), and `editedBy`/`deletedBy`
  - Unknown canonical suffix → log + ack

- [ ] **Step 2: Verify the existing canonical subscription wildcard** covers `.edited` / `.deleted`. If the subscription is on a specific subject, broaden to `chat.msg.canonical.{siteID}.>` or add the two new subjects explicitly.

- [ ] **Step 3: Implement event translation** — turn the canonical event into the user-facing event subject per subscribed member and publish.

---

## Section E: history-service — Regression Tests Only

### Task 8: Add integration tests confirming edits/deletes are visible via reads

**Files:**
- Modify: `history-service/internal/cassrepo/integration_test.go`

- [ ] **Step 1: Write tests:**
  - Insert a message, run `UpdateMessageContent`-equivalent CQL directly in the test, assert `LoadHistory` / `GetMessageByID` returns new `msg` and non-zero `edited_at`
  - Insert a message, run `SoftDeleteMessage`-equivalent CQL, assert reads return `deleted=true`

- [ ] **Step 2: No production code changes** in history-service.

---

## Section F: Documentation

### Task 9: Update service-level docs

**Files:**
- Modify: `message-worker/README.md`
- Modify (if present): `message-gatekeeper/README.md`

- [ ] **Step 1: Document the new subject handlers** in `message-worker/README.md`: `.created`, `.edited`, `.deleted` behaviors and the table fan-out.

- [ ] **Step 2: Add a "Known eventual-consistency gaps" section** covering:
  - Multi-table UPDATE partial failure (JetStream retry convergence)
  - Thread parent snapshot drift (intentionally not propagated)

- [ ] **Step 3: No DDL change** — `docs/cassandra_message_model.md` already has `edited_at`, `updated_at`, `deleted` columns.

---

## Implementation Order (dependency-aware)

1. Task 1 — model event types (unblocks everything)
2. Task 2 — subject builders
3. Task 3 — message-worker store methods (locks Cassandra contract first)
4. Task 4 — message-worker dispatch
5. Task 5 — gatekeeper Cassandra read
6. Task 6 — gatekeeper handlers
7. Task 7 — broadcast-worker fan-out
8. Task 8 — history-service regression tests
9. Task 9 — docs
10. Manual smoke test via `docker-local` compose

## Coverage & Quality Gates

- Minimum 80% coverage per package, 90%+ for handlers/stores (CLAUDE.md §Testing Rules)
- All new tests follow Red-Green-Refactor; never write implementation before a failing test exists
- Run `make lint` and `make test` (with `-race`) before each commit
- Run `make test-integration SERVICE=<name>` for any package with new integration tests
- Pre-commit hook enforces lint/tests — fix root causes, never bypass with `--no-verify`

## Risk Callouts

- **Denormalization fan-out:** partial failure leaves a room temporarily inconsistent. Mitigation: idempotent UPDATEs + JetStream retry (`MaxRedeliver=5`).
- **No audit trail:** a malicious owner can rewrite history silently. Accepted trade-off; flag in PR description.
- **Thread parent drift:** edits to a parent don't update child `QuotedParentMessage` snapshots. Deferred.
- **Cassandra timing:** `edited_at`/`updated_at` are set at the gatekeeper (publish site) not the worker, so they reflect user-intent time and survive worker retries. Document this choice.
