# Superpowers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Execute the three remaining approved design specs in dependency order, bringing the chat system to full feature parity with the specification.

**Scope:** Three specs, estimated ~35 files touched across 8 services and 3 shared packages.

---

## Current State

All 8 microservices are functional. Shared packages (`pkg/model`, `pkg/subject`, `pkg/natsutil`, `pkg/mongoutil`, `pkg/cassutil`, `pkg/stream`, `pkg/shutdown`, `pkg/otelutil`, `pkg/roomcrypto`, `pkg/roomkeystore`) are complete. The gaps are:

1. Broadcast events use separate `RoomMetadataUpdateEvent` + `MessageEvent` instead of a unified `RoomEvent`
2. NATS subjects inconsistently use `userID` vs `username` across services
3. `roomkeystore` lacks key rotation and versioning

---

## Dependency Graph

```
Phase 1: Combine Broadcast Events (Spec 2026-03-26)
   │
   ├── Introduces Subscription.User.Username (required by Phase 2)
   ├── Introduces UserRoomEvent(username) subject (convention adopted by Phase 2)
   └── Restructures Subscription model (all services must migrate)
   │
   ▼
Phase 2: System-Wide Username Subject Migration (Spec 2026-03-27)
   │
   ├── Renames all subject builder params from userID → username
   ├── Updates store queries from u._id → u.username
   └── Depends on Phase 1's Subscription.User.Username field
   │
   ▼
Phase 3: Room Key Rotation (Spec 2026-03-30)    ← independent, can run in parallel with Phase 2
   │
   ├── Extends pkg/roomkeystore with VersionedKeyPair, Rotate, GetByVersion
   └── No dependencies on Phases 1 or 2
```

**Critical path:** Phase 1 → Phase 2. Phase 3 is independent and can be developed in parallel with either phase.

---

## Phase 1: Combine Broadcast Events

**Spec:** `docs/superpowers/specs/2026-03-26-combine-broadcast-events-design.md`
**Detailed plan:** `docs/superpowers/plans/2026-03-26-combine-broadcast-events.md`

This is the largest and most impactful change. It restructures the Subscription model (breaking change across all services), introduces a unified RoomEvent type, adds mention detection, and refactors broadcast-worker into the single authority for room metadata updates.

### Task 0: Restructure Subscription Model

**Impact:** All 8 services + `pkg/model`
**Risk:** Highest — breaking change to a core domain type

- [ ] **0.1** Update `pkg/model/subscription.go`: Replace flat `UserID` field with nested `SubscriptionUser` struct (`User.ID`, `User.Username`). Add `LastSeenAt` and `HasMention` fields.
- [ ] **0.2** Update `pkg/model/model_test.go`: Fix `TestSubscriptionJSON` round-trip test for new structure.
- [ ] **0.3** Migrate `message-worker`: Change MongoDB filter `"userId"` → `"u._id"`, update handler and test fixtures to use `sub.User.ID`.
- [ ] **0.4** Migrate `history-service`: Same filter change, update handler and tests.
- [ ] **0.5** Migrate `room-service`: Same filter change, update `CreateRoomRequest` handler to build `SubscriptionUser{ID, Username}`, update tests.
- [ ] **0.6** Migrate `room-worker`: Update handler to use `sub.User.ID` / `sub.User.Username`, update tests.
- [ ] **0.7** Migrate `inbox-worker`: Update subscription construction, update tests.
- [ ] **0.8** Migrate `notification-worker`: Use `sub.User.ID` for sender filtering, update tests.
- [ ] **0.9** Migrate `broadcast-worker`: Update existing test fixtures for new Subscription shape.
- [ ] **0.10** Run `make lint && make test` — all services must compile and pass.

### Task 1: Add Room Model Fields

- [ ] **1.1** Add `Origin`, `LastMsgAt`, `LastMsgID`, `LastMentionAllAt` fields to `Room` in `pkg/model/room.go`.
- [ ] **1.2** Update `TestRoomJSON` round-trip test in `pkg/model/model_test.go`.
- [ ] **1.3** Run `make test SERVICE=pkg/model`.

