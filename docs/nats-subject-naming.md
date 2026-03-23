# NATS Subject Naming Design

## Overview

This document defines the complete NATS subject naming scheme for the chat system. Clients (web & mobile) connect to NATS core subjects only — no JetStream consumers on the client side. On reconnect, clients use request/reply to catch up on missed data (message history, subscription lists, etc.).

## Subject Hierarchy

All subjects are dot-delimited and organized into four namespaces:

| Prefix | Scope | Description |
|--------|-------|-------------|
| `chat.user.{userID}.*` | Per-user | Events, streams, and requests scoped to a single user |
| `chat.room.{roomID}.*` | Per-room | Events and streams scoped to a single room |
| `fanout.{siteID}.*` | Backend | Internal message fan-out (JetStream only) |
| `outbox.{siteID}.*` | Backend | Cross-site federation (JetStream only) |

## Client Subscription Model

### 1. User Wildcard (always subscribed)

On connect, every client subscribes to `chat.user.{userID}.>`. This single wildcard captures all personal events:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.user.{userID}.stream.msg` | Server → Client | broadcast-worker | DM message delivery |
| `chat.user.{userID}.notification` | Server → Client | notification-worker | Desktop banner notification (new message alert) |
| `chat.user.{userID}.event.subscription.update` | Server → Client | room-worker, inbox-worker | Room added/removed from user's list |
| `chat.user.{userID}.event.room.metadata.update` | Server → Client | room-worker | Room metadata changed (for rooms in sidebar) |
| `chat.user.{userID}.response.{requestID}` | Server → Client | message-worker | Async acknowledgment of message send |

### 2. Per-Room Subjects (subscribed for each room in sidebar)

For each room displayed in the client's sidebar, the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.room.{roomID}.stream.msg` | Server → Client | broadcast-worker | Group room message delivery |
| `chat.room.{roomID}.event.metadata.update` | Server → Client | broadcast-worker | Room metadata: name, user count, lastMessageAt |
| `chat.room.{roomID}.event.typing` | Client → Client | Client (via NATS) | Typing indicators for the active room |

Clients subscribe to `chat.room.{roomID}.event.typing` only for the **currently opened room** (not all sidebar rooms) to minimize traffic. When the user switches rooms, the client unsubscribes from the old room's typing subject and subscribes to the new one.

### 3. Per-User Presence (subscribed per visible user)

