# NATS Subject Naming Design

## Overview

This document defines the complete NATS subject naming scheme for the chat system. All client-facing subjects include `{siteID}` at the second position (`chat.{siteID}.*`) for multi-site isolation. Clients (web & mobile) connect to NATS core subjects only — no JetStream consumers on the client side. On reconnect, clients use request/reply to catch up on missed data (message history, subscription lists, etc.).

## Subject Hierarchy

All subjects are dot-delimited and organized into four namespaces:

| Prefix | Scope | Description |
|--------|-------|-------------|
| `chat.{siteID}.user.{userID}.*` | Per-user | Events, streams, and requests scoped to a single user |
| `chat.{siteID}.room.{roomID}.*` | Per-room | Events and streams scoped to a single room |
| `fanout.{siteID}.*` | Backend | Internal message fan-out (JetStream only) |
| `outbox.{siteID}.*` | Backend | Cross-site federation (JetStream only) |

## Client Subscription Model

### 1. User Wildcard (always subscribed)

On connect, every client subscribes to `chat.{siteID}.user.{userID}.>`. This single wildcard captures all personal events:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.{siteID}.user.{userID}.stream.msg` | Server → Client | broadcast-worker | DM message delivery |
| `chat.{siteID}.user.{userID}.notification` | Server → Client | notification-worker | Desktop banner notification (new message alert) |
| `chat.{siteID}.user.{userID}.event.subscription.update` | Server → Client | room-worker, inbox-worker | Room added/removed from user's list |
| `chat.{siteID}.user.{userID}.event.room.metadata.update` | Server → Client | room-worker | Room metadata changed (for rooms in sidebar) |
| `chat.{siteID}.user.{userID}.response.{requestID}` | Server → Client | various services | Response to a client request |

### 2. Per-Room Subjects (subscribed for each room in sidebar)

For each room displayed in the client's sidebar, the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.{siteID}.room.{roomID}.stream.msg` | Server → Client | broadcast-worker | Group room message delivery |
| `chat.{siteID}.room.{roomID}.event.metadata.update` | Server → Client | broadcast-worker | Room metadata: name, user count, lastMessageAt |
| `chat.{siteID}.room.{roomID}.event.typing` | Server → Client | room-service (relay) | Typing indicators for the active room |

Clients subscribe to `chat.{siteID}.room.{roomID}.event.typing` only for the **currently opened room** (not all sidebar rooms) to minimize traffic. When the user switches rooms, the client unsubscribes from the old room's typing subject and subscribes to the new one.

### 3. Per-User Presence (subscribed per visible user)

