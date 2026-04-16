# Add Member Design

**Date:** 2026-04-14
**Status:** Approved
**Extracted from:** `2026-04-10-room-member-management-v2-design.md` (add-member feature only)

## Summary

Adding members to a chat room supports three sources: **individual users** (by account), **orgs** (expanded via `users` collection), and **channels** (copy members from an existing room). Rooms with the `Restricted` flag set to `true` require the requester to be an **owner** to add members; unrestricted rooms allow any member to add. All add-member operations are processed at the **room's site**. When a user on site-US adds members to a room on site-EU, NATS gateways route the request to the EU cluster. EU's `room-service` validates and publishes to the EU `ROOMS` stream. EU's `room-worker` handles all DB writes and event publishing. For members whose `SiteID` differs from the room's site, outbox events replicate subscription changes to the member's home site.

## Scope

Covers the NATS request/reply endpoint for add-member operations; org expansion via `users` collection; channel-sourced member copying; bot filtering; capacity enforcement; subscription and room member persistence; `userCount` maintenance; system message publishing; per-user and room-scoped event fan-out; cross-site outbox publishing for subscription and room_members replication; and inbox-worker processing of remote `member_added` events.

Out of scope: room creation/deletion, member removal, role updates, message history access control, typing indicators, presence, and read receipts.

## NATS Subjects

### Request/Reply (Client → room-service)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| Add members | `chat.user.{account}.request.room.{roomID}.{siteID}.member.add` | `room-service` |

The `{siteID}` in the subject is the room's site. NATS gateways route cross-site requests to the correct cluster transparently.

### ROOMS Stream (room-service → room-worker)

Stream `ROOMS_{siteID}` captures validated member mutations. Room-service publishes to `chat.room.canonical.{siteID}.member.add` via `subject.RoomCanonical(siteID, "member.add")` — the request is already validated, so the canonical subject is appropriate. Stream subjects match `chat.room.canonical.{siteID}.>` via `subject.RoomCanonicalWildcard(siteID)`. Consumer `room-worker` is durable with explicit ack.

### Events (room-worker → clients/systems)

| Subject | Payload | Purpose |
|---------|---------|---------|
| `chat.user.{account}.event.subscription.update` | `SubscriptionUpdateEvent` | Notify individual user their room list changed |
| `chat.room.{roomID}.event.member` | `MemberAddEvent` | Notify all room subscribers of membership change |
| `chat.msg.canonical.{siteID}.created` | `MessageEvent` | System message (`members_added`) persisted via message-worker pipeline |
| `outbox.{room.SiteID}.to.{destSiteID}.member_added` | `OutboxEvent` (wraps `MemberAddEvent` in `Payload`) | Remote site creates subscriptions + room_members |

## Data Models

### Room

```go
type RoomType string

const (
    RoomTypeDM      RoomType = "dm"
    RoomTypeChannel RoomType = "channel"
)

type Room struct {
    // ... existing fields ...
    Restricted bool `json:"restricted,omitempty" bson:"restricted,omitempty"`
}
```

Two room types: `dm` (fixed participants, no member management) and `channel` (supports adding/removing members). Add-member is only allowed on `channel` rooms. New optional `Restricted` field: when `true`, only owners can add members; when `false` or absent, any room member can add. Defaults to `false` (zero value) — no migration needed for existing rooms.

### User

```go
type User struct {
    ID          string `json:"id"          bson:"_id"`
    Account     string `json:"account"     bson:"account"`
    SiteID      string `json:"siteId"      bson:"siteId"`
    SectID      string `json:"sectId"      bson:"sectId"`
    EngName     string `json:"engName"     bson:"engName"`
    ChineseName string `json:"chineseName" bson:"chineseName"`
    EmployeeID  string `json:"employeeId"  bson:"employeeId"`
}
```

One org per user. `SectID` is the org identifier. Org resolution: `users.find({sectId: orgId})`.

### Subscription

```go
type Subscription struct {
    ID                 string           `json:"id" bson:"_id"`
    User               SubscriptionUser `json:"u" bson:"u"`
    RoomID             string           `json:"roomId" bson:"roomId"`
    SiteID             string           `json:"siteId" bson:"siteId"`
    Roles              []Role           `json:"roles" bson:"roles"`
    HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
    JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
    LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
    HasMention         bool             `json:"hasMention" bson:"hasMention"`
}

type SubscriptionUser struct {
    ID      string `json:"id" bson:"_id"`
    Account string `json:"account" bson:"account"`
}
```