For each user visible in the UI (room member list, DM list, etc.), the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.user.{userID}.event.presence` | Server → Client | Presence service (future) / Client heartbeat | Online/offline/away status |

Clients dynamically subscribe/unsubscribe to presence subjects as users appear/disappear from the viewport.

### 4. Sidebar Badge Model

Badges use two separate mechanisms: **unread** badges derive from room metadata events, while **mention** badges derive from the message stream itself.

#### Unread Badge (Bold Room Name)

broadcast-worker publishes `RoomMetadataUpdateEvent` to `chat.room.{roomID}.event.metadata.update` containing `lastMessageAt`. The client compares this against the user's locally cached `lastSeenAt` timestamp for each room.

| Badge | Source | Client Logic |
|-------|--------|-------------|
| **Bold room name** (unread) | `lastMessageAt` in `RoomMetadataUpdateEvent` | `lastMessageAt > lastSeenAt` |

#### Mention Badge (`@`) — Hybrid: Client-Derived Online + Server-Tracked Reconnect

A room-scoped `lastMentionAt` field cannot work for mention badges — it would be visible to ALL clients in the room, making it impossible for any individual client to determine whether *they* were mentioned. Instead, mentions use a hybrid approach modeled after how Slack and Matrix handle this:

**While online (client-derived):**

Clients already subscribe to `chat.room.{roomID}.stream.msg` for every sidebar room and `chat.user.{userID}.stream.msg` for DMs. When a message arrives, the client checks the `mentionedUserIDs` field in the `Message` payload:

1. If the logged-in user's ID is in `mentionedUserIDs` → show `@` badge
2. If `"all"` or `"here"` is in `mentionedUserIDs` → show `@` badge
3. Otherwise → no mention badge for this message

The client maintains a local per-room `hasMention` flag. It is set when a matching mention arrives and cleared when the user opens the room (advancing `lastSeenAt`).

This requires zero additional subjects, zero extra publishes, and zero write amplification — the data is already in the message payload the client receives.

**On reconnect (server-tracked):**

When offline, clients miss messages on non-active sidebar rooms. To restore mention badge state on reconnect, the server tracks mention counts per-user-per-room:

1. **broadcast-worker** — when processing a message with non-empty `mentionedUserIDs`, atomically increments `mentionCountSinceLastSeen` on the `Subscription` record in MongoDB for each mentioned user (expanding `"all"`/`"here"` to the full member list)
2. **Subscription list response** (`chat.rooms.list`) — includes `mentionCountSinceLastSeen` per room, allowing the client to restore `@` badges without fetching message history for every sidebar room
3. **Mark as read** — when the user opens a room, the client sends a read-position update (advancing `lastSeenAt`); the server resets `mentionCountSinceLastSeen` to `0` for that user+room

**Comparison with production systems:**

| System | Online Mention Detection | Reconnect/Bootstrap | Read Position |
|--------|--------------------------|---------------------|---------------|
| **Slack** | Server pushes via WebSocket | Bootstrap payload includes per-channel unread + mention counts | Per-user per-channel `last_read` cursor (`conversations.mark` API) |
| **Matrix** | Server evaluates push rules per event per user | `/sync` returns `highlight_count` per room | Per-user per-room read receipt marker |
| **Discord** | Server tracks per-user per-channel | Red badge with mention count at startup | Per-user per-channel read state |
| **This system** | Client derives from `mentionedUserIDs` in message stream | `mentionCountSinceLastSeen` in subscription list response | Per-user per-room `lastSeenAt` on `Subscription` |

**Why not a user-scoped mention subject?** An alternative would have broadcast-worker publish per-user mention events (e.g., `chat.user.{userID}.event.room.mention`). This adds N publishes per mention (one per mentioned user), with severe write amplification for `@all`/`@here` in large rooms. Since clients already receive the message with `mentionedUserIDs`, this extra subject provides no benefit while online. The server-tracked `mentionCountSinceLastSeen` handles the reconnect case without any additional NATS subjects.

#### Desktop Banner Notifications

notification-worker sends a `NotificationEvent` to `chat.user.{userID}.notification` for immediate desktop banners (including mention notifications). This is an interrupt-style notification, separate from the persistent badge state above.

#### Reconnect Badge Restoration

On reconnect:
1. Client fetches subscription list via `chat.rooms.list` — response includes `lastSeenAt` and `mentionCountSinceLastSeen` per room
2. For each sidebar room: if `mentionCountSinceLastSeen > 0`, show `@` badge
3. Client re-subscribes to room streams and resumes client-side mention detection
4. For the active room, client fetches message history and can highlight individual mentioned messages using `mentionedUserIDs` in each message

### 5. Client Publishes

Clients publish to subjects under their own `chat.user.{userID}.>` namespace:

| Subject | Direction | Consumer | Purpose |
|---------|-----------|----------|---------|
| `chat.user.{userID}.room.{roomID}.{siteID}.msg.send` | Client → Server | message-worker (MESSAGES stream) | Send a message to a room |
| `chat.user.{userID}.request.room.{roomID}.{siteID}.member.invite` | Client → Server | room-service (validates), room-worker (ROOMS stream) | Invite a member to a room |

Additionally, clients publish typing indicators directly to room subjects:

| Subject | Direction | Consumer | Purpose |
|---------|-----------|----------|---------|
| `chat.room.{roomID}.event.typing` | Client → Client | Other clients in the room | Typing indicator broadcast |

## Request/Reply Subjects (Reconnect Catch-Up)

On reconnect, clients use NATS request/reply to fetch missed data. All request subjects fall under the user's publish namespace:

| Subject | Responder (Queue Group) | Purpose |
|---------|------------------------|---------|
| `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history` | history-service | Fetch message history for a room |
| `chat.rooms.create` | room-service | Create a new room |
| `chat.rooms.list` | room-service | List available rooms |
| `chat.rooms.get.{roomID}` | room-service | Get room details by ID |

### Reconnect Flow

1. Client detects reconnect event from NATS connection
2. Client calls `chat.rooms.list` (or a subscription list endpoint) to get current room list — response includes `lastSeenAt` and `mentionCountSinceLastSeen` per room
3. Client restores badges: bold room name if `lastMessageAt > lastSeenAt`; `@` badge if `mentionCountSinceLastSeen > 0`
4. Client re-subscribes to all room subjects for rooms in sidebar
5. For the **currently active room**, client calls `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history` with the last known message timestamp to fetch missed messages
6. Client resumes receiving real-time events and client-side mention detection

## Backend-Only Subjects (JetStream)

These subjects are used exclusively by backend services via JetStream. Clients never interact with them.

### MESSAGES Stream (`MESSAGES_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `chat.user.{userID}.room.{roomID}.{siteID}.msg.send` | Client | message-worker | User message submissions |

