# Create Room Feature

**Date:** 2026-04-09
**Status:** Approved

## Problem

The existing `handleCreateRoom` in room-service creates a bare room with only an owner subscription. It doesn't support adding members (individuals, orgs, or channels) during room creation. The `CreateRoomRequest` model has a flat `Members []string` field that doesn't match the richer member specification pattern established by `AddMembersRequest`.

## Decision

Replace the existing create-room flow with one that supports the same member specification as add-members (users, orgs, channels). Room-service handles validation, channel/org expansion, dedup, and room type derivation. Room-worker handles all DB writes and async event publishing. Room type is derived from the payload rather than specified explicitly.

## Design

### 1. Model Changes

**Replace `CreateRoomRequest`** in `pkg/model/room.go`:

```go
type CreateRoomRequest struct {
    Name     string   `json:"name,omitempty" bson:"name,omitempty"`
    Users    []string `json:"users"          bson:"users"`
    Orgs     []string `json:"orgs"           bson:"orgs"`
    Channels []string `json:"channels"       bson:"channels"`
}
```

Remove the old `CreateRoomRequest` (with `Type`, `CreatedBy`, `CreatedByAccount`, `SiteID`, `Members` fields) and `ListRoomsResponse`.

**Add `Name` and `Type` to `Subscription`** in `pkg/model/subscription.go`:

```go
type Subscription struct {
    ID                 string           `json:"id" bson:"_id"`
    User               SubscriptionUser `json:"u" bson:"u"`
    RoomID             string           `json:"roomId" bson:"roomId"`
    SiteID             string           `json:"siteId" bson:"siteId"`
    Roles              []Role           `json:"roles" bson:"roles"`
    Name               string           `json:"name" bson:"name"`
    Type               RoomType         `json:"type" bson:"t"`
    HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
    JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
    LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
    HasMention         bool             `json:"hasMention" bson:"hasMention"`
}
```

**Change `Room.Type` bson tag** from `"type"` to `"t"`:

```go
Type RoomType `json:"type" bson:"t"`
```

### 2. Room Type Derivation

Room type is never sent in the payload — it is derived by room-service based on the **raw payload** (before expansion):

| Condition | Type |
|-----------|------|
| `Name` provided in payload | `group` |
| `Name` absent + exactly 1 entry in `Users` + `Orgs` and `Channels` both empty | `dm` |
| Everything else (orgs present, channels present, or multiple users) | `group` |

DM is only possible when the raw payload has a single user and nothing else. Orgs or channels always produce a group, even if they resolve to a single user.

### 3. Room Name Derivation

When `Name` is absent from the payload:

| Type | Name Source |
|------|-------------|
| DM | Target user's `User.Name` (becomes room name; subscriptions swap — see DM section) |
| Group | Concatenate individual `User.Name`s and org IDs (e.g., `"Alice, Bob, engineering-team"`), truncated to 100 characters |

For channels referenced in the payload, use the channel's `Room.Name` when building the auto-generated name.

### 4. DM-Specific Behavior

**Constraints:**
- Strictly `Users: ["one-person"]` with no orgs and no channels
- No `room_members` docs (individuals only — no orgs involved)
- No `Name` in payload (presence of name forces group type)

**Subscription naming:** Each user's subscription `Name` is the **other** user's display name:
- Requester's subscription `Name` = target user's `User.Name`
- Target user's subscription `Name` = requester's `User.Name`

**Duplicate prevention:** Room-service queries for an existing DM between the two users. If one exists, return an error (`"DM already exists between these users"`). New store method: `FindDMBetween(ctx, account1, account2) (*Room, error)`.

### 5. Room-Service Flow (Sync, No DB Writes)

Subject: `chat.user.{account}.request.rooms.create`

`handleCreateRoom` logic:

