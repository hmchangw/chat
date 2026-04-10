# Room Member Management Design (v2)

**Date:** 2026-04-10
**Status:** Approved
**Supersedes:** `2026-04-07-room-member-management-design.md`

## Summary

Room member management covers three operations: **adding members** (individuals, orgs, or channel-sourced), **removing members** (self-leave, owner-removes-other, org removal), and **updating member roles** (promote/demote between owner and member).

All member operations are processed at the **room's site**. When a user on site-US adds members to a room on site-EU, NATS gateways route the request to the EU cluster. EU's `room-service` validates and publishes to the EU `ROOMS` stream. EU's `room-worker` handles all DB writes and event publishing. For members whose `SiteID` differs from the room's site, outbox events replicate subscription changes to the member's home site.

## Changes from v1

| Change | Detail |
|--------|--------|
| `hr_data` removed | Org resolution uses `users.find({sectId: orgId})` instead |
| `User.SectID` added | Each user belongs to exactly one org |
| Member invite removed | `InviteMemberRequest`, `processInvite`, `NatsHandleInvite` all deleted |
| Cross-site routing via NATS gateways | Room-service only processes requests for its own site's rooms; gateways handle cross-site delivery |
| All writes before ack | Room-worker completes all DB writes + event publishing synchronously before ack. NAK on any failure for full retry |
| Cross-site owner promotion allowed | Federation guard removed; any member can be promoted to owner |
| Outbox direction flipped | `outbox.{room.SiteID}.to.{user.SiteID}.{eventType}` — room's site pushes to each remote user's site |
| System messages added | `members_added` and `member_removed` messages published to MESSAGES_CANONICAL |
| `RemoveMemberRequest.Username` omitempty | Either `username` or `orgId`, never both |
| Message struct updated | `Type` and `SysMsgData` fields added for system messages |
| Inbox-worker handles `role_updated` | New outbox event type for cross-site role changes |

## Scope

Covers NATS request/reply endpoints for add, remove, and role-update operations; org expansion via `users` collection; channel-sourced member copying; bot filtering; capacity enforcement; authorization guards; subscription and room member persistence; `userCount` maintenance; system message publishing; per-user and room-scoped event fan-out; cross-site outbox publishing for subscription replication; and inbox-worker processing of remote membership events.

Out of scope: room creation/deletion, message history access control, typing indicators, presence, and read receipts.

## NATS Subjects

### Request/Reply (Client → room-service)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| Add members | `chat.user.{account}.request.room.{roomID}.{siteID}.member.add` | `room-service` |
| Remove member | `chat.user.{account}.request.room.{roomID}.{siteID}.member.remove` | `room-service` |
| Update role | `chat.user.{account}.request.room.{roomID}.{siteID}.member.role-update` | `room-service` |

The `{siteID}` in the subject is the room's site. NATS gateways route cross-site requests to the correct cluster transparently.

### ROOMS Stream (room-service → room-worker)

Stream `ROOMS_{siteID}` captures all validated member mutations. Subjects match `chat.user.*.request.room.*.{siteID}.member.>`. Consumer `room-worker` is durable with explicit ack. The published subject equals the original request subject.

### Events (room-worker → clients/systems)

| Subject | Payload | Purpose |
|---------|---------|---------|
| `chat.user.{account}.event.subscription.update` | `SubscriptionUpdateEvent` | Notify individual user their room list changed |
| `chat.room.{roomID}.event.member` | `MemberChangeEvent` | Notify all room subscribers of membership change |
| `chat.msg.canonical.{siteID}.created` | `MessageEvent` | System message (members_added / member_removed) persisted via message-worker pipeline |
| `outbox.{room.SiteID}.to.{user.SiteID}.member_added` | `MemberChangeEvent` | Remote site creates subscription |
| `outbox.{room.SiteID}.to.{user.SiteID}.member_removed` | `MemberChangeEvent` | Remote site deletes subscription |
| `outbox.{room.SiteID}.to.{user.SiteID}.role_updated` | `SubscriptionUpdateEvent` | Remote site updates subscription role |

## Data Models

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

