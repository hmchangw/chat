# RoomType on Subscription Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `RoomType` field to `model.Subscription`, add the new `RoomTypeBotDM` and `RoomTypeDiscussion` constants, and populate `RoomType` at every subscription creation site and on the partial Subscription payloads carried by removed `SubscriptionUpdateEvent`s.

**Architecture:** Denormalise the room kind onto the subscription document so downstream consumers (frontend room-list categorization, notification rules, future per-type handling) can route on `Subscription.RoomType` without an extra room lookup. Each producer either reads the type from a fetched room or hardcodes `RoomTypeChannel` when only that type is reachable.

**Tech Stack:** Go 1.25, MongoDB driver v2 (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` (mockgen), `stretchr/testify` (assertions), NATS JetStream consumers via `pkg/idgen` and `pkg/subject`.

**Spec:** `docs/superpowers/specs/2026-04-23-roomtype-on-subscription-design.md`.

---

## Prerequisites

- Branch off `origin/main` after PR #118 ("Remove-member / role-update hardening") has merged. PR #118 already removed `RoomTypeGroup`, deleted `processInvite`, renamed `broadcast-worker`'s `publishGroupEvent` → `publishChannelEvent`, and updated `room-service`'s role-update guard. Do not redo any of that.
- All commands assume the repo root `/home/user/chat`. Use the `Makefile` targets — never raw `go` commands.
- Pre-commit hook runs lint + tests; commits fail if either fails.

## File Structure

| File | Responsibility | Touched in |
|---|---|---|
| `pkg/model/room.go` | Add new RoomType constants. | Task 1 |
| `pkg/model/subscription.go` | Add `RoomType` field on `Subscription`. | Task 1 |
| `pkg/model/model_test.go` | Assert new constants; round-trip the new field. | Task 1 |
| `room-service/handler.go` | Stamp `req.Type` onto the auto-created owner sub. | Task 2 |
| `room-service/handler_test.go` | Assert captured sub carries `RoomType`. | Task 2 |
| `room-worker/handler.go` | Hardcode `RoomTypeChannel` on `processAddMembers` subs; fetch room and stamp `RoomType` on the partial subs in `processRemoveIndividual` and `processRemoveOrg` event payloads. | Task 3 |
| `room-worker/handler_test.go` | Add `GetRoom` mocks + `RoomType` assertions on five existing tests. | Task 3 |
| `inbox-worker/handler.go` | Hardcode `RoomTypeChannel` on cross-site `member_added` sub. | Task 4 |
| `inbox-worker/handler_test.go` | Assert created sub has `RoomType: RoomTypeChannel`. | Task 4 |

No store interfaces change. `room-worker`'s `SubscriptionStore` already has `GetRoom`; no `make generate` needed.

## Tasks

1. **`pkg/model` foundation** — new constants + new `Subscription.RoomType` field + model tests. Backward-compatible: existing call sites keep compiling because the new field defaults to `""`.
2. **`room-service` CreateRoom** — owner subscription carries `req.Type`.
3. **`room-worker`** — three sub literals updated:
   - `processAddMembers`: hardcode `RoomTypeChannel`.
   - `processRemoveIndividual`: fetch the room, stamp `RoomType` on the partial sub literal carried by the "removed" `SubscriptionUpdateEvent`.
   - `processRemoveOrg`: same, with one room fetch reused across the per-account event loop.
4. **`inbox-worker`** — cross-site `handleMemberAdded` hardcodes `RoomTypeChannel` (only channel/discussion-style rooms ever produce cross-site `member_added` events, never DM/botDM).

---

### Task 1: `pkg/model` foundation

**Files:**
- Modify: `pkg/model/room.go` (the `RoomType` const block)
- Modify: `pkg/model/subscription.go` (the `Subscription` struct)
- Modify: `pkg/model/model_test.go` (`TestSubscriptionJSON`, `TestRoomTypeValues`)

This task is independently buildable: every existing call site keeps compiling because the new `Subscription.RoomType` field defaults to `""`.

- [ ] **Step 1.1: Update `TestSubscriptionJSON` to set the new field**

  Edit `pkg/model/model_test.go`. Find the `TestSubscriptionJSON` function and add `RoomType: model.RoomTypeChannel` between `RoomID` and `SiteID` so the round-trip exercises the new field:

  ```go
  func TestSubscriptionJSON(t *testing.T) {
      hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
      s := model.Subscription{
          ID:                 "s1",
          User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
          RoomID:             "r1",
          RoomType:           model.RoomTypeChannel,
          SiteID:             "site-a",
          Roles:              []model.Role{model.RoleOwner},
          HistorySharedSince: &hss,
          JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
          LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
          HasMention:         true,
      }
      // (rest of the test unchanged — it already round-trips s through JSON)
  }
  ```

- [ ] **Step 1.2: Update `TestRoomTypeValues` to assert the four constants**

  Replace the existing `TestRoomTypeValues` body with:

  ```go
  func TestRoomTypeValues(t *testing.T) {
      if model.RoomTypeChannel != "channel" {
          t.Errorf("RoomTypeChannel = %q", model.RoomTypeChannel)
      }
      if model.RoomTypeDM != "dm" {
          t.Errorf("RoomTypeDM = %q", model.RoomTypeDM)
      }
      if model.RoomTypeBotDM != "botDM" {
          t.Errorf("RoomTypeBotDM = %q", model.RoomTypeBotDM)
      }
      if model.RoomTypeDiscussion != "discussion" {
          t.Errorf("RoomTypeDiscussion = %q", model.RoomTypeDiscussion)
      }
  }
  ```

- [ ] **Step 1.3: Run the tests — expect a build failure**

  ```
  make test SERVICE=pkg/model
  ```

  Expected output (verbatim):
  ```
  pkg/model/model_test.go:387:3: unknown field RoomType in struct literal of type model.Subscription
  pkg/model/model_test.go:416:11: undefined: model.RoomTypeBotDM
  pkg/model/model_test.go:419:11: undefined: model.RoomTypeDiscussion
  FAIL	github.com/hmchangw/chat/pkg/model [build failed]
  ```

- [ ] **Step 1.4: Add the new constants in `pkg/model/room.go`**

  Replace the existing `const ( ... )` block with:

  ```go
  const (
      RoomTypeChannel    RoomType = "channel"
      RoomTypeDM         RoomType = "dm"
      RoomTypeBotDM      RoomType = "botDM"
      RoomTypeDiscussion RoomType = "discussion"
  )
  ```

- [ ] **Step 1.5: Add the `RoomType` field on `Subscription`**

  Edit `pkg/model/subscription.go`. Insert the new field between `RoomID` and `SiteID`:

  ```go
  type Subscription struct {
      ID                 string           `json:"id" bson:"_id"`
      User               SubscriptionUser `json:"u" bson:"u"`
      RoomID             string           `json:"roomId" bson:"roomId"`
      RoomType           RoomType         `json:"roomType" bson:"roomType"`
      SiteID             string           `json:"siteId" bson:"siteId"`
      Roles              []Role           `json:"roles" bson:"roles"`
      HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
      JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
      LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
      HasMention         bool             `json:"hasMention" bson:"hasMention"`
  }
  ```

- [ ] **Step 1.6: Run the tests — expect green**

  ```
  make test SERVICE=pkg/model
  ```

  Expected:
  ```
  ok  	github.com/hmchangw/chat/pkg/model	1.0xxs
  ok  	github.com/hmchangw/chat/pkg/model/cassandra	(cached)
  ```

- [ ] **Step 1.7: Lint clean**

  ```
  make lint
  ```

  Expected: `0 issues.`

- [ ] **Step 1.8: Commit**

  ```bash
  git add pkg/model/
  git commit -m "model: add RoomType on Subscription and botDM/discussion constants"
  ```

---

### Task 2: `room-service` CreateRoom owner subscription

**Files:**
- Modify: `room-service/handler.go` (the `handleCreateRoom` function — the `sub := model.Subscription{...}` literal)
- Modify: `room-service/handler_test.go` (`TestHandler_CreateRoom`)

Depends on Task 1 (`Subscription.RoomType` must exist).

- [ ] **Step 2.1: Add the RoomType assertion to `TestHandler_CreateRoom`**

  Edit `room-service/handler_test.go`. The test already captures the auto-created subscription via `var capturedSub *model.Subscription`. Append a `RoomType` assertion at the bottom of the test, just before the closing brace:

  ```go
  if capturedSub != nil && capturedSub.RoomType != model.RoomTypeChannel {
      t.Errorf("expected owner subscription RoomType=%q, got %q", model.RoomTypeChannel, capturedSub.RoomType)
  }
  ```

  The request fixture in this test already uses `Type: model.RoomTypeChannel`, so the assertion is internally consistent.

- [ ] **Step 2.2: Run the test — expect red**

  ```
  make test SERVICE=room-service
  ```

  Expected:
  ```
  --- FAIL: TestHandler_CreateRoom (0.00s)
      handler_test.go:51: expected owner subscription RoomType="channel", got ""
  ```

- [ ] **Step 2.3: Stamp `req.Type` onto the owner subscription**

  Edit `room-service/handler.go` inside `handleCreateRoom`. The block `sub := model.Subscription{...}` builds the auto-created owner sub. Add `RoomType: req.Type,` between `RoomID` and `SiteID`:

  ```go
  // Auto-create owner subscription
  sub := model.Subscription{
      ID:                 idgen.GenerateID(),
      User:               model.SubscriptionUser{ID: req.CreatedBy, Account: req.CreatedByAccount},
      RoomID:             room.ID,
      RoomType:           req.Type,
      SiteID:             req.SiteID,
      Roles:              []model.Role{model.RoleOwner},
      HistorySharedSince: &now,
      JoinedAt:           now,
  }
  ```

- [ ] **Step 2.4: Run the test — expect green**

  ```
  make test SERVICE=room-service
  ```

  Expected: `ok  	github.com/hmchangw/chat/room-service	1.0xxs`

- [ ] **Step 2.5: Lint clean**

  ```
  make lint
  ```

  Expected: `0 issues.`

- [ ] **Step 2.6: Commit**

  ```bash
  git add room-service/
  git commit -m "room-service: populate Subscription.RoomType on CreateRoom"
  ```

---

### Task 3: `room-worker` — add and remove paths

**Files:**
- Modify: `room-worker/handler.go`
  - `processAddMembers` — `sub := &model.Subscription{...}` literal inside the per-account loop
  - `processRemoveIndividual` — the partial `model.Subscription{...}` literal inside the `SubscriptionUpdateEvent`
  - `processRemoveOrg` — the partial `model.Subscription{...}` literal inside the per-account event loop
- Modify: `room-worker/handler_test.go`
  - `TestHandler_ProcessAddMembers`
  - `TestHandler_ProcessRemoveMember_SelfLeave_IndividualOnly`
  - `TestHandler_ProcessRemoveMember_OwnerRemovesIndividual`
  - `TestHandler_ProcessRemoveMember_OwnerRemovesOrg`
  - `TestHandler_ProcessRemoveMember_CrossSiteOutbox`
  - `TestHandler_ProcessRemoveIndividual_OutboxFailurePropagates`
  - `TestHandler_ProcessRemoveOrg_OutboxFailurePropagates`

Depends on Task 1. The room-worker `SubscriptionStore` interface already has `GetRoom`; no `make generate` needed.

**Design notes (from spec §4):**

The remove paths fetch the room once per operation and stamp `room.Type` onto the partial Subscription literal that the `SubscriptionUpdateEvent` payload carries. The fetch is **non-fatal** — on `GetRoom` error, log at warn and continue with `RoomType: ""`. The removal itself must not block on this lookup.

`processAddMembers` does NOT do a room-fetch — it hardcodes `RoomTypeChannel` because DM/botDM rooms reject `member.add` upstream.

- [ ] **Step 3.1: Update `TestHandler_ProcessAddMembers` — assert RoomType on every created sub**

  Inside the existing `BulkCreateSubscriptions` `DoAndReturn` callback, add a per-sub assertion:

  ```go
  store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).DoAndReturn(
      func(_ context.Context, subs []*model.Subscription) error {
          assert.Len(t, subs, 2)
          for _, s := range subs {
              assert.Equal(t, "site-a", s.SiteID)
              assert.Equal(t, model.RoomTypeChannel, s.RoomType)
              assert.Equal(t, []model.Role{model.RoleMember}, s.Roles)
              require.NotNil(t, s.HistorySharedSince)
              assert.Equal(t, s.JoinedAt, *s.HistorySharedSince)
          }
          return nil
      })
  ```

- [ ] **Step 3.2: Update `TestHandler_ProcessRemoveMember_SelfLeave_IndividualOnly` — add `GetRoom` mock and `RoomType` assertion**

  Add a new `EXPECT` for `GetRoom` after the existing `ReconcileUserCount` expect:

  ```go
  store.EXPECT().
      ReconcileUserCount(gomock.Any(), roomID).Return(nil)
  store.EXPECT().
      GetRoom(gomock.Any(), roomID).
      Return(&model.Room{ID: roomID, Type: model.RoomTypeChannel}, nil)
  ```

  Then after the `assert.True(t, subjSet[subject.MemberEvent(roomID)], ...)` line, add:

  ```go
  // The subscription update event should carry the RoomType from the fetched room.
  for _, p := range published {
      if p.subj != subject.SubscriptionUpdate(account) {
          continue
      }
      var evt model.SubscriptionUpdateEvent
      require.NoError(t, json.Unmarshal(p.data, &evt))
      assert.Equal(t, model.RoomTypeChannel, evt.Subscription.RoomType, "subscription update should carry RoomType")
  }
  ```

- [ ] **Step 3.3: Update `TestHandler_ProcessRemoveMember_OwnerRemovesIndividual` — add `GetRoom` mock**

  After the existing `ReconcileUserCount` expect, append:

  ```go
  store.EXPECT().
      GetRoom(gomock.Any(), roomID).
      Return(&model.Room{ID: roomID, Type: model.RoomTypeChannel}, nil)
  ```

  No additional assertion needed — the previous test already asserts the RoomType payload contract.

- [ ] **Step 3.4: Update `TestHandler_ProcessRemoveMember_OwnerRemovesOrg` — add `GetRoom` mock**

  After the existing `ReconcileUserCount(... )` expect, append:

  ```go
  store.EXPECT().
      GetRoom(gomock.Any(), roomID).
      Return(&model.Room{ID: roomID, Type: model.RoomTypeChannel}, nil)
  ```

- [ ] **Step 3.5: Update `TestHandler_ProcessRemoveMember_CrossSiteOutbox` — add `GetRoom` mock**

  After the existing `ReconcileUserCount(... )` expect, append:

  ```go
  store.EXPECT().
      GetRoom(gomock.Any(), roomID).
      Return(&model.Room{ID: roomID, Type: model.RoomTypeChannel}, nil)
  ```

- [ ] **Step 3.6: Update both `OutboxFailurePropagates` tests — add `GetRoom` mock**

  Both `TestHandler_ProcessRemoveIndividual_OutboxFailurePropagates` and `TestHandler_ProcessRemoveOrg_OutboxFailurePropagates` exercise the publish path, so they will also hit the new `GetRoom` call. Add this expect after `ReconcileUserCount` in each test:

  ```go
  store.EXPECT().
      GetRoom(gomock.Any(), roomID).
      Return(&model.Room{ID: roomID, Type: model.RoomTypeChannel}, nil)
  ```

- [ ] **Step 3.7: Run tests — expect red**

  ```
  make test SERVICE=room-worker
  ```

  Expected failures: `TestHandler_ProcessAddMembers` (`RoomType = ""`), and the four+two remove tests fail with `missing call(s) to *main.MockSubscriptionStore.GetRoom`.

- [ ] **Step 3.8: Hardcode `RoomTypeChannel` on `processAddMembers` subs**

  In `room-worker/handler.go` find the literal inside `processAddMembers`:

  ```go
  sub := &model.Subscription{
      ID:       idgen.GenerateID(),
      User:     model.SubscriptionUser{ID: user.ID, Account: user.Account},
      RoomID:   req.RoomID,
      RoomType: model.RoomTypeChannel,
      SiteID:   room.SiteID,
      Roles:    []model.Role{model.RoleMember},
      JoinedAt: acceptedAt,
  }
  ```

- [ ] **Step 3.9: Fetch room and stamp RoomType in `processRemoveIndividual`**

  In `room-worker/handler.go` find the block that begins with `now := time.Now().UTC()` followed by `// Subscription update event` (inside `processRemoveIndividual`). Insert a `GetRoom` call between them and use the fetched type when building the partial Subscription:

  ```go
  now := time.Now().UTC()

  // Fetch the room so the removed-event payload carries RoomType.
  // Non-fatal: on failure we proceed with an empty RoomType.
  var roomType model.RoomType
  if room, err := h.store.GetRoom(ctx, req.RoomID); err != nil {
      slog.Warn("get room failed during remove-individual", "error", err, "roomID", req.RoomID)
  } else if room != nil {
      roomType = room.Type
  }

  // Subscription update event
  subEvt := model.SubscriptionUpdateEvent{
      UserID: user.ID,
      Subscription: model.Subscription{
          RoomID:   req.RoomID,
          RoomType: roomType,
          User:     model.SubscriptionUser{ID: user.ID, Account: req.Account},
      },
      Action:    "removed",
      Timestamp: now.UnixMilli(),
  }
  ```

