# Room Member Management Design

**Date:** 2026-04-07
**Status:** Approved

## Summary

Room member management covers three operations on chat room membership: **adding members** (individuals, orgs, or channel-sourced), **removing members** (self-leave, owner-removes-other, org removal), and **updating member roles** (promote/demote between owner and member).

`room-service` acts as a validation gateway — it handles auth, capacity checks, and input normalization, then publishes validated requests to the `ROOMS_{siteID}` JetStream stream. `room-worker` consumes from the stream and handles all database persistence, event fan-out, and cross-site federation.

## Scope

Covers NATS request/reply endpoints for add, remove, invite, and role-update operations; org expansion via `hr_data`; channel-sourced member copying; bot filtering; capacity enforcement; authorization guards; federation guards; subscription and room member persistence; `userCount` maintenance; per-user and room-scoped event fan-out; and cross-site outbox publishing.

Out of scope: room creation/deletion, message history access control, typing indicators, presence, and read receipts.

## NATS Subjects

### Request/Reply (Client → room-service)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| Invite member | `chat.user.{account}.request.room.{roomID}.{siteID}.member.invite` | `room-service` |
| Add members | `chat.user.{account}.request.room.{roomID}.{siteID}.member.add` | `room-service` |
| Remove member | `chat.user.{account}.request.room.{roomID}.{siteID}.member.remove` | `room-service` |
| Update role | `chat.user.{account}.request.room.{roomID}.{siteID}.member.role-update` | `room-service` |

### ROOMS Stream (room-service → room-worker)

Stream `ROOMS_{siteID}` captures all validated member mutations. Subjects match `chat.user.*.request.room.*.{siteID}.member.>`. Consumer `room-worker` is durable with explicit ack. The published subject equals the original request subject, preserving requester and room context for the worker.

### Events (room-worker → clients)

| Subject | Payload | Purpose |
|---------|---------|---------|
| `chat.user.{account}.event.subscription.update` | `SubscriptionUpdateEvent` | Notify individual user their room list changed |
| `chat.room.{roomID}.event.member` | `MemberChangeEvent` | Notify all room subscribers of a membership change |
| `outbox.{siteID}.to.{room.SiteID}.member_added` | `MemberChangeEvent` | Room's home site processes addition |
| `outbox.{siteID}.to.{room.SiteID}.member_removed` | `MemberChangeEvent` | Room's home site processes removal |

## Data Models

### Core types

```go
type Subscription struct {
    ID                 string           `bson:"_id"`
    User               SubscriptionUser `bson:"u"`
    RoomID             string           `bson:"roomId"`
    SiteID             string           `bson:"siteId"`
    Roles              []Role           `bson:"roles"`
    HistorySharedSince *time.Time       `bson:"historySharedSince,omitempty"`
    JoinedAt           time.Time        `bson:"joinedAt"`
    LastSeenAt         time.Time        `bson:"lastSeenAt"`
    HasMention         bool             `bson:"hasMention"`
}

type RoomMember struct {
    ID     string          `bson:"_id"`
    RoomID string          `bson:"rid"`
    Ts     time.Time       `bson:"ts"`
    Member RoomMemberEntry `bson:"member"`
}

type RoomMemberEntry struct {
    ID       string         `bson:"id"`
    Type     RoomMemberType `bson:"type"`              // "individual" or "org"
    Username string         `bson:"username,omitempty"` // individual only
}
```

Unique indexes: `subscriptions (roomId, u.account)` and `room_members (rid, member.type, member.id, member.username)`. Duplicate key errors are treated as success — retries are safe.

### Request payloads

```go
type AddMembersRequest struct {
    RoomID   string        `json:"roomId"   bson:"roomId"`
    Users    []string      `json:"users"    bson:"users"`
    Orgs     []string      `json:"orgs"     bson:"orgs"`
    Channels []string      `json:"channels" bson:"channels"`
    History  HistoryConfig `json:"history"  bson:"history"`
}

type RemoveMemberRequest struct {
    RoomID   string `json:"roomId"`
    Username string `json:"username"`
    OrgID    string `json:"orgId,omitempty"`
}

type UpdateRoleRequest struct {
    RoomID  string `json:"roomId"`
    Username string `json:"username"`
    NewRole  Role   `json:"newRole"`
}
```

### Event payloads