For each user visible in the UI (room member list, DM list, etc.), the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.{siteID}.user.{userID}.event.presence` | Server → Client | Presence service (future) / Client heartbeat | Online/offline/away status |

Clients dynamically subscribe/unsubscribe to presence subjects as users appear/disappear from the viewport.

### 4. Sidebar Badge Model

Badges use two separate mechanisms: **unread** badges derive from room metadata events, while **mention** badges derive from the message stream itself.

#### Unread Badge (Bold Room Name)

broadcast-worker publishes `RoomMetadataUpdateEvent` to `chat.{siteID}.room.{roomID}.event.metadata.update` containing `lastMessageAt`. The client compares this against the user's locally cached `lastSeenAt` timestamp for each room.

| Badge | Source | Client Logic |
|-------|--------|-------------|
| **Bold room name** (unread) | `lastMessageAt` in `RoomMetadataUpdateEvent` | `lastMessageAt > lastSeenAt` |

#### Mention Badge (`@`) — Hybrid: Client-Derived Online + Server-Tracked Reconnect

Mention badges use a hybrid approach: client-side detection while online, server-tracked state for reconnect.

**While online (client-derived):**

Clients already subscribe to `chat.{siteID}.room.{roomID}.stream.msg` for every sidebar room and `chat.{siteID}.user.{userID}.stream.msg` for DMs. When a message arrives, the client checks the `mentionedUserIDs` field in the `Message` payload:

1. If the logged-in user's ID is in `mentionedUserIDs` → show `@` badge
2. If `"all"` or `"here"` is in `mentionedUserIDs` → show `@` badge
3. Otherwise → no mention badge for this message

The client maintains a local per-room `hasMention` flag. It is set when a matching mention arrives and cleared when the user opens the room (advancing `lastSeenAt`).

This requires zero additional subjects, zero extra publishes, and zero write amplification — the data is already in the message payload the client receives.

**On reconnect (server-tracked):**

When offline, clients miss messages on non-active sidebar rooms. To restore mention badge state on reconnect, the server tracks mention counts per-user-per-room:

1. **broadcast-worker** — when processing a message with non-empty `mentionedUserIDs`, atomically increments `mentionCountSinceLastSeen` on the `Subscription` record in MongoDB for each mentioned user (expanding `"all"`/`"here"` to the full member list)
2. **Subscription list response** (`chat.{siteID}.user.{userID}.request.rooms.list`) — includes `mentionCountSinceLastSeen` per room, allowing the client to restore `@` badges without fetching message history for every sidebar room
3. **Mark as read** — when the user opens a room, the client sends a read-position update (advancing `lastSeenAt`); the server resets `mentionCountSinceLastSeen` to `0` for that user+room

#### Desktop Banner Notifications

notification-worker sends a `NotificationEvent` to `chat.{siteID}.user.{userID}.notification` for immediate desktop banners (including mention notifications). This is an interrupt-style notification, separate from the persistent badge state above.

#### Reconnect Badge Restoration

On reconnect:
1. Client fetches subscription list via `chat.{siteID}.user.{userID}.request.rooms.list` — response includes `lastSeenAt` and `mentionCountSinceLastSeen` per room
2. For each sidebar room: if `mentionCountSinceLastSeen > 0`, show `@` badge
3. Client re-subscribes to room streams and resumes client-side mention detection
4. For the active room, client fetches message history and can highlight individual mentioned messages using `mentionedUserIDs` in each message

### 5. Client Publishes

Clients publish exclusively to subjects under their own `chat.{siteID}.user.{userID}.>` namespace — no exceptions:

| Subject | Direction | Consumer | Purpose |
|---------|-----------|----------|---------|
| `chat.{siteID}.user.{userID}.room.{roomID}.msg.send` | Client → Server | message-worker (MESSAGES stream) | Send a message to a room |
| `chat.{siteID}.user.{userID}.request.room.{roomID}.member.invite` | Client → Server | room-service (validates), room-worker (ROOMS stream) | Invite a member to a room |
| `chat.{siteID}.user.{userID}.room.{roomID}.typing` | Client → Server | room-service (relay) | Typing indicator |

#### Typing Indicator Flow

Clients never publish directly to room-scoped subjects. For typing indicators:

1. Client publishes to `chat.{siteID}.user.{userID}.room.{roomID}.typing` (user-scoped)
2. room-service subscribes to `chat.{siteID}.user.*.room.*.typing` and relays to `chat.{siteID}.room.{roomID}.event.typing`
3. Clients with that room actively opened receive the typing event via their room subscription

This keeps all client publishes under the user namespace, simplifying auth permissions and maintaining consistent security boundaries. Room-scoped subjects (`chat.{siteID}.room.*`) are server-published only — clients can subscribe but never publish to them.

## Request/Reply Subjects

All request subjects fall under the user's publish namespace. Responses are delivered to `chat.{siteID}.user.{userID}.response.{requestID}`, which the client receives via its user wildcard subscription:

| Subject | Responder (Queue Group) | Purpose |
|---------|------------------------|---------|
| `chat.{siteID}.user.{userID}.request.room.{roomID}.msg.history` | history-service | Fetch message history for a room |
| `chat.{siteID}.user.{userID}.request.rooms.create` | room-service | Create a new room |
| `chat.{siteID}.user.{userID}.request.rooms.list` | room-service | List user's rooms |
| `chat.{siteID}.user.{userID}.request.rooms.get.{roomID}` | room-service | Get room details by ID |

### Reconnect Flow

1. Client detects reconnect event from NATS connection
2. Client subscribes to `chat.{siteID}.user.{userID}.>` (user wildcard)
3. Client calls `chat.{siteID}.user.{userID}.request.rooms.list` to get current room list — response includes `lastSeenAt` and `mentionCountSinceLastSeen` per room
4. Client restores badges: bold room name if `lastMessageAt > lastSeenAt`; `@` badge if `mentionCountSinceLastSeen > 0`
5. Client subscribes to room subjects for each sidebar room
6. For the **currently active room**, client calls `chat.{siteID}.user.{userID}.request.room.{roomID}.msg.history` with the last known message timestamp to fetch missed messages
7. Client resumes receiving real-time events and client-side mention detection

## Backend-Only Subjects (JetStream)

These subjects are used exclusively by backend services via JetStream. Clients never interact with them.

### MESSAGES Stream (`MESSAGES_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `chat.{siteID}.user.{userID}.room.{roomID}.msg.send` | Client | message-worker | User message submissions |

