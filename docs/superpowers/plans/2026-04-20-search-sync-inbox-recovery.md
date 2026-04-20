# search-sync-worker INBOX Recovery & Payload Migration Plan

**Goal:** Recover the search-sync-worker spotlight + user-room collections from
PR #78's pre-force-push state (INBOX-based architecture) and adapt them to
main's new bulk member event format (`Accounts []string` + event-level
`HistorySharedSince`).

**Branch:** `claude/recover-search-sync-worker-dCjyV`

**Architecture:** Two collections consume the INBOX stream and maintain
Elasticsearch indexes for room typeahead (spotlight) and per-user message-
search access control (user-room). Cross-site federation flows via existing
`OUTBOX_{remote}` → `INBOX_{local}` stream sourcing with SubjectTransforms —
no new streams. Collections are payload-driven only; publishers (room-worker)
are explicitly out of scope for this PR.

**Tech Stack:** Go 1.25, NATS JetStream (v2.10+ for SubjectTransforms),
Elasticsearch (bulk API + external versioning + painless scripts).

---

## Context: why recovery

PR #78 was force-pushed on 2026-04-16 from `3026f46` → `61a3cf9`, then
rewritten further to a ROOMS-stream architecture (`0fc366a`). The
ROOMS-stream approach duplicates what the existing OUTBOX/INBOX federation
already does (Sources + SubjectTransforms) and creates a third parallel
federation stream just for search indexing.

The original INBOX-based design (commits `c906357` → `3026f46`) is the
correct architectural fit: one federation pipe per site, many local consumers
off INBOX. This plan recovers that work and adapts it to main's newer event
format.

## Scope

**In scope (this PR):**
- `pkg/model/event.go` — new `InboxMemberEvent` payload type
- `search-sync-worker/inbox_stream.go` — `parseMemberEvent` helper + cross-
  site bootstrap stream config
- `search-sync-worker/spotlight.go` + tests — per-(user, room) typeahead docs
- `search-sync-worker/user_room.go` + tests — per-user rooms array
- `search-sync-worker/inbox_integration_test.go` — testcontainers-go
  integration coverage
- `pkg/subject/subject.go` — `InboxMember*` builders (already landed in
  recovered commits)
- `pkg/stream/stream.go` — `Inbox(siteID)` with two subject patterns
  (already landed)

**Out of scope (follow-up PRs):**
- `room-worker` publisher migration — publishing `InboxMemberEvent` to local
  `chat.inbox.{site}.member_added/removed` for same-site members + to OUTBOX
  for cross-site. The integration tests hand-publish to INBOX to prove the
  collections work end-to-end without the publisher change.
- `inbox-worker` migration — owns INBOX stream creation with cross-site
  Sources in production (currently search-sync-worker's bootstrap does it
  behind `BOOTSTRAP_STREAMS=true`).

## Key design decisions

1. **Event-level `HistorySharedSince`.** The new `InboxMemberEvent` carries
   ONE `HistorySharedSince int64` for the whole bulk — all accounts in one
   event share the same restricted-or-not flag. When non-zero, the entire
   event is skipped (empty actions slice, handler acks without touching ES).
   The search service handles restricted rooms via DB+cache at query time.

2. **Synthesized spotlight DocID = `{account}_{roomID}`.** The new payload
   drops subscription IDs (only `Accounts []string`), so spotlight docs are
   keyed by the synthesized ID. Safe because the ID is ES-internal and has
   no external contract. User-room DocID is unchanged (`{account}`).

3. **Fan-out by account.** Each inbox event produces N bulk actions (one per
   account). Handler already supports this via `BuildAction` returning
   `[]BulkAction`.

4. **Single `InboxMemberEvent` type for add + remove.** The `OutboxEvent.Type`
   field discriminates; `HistorySharedSince` and `JoinedAt` are omitted on
   removes via `omitempty`. Keeps the payload shape consistent across the
   two event types.

5. **`int64` millis, not `*time.Time`.** `HistorySharedSince` is `int64`
   (zero = not restricted) matching main's existing `MemberAddEvent`. Avoids
   a nullable pointer field in JSON wire format.

---

## File structure

### Files modified in Phase 1 (committed as `023aadb`)

- `pkg/model/event.go` — replaced `MemberAddedPayload` struct with
  `InboxMemberEvent`. Fields: `RoomID, RoomName, RoomType, SiteID,
  Accounts, HistorySharedSince (omitempty), JoinedAt (omitempty), Timestamp`.
- `pkg/model/model_test.go` — replaced `TestMemberAddedPayloadJSON` with
  `TestInboxMemberEventJSON` (3 subtests: unrestricted add, restricted add,
  remove-with-zeros-omitted).