### RoomMember

```go
type RoomMember struct {
    ID     string          `json:"id"     bson:"_id"`
    RoomID string          `json:"rid"    bson:"rid"`
    Ts     time.Time       `json:"ts"     bson:"ts"`
    Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
    ID       string         `json:"id"                 bson:"id"`
    Type     RoomMemberType `json:"type"               bson:"type"`     // "individual" or "org"
    Username string         `json:"username,omitempty" bson:"username,omitempty"`
}
```

`room_members` docs are written only when orgs are involved. Unique index: `(rid, member.type, member.id, member.username)`.

### Request Payloads

```go
type AddMembersRequest struct {
    RoomID   string        `json:"roomId"   bson:"roomId"`
    Users    []string      `json:"users"    bson:"users"`
    Orgs     []string      `json:"orgs"     bson:"orgs"`
    Channels []string      `json:"channels" bson:"channels"`
    History  HistoryConfig `json:"history"  bson:"history"`
}

type RemoveMemberRequest struct {
    RoomID   string `json:"roomId"            bson:"roomId"`
    Username string `json:"username,omitempty" bson:"username,omitempty"`
    OrgID    string `json:"orgId,omitempty"    bson:"orgId,omitempty"`
}

type UpdateRoleRequest struct {
    RoomID   string `json:"roomId"   bson:"roomId"`
    Username string `json:"username" bson:"username"`
    NewRole  Role   `json:"newRole"  bson:"newRole"`
}
```

### Event Payloads

```go
type MemberChangeEvent struct {
    Type     string   `json:"type"     bson:"type"`     // "member-added" or "member-removed"
    RoomID   string   `json:"roomId"   bson:"roomId"`
    Accounts []string `json:"accounts" bson:"accounts"`
    SiteID   string   `json:"siteId"   bson:"siteId"`
}

type SubscriptionUpdateEvent struct {
    UserID       string       `json:"userId"`
    Subscription Subscription `json:"subscription"`
    Action       string       `json:"action"` // "added" | "removed" | "role_updated"
    Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

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

type MembersRemoved struct {
    Username          string `json:"username,omitempty"`
    OrgID             string `json:"orgId,omitempty"`
    RemovedUsersCount int    `json:"removedUsersCount"`
}
```

## Org Resolution

`AddMembersRequest.Orgs` contains org IDs that match `User.SectID`. Resolution: `users.find({sectId: orgId})` returns all users in that org. Extract `Account` from each document. Replaces the old `hr_data` collection lookup.

## Identity Model

Federation is determined by `SiteID` comparison. `Account` is the stable plain identifier used in NATS subjects and unique indexes. A user is federated when `user.SiteID != room.SiteID`. Cross-site users **can** be promoted to owner.

## Cross-Site Request Routing

Each site runs its own NATS cluster with room-service and room-worker. NATS gateways connect all site clusters. The subject `chat.user.{account}.request.room.{roomID}.{room.siteID}.member.add` contains the room's siteID. Room-service subscribes to `chat.user.*.request.room.*.{localSiteID}.member.>`, so it only receives requests for rooms belonging to its site. Gateways route cross-site requests transparently.

## Processing Order

All operations are **fully synchronous before ack**. Any failure NAKs the message for JetStream retry. Unique indexes ensure idempotency on retries (duplicate key = no-op).

### Add Members (room-worker)

1. Look up each user via `GetUser` (need SiteID, sectId)
2. `BulkCreateSubscriptions` — all member subscriptions
3. Write `room_members` docs (only if orgs involved)
4. Increment `userCount`
5. Publish `SubscriptionUpdateEvent` (action: `"added"`) per member
6. Publish `MemberChangeEvent` to `chat.room.{roomID}.event.member`
7. Publish system message (`members_added`) to MESSAGES_CANONICAL
8. For each member where `user.SiteID != room.SiteID`: publish outbox to `outbox.{room.SiteID}.to.{user.SiteID}.member_added`
9. **Ack**

### Remove Member (room-worker)