Stream wildcard: `chat.user.*.room.*.{siteID}.msg.>`

### FANOUT Stream (`FANOUT_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `fanout.{siteID}.{roomID}.{msgID}` | message-worker | broadcast-worker, notification-worker | Stored message ready for delivery |

Stream wildcard: `fanout.{siteID}.>`

### ROOMS Stream (`ROOMS_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `chat.user.{userID}.request.room.{roomID}.{siteID}.member.invite` | room-service | room-worker | Member invitation (after authorization) |

Stream wildcard: `chat.user.*.request.room.*.{siteID}.member.>`

### OUTBOX Stream (`OUTBOX_{siteID}`)

| Subject Pattern | Publisher | Consumer | Purpose |
|-----------------|-----------|----------|---------|
| `outbox.{siteID}.to.{destSiteID}.{eventType}` | room-worker, broadcast-worker | Remote site's INBOX | Cross-site outbound events |

Stream wildcard: `outbox.{siteID}.>`

### INBOX Stream (`INBOX_{siteID}`)

Sourced from remote sites' OUTBOX streams. Processed by `inbox-worker`.

## Auth Permissions (NATS JWT)

The auth-service issues per-user JWTs with these permissions:

### Current Permissions

| Type | Pattern | Rationale |
|------|---------|-----------|
| Pub.Allow | `chat.user.{userID}.>` | User can publish messages, requests under own namespace |
| Pub.Allow | `_INBOX.>` | Required for NATS request/reply |
| Sub.Allow | `chat.user.{userID}.>` | User receives own events, responses, notifications |
| Sub.Allow | `chat.room.>` | User can subscribe to any room's message stream and events |
| Sub.Allow | `_INBOX.>` | Required for NATS request/reply |

### Required Change: Typing Indicator Publish Permission

Typing indicators are published directly to `chat.room.{roomID}.event.typing`. This falls outside the user's `chat.user.{userID}.>` publish namespace.

**Add to Pub.Allow:** `chat.room.*.event.typing`

This is narrowly scoped — users can only publish to `event.typing` subjects under rooms, not to `stream.msg` or `event.metadata.update` (which remain server-only). NATS enforces this at the connection level.

### Updated Permissions Summary

| Type | Pattern |
|------|---------|
| Pub.Allow | `chat.user.{userID}.>` |
| Pub.Allow | `chat.room.*.event.typing` |
| Pub.Allow | `_INBOX.>` |
| Sub.Allow | `chat.user.{userID}.>` |
| Sub.Allow | `chat.room.>` |
| Sub.Allow | `_INBOX.>` |

## Subject Builders (`pkg/subject`)

### Existing Builders (no changes needed)