- `search-sync-worker/inbox_stream.go` — `parseMemberEvent` now returns
  `(*model.OutboxEvent, *model.InboxMemberEvent, error)`. Validation: envelope
  unmarshal, positive timestamp, payload unmarshal. Event-level HSS short-
  circuit is caller's responsibility.
- `search-sync-worker/spotlight.go` — `BuildAction` fans out by account with
  DocID `fmt.Sprintf("%s_%s", account, payload.RoomID)`; `SpotlightSearchIndex`
  now has `UserAccount, RoomID, RoomName, RoomType, SiteID, JoinedAt`
  (dropped `SubscriptionID`, `UserID`).
- `search-sync-worker/user_room.go` — `BuildAction` fans out by account; docID
  = account; event-level HSS short-circuit.
- `search-sync-worker/spotlight_test.go` + `user_room_test.go` — updated
  helpers (`baseInboxMemberEvent`, `makeInboxMemberEvent`) and all assertions.

### Files remaining in Phase 2 (not yet committed)

- `search-sync-worker/inbox_integration_test.go` — still uses old
  `MemberAddedPayload`. Needs full migration. See task list below.

### Files NOT modified (confirmed unchanged on this branch)

- `pkg/subject/subject.go` — `InboxMemberAdded`, `InboxMemberRemoved`,
  `InboxMemberAddedAggregate`, `InboxMemberRemovedAggregate`,
  `InboxMemberEventSubjects` already present.
- `pkg/stream/stream.go` — `Inbox(siteID)` already returns two subject
  patterns (`chat.inbox.{site}.*` + `chat.inbox.{site}.aggregate.>`).
- `search-sync-worker/main.go` — bootstrap flow + INBOX stream creation
  already in place. No changes needed for payload migration.
- `search-sync-worker/handler.go` + `handler_test.go` — handler is payload-
  agnostic; fan-out path already supported via `BuildAction` returning
  `[]BulkAction`.

---

## Task list

### Phase 1: payload + collections (DONE — commit `023aadb`)

- [x] Replace `MemberAddedPayload` with `InboxMemberEvent` in `pkg/model`
- [x] Update `parseMemberEvent` for new shape
- [x] Update `spotlightCollection.BuildAction` for `Accounts` fan-out
- [x] Update `userRoomCollection.BuildAction` for `Accounts` fan-out
- [x] Update unit tests + helpers for new shape
- [x] `make lint` clean + `make test` green
- [x] Commit + push

### Phase 2: integration tests (TODO)

- [ ] **Task 2.1: Update shared helpers in `inbox_integration_test.go`**

  Replace these functions/types:

  ```go
  // OLD
  type memberFixture struct {
      SubID              string
      Account            string
      Restricted         bool
      HistorySharedSince *time.Time
  }

  func buildMemberEventPayload(subID, account, roomID, roomName, siteID string,
      joinedAt time.Time, historyShared *time.Time) model.MemberAddedPayload

  func buildBulkMemberEventPayload(roomID, roomName, siteID string,
      joinedAt time.Time, members []memberFixture) model.MemberAddedPayload

  func publishMemberOutboxEvent(t *testing.T, ctx context.Context,
      js jetstream.JetStream, subj, eventType string,
      payload model.MemberAddedPayload, timestamp int64)
  ```

  With:

  ```go
  // NEW
  func buildInboxMemberEvent(roomID, roomName, siteID string,
      joinedAt int64, accounts []string, historySharedSince int64,
      timestamp int64) model.InboxMemberEvent {
      return model.InboxMemberEvent{
          RoomID:             roomID,
          RoomName:           roomName,
          RoomType:           model.RoomTypeGroup,
          SiteID:             siteID,
          Accounts:           accounts,
          HistorySharedSince: historySharedSince,
          JoinedAt:           joinedAt,
          Timestamp:          timestamp,
      }
  }

  func publishInboxMemberEvent(t *testing.T, ctx context.Context,
      js jetstream.JetStream, subj, eventType string,
      payload model.InboxMemberEvent) {
      t.Helper()
      payloadData, err := json.Marshal(payload)
      require.NoError(t, err)
      evt := model.OutboxEvent{
          Type:       eventType,
          SiteID:     payload.SiteID,
          DestSiteID: payload.SiteID,
          Payload:    payloadData,
          Timestamp:  payload.Timestamp,
      }
      data, err := json.Marshal(evt)
      require.NoError(t, err)
      _, err = js.Publish(ctx, subj, data)
      require.NoError(t, err, "publish to %s", subj)
  }
  ```

  Drop the `memberFixture` type entirely — bulk tests now just pass
  `[]string` for accounts. Drop per-fixture `Restricted` — restricted-room
  testing becomes a separate scenario (homogeneous event with non-zero
  HSS).

