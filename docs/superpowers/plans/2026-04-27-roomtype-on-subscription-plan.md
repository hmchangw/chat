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