**`SiteID` is the room's origin site**, not the user's site. This means subscriptions for the same room always share the same `SiteID` regardless of where the user belongs. This is consistent across both the room's site and remote sites (inbox-worker creates subscriptions with the same `SiteID` from the event payload).

### RoomMember

```go
type RoomMember struct {
    ID     string          `json:"id"     bson:"_id"`
    RoomID string          `json:"rid"    bson:"rid"`
    Ts     time.Time       `json:"ts"     bson:"ts"`
    Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
    ID      string         `json:"id"                bson:"id"`
    Type    RoomMemberType `json:"type"              bson:"type"`    // RoomMemberIndividual or RoomMemberOrg
    Account string         `json:"account,omitempty" bson:"account,omitempty"`
}
```

`room_members` docs are written when the current request has orgs OR the room already has org-based room_members from a prior operation. This ensures individual members are always visible to channel expansion in rooms that use org membership. Unique index: `(rid, member.type, member.id, member.account)`.

### Request Payload

```go
type HistoryMode string

const (
    HistoryModeNone HistoryMode = "none"
    HistoryModeAll  HistoryMode = "all"
)

type HistoryConfig struct {
    Mode HistoryMode `json:"mode" bson:"mode"`
}

type AddMembersRequest struct {
    RoomID   string        `json:"roomId"   bson:"roomId"`
    Users    []string      `json:"users"    bson:"users"`
    Orgs     []string      `json:"orgs"     bson:"orgs"`
    Channels []string      `json:"channels" bson:"channels"`
    History  HistoryConfig `json:"history"  bson:"history"`
}
```

- `HistoryModeNone` (`"none"`) — new members can only see messages from their join time onwards. `HistorySharedSince` is set to the same timestamp as `JoinedAt`.
- `HistoryModeAll` (`"all"`) or absent — new members can see full room history. `HistorySharedSince` is omitted from the subscription document (`nil`).

### Event Payloads

```go
// MemberAddEvent is the domain event for add-member operations.
// Used for room-scoped notifications and cross-site outbox replication.
// Separate from MemberRemoveEvent (used by remove-member feature).
type MemberAddEvent struct {
    Type               string   `json:"type"               bson:"type"`               // "member_added"
    RoomID             string   `json:"roomId"             bson:"roomId"`
    Accounts           []string `json:"accounts"           bson:"accounts"`
    Orgs               []string `json:"orgs,omitempty"     bson:"orgs,omitempty"`
    SiteID             string   `json:"siteId"             bson:"siteId"`
    JoinedAt           int64    `json:"joinedAt"           bson:"joinedAt"`
    HistorySharedSince int64    `json:"historySharedSince" bson:"historySharedSince"`
    Timestamp          int64    `json:"timestamp"          bson:"timestamp"`
}

type SubscriptionUpdateEvent struct {
    UserID       string       `json:"userId"`
    Subscription Subscription `json:"subscription"`
    Action       string       `json:"action"` // "added"
    Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}

type OutboxEvent struct {
    Type       string `json:"type"`       // "member_added", "member_removed", "role_updated", "room_sync"
    SiteID     string `json:"siteId"`
    DestSiteID string `json:"destSiteId"`
    Payload    []byte `json:"payload"`    // JSON-encoded inner event (e.g., MemberAddEvent)
    Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}
```

`MemberAddEvent` carries `Accounts` (the new members), `Orgs` (org IDs if any orgs were added), `JoinedAt`, and `HistorySharedSince`. All set by room-worker at publish time. `JoinedAt` is always `now.UnixMilli()`. `HistorySharedSince` equals `JoinedAt` when history mode is `"none"`, or `0` when mode is `"all"`/absent (inbox-worker treats `0` as omit).

Inbox-worker on the remote site uses `Accounts` to look up users locally via `FindUsersByAccounts`, and uses `Orgs` to determine whether room_members docs need to be written. No `UserIDs` field — inbox-worker resolves user IDs from its own `users` collection.

### Message Struct Updates

```go
type Message struct {
    // ... existing fields ...
    Type       string `json:"type,omitempty"       bson:"type,omitempty"`
    SysMsgData []byte `json:"sysMsgData,omitempty" bson:"sysMsgData,omitempty"`
}
```