1. Unmarshal `CreateRoomRequest`
2. Extract requester account from NATS subject
3. **Expand channels** — for each channel ID, fetch `room_members` (orgs + individuals) and `subscriptions`; also fetch the channel's `Room.Name` for name generation
4. **Resolve orgs** — convert org IDs to individual accounts via `GetOrgAccounts`
5. **Merge & deduplicate** all usernames from `Users` + channels + orgs, filter bots
6. **Remove requester** from the resolved user list (they are added separately as owner)
7. **Determine room type** using the derivation rules in Section 2 (evaluated on the raw payload, before expansion)
8. **DM duplicate check** — if type is DM, call `FindDMBetween`. If found, return error
9. **Look up requester's `User` record** — needed for display name (DM subscription naming and `CreatedBy`)
10. **Look up each individual user's `User` record** — needed for display names in auto-generated room names and DM subscription naming
11. **Derive room name** if absent, using the rules in Section 3
12. **Capacity check** — `1 (owner) + len(resolved users) <= maxRoomSize`
13. **Generate room ID** — `uuid.New().String()`
14. **Publish** a normalized internal message to the ROOMS JetStream stream containing: room ID, room name, room type, requester info (ID, account, name), resolved user list, org IDs, siteID, has-orgs flag
15. **Reply** with `{ "id": roomID, "name": name, "type": type }` to the client

**New store methods on `RoomStore`:**
- `FindDMBetween(ctx context.Context, account1, account2 string) (*model.Room, error)` — queries for a room with `Type == "dm"` where both accounts have subscriptions
- `GetUser(ctx context.Context, account string) (*model.User, error)` — looks up a user by account for display name resolution

### 6. Room-Worker Flow

New `processCreateRoom` handler triggered by a `room.create` operation on the ROOMS stream.

**Sync (before ack):**

1. Unmarshal the normalized create-room message
2. **Create `Room` document** — ID, Name, Type (`bson:"t"`), CreatedBy, SiteID, UserCount (1 + resolved member count), CreatedAt, UpdatedAt
3. **Create owner subscription** — requester with `Roles: [owner]`, `Type` = room type, `Name` = room name (group) or target user's name (DM), `HistorySharedSince` = nil (new room, full history)
4. **Bulk-create remaining member subscriptions** — each with `Roles: [member]`, `Type` = room type, `Name` = room name (group) or requester's name (DM), `HistorySharedSince` = nil
5. **Ack** the JetStream message

**Async (after ack):**

1. **Write `room_members` docs** — only if orgs are involved. One doc per org (`RoomMemberTypeOrg`), one per individual account (`RoomMemberTypeIndividual`) including the requester
2. **Publish two system messages** to the MESSAGES_CANONICAL stream (persisted to Cassandra via message-worker):
   - **`room_created`** message — `type: "room_created"`, `msg: "A new room has been created"`, `sender` = requester
   - **`members_added`** message — `type: "members_added"`, `msg: "{creator name} added members to the channel"`, `sender` = requester, `sys_msg_data` = JSON blob containing the `MembersAdded` struct (see Section 7)
3. **Publish `SubscriptionUpdateEvent`** (action: `"added"`) per member account — notifies connected clients of the new subscription
4. **Publish `MemberChangeEvent`** (type: `"member-added"`) to `chat.room.{roomID}.event.member` — notifies room subscribers of new members
5. **Cross-site federation** — if any member's `SiteID != local siteID`, wrap `MemberChangeEvent` in an `OutboxEvent` (type: `"member_added"`) and publish to OUTBOX stream

**New store methods on `SubscriptionStore`:**
- `CreateRoom(ctx context.Context, room *model.Room) error` — inserts a room document

### 7. Internal Message Format and System Message Data

**CreateRoomMessage** — published from room-service to the ROOMS stream:

```go
type CreateRoomMessage struct {
    RoomID         string   `json:"roomId"`
    Name           string   `json:"name"`
    Type           RoomType `json:"type"`
    CreatorID      string   `json:"creatorId"`
    CreatorAccount string   `json:"creatorAccount"`
    CreatorName    string   `json:"creatorName"`
    SiteID         string   `json:"siteId"`
    Users          []string `json:"users"`        // resolved, deduped account list (excludes requester)
    Orgs           []string `json:"orgs"`          // org IDs (for room_members docs)
    Channels       []string `json:"channels"`      // original channel room IDs from payload
    RawIndividuals []string `json:"rawIndividuals"` // original individual accounts from payload (before channel/org expansion)
    HasOrgs        bool     `json:"hasOrgs"`       // controls whether room_members are written
}
```