Stream wildcard: `chat.{siteID}.user.*.room.*.msg.>`

### FANOUT Stream (`FANOUT_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `fanout.{siteID}.{roomID}.{msgID}` | message-worker | broadcast-worker, notification-worker | Stored message ready for delivery |

Stream wildcard: `fanout.{siteID}.>`

### ROOMS Stream (`ROOMS_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `chat.{siteID}.user.{userID}.request.room.{roomID}.member.invite` | room-service | room-worker | Member invitation (after authorization) |

Stream wildcard: `chat.{siteID}.user.*.request.room.*.member.>`

### OUTBOX Stream (`OUTBOX_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `outbox.{siteID}.to.{destSiteID}.{eventType}` | room-worker, broadcast-worker | Remote site's INBOX | Cross-site outbound events |

Stream wildcard: `outbox.{siteID}.>`

### INBOX Stream (`INBOX_{siteID}`)

Sourced from remote sites' OUTBOX streams. Processed by `inbox-worker`.

## Auth Permissions (NATS JWT)

The auth-service issues per-user JWTs with these permissions:

| Type | Pattern | Rationale |
|------|---------|-----------|
| Pub.Allow | `chat.{siteID}.user.{userID}.>` | User can publish messages, requests, typing under own namespace |
| Sub.Allow | `chat.{siteID}.user.{userID}.>` | User receives own events, responses, notifications |
| Sub.Allow | `chat.{siteID}.room.>` | User can subscribe to any room's message stream and events |

All client publishes — message sends, member invites, typing indicators, and request/reply — fall under `chat.{siteID}.user.{userID}.>`. No additional publish permissions are needed. Room-scoped subjects (`chat.{siteID}.room.*`) are server-published only; clients can subscribe but never publish to them.

## Subject Builders (`pkg/subject`)

### Existing Builders (to be updated with siteID at first parameter)

| Function | Subject |
|----------|---------|
| `MsgSend(siteID, userID, roomID)` | `chat.{siteID}.user.{userID}.room.{roomID}.msg.send` |
| `UserResponse(siteID, userID, requestID)` | `chat.{siteID}.user.{userID}.response.{requestID}` |
| `RoomMetadataUpdate(siteID, roomID)` | `chat.{siteID}.room.{roomID}.event.metadata.update` |
| `RoomMsgStream(siteID, roomID)` | `chat.{siteID}.room.{roomID}.stream.msg` |
| `UserMsgStream(siteID, userID)` | `chat.{siteID}.user.{userID}.stream.msg` |
| `MemberInvite(siteID, userID, roomID)` | `chat.{siteID}.user.{userID}.request.room.{roomID}.member.invite` |
| `MsgHistory(siteID, userID, roomID)` | `chat.{siteID}.user.{userID}.request.room.{roomID}.msg.history` |
| `SubscriptionUpdate(siteID, userID)` | `chat.{siteID}.user.{userID}.event.subscription.update` |
| `RoomMetadataChanged(siteID, userID)` | `chat.{siteID}.user.{userID}.event.room.metadata.update` |
| `Notification(siteID, userID)` | `chat.{siteID}.user.{userID}.notification` |
| `Outbox(siteID, destSiteID, eventType)` | `outbox.{siteID}.to.{destSiteID}.{eventType}` |
| `Fanout(siteID, roomID, msgID)` | `fanout.{siteID}.{roomID}.{msgID}` |

### New Builders (to be added)

| Function | Subject | Purpose |
|----------|---------|---------|
| `UserTyping(siteID, userID, roomID)` | `chat.{siteID}.user.{userID}.room.{roomID}.typing` | Client publishes typing indicator |
| `RoomTyping(siteID, roomID)` | `chat.{siteID}.room.{roomID}.event.typing` | room-service relays typing to room |
| `UserPresence(siteID, userID)` | `chat.{siteID}.user.{userID}.event.presence` | User presence status |
| `UserWildcard(siteID, userID)` | `chat.{siteID}.user.{userID}.>` | Client subscribes to all personal events |
| `RoomsCreate(siteID, userID)` | `chat.{siteID}.user.{userID}.request.rooms.create` | Create room request |
| `RoomsList(siteID, userID)` | `chat.{siteID}.user.{userID}.request.rooms.list` | List rooms request |
| `RoomsGet(siteID, userID, roomID)` | `chat.{siteID}.user.{userID}.request.rooms.get.{roomID}` | Get room details request |

