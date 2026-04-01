# Broadcast Worker Model Updates

## Overview

Update the broadcast-worker to enrich room events with full message payloads and participant details (including employee name data from MongoDB), remove the redundant `Origin` field from Room (replaced by `SiteID`), and restructure `Mentions` from plain usernames to rich `Participant` objects.

## Requirements

1. **Remove `Room.Origin`** — `SiteID` already represents the originating site; `Origin` is redundant and always mirrors `SiteID`.
2. **Return full `Message` in `RoomEvent`** for both group and DM events (currently DM-only) so clients can render directly.
3. **Enrich message with `Sender`** — a `Participant` struct containing `userID` (optional), `username`, `chineseName`, `engName`, looked up from the `employee` MongoDB collection.
4. **Change `Mentions` to `[]Participant`** — each mentioned user is resolved to a `Participant` with `username`, `chineseName`, `engName` (no `userID` for mentions).
5. **Employee lookup fallback** — if the employee DB query fails or an individual employee is not found, fall back to populating `chineseName` and `engName` with the `username`.

## Model Changes

### Remove `Room.Origin`

```go
// pkg/model/room.go — remove this field:
// Origin string `json:"origin" bson:"origin"`
```

All references to `Origin` in `RoomEvent`, handler, and tests are replaced with `SiteID`.

### New `Participant` struct (`pkg/model`)

```go
type Participant struct {
    UserID      string `json:"userId,omitempty" bson:"userId,omitempty"`
    Username    string `json:"username" bson:"username"`
    ChineseName string `json:"chineseName" bson:"chineseName"`
    EngName     string `json:"engName" bson:"engName"`
}
```

- `UserID` is `omitempty` — present for the message sender (from `Message.UserID`), omitted for mentioned users.

### New `Employee` struct (`pkg/model`)

```go
type Employee struct {
    AccountName string `bson:"accountName"`
    Name        string `bson:"name"`     // Chinese name
    EngName     string `bson:"engName"`
}
```

- Internal struct for store layer only — no JSON tags needed.
- Only includes fields required for building `Participant`.

### New `ClientMessage` struct (`pkg/model`)

```go
type ClientMessage struct {
    Message `json:",inline" bson:",inline"`
    Sender  *Participant `json:"sender,omitempty"`
}
```

- Embeds existing `Message` and adds enriched sender info.
- Used exclusively in `RoomEvent` for client-facing payloads.

### Updated `RoomEvent` struct (`pkg/model`)

```go
type RoomEvent struct {
    Type      RoomEventType `json:"type"`
    RoomID    string        `json:"roomId"`
    Timestamp time.Time     `json:"timestamp"`

    RoomName  string   `json:"roomName"`
    RoomType  RoomType `json:"roomType"`
    SiteID    string   `json:"siteId"`          // was Origin
    UserCount int      `json:"userCount"`
    LastMsgAt time.Time `json:"lastMsgAt"`
    LastMsgID string   `json:"lastMsgId"`

    Mentions   []Participant `json:"mentions,omitempty"`    // was []string
    MentionAll bool          `json:"mentionAll,omitempty"`

    HasMention bool           `json:"hasMention,omitempty"`
    Message    *ClientMessage `json:"message,omitempty"`    // was *Message, now included for both group and DM
}
```

## Store Layer Changes

### New store method

```go
// broadcast-worker/store.go
type Store interface {
    GetRoom(ctx context.Context, roomID string) (*model.Room, error)
    ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
    UpdateRoomOnNewMessage(ctx context.Context, roomID string, msgID string, msgAt time.Time, mentionAll bool) error
    SetSubscriptionMentions(ctx context.Context, roomID string, usernames []string) error
    FindEmployeesByAccountNames(ctx context.Context, accountNames []string) ([]model.Employee, error)
}
```

### MongoDB implementation

- Collection: `employee`
- Query: `{"accountName": {"$in": accountNames}}`
- Projection: `accountName`, `name`, `engName` only
- Returns `[]model.Employee`

## Handler Logic Changes

### Updated `HandleMessage` flow

1. Unmarshal `MessageEvent` from JSON (unchanged)
2. Get Room from store (unchanged)
3. Detect `mentionAll` and extract mentioned usernames (unchanged)
4. Update room metadata + subscription mentions (unchanged)
5. **New:** Collect all usernames (sender username + mentioned usernames), deduplicate, call `FindEmployeesByAccountNames`
6. **New:** Build `map[string]model.Employee` lookup from results
7. **New:** Build `ClientMessage` — embed original `Message`, attach `Sender` as `Participant` with `UserID` from message, `Username`, and employee name data
8. **New:** Build `Mentions` as `[]Participant` from mentioned usernames + employee lookup (no `UserID`)
9. Route to `publishGroupEvent` or `publishDMEvents` (signatures updated)

### Fallback behavior

- If `FindEmployeesByAccountNames` returns a DB error: log warning, build all `Participant` entries with `Username` as both `ChineseName` and `EngName`.
- If an individual username has no matching employee record: that `Participant` gets `Username` as both `ChineseName` and `EngName`.
- Message delivery is never blocked by employee lookup failures.

### `publishGroupEvent` changes

- Receives `mentions []model.Participant` instead of `[]string`
- Attaches `*ClientMessage` (with sender) to the event
- Publishes to `subject.RoomEvent(room.ID)` (unchanged)

### `publishDMEvents` changes

- Uses `*ClientMessage` with `Sender` instead of raw `*Message`
- Per-subscriber `HasMention` logic stays the same — checks `Participant.Username`
- Publishes to `subject.UserRoomEvent(username)` (unchanged)

## Files Affected

| File | Change |
|------|--------|
| `pkg/model/room.go` | Remove `Origin` field |
| `pkg/model/event.go` | Add `Participant`, `Employee`, `ClientMessage`; update `RoomEvent` (`SiteID` replaces `Origin`, `Mentions` type, `Message` type) |
| `pkg/model/model_test.go` | Update round-trip tests for changed structs |
| `broadcast-worker/store.go` | Add `FindEmployeesByAccountNames` to interface |
| `broadcast-worker/store_mongo.go` | Implement `FindEmployeesByAccountNames` |
| `broadcast-worker/handler.go` | Employee lookup, build `ClientMessage`/`Participant`, update `buildRoomEvent`, update publish methods |
| `broadcast-worker/handler_test.go` | Update all tests for new types and employee lookup mock |
| `broadcast-worker/integration_test.go` | Update for removed `Origin`, add employee collection setup |