1. Delete subscription(s) — single user or all org members via `users.find({sectId: orgId})`
2. Delete `room_members` doc(s)
3. Decrement `userCount`
4. Publish `SubscriptionUpdateEvent` (action: `"removed"`) per removed account
5. Publish `MemberChangeEvent` to `chat.room.{roomID}.event.member`
6. Publish system message (`member_removed`) to MESSAGES_CANONICAL
7. For each removed member where `user.SiteID != room.SiteID`: publish outbox to `outbox.{room.SiteID}.to.{user.SiteID}.member_removed`
8. **Ack**

### Role Update (room-worker)

1. `UpdateSubscriptionRole`
2. Publish `SubscriptionUpdateEvent` (action: `"role_updated"`)
3. If user's `SiteID != room.SiteID`: publish outbox to `outbox.{room.SiteID}.to.{user.SiteID}.role_updated`
4. **Ack**

## Validation Rules

**Add (room-service):** Resolve channels (copy `room_members` + `subscriptions` from source room), expand orgs via `users.find({sectId: orgId})`, deduplicate, filter bots (regex `\.bot$|^p_`), check capacity against `MAX_ROOM_SIZE`, publish to stream, reply `{"status":"accepted"}`.

**Remove (room-service):** Parse requester and roomID from subject. Exactly one of `username` or `orgId` must be set (both are omitempty). Self-leave triggers last-owner guard. Non-self removal requires owner role.

**Role update (room-service):** Requester must have owner role. `newRole` must be `"owner"` or `"member"`. Last-owner guard rejects demoting self when `CountOwners <= 1`. No federation guard — cross-site users can be promoted to owner.

## Inbox-Worker (Remote Site Processing)

Inbox-worker processes inbound outbox events from remote sites:

| Event Type | Action |
|------------|--------|
| `member_added` | Create subscription in local MongoDB from `MemberChangeEvent` payload |
| `member_removed` | Delete subscription from local MongoDB |
| `role_updated` | Update subscription role from `SubscriptionUpdateEvent` payload |

For each processed event, inbox-worker publishes `SubscriptionUpdateEvent` to the local user's subject so their client is notified.

## Message-Worker Changes

`SaveMessage` in message-worker must persist the `type` and `sys_msg_data` columns to Cassandra. The insert query for `messages_by_room` and `messages_by_id` is updated to include these fields. For regular messages, these fields are empty/nil and stored as null — no impact on normal message flow.

## What Gets Removed

| Removed | Reason |
|---------|--------|
| `InviteMemberRequest` struct | Member invite operation removed |
| `processInvite` in room-worker | Replaced by `processAddMembers` |
| `handleInvite` / `NatsHandleInvite` in room-service | No longer needed |
| `MemberInviteWildcard` subject and subscription | No longer needed |
| `hr_data` collection and all queries against it | Org resolution now uses `users` collection |
| Federation guard on role promotion | Cross-site owners now allowed |

## Callers

- **Clients** — send NATS request/reply to room-service (routed via gateways to room's site)
- **room-service** — validates requests, publishes to `ROOMS_{siteID}` stream
- **room-worker** — consumes from `ROOMS_{siteID}`, performs all DB writes and events synchronously before ack
- **inbox-worker** — processes `member_added`, `member_removed`, `role_updated` events from INBOX stream to replicate subscriptions on remote user's site
- **message-worker** — persists system messages (type + sys_msg_data) to Cassandra

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| All writes before ack, NAK on failure | Ensures complete consistency; JetStream retries with idempotent unique indexes make this safe |
| No async goroutines in room-worker | Simpler reasoning about failure modes; all work must succeed or all retries |
| Outbox from room site to user site | Room's site is the authority; it pushes changes to each remote member's site |
| Cross-site owner promotion allowed | Business requirement; outbox handles replicating role changes |
| Org resolution via users collection | Eliminates hr_data dependency; User.SectID is the single source of truth for org membership |
| System messages via MESSAGES_CANONICAL | Reuses existing message-worker + broadcast-worker pipeline; no new delivery mechanism |
| RemoveMemberRequest both fields omitempty | Clean payload — client sends exactly one of username or orgId |
