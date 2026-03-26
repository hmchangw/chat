# Combine Broadcast Events — Design Spec

**Date:** 2026-03-26
**Status:** Approved
**Scope:** broadcast-worker, message-worker, pkg/model, pkg/subject

## Overview

Replace the current multi-event broadcast system with a unified `RoomEvent` type. Group rooms use fan-out-on-read (one event to room subject); DM rooms use fan-out-on-write (two events to per-user subjects). Broadcast-worker becomes the single authority for room document updates and mention detection on new messages.

## Motivation

The current broadcast-worker publishes two separate events per message (`RoomMetadataUpdateEvent` + `MessageEvent`) on different subjects. Clients must subscribe to multiple subjects and correlate events, creating race conditions and complexity. Additionally, there is no mention detection or per-user mention state.

## Data Model Changes

### Room Model (`pkg/model/room.go`)

Add four fields:

| Field | Type | BSON | Purpose |
|---|---|---|---|
| `Origin` | `string` | `origin` | Origin site ID (federation) |
| `LastMsgAt` | `time.Time` | `lastMsgAt` | Timestamp of last message (sidebar sorting) |
| `LastMsgID` | `string` | `lastMsgId` | ID of last message |
| `LastMentionAllAt` | `time.Time` | `lastMentionAllAt` | Timestamp of last `@All`/`@here` message |

Full struct:

```go
type Room struct {
    ID               string    `json:"id" bson:"_id"`
    Name             string    `json:"name" bson:"name"`
    Type             RoomType  `json:"type" bson:"type"`
    CreatedBy        string    `json:"createdBy" bson:"createdBy"`
    SiteID           string    `json:"siteId" bson:"siteId"`
    Origin           string    `json:"origin" bson:"origin"`
    UserCount        int       `json:"userCount" bson:"userCount"`
    LastMsgAt        time.Time `json:"lastMsgAt" bson:"lastMsgAt"`
    LastMsgID        string    `json:"lastMsgId" bson:"lastMsgId"`
    LastMentionAllAt time.Time `json:"lastMentionAllAt" bson:"lastMentionAllAt"`
    CreatedAt        time.Time `json:"createdAt" bson:"createdAt"`
    UpdatedAt        time.Time `json:"updatedAt" bson:"updatedAt"`
}
```

### Subscription Model (`pkg/model/subscription.go`)

Add two fields:

| Field | Type | BSON | Purpose |
|---|---|---|---|
| `LastSeenAt` | `time.Time` | `lastSeenAt` | When user last read the room (set by "mark as read" API, not in scope) |
| `HasMention` | `bool` | `hasMention` | Whether user has an unread individual mention (set by broadcast-worker, cleared by "mark as read" API) |

Full struct:

```go
type Subscription struct {
    ID                 string    `json:"id" bson:"_id"`
    UserID             string    `json:"userId" bson:"userId"`
    RoomID             string    `json:"roomId" bson:"roomId"`
    SiteID             string    `json:"siteId" bson:"siteId"`
    Role               Role      `json:"role" bson:"role"`
    LastSeenAt         time.Time `json:"lastSeenAt" bson:"lastSeenAt"`
    HasMention         bool      `json:"hasMention" bson:"hasMention"`
    SharedHistorySince time.Time `json:"sharedHistorySince" bson:"sharedHistorySince"`
    JoinedAt           time.Time `json:"joinedAt" bson:"joinedAt"`
}
```

## Event Model

### RoomEvent (`pkg/model/event.go`)

New unified event type replacing `RoomMetadataUpdateEvent`:

```go
type RoomEventType string

const (
    RoomEventNewMessage RoomEventType = "new_message"
    // Future: "member_added", "member_removed", "room_renamed", etc.
)

type RoomEvent struct {
    // Envelope
    Type      RoomEventType `json:"type"`
    RoomID    string        `json:"roomId"`
    Timestamp time.Time     `json:"timestamp"`

    // Room metadata snapshot
    RoomName  string   `json:"roomName"`
    RoomType  RoomType `json:"roomType"`
    Origin    string   `json:"origin"`
    UserCount int      `json:"userCount"`
    LastMsgAt time.Time `json:"lastMsgAt"`
    LastMsgID string   `json:"lastMsgId"`

    // Mention metadata (new_message only)
    Mentions   []string `json:"mentions,omitempty"`
    MentionAll bool     `json:"mentionAll,omitempty"`

    // Per-user context (DM only, personalized during fan-out)
    HasMention bool `json:"hasMention,omitempty"`

    // Type-specific payload
    Message *Message `json:"message,omitempty"` // full message for DM, nil for group
}
```

