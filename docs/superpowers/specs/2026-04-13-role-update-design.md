# Role Update Design

**Date:** 2026-04-13
**Status:** Approved

## Overview

Add a role-update operation that promotes or demotes a room member between `owner` and `member` roles. The request flows through the existing two-service split: `room-service` validates authorization and publishes to the `ROOMS_{siteID}` JetStream stream, `room-worker` consumes the message, persists the role change, and fans out notifications. For cross-site members, an outbox event replicates the role change to the member's home site via `inbox-worker`.

## Scope

Covers the NATS request/reply endpoint for role-update, subject builder, room-service validation (room type guard, owner authorization, role validity, duplicate role guard, last-owner guard), room-worker persistence and event publishing, outbox publishing for cross-site members, and inbox-worker processing of `role_updated` events.

Out of scope: add-member, remove-member, room creation/deletion, message history access control.

## Architecture

### Event Flow

```
Client
  |
  v  NATS request/reply
room-service
  |  validate: room type, owner auth, role validity,
  |            duplicate role, last-owner guard
  |  reply {"status":"accepted"}
  v  publish UpdateRoleRequest to ROOMS stream
ROOMS_{siteID} stream
  subject: chat.room.canonical.{siteID}.member.role-update
  |
  v  consume (durable consumer: room-worker)
room-worker
  |  1. UpdateSubscriptionRoles
  |  2. Publish SubscriptionUpdateEvent to user
  |  3. If cross-site: publish outbox event
  |  4. Ack (NAK on any failure)
  |
  +-- local user: done
  |
  +-- cross-site user:
        |
        v  outbox.{room.SiteID}.to.{user.SiteID}.role_updated
      OUTBOX_{siteID} stream
        |
        v  (sourced into remote INBOX)
      inbox-worker (remote site)
        1. UpdateSubscriptionRoles in local DB
        2. Publish SubscriptionUpdateEvent to local user
```

## NATS Subjects

### Request/Reply (Client -> room-service)

| Operation | Subject | Queue Group |
|-----------|---------|-------------|
| Update role | `chat.user.{account}.request.room.{roomID}.{siteID}.member.role-update` | `room-service` |

The `{siteID}` in the subject is the room's site. NATS gateways route cross-site requests to the correct cluster.

### Subject Builders

```go
// Client request subject
func MemberRoleUpdate(account, roomID, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.role-update", account, roomID, siteID)
}

// Room-service subscription pattern
func MemberRoleUpdateWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.room.*.%s.member.role-update", siteID)
}

// Canonical subject for publishing to ROOMS stream
func RoomCanonical(siteID, operation string) string {
    return fmt.Sprintf("chat.room.canonical.%s.%s", siteID, operation)
}
```

Room-service subscribes to `MemberRoleUpdateWildcard` with the `room-service` queue group. After validation, it publishes to the ROOMS stream using `RoomCanonical(siteID, "member.role-update")`.

### ROOMS Stream

The `ROOMS_{siteID}` stream subject filter is updated to capture canonical room operations:

```
chat.room.canonical.{siteID}.>
```

This replaces the old `chat.user.*.request.room.*.{siteID}.member.>` filter, separating client-facing request subjects from internal stream subjects. Room-worker dispatches by the canonical subject's trailing tokens (`member.role-update`).

### Events (room-worker -> clients/systems)

| Subject | Payload | Purpose |
|---------|---------|---------|
| `chat.user.{account}.event.subscription.update` | `SubscriptionUpdateEvent` | Notify the affected user of their role change |
| `outbox.{room.SiteID}.to.{user.SiteID}.role_updated` | `OutboxEvent` wrapping `SubscriptionUpdateEvent` | Replicate role change to remote user's site |

## Data Models

### New: UpdateRoleRequest

```go
type UpdateRoleRequest struct {
    RoomID  string `json:"roomId"  bson:"roomId"`
    Account string `json:"account" bson:"account"`
    NewRole Role   `json:"newRole" bson:"newRole"`
}
```

### Modified: SubscriptionUpdateEvent

The `Action` field gains a new value `"role_updated"` in addition to the existing `"added"` and `"removed"`.

```go
type SubscriptionUpdateEvent struct {
    UserID       string       `json:"userId"`
    Subscription Subscription `json:"subscription"`
    Action       string       `json:"action"` // "added" | "removed" | "role_updated"
    Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

No structural change to the struct; only the documented set of valid `Action` values expands.

### Modified: OutboxEvent

The `Type` field gains a new value `"role_updated"` in addition to the existing `"member_added"` and `"room_sync"`.

```go
type OutboxEvent struct {
    Type       string `json:"type"` // "member_added" | "room_sync" | "role_updated"
    SiteID     string `json:"siteId"`
    DestSiteID string `json:"destSiteId"`
    Payload    []byte `json:"payload"` // JSON-encoded SubscriptionUpdateEvent
    Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}
```

### Modified: Subscription

The `Role` field changes from singular `Role` to `Roles []Role`. `HistorySharedSince` is preserved (not modified by role-update).

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
```

### Existing types used (no changes)

- `Room` — `Type` field checked during validation (`RoomTypeGroup` vs `RoomTypeDM`)
- `Role` — constants `RoleOwner` and `RoleMember`

## Helpers (room-service)

`HasRole(roles []Role, target Role) bool` — checks if a role is present in the `Roles` slice. Used for owner authorization checks.