`Channels`, `RawIndividuals`, and `Orgs` carry the original payload breakdown so the room-worker can construct the `MembersAdded` data for the system message. `Users` is the fully resolved/deduped list used for subscription creation.

**MembersAdded** — stored as JSON in the `sys_msg_data` BLOB column on `messages_by_room`:

```go
type MembersAdded struct {
    Individuals    []string `json:"individuals"`    // original individual accounts from payload
    Orgs           []string `json:"orgs"`           // org IDs from payload
    Channels       []string `json:"channels"`       // channel room IDs from payload
    AddedUsersCount int     `json:"addedUsersCount"` // total resolved user count (excluding requester)
}
```

Both structs are internal — `CreateRoomMessage` is used only on the ROOMS stream, `MembersAdded` is persisted in Cassandra but not a client-facing API model.

### 8. Data Flow Diagram

```
Client
  |
  v
room-service (sync, no DB writes)
  |- unmarshal CreateRoomRequest
  |- expand channels -> orgs + individuals + channel names
  |- resolve orgs -> individual accounts
  |- merge, dedup, filter bots
  |- remove requester from member list
  |- determine type: Name present -> group; absent + 1 user -> dm; absent + many -> group
  |- DM duplicate check (FindDMBetween)
  |- derive room name if absent
  |- generate room ID (uuid)
  |- look up requester User record
  +- publish CreateRoomMessage to ROOMS stream, reply { id, name, type }
          |
          v
     room-worker (processCreateRoom)
      |- sync: create Room doc
      |        create owner subscription
      |        bulk-create member subscriptions
      |        ack
      +- async:
         |- write room_members docs (only if orgs involved)
         |- publish SubscriptionUpdateEvent per member
         |- publish MemberChangeEvent to room
         +- if cross-site: OutboxEvent -> OUTBOX
```

### 9. Files Modified

| File | Change |
|------|--------|
| `pkg/model/room.go` | Replace `CreateRoomRequest`, remove `ListRoomsResponse`, change `Room.Type` bson tag to `"t"` |
| `pkg/model/subscription.go` | Add `Name` and `Type` fields to `Subscription` |
| `pkg/model/event.go` | Add `CreateRoomMessage` and `MembersAdded` internal structs |
| `pkg/model/model_test.go` | Add round-trip tests for new/changed models |
| `room-service/handler.go` | Rewrite `handleCreateRoom` with expansion/dedup/type-derivation logic |
| `room-service/handler_test.go` | Tests for all create-room scenarios |
| `room-service/store.go` | Add `FindDMBetween` to `RoomStore` interface |
| `room-service/store_mongo.go` | Implement `FindDMBetween` |
| `room-worker/handler.go` | Add `processCreateRoom` |
| `room-worker/handler_test.go` | Tests for create-room worker processing |
| `room-worker/store.go` | Add `CreateRoom` to `SubscriptionStore` interface |
| `room-worker/store_mongo.go` | Implement `CreateRoom` |
| `pkg/subject/subject.go` | Add subject builder for `room.create` stream operation if needed |

### 10. Testing Strategy

**Room-service unit tests (table-driven):**
- Group room creation with users only
- Group room creation with orgs + users (room_members flag)
- Group room creation with channels (expansion)
- DM creation (1 user, no name)
- DM duplicate detection (error returned)
- Name present forces group type regardless of member count
- Auto-generated name truncation at 100 chars
- Requester deduplication from member list
- Bot filtering
- Capacity check
- Invalid payloads (empty users, malformed JSON)

**Room-worker unit tests (table-driven):**
- Room + owner + member subscriptions created (sync)
- DM subscription naming (names swapped)
- Group subscription naming (room name)
- room_members written only when hasOrgs is true
- System messages published: `room_created` with correct text, `members_added` with correct text and `MembersAdded` data
- SubscriptionUpdateEvent published per member
- MemberChangeEvent published
- Cross-site outbox publishing
- Store errors for room creation, subscription creation

**Model tests:**
- Round-trip JSON/BSON for updated `CreateRoomRequest`, `Subscription`, `Room`, `CreateRoomMessage`, `MembersAdded`
