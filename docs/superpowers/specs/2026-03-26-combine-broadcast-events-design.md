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

### User Model (`pkg/model/user.go`)

Add one field:

| Field | Type | BSON | Purpose |
|---|---|---|---|
| `Username` | `string` | `username` | Unique account name within a company, used for `@mentions` |

### Subscription Model (`pkg/model/subscription.go`)

Restructure the `UserID` and add `Username` into a nested `User` field (`bson:"u"`), matching the MongoDB document structure where user info is stored as a subdocument. Add `LastSeenAt` and `HasMention` fields.

| Field | Type | BSON | Purpose |
|---|---|---|---|
| `User` | `SubscriptionUser` | `u` | Nested user object containing `_id` and `username` |
| `LastSeenAt` | `time.Time` | `lastSeenAt` | When user last read the room (set by "mark as read" API, not in scope) |
| `HasMention` | `bool` | `hasMention` | Whether user has an unread individual mention (set by broadcast-worker, cleared by "mark as read" API) |

Full structs:

```go
type SubscriptionUser struct {
    ID       string `json:"id" bson:"_id"`
    Account string `json:"account" bson:"account"`
}

type Subscription struct {
    ID                 string           `json:"id" bson:"_id"`
    User               SubscriptionUser `json:"u" bson:"u"`
    RoomID             string           `json:"roomId" bson:"roomId"`
    SiteID             string           `json:"siteId" bson:"siteId"`
    Role               Role             `json:"role" bson:"role"`
    LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
    HasMention         bool             `json:"hasMention" bson:"hasMention"`
    SharedHistorySince time.Time        `json:"sharedHistorySince" bson:"sharedHistorySince"`
    JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
}
```

**Breaking change:** The flat `UserID` field (`bson:"userId"`) is replaced by `User.ID` (`bson:"u._id"`). All services that query subscriptions by user ID must change their MongoDB filter from `"userId"` to `"u._id"`, and all Go code referencing `sub.UserID` must change to `sub.User.ID`. Similarly, `sub.User.Account` replaces what would have been `sub.Username`.

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
    Mentions   []string `json:"mentions,omitempty"` // mentioned accounts (not userIDs)
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
| `Mentions` | list of mentioned accounts | omitted |
| `MentionAll` | `true` if `@all`/`@here` detected (case-insensitive) | omitted |
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
| `UserRoomEvent(account)` | `chat.user.{account}.event.room` | DM per-user events (fan-out-on-write) |

### Retired Subjects

| Function | Subject Pattern | Replacement |
|---|---|---|
| `RoomMsgStream(roomID)` | `chat.room.{roomID}.stream.msg` | `RoomEvent(roomID)` |
| `UserMsgStream(userID)` | `chat.user.{account}.stream.msg` | `UserRoomEvent(userID)` |
| `RoomMetadataUpdate(roomID)` | `chat.room.{roomID}.event.metadata.update` | `RoomEvent(roomID)` |

## Broadcast-Worker Changes

### Store Interface

```go
type Store interface {
    GetRoom(ctx context.Context, roomID string) (*model.Room, error)
    ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
    UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
    SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
}
```

- `UpdateRoomOnNewMessage`: single MongoDB call. Always sets `lastMsgAt`, `lastMsgId`, `updatedAt`. Conditionally sets `lastMentionAllAt` only when `mentionAll` is `true`.
- `SetSubscriptionMentions`: batch update setting `hasMention = true` for matching subscriptions by `roomID` and `u.account in [...]`. Uses the nested `u.username` field on subscriptions.

### Handler Flow — Group Room

1. Unmarshal `MessageEvent` from FANOUT
2. `GetRoom(roomID)`
3. Detect mentions: exact token match, case-insensitive — `@all`/`@here` for mentionAll, `@username` tokens for individual mentions
4. `UpdateRoomOnNewMessage(roomID, msg.ID, msg.CreatedAt, mentionAll)`
5. If individual mentions found: `SetSubscriptionMentions(roomID, mentionedAccounts)`
6. Build `RoomEvent` — no `Message` payload, `Mentions` contains accounts, `MentionAll` set
7. Publish **one event** to `chat.room.{roomID}.event`

### Handler Flow — DM Room

1. Unmarshal `MessageEvent` from FANOUT
2. `GetRoom(roomID)`
3. `ListSubscriptions(roomID)` — always 2 users (subscriptions include `username` field)
4. Detect mentions: exact token match, case-insensitive, scan `msg.Content` for `@username` of each participant
5. `UpdateRoomOnNewMessage(roomID, msg.ID, msg.CreatedAt, false)` (no `@All` in DMs)
6. If individual mentions found: `SetSubscriptionMentions(roomID, mentionedAccounts)`
7. For each subscription:
   a. Build `RoomEvent` — includes full `Message`, personalized `HasMention`
   b. Publish to `chat.user.{subscription.User.Account}.event.room`
8. Always **2 publishes** (both participants including sender)