`sanitizeError(err error) string` — returns user-safe error messages. Errors with known user-facing prefixes (e.g., `"only owners"`, `"cannot demote"`, `"invalid"`) pass through; all others return `"internal error"`.

## Validation Rules (room-service)

All checks run before publishing to the ROOMS stream. On any failure, `natsutil.ReplyError` returns the error to the client; no stream message is published.

| # | Check | Error |
|---|-------|-------|
| 1 | Parse requester `account` and `roomID` from subject | `"invalid role-update subject"` |
| 2 | Unmarshal `UpdateRoleRequest` from payload | `"invalid request"` |
| 3 | `newRole` must be `RoleOwner` or `RoleMember` | `"invalid role: must be owner or member"` |
| 4 | Room must exist and `room.Type == RoomTypeGroup` | `"role update is only allowed in group rooms"` |
| 5 | Requester must have a subscription with `HasRole(sub.Roles, RoleOwner)` | `"only owners can update roles"` |
| 6 | Target user must have a subscription in the room | `"target user is not a member of this room"` |
| 7 | Target user's current `Roles` must not already contain `newRole` | `"user already has the requested role"` |
| 8 | If demoting (`newRole == RoleMember`) and requester == target, `CountOwners(roomID) > 1` | `"cannot demote: you are the last owner"` |

On success: marshal the request (with `roomID` set from subject), publish to the ROOMS stream via `RoomCanonical(siteID, "member.role-update")`, reply `{"status":"accepted"}`.

### Publishing to Stream

The `Handler.publishToStream` field has signature `func(ctx context.Context, subject string, data []byte) error`. The handler passes the canonical subject and marshalled request data. In `main.go`, this is wired to `js.Publish`.

## Processing (room-worker)

Room-worker consumes from the `ROOMS_{siteID}` stream. `HandleJetStreamMsg` inspects the canonical message subject to dispatch to the correct processor.

### Dispatch

Extract the operation from the canonical subject's trailing tokens (`chat.room.canonical.{siteID}.<operation>`):
- `member.role-update` -> `processRoleUpdate`

### processRoleUpdate

All steps are synchronous before ack. NAK on any failure for JetStream retry.

1. Unmarshal `UpdateRoleRequest` from message data
2. Look up the target user's subscription via `GetSubscription(ctx, req.Account, req.RoomID)`
3. `UpdateSubscriptionRoles(ctx, req.Account, req.RoomID, req.NewRole)` — sets `roles: [newRole]` on the subscription document
4. Publish `SubscriptionUpdateEvent` with action `"role_updated"` to `chat.user.{account}.event.subscription.update`
5. If `subscription.SiteID != h.siteID`: publish `OutboxEvent` with type `"role_updated"` and `SubscriptionUpdateEvent` as payload to `outbox.{room.SiteID}.to.{user.SiteID}.role_updated`
6. **Ack**

## Cross-Site Handling (inbox-worker)

Inbox-worker's `HandleEvent` switch gains a new case for `"role_updated"`.

### handleRoleUpdated

1. Unmarshal `SubscriptionUpdateEvent` from `OutboxEvent.Payload`
2. `UpdateSubscriptionRoles(ctx, evt.Subscription.User.Account, evt.Subscription.RoomID, evt.Subscription.Roles)` in local MongoDB
3. Publish `SubscriptionUpdateEvent` to `chat.user.{account}.event.subscription.update` so the local client is notified

## Store Interface Changes

### room-service: RoomStore

New methods:
```go
CountOwners(ctx context.Context, roomID string) (int, error)
```

`GetRoom` and `GetSubscription` already exist.

### room-worker: SubscriptionStore

New methods:
```go
GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
```

### inbox-worker: InboxStore

New method:
```go
UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
```

## MongoDB Operations

### UpdateSubscriptionRoles

```
db.subscriptions.updateOne(
    { "u.account": account, "roomId": roomID },
    { $set: { "roles": [newRole] } }
)
```

### CountOwners

```
db.subscriptions.countDocuments(
    { "roomId": roomID, "roles": "owner" }
)
```

## Design Decisions

| Decision | Rationale |
|----------|-----------|
| Room type guard (group only) | DMs have no role hierarchy; role management is meaningless for two-person conversations |
| Duplicate role guard | Prevents no-op writes and misleading "accepted" responses; fail fast with a clear error |
| No federation guard | Cross-site users can be promoted to owner per v2 business requirement; outbox handles replicating role changes |
| Canonical subjects for ROOMS stream | Separates client-facing request subjects from internal stream subjects; follows `MESSAGES_CANONICAL` pattern; room-worker dispatches by canonical subject trailing tokens |
| Per-operation wildcard subscriptions | Each operation (`member.role-update`, future `member.add`, `member.remove`) gets its own subscription in room-service; avoids dispatch logic in the handler |
| All writes synchronous before ack | Ensures complete consistency; JetStream retry on NAK is safe because `updateOne` with `$set` is idempotent |
| OutboxEvent wraps SubscriptionUpdateEvent | Reuses existing OutboxEvent envelope; inbox-worker already switches on `Type` |

## Callers

- **Clients** — send NATS request/reply to room-service
- **room-service** — validates request, publishes to `ROOMS_{siteID}` stream
- **room-worker** — consumes from `ROOMS_{siteID}`, persists role change, publishes events
- **inbox-worker** — processes `role_updated` events from INBOX stream to replicate role changes on remote user's site