## Implementation Changes

### 1. `pkg/subject/subject.go` — Update all builders with siteID, add new builders

All existing builder functions gain a `siteID` parameter at the first position. Add `UserTyping`, `RoomTyping`, `UserPresence`, `UserWildcard`, `RoomsCreate`, `RoomsList`, and `RoomsGet` functions.

### 2. `auth-service/handler.go` — Update JWT permissions

Update JWT claims to use `chat.{siteID}.user.{userID}.>` for Pub.Allow and Sub.Allow, and `chat.{siteID}.room.>` for Sub.Allow. Remove all `_INBOX.>` permissions and `chat.room.*.event.typing` publish permission.

### 3. `auth-service/handler_test.go` — Update permission tests

Update tests to verify the new permission patterns with siteID and confirm no `_INBOX.>` or typing publish permissions are present.

### 4. `pkg/subject/subject_test.go` — Update and add tests

Update existing test cases for siteID parameter and add tests for new builder functions.

## Visual: Complete Message Flow

```
Client A (sender)                    NATS                         Client B (receiver)
    |                                  |                               |
    |--- pub: chat.site1.user.A       |                               |
    |        .room.R1.msg.send ------>|                               |
    |                                  |                               |
    |                          [MESSAGES stream]                       |
    |                                  |                               |
    |                          message-worker                          |
    |                    (store msg + resolve mentions                  |
    |                     + publish to fanout)                          |
    |                                  |                               |
    |                          [FANOUT stream]                         |
    |                                  |                               |
    |                         broadcast-worker                         |
    |                                  |                               |
    |<-- sub: chat.site1.user.A       |--- pub: chat.site1.room.R1    |
    |        .response.{reqID} -------|        .stream.msg ---------->|
    |                                  |                               |
    |                                  |--- pub: chat.site1.room.R1    |
    |                                  |        .event.metadata        |
    |                                  |        .update --------------->|
    |                                  |   (lastMessageAt)             |
    |                                  |                               |
    |                        notification-worker                       |
    |                                  |                               |
    |                                  |--- pub: chat.site1.user.B    |
    |                                  |        .notification ------->|
    |                                  |   (desktop banner)            |
    |                                  |                               |
    |--- pub: chat.site1.user.A       |                               |
    |        .room.R1.typing -------->|                               |
    |                                  |                               |
    |                          room-service (relay)                    |
    |                                  |                               |
    |                                  |--- pub: chat.site1.room.R1   |
    |                                  |        .event.typing -------->|
```

## Visual: Client Reconnect Flow

```
Client                              NATS                        Services
  |                                   |                             |
  |-- (reconnect detected) --------->|                             |
  |                                   |                             |
  |-- sub: chat.{siteID}             |                             |
  |        .user.{userID}.> -------->|                             |
  |                                   |                             |
  |-- req: chat.{siteID}.user        |                             |
  |    .{userID}.request             |                             |
  |    .rooms.list ----------------->|-----> room-service          |
  |<-- resp: [rooms + lastSeenAt    -|<-----                       |
  |    + mentionCountSinceLastSeen]  |                             |
  |                                   |                             |
  |-- (restore badges:               |                             |
  |    bold if lastMessageAt >       |                             |
  |    lastSeenAt; @ badge if        |                             |
  |    mentionCount > 0)             |                             |
  |                                   |                             |
  |-- sub: chat.{siteID}.room.R1    |                             |
  |        .stream.msg ------------->|                             |
  |-- sub: chat.{siteID}.room.R1    |                             |
  |        .event.* ---------------->|                             |
  |-- sub: chat.{siteID}.room.R2    |                             |
  |        .stream.msg ------------->|                             |
  |-- sub: chat.{siteID}.room.R2    |                             |
  |        .event.* ---------------->|                             |
  |                                   |                             |
  |-- req: msg.history (active room)->|-----> history-service       |
  |<-- resp: [missed messages] ------|<-----                       |
  |                                   |                             |
  |-- (resume real-time + mention -->|                             |
  |    detection from msg stream)    |                             |
```