### System Message Data

```go
type MembersAdded struct {
    Individuals     []string `json:"individuals"`
    Orgs            []string `json:"orgs"`
    Channels        []string `json:"channels"`
    AddedUsersCount int      `json:"addedUsersCount"`
}
```

## Validation & Resolution (room-service)

### Step-by-step flow

1. **Parse subject** — Extract `requester` and `roomID` from NATS subject via `subject.ParseUserRoomSubject` (existing in `pkg/subject`)
2. **Verify requester** — `GetSubscription(requester, roomID)` confirms requester is in the room (returns subscription with roles)
3. **Authorization** — `GetRoom(roomID)` (existing in `room-service/store.go`). Two checks:
   - **Room type guard**: If `room.Type != RoomTypeChannel`, reject with error `"cannot add members to a DM room"`
   - **Restricted room guard**: If `room.Restricted == true` and requester does not have owner role (checked via `HasRole()` on the subscription from step 2), reject with error `"only owners can add members to this room"`
4. **Unmarshal** — Decode `AddMembersRequest` from payload
5. **Resolve channels** — For each channel ID in `req.Channels`:
   - Query `room_members` for the channel (`GetRoomMembersByRooms` — batch all channel IDs in one call, see [Store Methods](#new-store-methods-room-service))
   - **If the channel has room_members** (orgs or individuals): append org IDs to `req.Orgs`, append individual accounts to `req.Users`
   - **If the channel has NO room_members**: query `subscriptions` for that channel to get user accounts (`GetAccountsByRooms` — batch channels without room_members, see [Store Methods](#new-store-methods-room-service)), append to `req.Users`
6. **Resolve orgs + dedup + filter bots + exclude existing members** — Single aggregation pipeline via `ResolveAccounts(orgIDs, directAccounts, roomID)` (see [Store Methods](#new-store-methods-room-service)). Inputs: all org IDs (from `req.Orgs` + channel-sourced orgs), all direct accounts collected so far (`req.Users` + channel individuals + channel subscription accounts), and the target `roomID`. The pipeline resolves orgs, deduplicates, filters bots, and excludes accounts that already have a subscription in the room — all in one DB call. The result is the **net new members** to add.
7. **Capacity check** — `CountSubscriptions(roomID)` (existing) + net new member count must not exceed `MAX_ROOM_SIZE`
8. **Publish to ROOMS stream** — Publish normalized `AddMembersRequest` (with `Users` containing all resolved accounts, `Orgs` containing all org IDs, `Channels` cleared) to `chat.room.canonical.{siteID}.member.add` via `subject.RoomCanonical(siteID, "member.add")`
9. **Reply** — `{"status":"accepted"}`

### New store methods (room-service)

These methods reduce DB round-trips by batching queries. All live in `room-service/store.go` and `room-service/store_mongo.go`.

| Method | Signature | MongoDB Query | Purpose |
|--------|-----------|--------------|---------|
| `GetRoomMembersByRooms` | `(ctx, roomIDs []string) ([]model.RoomMember, error)` | `room_members.find({rid: {$in: roomIDs}})` | Batch-fetch room_members for all channel IDs in one call |
| `GetAccountsByRooms` | `(ctx, roomIDs []string) ([]string, error)` | Aggregation pipeline on `subscriptions` (see below) | Get distinct accounts from subscriptions for channels that have no room_members |
| `ResolveAccounts` | `(ctx, orgIDs []string, directAccounts []string, roomID string) ([]string, error)` | Aggregation pipeline on `users` with `$lookup` to `subscriptions` (see below) | Resolve orgs + dedup + filter bots + exclude existing members in one call |

**`GetAccountsByRooms` aggregation pipeline:**

```js
db.subscriptions.aggregate([
  { $match: { roomId: { $in: roomIDs } } },
  { $group: { _id: null, accounts: { $addToSet: "$u.account" } } }
])
```

Returns a flat, deduplicated list of accounts across all matched rooms in a single DB call.

**`ResolveAccounts` aggregation pipeline:**

Combines org resolution, deduplication, bot filtering, and existing member exclusion into a single DB call. Takes three inputs: `orgIDs` (org IDs to expand), `directAccounts` (accounts already collected from req.Users, channel individuals, and channel subscription accounts), and `roomID` (target room to check for existing subscriptions).

```js
db.users.aggregate([
  { $match: {
    $or: [
      { sectId: { $in: orgIDs } },
      { account: { $in: directAccounts } }
    ],
    account: { $not: { $regex: /\.bot$|^p_/ } }
  }},
  { $lookup: {
    from: "subscriptions",
    let: { userAccount: "$account" },
    pipeline: [
      { $match: { $expr: { $and: [
        { $eq: ["$roomId", roomID] },
        { $eq: ["$u.account", "$$userAccount"] }
      ]}}},
      { $limit: 1 }
    ],
    as: "existingSub"
  }},
  { $match: { existingSub: { $eq: [] } } },
  { $group: { _id: null, accounts: { $addToSet: "$account" } } }
])
```

- `$match` — resolves orgs (`sectId $in`) + direct accounts (`account $in`), filters bots (`$not $regex`)
- `$lookup` — left joins against `subscriptions` for the target `roomID`; uses the existing unique index on `(roomId, u.account)` so the lookup is fast; `$limit: 1` short-circuits after first match
- `$match existingSub: []` — keeps only users who are **not** already in the room
- `$group $addToSet` — deduplicates the final account list

Returns only **net new members** — the capacity check and room-worker both operate on this filtered list, so no duplicate subscriptions are created and the capacity count is accurate.

### Existing functions reused

| Function / Package | Location | Usage |
|-------------------|----------|-------|
| `subject.ParseUserRoomSubject` | `pkg/subject/subject.go` | Extract requester + roomID from NATS subject |
| `subject.RoomCanonical` | `pkg/subject/subject.go` | Build canonical publish subject for ROOMS stream |
| `subject.MemberAddWildcard` | `pkg/subject/subject.go` | Wildcard for room-service queue subscription on request/reply |
| `HasRole()` | `room-service/helper.go` | Check role membership (used for restricted room authorization) |
| `sanitizeError()` | `room-service/helper.go` | User-safe error messages |
| `GetSubscription` | `room-service/store.go` (existing) | Verify requester is in room, returns roles for authorization |
| `GetRoom` | `room-service/store.go` (existing) | Fetch room to check `Restricted` flag |
| `CountSubscriptions` | `room-service/store.go` (existing) | Capacity check (excludes bots) |

## Org Resolution

`AddMembersRequest.Orgs` contains org IDs that match `User.SectID`. Resolution is handled by `ResolveAccounts`, which combines org expansion, bot filtering, existing member exclusion, and dedup in a single aggregation pipeline. The `$or` clause matches users by `sectId` (for orgs) and by `account` (for direct accounts), `$match` excludes bots, `$lookup` against `subscriptions` filters out users already in the room, and `$addToSet` deduplicates — all in one DB call.

## Identity Model

Federation is determined by `SiteID` comparison. `Account` is the stable plain identifier used in NATS subjects and unique indexes. A user is federated when `user.SiteID != room.SiteID`.

## Cross-Site Request Routing

Each site runs its own NATS cluster with room-service and room-worker. NATS gateways connect all site clusters. The subject `chat.user.{account}.request.room.{roomID}.{room.siteID}.member.add` contains the room's siteID. Room-service subscribes to `chat.user.*.request.room.*.{localSiteID}.member.add`, so it only receives requests for rooms belonging to its site. Gateways route cross-site requests transparently.

## Processing Order

All operations are **fully synchronous before ack**. Any failure NAKs the message for JetStream retry. Unique indexes ensure idempotency on retries (duplicate key = no-op).

### Add Members (room-worker)

1. Look up each user via `FindUsersByAccounts` (existing in `pkg/userstore` and `room-worker/store.go`) — batch all accounts in one call, returns `UserID`, `SiteID`, and `SectID` for each
2. **Build subscriptions** — Capture `now = time.Now().UTC()` once. For each user, create `Subscription` with `SiteID = room.SiteID`, `Roles = [RoleMember]`, `JoinedAt = now`. If `req.History.Mode == HistoryModeNone`, set `HistorySharedSince = &now` (same timestamp as `JoinedAt`); if mode is `HistoryModeAll` or absent, omit `HistorySharedSince` (`nil`). Then `BulkCreateSubscriptions` (existing in `room-worker/store.go`)
3. Write `room_members` docs if current request has orgs OR room already has org-based room_members (`HasOrgRoomMembers(roomID)` — single doc lookup). Write individual docs for new members + org docs for new orgs. This ensures individuals added later are visible to channel expansion in rooms that use org membership.
4. Increment `userCount` via `IncrementUserCount` (existing in `room-worker/store.go`)
5. Publish `SubscriptionUpdateEvent` (action: `"added"`) per new member via `subject.SubscriptionUpdate` (existing in `pkg/subject`)
6. Publish `MemberAddEvent` (with `Accounts`, `Orgs`, `JoinedAt` = `now.UnixMilli()`, `HistorySharedSince` = `now.UnixMilli()` if mode is `"none"` or `0` if mode is `"all"`/absent) to `chat.room.{roomID}.event.member` via `subject.RoomMemberEvent` (existing in `pkg/subject`)
7. Publish system message (`members_added`) to MESSAGES_CANONICAL via `subject.MsgCanonicalCreated` (existing in `pkg/subject`)
8. **Outbox for cross-site members (batched by destination site)** — Group cross-site members by their `user.SiteID`. For each unique remote site, build a `MemberAddEvent` containing only that site's accounts plus all `Orgs` from the request, wrap in `OutboxEvent`, and publish to `outbox.{room.SiteID}.to.{destSiteID}.member_added` via `subject.Outbox`. This produces one outbox event per remote site. The `Orgs` field is included so inbox-worker can determine whether room_members docs need to be written on the remote site.
9. **Ack**

## Inbox-Worker (Remote Site Processing)

Inbox-worker processes inbound `member_added` events from remote sites. The incoming message is an `OutboxEvent` — unwrap `Payload` to get the `MemberAddEvent`.

| Event Type | Action |
|------------|--------|
| `member_added` | Create subscriptions + room_members in local MongoDB from `MemberAddEvent` payload |

### Step-by-step flow

1. **Unmarshal** `MemberAddEvent` from `OutboxEvent.Payload`
2. **Look up users locally** — `FindUsersByAccounts(event.Accounts)` to get `User.ID` for each account on this site. Build a `userMap[account]*User`.
3. **Create subscriptions** — For each account in `event.Accounts`:
   - Look up user from `userMap` (skip if not found)
   - Create `Subscription` with `User.ID` from local lookup, `SiteID = event.SiteID` (room's origin site), `Roles = [RoleMember]`, `JoinedAt` from event, `HistorySharedSince` from event (nil if 0)
   - `BulkCreateSubscriptions`
4. **Write room_members** — Determine if room_members docs are needed:
   - **If `event.Orgs` is non-empty**: the add-member operation included orgs. Write:
     - Org room_member docs for each org in `event.Orgs`
     - Individual room_member docs for each new account in `event.Accounts`
     - **Backfill**: query existing subscription accounts for this room (`GetSubscriptionAccounts(roomID)`) and write individual room_member docs for any that don't already have one. This covers members added before orgs existed in the room.
   - **If `event.Orgs` is empty but room already has org room_members** (`HasOrgRoomMembers(roomID)`): write individual room_member docs for new accounts only
   - **If no orgs anywhere**: skip room_members (just subscriptions)
5. **Publish `SubscriptionUpdateEvent`** (action: `"added"`) per new member to `subject.SubscriptionUpdate(account)`

### New inbox-worker store methods

| Method | Signature | Purpose |
|--------|-----------|---------|
| `FindUsersByAccounts` | `(ctx, accounts []string) ([]model.User, error)` | Batch user lookup for local user IDs |
| `BulkCreateSubscriptions` | `(ctx, subs []*model.Subscription) error` | Batch subscription creation |
| `CreateRoomMember` | `(ctx, member *model.RoomMember) error` | Write individual/org room_member docs |
| `HasOrgRoomMembers` | `(ctx, roomID string) (bool, error)` | Check if room has any org-based room_members |
| `GetSubscriptionAccounts` | `(ctx, roomID string) ([]string, error)` | Get all subscription accounts for backfill |

This ensures both **subscriptions** and **room_members** are consistent across sites. When orgs appear for the first time via a cross-site event, existing members are backfilled into room_members so channel expansion works identically on all sites.

## Message-Worker Changes

`SaveMessage` in message-worker must persist the `type` and `sys_msg_data` columns to Cassandra. The insert query for `messages_by_room` and `messages_by_id` is updated to include these fields. For regular messages, these fields are empty/nil and stored as null — no impact on normal message flow.

## Callers

- **Clients** — send NATS request/reply to room-service (routed via gateways to room's site)
- **room-service** — validates add-member requests, publishes to `ROOMS_{siteID}` stream
- **room-worker** — consumes from `ROOMS_{siteID}`, performs all DB writes and events synchronously before ack
- **inbox-worker** — processes `member_added` events from INBOX stream to replicate subscriptions and room_members on remote user's site
- **message-worker** — persists system messages (type + sys_msg_data) to Cassandra

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| All writes before ack, NAK on failure | Ensures complete consistency; JetStream retries with idempotent unique indexes make this safe |
| No async goroutines in room-worker | Simpler reasoning about failure modes; all work must succeed or all retries |
| Outbox from room site to user site | Room's site is the authority; it pushes changes to each remote member's site |
| Org resolution via users collection | Eliminates hr_data dependency; User.SectID is the single source of truth for org membership |
| System messages via MESSAGES_CANONICAL | Reuses existing message-worker + broadcast-worker pipeline; no new delivery mechanism |
| Bot filtering excluded from capacity | Bots are infrastructure participants, not human occupants; capacity limits should reflect real user load |
| Room type guard | Only two room types: `dm` and `channel`; add-member only allowed on `channel` rooms; DMs have fixed participants by definition — reject early before any resolution work |
| `Restricted` field on Room | Optional `bool`, defaults to `false` (zero value) — no migration needed; checked early in room-service validation before any resolution work; only owners can add members when `true`, any member can add when `false`/absent |
| Conditional channel resolution | Channels with room_members (orgs/individuals) merge into request-level orgs/users; channels without room_members fall back to subscription accounts — avoids redundant subscription queries when structured membership data exists |
| `ResolveAccounts` combined pipeline | Single aggregation on `users` with `$lookup` to `subscriptions` handles org expansion, bot filtering, existing member exclusion, and dedup — replaces four separate operations with one DB call; `$lookup` leverages the unique index on `(roomId, u.account)` |
| Batch room_members fetch | `GetRoomMembersByRooms` uses `{rid: {$in: roomIDs}}` to fetch all channel room_members in one call instead of per-channel queries |
| Batch subscription accounts | `GetAccountsByRooms` uses aggregation pipeline to get distinct accounts across multiple rooms in one call instead of per-channel queries |
| Two-subject design | Client request/reply uses `member.invite` pattern (`chat.user.{account}.request.room.{roomID}.{siteID}.member.add`); room-service publishes validated payload to canonical stream (`chat.room.canonical.{siteID}.member.add`) — request subject carries routing context, canonical subject signals validation is done |
| `OutboxEvent` wrapper | Outbox events wrap inner payloads (e.g., `MemberAddEvent`) in a typed envelope with `Type`, `SiteID`, `DestSiteID`, and `Timestamp` — lets inbox-worker route by type without parsing the inner payload first |
| Outbox batched by destination site | Group cross-site members by `user.SiteID`, publish one `OutboxEvent` per remote site instead of one per member. `MemberAddEvent` payload carries account strings + org IDs (lightweight). Reduces outbox event count from K (cross-site members) to S (unique remote sites) |
| `MemberAddEvent` separate from `MemberRemoveEvent` | Add-member and remove-member have different payload needs — add carries `Orgs`, `JoinedAt`, `HistorySharedSince`; remove carries `OrgID` for single-org removal. Separate structs avoid overloaded fields |
| Outbox type `"member_added"` | Consistent with `"member_removed"` (remove-member feature). Both describe the domain operation, not the implementation detail |
| No `UserIDs` in event | Inbox-worker resolves user IDs locally via `FindUsersByAccounts` — avoids cross-site user ID assumptions and keeps the event payload minimal |
| Outbox carries `Orgs` | Inbox-worker needs org IDs to determine whether room_members docs should be written on the remote site. Without this, remote sites would miss room_members for org-based rooms |
| Room_members backfill on first org | When orgs appear for the first time via outbox, inbox-worker backfills existing subscription holders into room_members. This ensures channel expansion works identically on all sites |
| `Subscription.SiteID` = room's site | Subscriptions always reference the room's origin site, not the user's site — consistent across both local and remote sites |
| `RoomMemberIndividual`/`RoomMemberOrg` constants | Aligned with remove-member PR (#79) naming convention — shorter, no "Type" suffix |