- [ ] **Task 2.2: Update `TestSpotlightSyncIntegration`**

  Same 4 published events (local alice-eng, local alice-platform, federated
  bob-eng, federated alice-platform remove). Changes:
  - `countDocs` assertion unchanged (2)
  - DocID lookups: `sub-alice-eng` → `alice_r-eng`, `sub-bob-eng` →
    `bob_r-eng`, `sub-alice-platform` → `alice_r-platform`
  - Drop `assert.Equal(t, "sub-alice-eng", doc["subscriptionId"])` and
    `assert.Equal(t, "u-alice", doc["userId"])` — those fields are gone.
  - Keep: `userAccount`, `roomId`, `roomName`, `roomType`, `siteId`
    assertions.

- [ ] **Task 2.3: Update `TestSpotlightSync_BulkInvite`**

  Bulk event of 3 users. Changes:
  - `buildBulkMemberEventPayload` → `buildInboxMemberEvent` with
    `accounts = []string{"dave", "erin", "frank"}`
  - DocIDs: `sub-dave-platform` → `dave_r-platform`, etc.
  - Drop `subscriptionId` assertion
  - `countDocs` still 3 before remove, 0 after.

- [ ] **Task 2.4: Update `TestUserRoomSyncIntegration`**

  Same 6 published events. Changes:
  - Replace `buildMemberEventPayload` calls throughout.
  - The restricted event: `historySharedSince = 1735689500000` (any
    non-zero value); the test proves the WHOLE event is skipped (alice not
    indexed for r-restricted, no timestamp entry).
  - `roomTimestamps` assertions unchanged (r1, r2, r3 timestamps, no
    r-restricted).

- [ ] **Task 2.5: Replace `TestUserRoomSync_BulkInvite`**

  Old test had mixed restricted/unrestricted within one bulk — impossible
  under event-level HSS. Replace with two scenarios:

  1. **Bulk unrestricted**: 3 accounts, HSS=0 → 3 user-room docs with
     correct `rooms` arrays.
  2. **Bulk restricted (all-or-nothing)**: 3 accounts, HSS=12345 → no user-
     room docs (whole event skipped).

- [ ] **Task 2.6: Update `TestUserRoomSync_LWWGuard`**

  Linear sequence unchanged (6 steps: initial add → stale add → stale
  remove → newer remove → re-add → stale add after re-add). Only change:
  the per-publish `buildMemberEventPayload` call → `buildInboxMemberEvent`
  with `accounts = []string{"charlie"}`.

- [ ] **Task 2.7: Run integration tests**

  Requires Docker for testcontainers-go.

  ```
  make test-integration SERVICE=search-sync-worker
  ```

  Expected: all 4 tests pass.

- [ ] **Task 2.8: Commit + push**

  ```
  git add search-sync-worker/inbox_integration_test.go
  git commit -m "test(search-sync-worker): migrate integration tests to InboxMemberEvent"
  git push
  ```

### Phase 3: PR (TODO)

- [ ] **Task 3.1: Open new PR**

  Base: `main` (e871010), head: `claude/recover-search-sync-worker-dCjyV`.
  Title: `feat(search-sync-worker): add spotlight + user-room sync via INBOX`

  PR description highlights:
  - Architecture: INBOX-based, reuses existing OUTBOX→INBOX federation
  - New `InboxMemberEvent` payload (account-list + event-level HSS)
  - Spotlight typeahead + user-room access-control collections
  - room-worker publisher migration is a separate, follow-up PR
  - Links to this plan doc

- [ ] **Task 3.2: Close PR #78**

  Comment: "Superseded by #{new} — architectural direction changed to
  INBOX-based per earlier discussion (ROOMS-stream approach duplicates
  existing OUTBOX/INBOX federation)."

---

## Follow-up (separate PR, separate plan)

**room-worker publisher migration** — add a second publish alongside the
existing `chat.room.{roomID}.event.member` publish:
- Same-site members → `chat.inbox.{site}.member_added/removed`
- Cross-site members → `outbox.{site}.to.{dest}.member_added/removed`
  (existing behavior, payload shape changes to `InboxMemberEvent`)

Coordinate with `inbox-worker` to own INBOX stream creation with
`Sources + SubjectTransforms` in production (currently
`inboxBootstrapStreamConfig` in search-sync-worker is behind a
`BOOTSTRAP_STREAMS` dev-only toggle).