### Task 2: Add Username to User Model

- [ ] **2.1** Add `Username` field to `User` in `pkg/model/user.go`.
- [ ] **2.2** Update `TestUserJSON` round-trip test.
- [ ] **2.3** Run `make test SERVICE=pkg/model`.

### Task 3: Add RoomEvent Type and New Subjects

- [ ] **3.1** Add `RoomEvent` struct and `RoomEventType` constants to `pkg/model/event.go`.
- [ ] **3.2** Add `TestRoomEventJSON` round-trip test.
- [ ] **3.3** Add `RoomEvent(roomID)` and `UserRoomEvent(username)` builders to `pkg/subject/subject.go`.
- [ ] **3.4** Add test entries for new builders in `pkg/subject/subject_test.go`.
- [ ] **3.5** Run `make test SERVICE=pkg/model && make test SERVICE=pkg/subject`.

### Task 4: Broadcast-Worker Store Layer

- [ ] **4.1** Create `broadcast-worker/store.go` with `Store` interface: `GetRoom`, `ListSubscriptions`, `UpdateRoomOnNewMessage`, `SetSubscriptionMentions`. Add `//go:generate mockgen` directive.
- [ ] **4.2** Create `broadcast-worker/store_mongo.go` implementing `mongoStore`.
- [ ] **4.3** Write integration tests (`broadcast-worker/integration_test.go`) for all store methods.
- [ ] **4.4** Run `make generate SERVICE=broadcast-worker && make test-integration SERVICE=broadcast-worker`.

### Task 5: Broadcast-Worker Handler Rewrite

- [ ] **5.1** Implement `detectMentionAll` and `extractMentionedUsernames` helper functions in `broadcast-worker/handler.go`.
- [ ] **5.2** Rewrite handler: group room flow (one publish to `chat.room.{roomID}.event`) and DM flow (two publishes to `chat.user.{username}.event.room`).
- [ ] **5.3** Write comprehensive unit tests (`broadcast-worker/handler_test.go`): group no-mentions, group with @username, group with @All, group with both, DM no-mentions, DM with mention, invalid JSON, room not found, store errors.
- [ ] **5.4** Update `broadcast-worker/main.go` to wire `NewMongoStore` and new handler.
- [ ] **5.5** Run `make generate SERVICE=broadcast-worker && make test SERVICE=broadcast-worker`.

### Task 6: Remove Room Update from Message-Worker

- [ ] **6.1** Remove `UpdateRoomLastMessage` from `message-worker/store.go` interface and `store_mongo.go` implementation.
- [ ] **6.2** Remove the `UpdateRoomLastMessage` call from `message-worker/handler.go`.
- [ ] **6.3** Remove corresponding mock expectations from `message-worker/handler_test.go`.
- [ ] **6.4** Run `make generate SERVICE=message-worker && make test SERVICE=message-worker`.

### Task 7: Phase 1 Validation

- [ ] **7.1** Run `make lint` — zero warnings.
- [ ] **7.2** Run `make test` — all unit tests pass.
- [ ] **7.3** Run `make test-integration` — all integration tests pass (requires Docker).
- [ ] **7.4** Commit with message: `feat: unify broadcast events with RoomEvent type and mention detection`.

---

## Phase 2: System-Wide Username Subject Migration

**Spec:** `docs/superpowers/specs/2026-03-27-system-wide-username-subjects-design.md`
**Detailed plan:** `docs/superpowers/plans/2026-03-27-system-wide-username-subjects.md`
**Depends on:** Phase 1 (Subscription.User.Username must exist)

This is a mechanical refactor: rename parameters, update MongoDB filters from `u._id` to `u.username`, and ensure services populate `Message.UserID` from `sub.User.ID` rather than from the subject.

