# Remove Member Design

**Date:** 2026-04-14
**Status:** Draft
**Derived from:** `2026-04-07-room-member-management-design.md` (v1), `2026-04-10-room-member-management-v2-design.md` (v2)

## Summary

Remove-member covers removing a single user (self-leave or owner-removes-other) or all users belonging to an org from a chat room.

`room-service` validates the request (authorization, last-owner guard, org-only self-leave guard) and publishes to the `ROOMS_{siteID}` JetStream stream. `room-worker` consumes from the stream and performs all DB deletes, event fan-out, system message publishing, and cross-site outbox publishing **synchronously before ack**. Any failure NAKs the message for JetStream retry.

All operations are processed at the **room's site**. NATS gateways route cross-site requests to the correct cluster transparently. Room-service only handles requests for rooms belonging to its own site.

## Scope

Covers the NATS request/reply endpoint for removing a member; authorization guards (owner-only removal, self-leave for individual members, last-owner protection, org-only self-leave rejection); dual-membership handling during org removal; subscription and room-member deletion; `userCount` maintenance; system message publishing (with distinct messages for self-leave vs owner-removes); per-user and room-scoped event fan-out; cross-site outbox publishing for subscription replication; and inbox-worker processing of remote `member_removed` events.

Out of scope: adding members, role updates, room creation/deletion, message history access control, typing indicators, presence, and read receipts.

## NATS Subjects

### Request/Reply (Client -> room-service)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| Remove member | `chat.user.{account}.request.room.{roomID}.{siteID}.member.remove` | `room-service` |

The `{siteID}` in the subject is the room's site. NATS gateways route cross-site requests to the correct cluster.

### ROOMS Stream (room-service -> room-worker)

Stream `ROOMS_{siteID}` captures all canonical room operations. `room-service` publishes to `subject.RoomCanonical(siteID, "member.remove")` (= `chat.room.canonical.{siteID}.member.remove`). Consumer `room-worker` is durable with explicit ack. Since canonical subjects don't carry the requester account, `RemoveMemberRequest` includes a `Requester` field that `room-service` fills in before publishing.

### Events (room-worker -> clients/systems)

