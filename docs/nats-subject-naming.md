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
| `chat.user.{userID}.notification` | Server → Client | notification-worker | New message notifications |
| `chat.user.{userID}.event.subscription.update` | Server → Client | room-worker, inbox-worker | Room added/removed from user's list |
| `chat.user.{userID}.event.room.metadata.update` | Server → Client | room-worker | Room metadata changed (for rooms in sidebar) |
| `chat.user.{userID}.response.{requestID}` | Server → Client | message-worker | Async acknowledgment of message send |

### 2. Per-Room Subjects (subscribed for each room in sidebar)

For each room displayed in the client's sidebar, the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.room.{roomID}.stream.msg` | Server → Client | broadcast-worker | Group room message delivery |
| `chat.room.{roomID}.event.metadata.update` | Server → Client | broadcast-worker | Room name/avatar/topic changes |
| `chat.room.{roomID}.event.typing` | Client → Client | Client (via NATS) | Typing indicators for the active room |

Clients subscribe to `chat.room.{roomID}.event.typing` only for the **currently opened room** (not all sidebar rooms) to minimize traffic. When the user switches rooms, the client unsubscribes from the old room's typing subject and subscribes to the new one.

### 3. Per-User Presence (subscribed per visible user)

For each user visible in the UI (room member list, DM list, etc.), the client subscribes to:

| Subject | Direction | Publisher | Purpose |
|---------|-----------|-----------|---------|
| `chat.user.{userID}.event.presence` | Server → Client | Presence service (future) / Client heartbeat | Online/offline/away status |

Clients dynamically subscribe/unsubscribe to presence subjects as users appear/disappear from the viewport.

### 4. Client Publishes

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
2. Client calls `chat.rooms.list` (or a subscription list endpoint) to get current room list
3. Client re-subscribes to all room subjects for rooms in sidebar
4. For the **currently active room**, client calls `chat.user.{userID}.request.room.{roomID}.{siteID}.msg.history` with the last known message timestamp to fetch missed messages
5. Client resumes receiving real-time events

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
    |                          (store + fanout)                        |
    |                                  |                               |
    |                          [FANOUT stream]                         |
    |                                  |                               |
    |                         broadcast-worker                         |
    |                                  |                               |
    |<-- sub: chat.user.A             |--- pub: chat.room.R1          |
    |        .response.{reqID} -------|        .stream.msg ---------->|
    |                                  |                               |
    |                        notification-worker                       |
    |                                  |                               |
    |                                  |--- pub: chat.user.B          |
    |                                  |        .notification ------->|
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
  |<-- resp: [room1, room2, ...] ----|<-----                       |
  |                                   |                             |
  |-- sub: chat.room.R1.stream.msg ->|                             |
  |-- sub: chat.room.R1.event.* ---->|                             |
  |-- sub: chat.room.R2.stream.msg ->|                             |
  |-- sub: chat.room.R2.event.* ---->|                             |
  |                                   |                             |
  |-- req: msg.history (active room)->|-----> history-service       |
  |<-- resp: [missed messages] ------|<-----                       |
  |                                   |                             |
  |-- (resume real-time) ----------->|                             |
```