### Task 8: Rename Subject Builder Parameters

- [ ] **8.1** Rename all `userID` parameters to `username` in `pkg/subject/subject.go` (12 functions + `ParseUserRoomSubject` return value).
- [ ] **8.2** Update `pkg/subject/subject_test.go`: change test values from `"u1"` to `"alice"` for semantic clarity.
- [ ] **8.3** Run `make test SERVICE=pkg/subject`.

### Task 9: Add Request Model Fields

- [ ] **9.1** Add `CreatedByUsername` field to `CreateRoomRequest` in `pkg/model/room.go`.
- [ ] **9.2** Add `InviteeUsername` field to `InviteMemberRequest` in `pkg/model/event.go`.
- [ ] **9.3** Update round-trip tests.
- [ ] **9.4** Run `make test SERVICE=pkg/model`.

### Task 10: Migrate message-worker

- [ ] **10.1** Rename `GetSubscription` store param from `userID` to `username`.
- [ ] **10.2** Change MongoDB filter in `store_mongo.go`: `"u._id"` → `"u.username"`.
- [ ] **10.3** Update handler: extract `username` from subject, query subscription by username, populate `Message.UserID` from `sub.User.ID`.
- [ ] **10.4** Update handler tests and integration tests.
- [ ] **10.5** Run `make generate SERVICE=message-worker && make test SERVICE=message-worker`.

### Task 11: Migrate history-service

- [ ] **11.1** Same pattern: rename store param, change filter to `u.username`, update handler variable names.
- [ ] **11.2** Update tests.
- [ ] **11.3** Run `make generate SERVICE=history-service && make test SERVICE=history-service`.

### Task 12: Migrate room-service

- [ ] **12.1** Change `GetSubscription` filter to `u.username`.
- [ ] **12.2** Update handler to pass `CreatedByUsername` through to subscription creation.
- [ ] **12.3** Update tests.
- [ ] **12.4** Run `make generate SERVICE=room-service && make test SERVICE=room-service`.

### Task 13: Migrate notification-worker

- [ ] **13.1** Change notification publish subject: `subject.Notification(sub.User.ID)` → `subject.Notification(sub.User.Username)`.
- [ ] **13.2** Update tests.
- [ ] **13.3** Run `make test SERVICE=notification-worker`.

### Task 14: Migrate room-worker

- [ ] **14.1** Change metadata update subject: use `sub.User.Username` instead of `sub.User.ID`.
- [ ] **14.2** Populate `SubscriptionUser.Username` from `InviteeUsername` in invite flow.
- [ ] **14.3** Update tests.
- [ ] **14.4** Run `make test SERVICE=room-worker`.

### Task 15: Migrate inbox-worker

- [ ] **15.1** Change `SubscriptionUpdate` subject to pass `sub.User.Username`.
- [ ] **15.2** Populate username from request data.
- [ ] **15.3** Update tests.
- [ ] **15.4** Run `make test SERVICE=inbox-worker`.

### Task 16: Phase 2 Validation

- [ ] **16.1** Run `make lint` — zero warnings.
- [ ] **16.2** Run `make test` — all unit tests pass.
- [ ] **16.3** Run `make test-integration` — all integration tests pass.
- [ ] **16.4** Commit with message: `feat: migrate all user-scoped NATS subjects from userID to username`.

---

## Phase 3: Room Key Rotation

**Spec:** `docs/superpowers/specs/2026-03-30-room-key-rotation-design.md`
**Detailed plan:** `docs/superpowers/plans/2026-03-30-valkey-room-key-library.md` (base library — already implemented)
**Depends on:** Nothing (independent of Phases 1 and 2)

Extends `pkg/roomkeystore` with versioned key pairs, rotation with grace-period TTL for the previous key, and version-based lookup.

### Task 17: Update Config and Types