- [ ] **Step 3.10: Fetch room and stamp RoomType in `processRemoveOrg`**

  In `room-worker/handler.go` find the matching `now := time.Now().UTC()` followed by `// Publish per-account subscription update and collect cross-site accounts` block (inside `processRemoveOrg`). Make the same insertion — one `GetRoom` call before the loop, reuse `roomType` per iteration:

  ```go
  now := time.Now().UTC()

  // Fetch the room once so every per-account removed event carries RoomType.
  // Non-fatal: on failure we proceed with an empty RoomType.
  var roomType model.RoomType
  if room, err := h.store.GetRoom(ctx, req.RoomID); err != nil {
      slog.Warn("get room failed during remove-org", "error", err, "roomID", req.RoomID)
  } else if room != nil {
      roomType = room.Type
  }

  // Publish per-account subscription update and collect cross-site accounts
  sectName := ""
  for _, m := range toRemove {
      if m.SectName != "" {
          sectName = m.SectName
      }
      subEvt := model.SubscriptionUpdateEvent{
          Subscription: model.Subscription{
              RoomID:   req.RoomID,
              RoomType: roomType,
              User:     model.SubscriptionUser{Account: m.Account},
          },
          Action:    "removed",
          Timestamp: now.UnixMilli(),
      }
      // ...rest of the loop unchanged
  }
  ```

- [ ] **Step 3.11: Run tests — expect green**

  ```
  make test SERVICE=room-worker
  ```

  Expected: `ok  	github.com/hmchangw/chat/room-worker	1.0xxs`

- [ ] **Step 3.12: Lint clean**

  ```
  make lint
  ```

  Expected: `0 issues.`

- [ ] **Step 3.13: Commit**

  ```bash
  git add room-worker/
  git commit -m "room-worker: populate Subscription.RoomType on add and remove"
  ```