### Field Usage by Room Type

| Field | Group Room | DM Room |
|---|---|---|
| `Type` | `"new_message"` | `"new_message"` |
| `Mentions` | list of mentioned userIDs | omitted |
| `MentionAll` | `true` if `@All`/`@here` detected | omitted |
| `HasMention` | omitted (client self-detects from `Mentions`) | personalized per recipient |
| `Message` | `nil` (room subject is publicly subscribable) | full message (per-user subject is secure) |

### Retired Event Type

- `RoomMetadataUpdateEvent` — replaced entirely by `RoomEvent`

### MessageEvent (Unchanged)

`MessageEvent` published to FANOUT by message-worker remains unchanged. It already carries the full `Message` struct, which broadcast-worker uses directly for DM fan-out and for extracting `msg.ID`/`msg.CreatedAt` for group room updates.

```go
type MessageEvent struct {
    Message Message `json:"message"`
    RoomID  string  `json:"roomId"`
    SiteID  string  `json:"siteId"`
}
```

## Subject Changes

### New Subjects (`pkg/subject`)

| Function | Subject Pattern | Purpose |
|---|---|---|
| `RoomEvent(roomID)` | `chat.room.{roomID}.event` | Group room events (fan-out-on-read) |
| `UserRoomEvent(userID)` | `chat.user.{userID}.event.room` | DM per-user events (fan-out-on-write) |

### Retired Subjects

| Function | Subject Pattern | Replacement |
|---|---|---|
| `RoomMsgStream(roomID)` | `chat.room.{roomID}.stream.msg` | `RoomEvent(roomID)` |
| `UserMsgStream(userID)` | `chat.user.{userID}.stream.msg` | `UserRoomEvent(userID)` |
| `RoomMetadataUpdate(roomID)` | `chat.room.{roomID}.event.metadata.update` | `RoomEvent(roomID)` |

## Broadcast-Worker Changes

### Store Interface

```go
type Store interface {
    GetRoom(ctx context.Context, roomID string) (*model.Room, error)
    ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
    UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
    SetSubscriptionMentions(ctx context.Context, roomID string, userIDs []string) error
}
```

- `UpdateRoomOnNewMessage`: single MongoDB call. Always sets `lastMsgAt`, `lastMsgId`, `updatedAt`. Conditionally sets `lastMentionAllAt` only when `mentionAll` is `true`.
- `SetSubscriptionMentions`: batch update setting `hasMention = true` for matching subscriptions by `roomID` and `userID in [...]`.

### Handler Flow — Group Room

1. Unmarshal `MessageEvent` from FANOUT
2. `GetRoom(roomID)`
3. Detect mentions: scan `msg.Content` for `@userID` patterns and `@All`/`@here` keywords
4. `UpdateRoomOnNewMessage(roomID, msg.ID, msg.CreatedAt, mentionAll)`
5. If individual mentions found: `SetSubscriptionMentions(roomID, mentionedUserIDs)`
6. Build `RoomEvent` — no `Message` payload, includes `Mentions` and `MentionAll`
7. Publish **one event** to `chat.room.{roomID}.event`

### Handler Flow — DM Room

1. Unmarshal `MessageEvent` from FANOUT
2. `GetRoom(roomID)`
3. `ListSubscriptions(roomID)` — always 2 users
4. Detect mentions: scan `msg.Content` for `@userID` of each participant
5. `UpdateRoomOnNewMessage(roomID, msg.ID, msg.CreatedAt, false)` (no `@All` in DMs)
6. If individual mentions found: `SetSubscriptionMentions(roomID, mentionedUserIDs)`
7. For each subscription:
   a. Build `RoomEvent` — includes full `Message`, personalized `HasMention`
   b. Publish to `chat.user.{userID}.event.room`
8. Always **2 publishes** (both participants including sender)

### Mention Detection

Two separate functions handle `@All`/`@here` detection and individual `@userID` extraction:

```go
// detectMentionAll checks for @All or @here keywords
func detectMentionAll(content string) bool {
    return strings.Contains(content, "@All") || strings.Contains(content, "@here")
}

// extractMentionedUserIDs extracts @-prefixed tokens from content.
// For group rooms, these are used directly as mentionedUserIDs (no subscription lookup).
// For DM rooms, pass the 2 subscription userIDs and match against them.
func extractMentionedUserIDs(content string, candidates []string) []string {
    var mentioned []string
    for _, uid := range candidates {
        if strings.Contains(content, "@"+uid) {
            mentioned = append(mentioned, uid)
        }
    }
    return mentioned
}
```

**Group rooms:** Do not call `ListSubscriptions` (avoids loading all members for large rooms). Instead, extract all `@`-prefixed tokens from the message content using simple string parsing, and pass them directly as `mentionedUserIDs` to `SetSubscriptionMentions`. The MongoDB update filter (`roomID + userID in [...]`) naturally ignores non-member user IDs — no membership verification needed.

**DM rooms:** `candidates` comes from the 2 subscriptions already loaded for fan-out. Match `@userID` against these 2 user IDs.

### Mention State for Offline/Reconnect

| Mention Type | Real-Time (Online Client) | Offline/Reconnect |
|---|---|---|
| Individual `@userID` | Client checks `mentions` array on `RoomEvent` | `subscription.hasMention == true` |
| `@All` / `@here` | Client checks `mentionAll` field on `RoomEvent` | `room.lastMentionAllAt > subscription.lastSeenAt` |

## Message-Worker Changes

Remove the room `updatedAt` update from message-worker. After this change, message-worker's flow is:

1. Extract `userID`, `roomID`, `siteID` from subject
2. Validate subscription exists (MongoDB `subscriptions` collection)
3. Save message to Cassandra
4. Publish `MessageEvent` to FANOUT
5. Reply to sender

Message-worker no longer writes to the `rooms` MongoDB collection. Its store interface loses the room update method.

## Security Considerations

- **Group room subjects** (`chat.room.{roomID}.event`): subscribable by any authenticated user via NATS wildcard permissions. `RoomEvent` for group rooms does **not** include the `Message` payload — only the message ID. Clients fetch message content via NATS request/reply (which enforces authorization).
- **DM per-user subjects** (`chat.user.{userID}.event.room`): only subscribable by the specific user. `RoomEvent` for DMs includes the full `Message` payload — safe because the subject is per-user scoped.

## Testing Strategy

### Unit Tests (`broadcast-worker/handler_test.go`)

Table-driven tests:

**Group room `new_message`:**
- Happy path, no mentions: 1 publish to room event subject, `UpdateRoomOnNewMessage` called with `mentionAll=false`, no `SetSubscriptionMentions` call
- Individual `@userID` mentions: 1 publish with `Mentions` populated, `SetSubscriptionMentions` called with correct userIDs
- `@All` mention: 1 publish with `MentionAll=true`, `UpdateRoomOnNewMessage` called with `mentionAll=true`
- Both `@All` and individual mentions: room updated with `mentionAll=true`, subscriptions also updated
- Invalid JSON: error returned
- Room not found: error returned
- Store errors (room update fails, subscription update fails): error returned

**DM room `new_message`:**
- Happy path, no mentions: 2 publishes to user event subjects, each with full message, `HasMention=false`
- Mentioning the other user: recipient's event has `HasMention=true`, sender's has `HasMention=false`
- Room metadata fields correctly populated on both events

**Mention detection:**
- `@userID` in middle of text
- Multiple mentions in one message
- `@All` keyword
- `@here` keyword
- No mentions
- `@` followed by non-member ID (no-op)

### Integration Tests (`broadcast-worker/integration_test.go`)

MongoDB testcontainer:
- `UpdateRoomOnNewMessage` persists `lastMsgAt`, `lastMsgID` correctly
- `UpdateRoomOnNewMessage` with `mentionAll=true` sets `lastMentionAllAt`
- `UpdateRoomOnNewMessage` with `mentionAll=false` does not modify `lastMentionAllAt`
- `SetSubscriptionMentions` sets `hasMention=true` on correct subscription docs
- `SetSubscriptionMentions` with non-member userIDs is a safe no-op

### Message-Worker Test Updates

- Remove tests verifying room `updatedAt` update
- Verify message-worker no longer calls room update methods

## Out of Scope

- "Mark as read" API (clears `hasMention`, updates `lastSeenAt`)
- Membership events (`member_added`, `member_removed`) through FANOUT — room-worker continues its current behavior
- Push notifications / notification-worker changes
- Cross-site federation changes for the new event format
- Client-side implementation