### Mention Detection

All mention matching uses **exact token matching** (whitespace-delimited) and is **case-insensitive**.

Two separate functions:

```go
// detectMentionAll checks for @all or @here as exact tokens (case-insensitive).
func detectMentionAll(content string) bool {
    for _, token := range strings.Fields(content) {
        lower := strings.ToLower(token)
        if lower == "@all" || lower == "@here" {
            return true
        }
    }
    return false
}

// extractMentionedAccounts extracts @-prefixed tokens from content as accounts.
// Returns lowercased, deduplicated accounts. Excludes "all" and "here" keywords.
func extractMentionedAccounts(content string) []string {
    seen := make(map[string]struct{})
    var accounts []string
    for _, token := range strings.Fields(content) {
        if !strings.HasPrefix(token, "@") || len(token) == 1 {
            continue
        }
        name := strings.ToLower(token[1:])
        if name == "all" || name == "here" {
            continue
        }
        if _, exists := seen[name]; exists {
            continue
        }
        seen[name] = struct{}{}
        accounts = append(usernames, name)
    }
    return accounts
}
```

**Key design decisions:**
- Case-insensitive: `@Alice`, `@alice`, `@ALICE` all resolve to `"alice"`
- Exact token match: `@alice` in `"email@alice"` does NOT match (not whitespace-delimited)
- `@all`/`@here` are excluded from individual mentions — handled separately by `detectMentionAll`
- Returned accounts are lowercased for consistent matching against `subscription.username`

**Group rooms:** Extract `@username` tokens from content, pass them directly to `SetSubscriptionMentions`. The MongoDB update filter (`roomID + account in [...]`) naturally ignores non-member accounts — no membership verification needed.

**DM rooms:** Extract `@username` tokens from content, match against the 2 subscription accounts already loaded for fan-out.

### Mention State for Offline/Reconnect

| Mention Type | Real-Time (Online Client) | Offline/Reconnect |
|---|---|---|
| Individual `@username` | Client checks `mentions` array on `RoomEvent` (contains accounts) | `subscription.hasMention == true` |
| `@all` / `@here` | Client checks `mentionAll` field on `RoomEvent` | `room.lastMentionAllAt > subscription.lastSeenAt` |

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
- **DM per-user subjects** (`chat.user.{account}.event.room`): only subscribable by the specific user. `RoomEvent` for DMs includes the full `Message` payload — safe because the subject is per-user scoped.

## Testing Strategy

### Unit Tests (`broadcast-worker/handler_test.go`)

Table-driven tests:

**Group room `new_message`:**
- Happy path, no mentions: 1 publish to room event subject, `UpdateRoomOnNewMessage` called with `mentionAll=false`, no `SetSubscriptionMentions` call
- Individual `@username` mentions: 1 publish with `Mentions` populated (lowercased accounts), `SetSubscriptionMentions` called with correct accounts
- `@All` mention (case-insensitive): 1 publish with `MentionAll=true`, `UpdateRoomOnNewMessage` called with `mentionAll=true`
- Both `@All` and individual mentions: room updated with `mentionAll=true`, subscriptions also updated
- Invalid JSON: error returned
- Room not found: error returned
- Store errors (room update fails, subscription update fails): error returned

**DM room `new_message`:**
- Happy path, no mentions: 2 publishes to user event subjects, each with full message, `HasMention=false`
- Mentioning the other user: recipient's event has `HasMention=true`, sender's has `HasMention=false`
- Room metadata fields correctly populated on both events

**Mention detection:**
- `@username` as exact token (case-insensitive)
- Multiple mentions in one message
- `@All` / `@all` / `@ALL` keyword (case-insensitive)
- `@here` / `@Here` keyword (case-insensitive)
- No mentions
- `@` followed by non-member account (no-op in MongoDB)
- `@all` and `@here` excluded from individual mentions list

### Integration Tests (`broadcast-worker/integration_test.go`)

MongoDB testcontainer:
- `UpdateRoomOnNewMessage` persists `lastMsgAt`, `lastMsgID` correctly
- `UpdateRoomOnNewMessage` with `mentionAll=true` sets `lastMentionAllAt`
- `UpdateRoomOnNewMessage` with `mentionAll=false` does not modify `lastMentionAllAt`
- `SetSubscriptionMentions` sets `hasMention=true` on correct subscription docs (matched by account)
- `SetSubscriptionMentions` with non-member accounts is a safe no-op

### Message-Worker Test Updates

- Remove tests verifying room `updatedAt` update
- Verify message-worker no longer calls room update methods

## Out of Scope

- "Mark as read" API (clears `hasMention`, updates `lastSeenAt`)
- Membership events (`member_added`, `member_removed`) through FANOUT — room-worker continues its current behavior
- Push notifications / notification-worker changes
- Cross-site federation changes for the new event format
- Client-side implementation
- System-wide migration of all NATS subjects from `userID` to `username` (separate session)