- [ ] **17.1** Add `VersionedKeyPair` type to `pkg/roomkeystore/roomkeystore.go`.
- [ ] **17.2** Replace `KeyTTL` with `GracePeriod` in `Config`.
- [ ] **17.3** Add `ErrNoCurrentKey` sentinel error.
- [ ] **17.4** Update `RoomKeyStore` interface: `Get` returns `*VersionedKeyPair`, add `GetByVersion` and `Rotate` methods.

### Task 18: Update hashCommander Interface

- [ ] **18.1** Add `rotatePipeline` and `deletePipeline` methods to `hashCommander` interface.
- [ ] **18.2** Implement in `redisAdapter` using `redis.Pipeline`.
- [ ] **18.3** Remove TTL from `Set` method (no longer calls `expire`).
- [ ] **18.4** Update `fakeHashClient` test double with new methods.

### Task 19: Implement New Methods

- [ ] **19.1** Update `Set`: write `pub`, `priv`, `ver` fields with no TTL.
- [ ] **19.2** Update `Get`: read `ver` field, return `*VersionedKeyPair`.
- [ ] **19.3** Implement `GetByVersion`: check current and previous hashes, return matching key pair.
- [ ] **19.4** Implement `Rotate`: pipeline copy-to-prev + write-new-current, return `ErrNoCurrentKey` if current absent.
- [ ] **19.5** Update `Delete`: remove both current and previous keys via `deletePipeline`.

### Task 20: Unit Tests

- [ ] **20.1** Update existing `Set`/`Get`/`Delete` tests for new signatures and behaviors.
- [ ] **20.2** Add `GetByVersion` tests: match current, match previous, no match, Valkey error.
- [ ] **20.3** Add `Rotate` tests: happy path, no current key returns `ErrNoCurrentKey`, replaces existing previous, pipeline error.
- [ ] **20.4** Add `Delete` tests: removes both keys, no-op when absent.
- [ ] **20.5** Run `make test SERVICE=pkg/roomkeystore`.

### Task 21: Integration Tests

- [ ] **21.1** Add rotate round-trip test: `Set` → `Rotate` → `Get` returns new → `GetByVersion` returns both.
- [ ] **21.2** Add grace period expiry test: rotate with 1s grace, verify old key expires after sleep.
- [ ] **21.3** Add rotate-with-no-current-key test: verify `errors.Is(err, ErrNoCurrentKey)`.
- [ ] **21.4** Run `make test-integration SERVICE=pkg/roomkeystore`.

### Task 22: Phase 3 Validation

- [ ] **22.1** Run `make lint` — zero warnings.
- [ ] **22.2** Verify coverage ≥ 90% for `pkg/roomkeystore`.
- [ ] **22.3** Commit with message: `feat(roomkeystore): add key rotation with versioning and grace period`.

---

## Execution Summary

| Phase | Spec | Tasks | Files | Risk | Parallel? |
|-------|------|-------|-------|------|-----------|
| 1 | Combine Broadcast Events | 0–7 | ~25 | High (breaking model change) | No — must complete first |
| 2 | Username Subject Migration | 8–16 | ~20 | Medium (mechanical refactor) | After Phase 1 |
| 3 | Room Key Rotation | 17–22 | ~3 | Low (isolated package) | Parallel with Phase 1 or 2 |

### Commit Strategy

Each phase produces one commit upon successful validation. Within a phase, intermediate commits are encouraged after each task passes tests, to provide safe rollback points.

### Risk Mitigations

1. **Phase 1 Task 0 (Subscription restructure):** This is the riskiest step — it touches every service. Execute it first and validate compilation across the entire repo before proceeding. Run `make lint && make test` after each service migration.

2. **Phase 2 (Username migration):** Mechanical but wide-reaching. The key correctness check is that `Message.UserID` is populated from `sub.User.ID` (not from the subject-extracted username). Every handler test must assert this.

3. **Phase 3 (Key rotation):** Lowest risk — contained within a single package. The `rotatePipeline` must be atomic (single Redis pipeline). Integration tests with real Valkey verify this.