```go
type MemberChangeEvent struct {
    Type     string   `json:"type"`     // "member-added" or "member-removed"
    RoomID   string   `json:"roomId"`
    Accounts []string `json:"accounts"`
    SiteID   string   `json:"siteId"`
}

type SubscriptionUpdateEvent struct {
    UserID       string       `json:"userId"`
    Subscription Subscription `json:"subscription"`
    Action       string       `json:"action"` // "added" | "removed" | "role_updated"
}
```

### Org resolution

`AddMembersRequest.Orgs` contains org IDs that map to `sectId` in the `hr_data` collection. `room-service` queries `hr_data.find({ sectId: orgId })` and extracts `accountName` from each document. `accountName` corresponds to `User.Account`.

## Identity Model

Federation is determined purely by `SiteID` comparison — no `@` parsing or domain detection. `Account` is the stable plain identifier (e.g., `"alice"`) used in NATS subjects and unique indexes. `Username` is the display name and may include a domain for federated users. A user is federated when `user.SiteID != room.SiteID`.

## Processing Order

Separating validation (`room-service`) from persistence (`room-worker`) means a failed DB write does not leave the client hanging. The request is accepted and JetStream ensures eventual, at-least-once processing with automatic retry.

**Add:** `room-worker` writes subscriptions first (synchronous), then acks the JetStream message. Remaining writes run asynchronously and independently: `room_members` documents, `userCount` increment, `SubscriptionUpdateEvent` per account, `MemberChangeEvent` to the room subject, and `MemberChangeEvent` to the outbox targeting `room.SiteID`.

**Remove:** Delete subscription first (synchronous), ack message, then async: delete `room_members`, decrement `userCount`, fan out `SubscriptionUpdateEvent` and `MemberChangeEvent` to room and outbox.

**Role update:** `UpdateSubscriptionRole` sets `roles: [newRole]`, then publishes `SubscriptionUpdateEvent` with action `"role_updated"`. No async fan-out beyond the subscription event.

## Validation Rules

**Add (room-service):** Resolve channels (copy `room_members` + `subscriptions` from source room), expand orgs via `hr_data`, deduplicate, filter bots (regex `\.bot$|^p_`), check capacity against `MAX_ROOM_SIZE` (excluding bots), publish to stream, reply `{"status":"accepted"}`.

**Remove (room-service):** Parse requester and roomID from subject. Exactly one of `username` or `orgId` must be set. Self-leave triggers last-owner guard. Non-self removal requires owner role.

**Role update (room-service):** Requester must have owner role. `newRole` must be `"owner"` or `"member"`. Federation guard rejects promoting a user whose `subscription.SiteID != h.siteID`. Last-owner guard rejects demoting self when `CountOwners <= 1`.

## Federation

`MemberChangeEvent` is always sent to `room.SiteID` via the outbox — the site where the room was originally created. Federated users (those whose `SiteID` differs from the room's `SiteID`) cannot be promoted to owner. All outbox payloads use typed `pkg/model` structs, never raw maps.

## Callers

- **Clients** — send NATS request/reply messages to `room-service` to add, remove, or update members
- **`room-service`** — validates requests and publishes to the `ROOMS_{siteID}` stream
- **`room-worker`** — consumes from `ROOMS_{siteID}`, persists all mutations, and fans out events
- **`inbox-worker`** — processes inbound `member_added` / `member_removed` events from remote sites via the INBOX stream to mirror membership on the room's home site

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Two-service split (room-service + room-worker) | Separates sync client-facing validation from async durable persistence; JetStream retries handle transient DB failures without blocking the caller |
| Subscriptions written before ack | Membership must be committed before events fire; all other writes are best-effort follow-ons |
| Unique indexes for idempotency | JetStream at-least-once delivery means retries must be safe; duplicate key errors treated as no-ops |
| Published subject = original request subject | Preserves requester account and roomID context so room-worker can reconstruct identity without embedding it redundantly in the payload |
| Outbox destination = room.SiteID | Room's home site is the authority for membership; all member changes are sourced through it for federation consistency |
| Federation guard on role promotion | Owners control room settings that affect the home site; remote users owning a room on a foreign site creates inconsistent authority |
| Bot filtering excluded from capacity | Bots are infrastructure participants, not human occupants; capacity limits should reflect real user load |