| Subject | Payload | Purpose |
|---------|---------|---------|
| `chat.user.{account}.event.subscription.update` | `SubscriptionUpdateEvent` | Notify individual user their room list changed |
| `chat.room.{roomID}.event.member` | `MemberRemoveEvent` | Notify all room subscribers of membership change |
| `chat.msg.canonical.{siteID}.created` | `MessageEvent` | System message (`member_left` or `member_removed`) persisted via message-worker pipeline |
| `outbox.{room.SiteID}.to.{user.SiteID}.member_removed` | `MemberRemoveEvent` | Remote site deletes subscriptions for the accounts listed in the event (already filtered at room's site) |

## Data Models

### Relevant Core Types

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

```go
type RoomMember struct {
    ID     string          `json:"id"     bson:"_id"`
    RoomID string          `json:"rid"    bson:"rid"`
    Ts     time.Time       `json:"ts"     bson:"ts"`
    Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
    ID      string         `json:"id"                bson:"id"`
    Type    RoomMemberType `json:"type"              bson:"type"`    // "individual" or "org"
    Account string         `json:"account,omitempty" bson:"account,omitempty"`
}
```

Unique index: `(rid, member.type, member.id, member.account)`.

### Membership Tracking via `room_members`

A user has a single `subscription` per room regardless of how they were added. `room_members` tracks the **source** of membership — how and why a user is in the room:

- **Individual add:** creates a `room_members` doc with `type="individual"`, `Account=user.Account`.
- **Org add:** creates a `room_members` doc with `type="org"`, `ID=orgId` (one doc per org, shared across all org members).

A user can have **both** an individual and an org membership source simultaneously (e.g., explicitly added as individual, then their org is also added). They still have only one subscription. The `room_members` entries determine what happens on removal:

| Membership sources | Self-leave | Owner removes individual | Org removal |
|--------------------|------------|--------------------------|-------------|
| Individual only | Allowed — subscription deleted, individual entry deleted | Subscription deleted, individual entry deleted | N/A |
| Org only | Blocked — org members cannot leave individually | Subscription deleted, all entries deleted | Subscription deleted, org entry deleted |
| Individual + Org | Allowed — individual entry deleted, **subscription stays** (user remains as org member) | Individual entry deleted, **subscription stays** (user remains as org member) | Org entry deleted, **subscription stays** (individual entry remains) |

**Prerequisite:** The add-member flow must write `room_members` docs for both individual and org entries to support these semantics.

### User

```go
type User struct {
    ID          string `json:"id"          bson:"_id"`
    Account     string `json:"account"     bson:"account"`
    SiteID      string `json:"siteId"      bson:"siteId"`
    SectID      string `json:"sectId"      bson:"sectId"`
    SectName    string `json:"sectName"    bson:"sectName"`
    EngName     string `json:"engName"     bson:"engName"`
    ChineseName string `json:"chineseName" bson:"chineseName"`
    EmployeeID  string `json:"employeeId"  bson:"employeeId"`
}
```

`SectID` is the org identifier. Org resolution uses `users.find({sectId: orgId})`.

### Request Payload

```go
type RemoveMemberRequest struct {
    RoomID    string `json:"roomId"              bson:"roomId"`
    Requester string `json:"requester"           bson:"requester"`
    Account   string `json:"account,omitempty"   bson:"account,omitempty"`
    OrgID     string `json:"orgId,omitempty"     bson:"orgId,omitempty"`
}
```

Exactly one of `Account` or `OrgID` is set, never both. `Requester` is the account of the user initiating the request (parsed from the NATS subject by `room-service`), so `room-worker` can distinguish self-leave from owner-initiated removal without re-parsing the subject.

### Event Payloads

```go
type MemberRemoveEvent struct {
    Type      string   `json:"type"               bson:"type"`      // "member_left" | "member_removed"
    RoomID    string   `json:"roomId"             bson:"roomId"`
    Accounts  []string `json:"accounts"           bson:"accounts"`
    SiteID    string   `json:"siteId"             bson:"siteId"`
    Timestamp int64    `json:"timestamp"          bson:"timestamp"`
}
```

`Accounts` carries the accounts whose subscriptions were actually deleted (after dual-membership filtering on the room's site). The inbox-worker on the member's home site simply deletes these subscriptions — no further filtering or event publishing needed.

```go
type SubscriptionUpdateEvent struct {
    UserID       string       `json:"userId"`
    Subscription Subscription `json:"subscription"`
    Action       string       `json:"action"` // "removed"
    Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

### System Message Data

System message structs use display names (`EngName` + `ChineseName`) for human-readable rendering. A shared `SysMsgUser` type carries the display fields:

```go
type SysMsgUser struct {
    Account     string `json:"account"`
    EngName     string `json:"engName"`
    ChineseName string `json:"chineseName"`
}
```

Two system message types distinguish self-leave from owner-initiated removal:

**`member_left`** — user leaves on their own:

```go
type MemberLeft struct {
    User SysMsgUser `json:"user"`
}
```

Rendered as: `"{user.engName user.chineseName} left the channel"`

**`member_removed`** — owner removes a user or an org:

```go
type MemberRemoved struct {
    User              *SysMsgUser `json:"user,omitempty"`
    OrgID             string      `json:"orgId,omitempty"`
    SectName          string      `json:"sectName,omitempty"`
    RemovedUsersCount int         `json:"removedUsersCount"`
}
```

Rendered as:
- Individual removal: `"{user.engName user.chineseName} has been removed from the channel"`
- Org removal: `"{sectName} has been removed from the channel"`

System messages use the existing `Message` struct with `Type` set to `"member_left"` or `"member_removed"` and `SysMsgData` containing the JSON-encoded payload. They are published to `MESSAGES_CANONICAL` and flow through the existing message-worker + broadcast-worker pipeline.

```go
type Message struct {
    // ... existing fields ...
    Type       string `json:"type,omitempty"       bson:"type,omitempty"`
    SysMsgData []byte `json:"sysMsgData,omitempty" bson:"sysMsgData,omitempty"`
}
```

## Identity Model

Federation is determined by `SiteID` comparison. `Account` is the stable plain identifier used in NATS subjects and unique indexes. A user is federated when `user.SiteID != room.SiteID`.

## Validation Rules (room-service)

1. Parse requester `account` and `roomID` from the NATS subject.
2. Unmarshal `RemoveMemberRequest` from the message payload. Bind `req.RoomID = roomID` and `req.Requester = account` (overwrite any client-supplied values).
3. Exactly one of `Account` or `OrgID` must be set. If both or neither are provided, reply with an error.
4. **Self-leave** (requester's account == `request.Account`):
   - **Single pipeline:** `GetSubscriptionWithMembership(roomID, account)` — aggregates `subscriptions` with a `$lookup` into `room_members` to return the subscription, `hasIndividualMembership`, and `hasOrgMembership` in one query.
   - **Org-only guard:** reject **only** if `hasOrgMembership` is true AND `hasIndividualMembership` is false. If the user has no org membership in this room, self-leave is always allowed regardless of whether a `room_members` doc exists for them (rooms without any orgs have no individual `room_members` docs).
   - **Last-owner guard:** if the subscription's role is owner and `CountOwners(roomID) <= 1`, reject — the room must retain at least one owner.
5. **Owner-removes-other** (requester's account != `request.Account`, or `OrgID` is set):
   - Look up the requester's subscription.
   - Requester must have the `owner` role. If not, reject with an authorization error.
6. On success, publish the validated request to `subject.RoomCanonical(siteID, "member.remove")` (captured by the `ROOMS_{siteID}` stream) and reply `{"status":"accepted"}`.

## Processing Order (room-worker)

All writes are **synchronous before ack**. Any failure NAKs the message for JetStream retry. Unique indexes ensure idempotency on retries (duplicate key errors treated as no-ops, missing deletes treated as no-ops).

### Individual removal (request.Account is set)

Used for both self-leave (`req.Requester == req.Account`) and owner-removes-other. Logic is identical except for the system message type.

1. **Single pipeline:** `GetUserWithOrgMembership(roomID, account)` — aggregates `users` with a `$lookup` into `room_members` (matching `rid=roomID`, `member.type="org"`, `member.id=user.SectID`) to return user data (`SiteID`, `EngName`, `ChineseName`, `SectID`) and a `hasOrgMembership` flag in one query.
2. **If user has org membership (dual-membership):**
   a. **Delete individual `room_members` doc** only. Subscription is preserved — the user remains in the room as an org member.
   b. **Ack**. No subscription deletion, no userCount change, no events, no system message.
3. **If user has no org membership:**
   a. **Delete subscription** by `(roomId, account)`.
   b. **Delete individual `room_members` doc** (`deleteOne`).
   c. **Decrement `userCount`** by the actual `DeletedCount` returned from subscription deletion (0 or 1).
   d. **Publish `SubscriptionUpdateEvent`** (action: `"removed"`) to `chat.user.{account}.event.subscription.update`.
   e. **Publish `MemberRemoveEvent`** to `chat.room.{roomID}.event.member`. `Type` = `"member_left"` if `req.Requester == req.Account`, otherwise `"member_removed"`.
   f. **Publish system message** to `chat.msg.canonical.{siteID}.created`:
      - Self-leave: type `"member_left"`, data `MemberLeft{User: SysMsgUser{Account, EngName, ChineseName}}`.
      - Owner removes: type `"member_removed"`, data `MemberRemoved{User: &SysMsgUser{target}, RemovedUsersCount: 1}`.
   g. **If `user.SiteID != room.SiteID`:** publish outbox event to `outbox.{room.SiteID}.to.{user.SiteID}.member_removed` with `MemberRemoveEvent` payload.
   h. **Ack**.

### Owner removes org (request.OrgID is set)

1. **Single pipeline:** `GetOrgMembersWithIndividualStatus(roomID, orgID)` — aggregates `users` (matching `sectId=orgID`) with a `$lookup` into `room_members` (matching `rid=roomID`, `member.type="individual"`, `member.account=user.Account`) to return each org member's data (`Account`, `SiteID`) and a `hasIndividualMembership` flag in one query. Also returns `sectName` from the first matched user.
2. **Partition results** into `toRemove` (no individual membership) and `toRetain` (have individual membership).
3. **Delete subscriptions** for `toRemove` accounts only.
4. **Delete the org `room_members` doc** (`type="org"`, `ID=orgId`).
5. **Decrement `userCount`** by `len(toRemove)`.
6. **Publish `SubscriptionUpdateEvent`** (action: `"removed"`) per `toRemove` account.
7. **Publish `MemberRemoveEvent`** (type: `"member-removed"`, accounts: `toRemove` only, `OrgID`: orgId) to `chat.room.{roomID}.event.member`.
8. **Publish system message** (type: `"member_removed"`, data: `MemberRemoved{OrgID, SectName, RemovedUsersCount: len(toRemove)}`) to `chat.msg.canonical.{siteID}.created`.
9. **Outbox grouped by destination site:** for each remote site, publish one `MemberRemoveEvent` carrying only the accounts whose subscriptions were actually deleted to `outbox.{room.SiteID}.to.{destSiteID}.member_removed`.
10. **Ack**.

## Inbox-Worker (Remote Site Processing)

`room_members` data lives only on the room's home site. Inbox-worker on other sites only needs to keep subscriptions in sync. The outbox event's `Accounts` list has already been filtered at the room's site (dual-membership users excluded), so the inbox-worker simply deletes the listed subscriptions:

1. For each account in `memberEvt.Accounts`, call `DeleteSubscription(roomID, account)`.

No dual-membership filtering, no `room_members` updates, and no `SubscriptionUpdateEvent` publishing — the room-worker on the room's site already publishes the subscription update events to the users' subjects, and the NATS supercluster routes them to the user's home site directly.

**Rationale:** centralizing `room_members` on the room's site avoids cross-site replication complexity. UIs that need to display org vs individual membership query the room's site directly (via a future RPC or request/reply).

## Store Interface (remove-member methods)

The following store methods are relevant to the remove-member flow. Aggregation pipelines are used where multiple collections need to be queried together to avoid unnecessary round trips to the database.

### Aggregation pipelines (read)

| Method | Service | Pipeline | Returns |
|--------|---------|----------|---------|
| `GetSubscriptionWithMembership(roomID, account)` | room-service | `subscriptions` → `$lookup room_members` (match `rid=roomID`, `type="individual"`, `account=account`) | Subscription + `hasIndividualMembership` flag |
| `GetUserWithOrgMembership(roomID, account)` | room-worker | `users` → `$lookup room_members` (match `rid=roomID`, `type="org"`, `id=user.SectID`) | User data (`Account`, `SiteID`, `SectID`, `EngName`, `ChineseName`) + `hasOrgMembership` flag |
| `GetOrgMembersWithIndividualStatus(roomID, orgID)` | room-worker | `users` (match `sectId=orgID`) → `$lookup room_members` (match `rid=roomID`, `type="individual"`, `account=user.Account`) | List of org members with `Account`, `SiteID`, `SectName`, `hasIndividualMembership` flag |

### Simple queries (read)

| Method | Service | Purpose |
|--------|---------|---------|
| `GetSubscription(roomID, account)` | room-service | Look up requester's subscription for auth checks (owner-removes-other path) |
| `CountOwners(roomID)` | room-service | Last-owner guard |

### Write operations

Prefer bulk MongoDB operations (`deleteMany` with `$in`) over looping individual calls. Each method below is a single DB round trip.

| Method | Service | Implementation |
|--------|---------|----------------|
| `DeleteSubscription(roomID, account)` | room-worker, inbox-worker | `deleteOne({roomId, "u.account": account})` on `subscriptions` |
| `DeleteSubscriptionsByAccounts(roomID, accounts)` | room-worker | `deleteMany({roomId, "u.account": {$in: accounts}})` on `subscriptions` — single round trip for all org members |
| `DeleteRoomMember(roomID, memberType, memberID)` | room-worker | `deleteOne({rid, "member.type": type, "member.id": id})` on `room_members` (room's site only) |
| `DeleteRoomMembersByAccount(roomID, account)` | room-worker | `deleteMany({rid, "member.account": account})` on `room_members` (room's site only) |
| `DecrementUserCount(roomID, count)` | room-worker | `updateOne({_id: roomID}, {$inc: {userCount: -count}})` on `rooms` |

## Callers

- **Clients** — send NATS request/reply to room-service (routed via gateways to room's site)
- **room-service** — validates the remove request (authorization, org-only guard, last-owner guard), publishes to `chat.room.canonical.{siteID}.member.remove` captured by `ROOMS_{siteID}` stream
- **room-worker** — consumes from `ROOMS_{siteID}`, performs all DB deletes and event fan-out synchronously before ack
- **inbox-worker** — processes inbound `member_removed` events from the INBOX stream; deletes subscriptions for listed accounts (`room_members` is not replicated)
- **message-worker** — persists the system message (type + sys_msg_data) to Cassandra

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Two-service split (room-service + room-worker) | Separates sync client-facing validation from async durable persistence; JetStream retries handle transient DB failures without blocking the caller |
| All writes synchronous before ack, NAK on failure | Ensures complete consistency; JetStream retries with idempotent unique indexes make this safe |
| Published subject = original request subject | Preserves requester account and roomID context so room-worker can reconstruct identity without embedding it redundantly in the payload |
| Outbox from room's site to user's site | Room's site is the authority for membership; it pushes removal to each remote member's home site |
| Last-owner guard | The room must always have at least one owner; prevents orphaned rooms |
| Org-only self-leave guard | Users added exclusively through an org cannot leave individually; the org relationship is managed by an owner removing the org |
| Dual-membership preserves subscription | A user present as both individual and org member retains their subscription when either source is removed — the remaining source keeps them in the room. Only when all membership sources are gone is the subscription deleted |
| Distinct system message types (`member_left` vs `member_removed`) | Clients render different text for self-leave vs owner-initiated removal; org removal uses `orgId` instead of individual account in the message |
| `RemoveMemberRequest` both fields omitempty | Clean payload — client sends exactly one of `account` or `orgId` |
| System messages via MESSAGES_CANONICAL | Reuses existing message-worker + broadcast-worker pipeline; no new delivery mechanism needed |
| `Account` instead of `Username` | `Account` is the stable identifier used in NATS subjects and indexes; `Username` is a display name that may include domain suffixes for federated users |
| `room_members` tracks all membership sources | Both individual and org entries are recorded so removal logic can determine whether a user should retain their subscription |
| Single outbox event syncs both subscription and `room_members` | `MemberRemoveEvent` carries `OrgID` when relevant, so inbox-worker can delete both subscriptions and the correct `room_members` entries in one pass — no separate events per collection |
| `room_members` replicated to remote sites | The UI on any site needs to display org vs individual membership without cross-site DB queries; inbox-worker maintains `room_members` parity with the room's home site |
