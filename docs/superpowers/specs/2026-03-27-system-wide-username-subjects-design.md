# System-Wide Username Subject Migration — Design Spec

**Date:** 2026-03-27
**Status:** Approved
**Scope:** pkg/subject, message-worker, history-service, room-service, room-worker, notification-worker, inbox-worker

## Overview

Migrate all NATS `chat.user.{userID}.*` subject patterns to use `{username}` instead of `{userID}`. This is a parameter rename at the subject builder level plus a query filter change (`u._id` → `u.username`) in services that resolve user identity from the subject. Stream configs and wildcard patterns are unaffected (token-agnostic).

## Motivation

The broadcast-worker already uses username-based subjects (`chat.user.{username}.event.room`) from the prior "Combine Broadcast Events" work. All other services still use userID-based subjects. This creates an inconsistency — some subjects carry username, others carry userID. Migrating system-wide unifies the convention: NATS subjects always use `username` as the user identifier.

## Subject Builder Changes (`pkg/subject/subject.go`)

All `chat.user.*` subject builder functions rename their `userID` parameter to `username`. The subject pattern strings are unchanged — only the Go parameter name changes.

| Function | Before | After |
|---|---|---|
| `MsgSend` | `MsgSend(userID, roomID, siteID)` | `MsgSend(username, roomID, siteID)` |
| `UserResponse` | `UserResponse(userID, requestID)` | `UserResponse(username, requestID)` |
| `UserRoomUpdate` | `UserRoomUpdate(userID)` | `UserRoomUpdate(username)` |
| `UserMsgStream` | `UserMsgStream(userID)` | `UserMsgStream(username)` |
| `MemberInvite` | `MemberInvite(userID, roomID, siteID)` | `MemberInvite(username, roomID, siteID)` |
| `MsgHistory` | `MsgHistory(userID, roomID, siteID)` | `MsgHistory(username, roomID, siteID)` |
| `SubscriptionUpdate` | `SubscriptionUpdate(userID)` | `SubscriptionUpdate(username)` |
| `RoomMetadataChanged` | `RoomMetadataChanged(userID)` | `RoomMetadataChanged(username)` |
| `Notification` | `Notification(userID)` | `Notification(username)` |
| `RoomsCreate` | `RoomsCreate(userID)` | `RoomsCreate(username)` |
| `RoomsList` | `RoomsList(userID)` | `RoomsList(username)` |
| `RoomsGet` | `RoomsGet(userID, roomID)` | `RoomsGet(username, roomID)` |

`UserRoomEvent(username)` — already uses `username`, no change needed.

### ParseUserRoomSubject

Return value renamed from `userID` to `username`:

```go
func ParseUserRoomSubject(subj string) (username, roomID string, ok bool)
```

### Stream Configs

No changes — wildcard patterns (`chat.user.*.room.*...`) are position-based and token-agnostic.

### Wildcard Patterns

No changes — `MsgSendWildcard`, `MemberInviteWildcard`, etc. use `*` which matches any token regardless of whether it's a userID or username.

## Service Changes

### message-worker

**Current flow:** Extract `userID` from subject position 2 → `GetSubscription(ctx, userID, roomID)` → create `Message{UserID: userID}`.

**New flow:** Extract `username` from subject position 2 → `GetSubscription(ctx, username, roomID)` → get `userID` from `sub.User.ID` → create `Message{UserID: sub.User.ID}`.

Store interface change:
```go
// Before
GetSubscription(ctx context.Context, userID, roomID string) (*model.Subscription, error)
// After
GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
```

MongoDB filter change: `bson.M{"u._id": userID, "roomId": roomID}` → `bson.M{"u.username": username, "roomId": roomID}`

Reply subject: `subject.UserResponse(username, reqID)` — now passes username.

### history-service

Same pattern as message-worker. Extract `username` from subject, query subscription by `u.username` for access validation. The history query itself uses `roomID` and timestamps — no userID dependency.

### room-service

`GetSubscription` changes to query by `u.username`. Handler passes username (from request subject) instead of userID.

### notification-worker

`subject.Notification(subs[i].User.ID)` → `subject.Notification(subs[i].User.Username)`. Already uses `sub.User.ID` for sender filtering (unchanged — that's a business logic comparison, not a subject).

### room-worker

`subject.RoomMetadataChanged(members[i].User.ID)` → `subject.RoomMetadataChanged(members[i].User.Username)`.

### inbox-worker

`subject.SubscriptionUpdate(...)` call changes to pass `sub.User.Username` instead of user ID. The `SubscriptionUpdateEvent.UserID` field remains as userID — it's a domain event field, not a subject routing field.

## Not Affected

- `broadcast-worker` — already uses username-based subjects
- Room-scoped subjects (`chat.room.{roomID}.*`) — no user identifier
- Fanout/Outbox subjects — use siteID/roomID, no user identifier
- Stream configs in `pkg/stream/stream.go` — wildcards are token-agnostic
- `Message.UserID` field — stays as userID (populated from `sub.User.ID`)
- `SubscriptionUpdateEvent.UserID` — stays as userID (domain event field)

## Testing

Mechanical migration — no new test cases needed.

- Subject test values change from `"u1"` to `"alice"` to clarify semantic intent
- Mock expectations update: `GetSubscription(gomock.Any(), "alice", "r1")`
- Subscription fixtures include `Username` where missing
- Integration tests verify `u.username` query filter works
- Verify `Message.UserID` is populated from `sub.User.ID`, not from the subject-extracted username