| Function | Subject |
|----------|---------|
| `MsgSend(userID, roomID, siteID)` | `chat.user.{userID}.room.{roomID}.{siteID}.msg.send` |
| `UserResponse(userID, requestID)` | `chat.user.{userID}.response.{requestID}` |
| `RoomMetadataUpdate(roomID)` | `chat.room.{roomID}.event.metadata.update` |
| `RoomMsgStream(roomID)` | `chat.room.{roomID}.stream.msg` |
| `UserRoomUpdate(userID)` | `chat.user.{userID}.event.room.update` |
| `UserMsgStream(userID)` | `chat.user.{userID}.stream.msg` |
| `MemberInvite(userID, roomID, siteID)` | `chat.user.{userID}.request.room.{roomID}.{siteID}.member.invite` |
| `MsgHistory(userID, roomID, siteID)` | `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history` |
| `SubscriptionUpdate(userID)` | `chat.user.{userID}.event.subscription.update` |
| `RoomMetadataChanged(userID)` | `chat.user.{userID}.event.room.metadata.update` |
| `Notification(userID)` | `chat.user.{userID}.notification` |
| `Outbox(siteID, destSiteID, eventType)` | `outbox.{siteID}.to.{destSiteID}.{eventType}` |
| `Fanout(siteID, roomID, msgID)` | `fanout.{siteID}.{roomID}.{msgID}` |

### New Builders (to be added)

| Function | Subject | Purpose |
|----------|---------|---------|
| `RoomTyping(roomID)` | `chat.room.{roomID}.event.typing` | Typing indicator for a room |
| `UserPresence(userID)` | `chat.user.{userID}.event.presence` | User presence status |
| `UserWildcard(userID)` | `chat.user.{userID}.>` | Client subscribes to all personal events |
| `RoomTypingWildcard()` | `chat.room.*.event.typing` | Auth permission pattern for typing publish |

## Implementation Changes

### 1. `pkg/subject/subject.go` — Add new builders

Add `RoomTyping`, `UserPresence`, `UserWildcard`, and `RoomTypingWildcard` functions.

### 2. `auth-service/handler.go` — Add typing publish permission

Add `chat.room.*.event.typing` to `Pub.Allow` in the JWT claims so clients can broadcast typing indicators directly.

### 3. `auth-service/handler_test.go` — Update permission tests

Update tests to verify the new `chat.room.*.event.typing` publish permission is present in issued JWTs.

### 4. `pkg/subject/subject_test.go` — Add tests for new builders

Add test cases for the new subject builder functions.

## Visual: Complete Message Flow

```
Client A (sender)                    NATS                         Client B (receiver)
    |                                  |                               |
    |--- pub: chat.user.A.room.R1     |                               |
    |        .site1.msg.send -------->|                               |
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
    |<-- sub: chat.user.A             |--- pub: chat.room.R1          |
    |        .response.{reqID} -------|        .stream.msg ---------->|
    |                                  |                               |
    |                                  |--- pub: chat.room.R1          |
    |                                  |        .event.metadata        |
    |                                  |        .update --------------->|
    |                                  |   (lastMessageAt)             |
    |                                  |                               |
    |                        notification-worker                       |
    |                                  |                               |
    |                                  |--- pub: chat.user.B          |
    |                                  |        .notification ------->|
    |                                  |   (desktop banner)            |
    |                                  |                               |
    |--- pub: chat.room.R1            |                               |
    |        .event.typing ---------->|-------- (direct relay) ------>|
```

## Visual: Client Reconnect Flow

```
Client                              NATS                        Services
  |                                   |                             |
  |-- (reconnect detected) --------->|                             |
  |                                   |                             |
  |-- sub: chat.user.{userID}.> ---->|                             |
  |                                   |                             |
  |-- req: chat.rooms.list --------->|-----> room-service          |
  |<-- resp: [rooms + lastSeenAt    -|<-----                       |
  |    + mentionCountSinceLastSeen]  |                             |
  |                                   |                             |
  |-- (restore badges:               |                             |
  |    bold if lastMessageAt >       |                             |
  |    lastSeenAt; @ badge if        |                             |
  |    mentionCount > 0)             |                             |
  |                                   |                             |
  |-- sub: chat.room.R1.stream.msg ->|                             |
  |-- sub: chat.room.R1.event.* ---->|                             |
  |-- sub: chat.room.R2.stream.msg ->|                             |
  |-- sub: chat.room.R2.event.* ---->|                             |
  |                                   |                             |
  |-- req: msg.history (active room)->|-----> history-service       |
  |<-- resp: [missed messages] ------|<-----                       |
  |                                   |                             |
  |-- (resume real-time + mention -->|                             |
  |    detection from msg stream)    |                             |
```
