# Chat System Architecture

## Overview

A distributed multi-site chat system built in Go. Users send messages in rooms with real-time delivery, federated across independent sites. The architecture is event-driven, using NATS JetStream for async event processing and NATS request/reply for synchronous operations.

Each site runs independently with its own NATS, MongoDB, and Cassandra instances. Cross-site communication uses the Outbox/Inbox pattern for loose coupling.

---

## Technology Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.24 |
| Messaging | NATS + JetStream |
| Operational DB | MongoDB (rooms, subscriptions, messages) |
| History DB | Cassandra (time-series message history) |
| Auth | NATS auth callout with JWT + NKeys |
| HTTP | Gin (server), Resty (client) |
| Observability | OpenTelemetry, Prometheus, log/slog |

---

## Services

| Service | Type | Purpose |
|---------|------|---------|
| **auth-service** | Auth Callout | Validates SSO tokens, issues scoped JWTs for NATS connections |
| **room-service** | Request/Reply | Room CRUD and invite authorization |
| **history-service** | Request/Reply | Paginated message history from Cassandra |
| **message-worker** | Stream Consumer | Validates, persists messages, triggers fanout |
| **broadcast-worker** | Stream Consumer | Delivers messages to room members |
| **notification-worker** | Stream Consumer | Sends notifications to room members |
| **room-worker** | Stream Consumer | Processes room invites, handles cross-site membership |
| **inbox-worker** | Stream Consumer | Processes inbound federation events from other sites |

---

## JetStream Streams

All streams are namespaced by `siteID` for multi-site isolation.

| Stream | Subjects | Purpose |
|--------|----------|---------|
| `MESSAGES_{siteID}` | `chat.user.*.room.*.{siteID}.msg.>` | User message submissions |
| `FANOUT_{siteID}` | `fanout.{siteID}.>` | Broadcast messages for fan-out |
| `ROOMS_{siteID}` | `chat.user.*.request.room.*.{siteID}.member.>` | Member invite requests |
| `OUTBOX_{siteID}` | `outbox.{siteID}.>` | Cross-site outbound events |
| `INBOX_{siteID}` | *(sourced from remote OUTBOX)* | Cross-site inbound events |

---

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              SITE A                                         │
│                                                                             │
│  ┌──────────┐                                                               │
│  │  Client   │──SSO Token──┐                                                │
│  └──────────┘              │                                                │
│       │                    ▼                                                │
│       │            ┌──────────────┐                                         │
│       │            │ auth-service │  Validates token, issues scoped JWT      │
│       │            └──────────────┘                                         │
│       │                                                                     │
│       │  (authenticated NATS connection)                                    │
│       │                                                                     │
│       ├──── request/reply ────┬──────────────────────────┐                  │
│       │                       │                          │                  │
│       ▼                       ▼                          ▼                  │
│  ┌──────────────┐    ┌────────────────┐    ┌──────────────────┐             │
│  │ room-service │    │history-service │    │  NATS JetStream   │            │
│  │  (CRUD,      │    │  (paginated    │    │                   │            │
│  │   invites)   │    │   queries)     │    │  MESSAGES stream  │◄── publish │
│  └──────┬───────┘    └────────────────┘    └────────┬──────────┘            │
│         │                                           │                       │
│         │ publish                                   │ consume               │
│         ▼                                           ▼                       │
│  ┌─────────────┐                          ┌──────────────────┐              │
│  │ ROOMS stream │                         │  message-worker  │              │
│  └──────┬──────┘                          │                  │              │
│         │                                 │  MongoDB+Cassan. │              │
│         │ consume                         └────────┬─────────┘              │
│         ▼                                          │                        │
│  ┌─────────────┐                                   │ publish                │
│  │ room-worker │                                   ▼                        │
│  │             │──(cross-site)──►OUTBOX    ┌───────────────┐                │
│  └─────────────┘                stream    │ FANOUT stream  │                │
│                                            └───┬───────┬───┘                │
│                                                │       │                    │
│                                       consume  │       │  consume           │
│                                                ▼       ▼                    │
│                                 ┌──────────────────┐ ┌─────────────────────┐│
│                                 │broadcast-worker  │ │notification-worker  ││
│                                 │                  │ │                     ││
│                                 │ Delivers to room │ │ Sends notifications ││
│                                 │ member streams   │ │ to members          ││
│                                 └──────────────────┘ └─────────────────────┘│
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘

                    ┌──────────────────────────────────┐
                    │        FEDERATION LAYER           │
                    │                                   │
                    │  SITE A OUTBOX ──Source──► SITE B  │
                    │                           INBOX   │
                    │                             │     │
                    │                             ▼     │
                    │                      inbox-worker │
                    │                                   │
                    │  SITE B OUTBOX ──Source──► SITE A  │
                    │                           INBOX   │
                    └──────────────────────────────────┘
```

---

## Sequence Diagrams

### 1. User Authentication

```
Client              NATS Server          auth-service
  │                      │                     │
  │── Connect(token) ───►│                     │
  │                      │── auth_callout ────►│
  │                      │                     │── Verify SSO token
  │                      │                     │── Build permissions:
  │                      │                     │   pub: chat.user.{id}.>
  │                      │                     │   sub: chat.user.{id}.> +
  │                      │                     │        chat.room.>
  │                      │                     │── Sign JWT with NKey
  │                      │◄── signed JWT ──────│
  │◄── Connected ────────│                     │
```

### 2. Send Message (Happy Path)

```
Client          NATS/JetStream     message-worker    MongoDB   Cassandra
  │                  │                   │              │          │
  │── publish ──────►│                   │              │          │
  │  chat.user.{uid} │                   │              │          │
  │  .room.{rid}     │                   │              │          │
  │  .{site}.msg.send│                   │              │          │
  │                  │                   │              │          │
  │          MESSAGES stream             │              │          │
  │                  │── deliver ───────►│              │          │
  │                  │                   │── validate ─►│          │
  │                  │                   │  subscription│          │
  │                  │                   │◄─── ok ──────│          │
  │                  │                   │              │          │
  │                  │                   │── save msg ─►│          │
  │                  │                   │── save msg ──┼─────────►│
  │                  │                   │              │          │
  │                  │◄── publish ───────│              │          │
  │                  │  fanout.{site}    │              │          │
  │                  │  .{roomID}        │              │          │
  │                  │                   │              │          │
  │◄── reply ────────┼───────────────────│              │          │
  │  chat.user.{uid} │                   │              │          │
  │  .response.{reqID}                   │              │          │
```

### 3. Message Broadcast & Notification

```
                NATS/JetStream    broadcast-worker   notification-worker   MongoDB
                     │                  │                    │                │
             FANOUT stream              │                    │                │
                     │── deliver ──────►│                    │                │
                     │── deliver ───────┼───────────────────►│                │
                     │                  │                    │                │
                     │                  │── get room ───────►┼───────────────►│
                     │                  │◄──────────────────►┼───────────────►│
                     │                  │                    │  get members   │
                     │                  │                    │                │
                     │                  │                    │                │
       ┌─────────────┼──────────────────┤                    │                │
       │ IF GROUP    │                  │                    │                │
       │             │◄── publish ──────│                    │                │
       │  chat.room.{roomID}            │                    │                │
       │  .stream.msg                   │                    │                │
       ├─────────────┼──────────────────┤                    │                │
       │ IF DM       │                  │                    │                │
       │             │◄── publish ──────│                    │                │
       │  chat.user.{memberA}           │                    │                │
       │  .stream.msg                   │                    │                │
       │             │◄── publish ──────│                    │                │
       │  chat.user.{memberB}           │                    │                │
       │  .stream.msg                   │                    │                │
       └─────────────┼──────────────────┘                    │                │
                     │                                       │                │
                     │◄── publish ──────────────────(for each member, not sender)
                     │  chat.user.{uid}.notification          │                │
```

### 4. Room Creation

```
Client            NATS             room-service           MongoDB
  │                 │                    │                    │
  │── request ─────►│                    │                    │
  │  chat.user.{uid}│                    │                    │
  │  .request.rooms │                    │                    │
  │  .create        │── deliver ────────►│                    │
  │                 │                    │── create room ────►│
  │                 │                    │── create owner ───►│
  │                 │                    │   subscription     │
  │                 │                    │◄── ok ─────────────│
  │◄── reply ───────┼────────────────────│                    │
  │   {room data}   │                    │                    │
```

### 5. Member Invite (Same Site)

```
Client          NATS/JetStream     room-service     room-worker       MongoDB
  │                  │                  │                │                │
  │── request ──────►│                  │                │                │
  │  .member.invite  │── deliver ──────►│                │                │
  │                  │                  │── check owner ─┼───────────────►│
  │                  │                  │── check size  ─┼───────────────►│
  │                  │                  │                │                │
  │◄── reply (ok) ───┼──────────────────│                │                │
  │                  │                  │── publish ────►│                │
  │                  │               ROOMS stream        │                │
  │                  │                                   │                │
  │                  │                  ┌── deliver ─────►│                │
  │                  │                  │                │── create sub ─►│
  │                  │                  │                │── incr count ─►│
  │                  │                  │                │                │
  │                  │◄─────────────────┼── publish ─────│                │
  │                  │  chat.user.{invitee}              │                │
  │                  │  .event.subscription.update        │                │
  │                  │                  │                │                │
  │                  │◄─────────────────┼── publish ─────│                │
  │                  │  chat.user.{member}               │                │
  │                  │  .event.room.metadata.update       │                │
```

### 6. Cross-Site Member Invite (Federation)

```
SITE A                                           SITE B
─────────────────────────────────────────────────────────────────────────
Client     room-worker    OUTBOX_A     INBOX_B     inbox-worker   MongoDB_B
  │            │              │           │              │            │
  │ (invite    │              │           │              │            │
  │  flow as   │              │           │              │            │
  │  above)    │              │           │              │            │
  │            │              │           │              │            │
  │            │── publish ──►│           │              │            │
  │            │  outbox.A    │           │              │            │
  │            │  .to.B       │           │              │            │
  │            │  .member_added           │              │            │
  │            │              │           │              │            │
  │            │              │──Source──►│              │            │
  │            │              │  (NATS    │              │            │
  │            │              │  JetStream│              │            │
  │            │              │  Sources) │── deliver ──►│            │
  │            │              │           │              │            │
  │            │              │           │              │── create  ►│
  │            │              │           │              │   sub      │
  │            │              │           │              │            │
  │            │              │           │◄── publish ──│            │
  │            │              │           │  chat.user.{invitee}      │
  │            │              │           │  .event.subscription      │
  │            │              │           │  .update                  │
```

### 7. Message History Query

```
Client            NATS           history-service     MongoDB     Cassandra
  │                 │                   │               │             │
  │── request ─────►│                   │               │             │
  │  .msg.history   │── deliver ───────►│               │             │
  │  {before,limit} │                   │── validate ──►│             │
  │                 │                   │  subscription  │             │
  │                 │                   │◄── ok ────────│             │
  │                 │                   │                             │
  │                 │                   │── query ──────────────────►│
  │                 │                   │  (room, before, limit)     │
  │                 │                   │◄── messages ──────────────│
  │                 │                   │                             │
  │◄── reply ───────┼───────────────────│                             │
  │  {messages[],   │                   │                             │
  │   hasMore}      │                   │                             │
```

---

## Data Flow Summary

```
                           ┌──────────────────┐
                           │     Client       │
                           └────┬────┬────┬───┘
                     send msg   │    │    │  request/reply
                                │    │    │  (rooms, history)
                                ▼    │    ▼
                         ┌───────┐   │   ┌─────────────────┐
                         │MESSAGES│   │   │  room-service   │
                         │stream │   │   │  history-service │
                         └───┬───┘   │   └────────┬────────┘
                             │       │            │
                             ▼       │            ▼
                      ┌──────────────┤     ┌────────────┐
                      │message-worker│     │ROOMS stream│
                      └──────┬───────┘     └─────┬──────┘
                             │                   │
                             ▼                   ▼
                      ┌─────────────┐     ┌─────────────┐
                      │FANOUT stream│     │ room-worker  │──────┐
                      └──┬──────┬───┘     └─────────────┘      │
                         │      │                          cross-site?
                         ▼      ▼                               │
              ┌────────────┐ ┌───────────────────┐              ▼
              │ broadcast- │ │notification-worker│       ┌─────────────┐
              │ worker     │ └───────────────────┘       │OUTBOX stream│
              └────────────┘                             └──────┬──────┘
                    │                                           │
                    ▼                                    JetStream Sources
           room member streams                                  │
           & notifications                                      ▼
                    │                                    ┌─────────────┐
                    ▼                                    │INBOX stream │
               ┌─────────┐                              │(remote site)│
               │ Clients  │                              └──────┬──────┘
               └──────────┘                                     │
                                                                ▼
                                                         ┌─────────────┐
                                                         │inbox-worker │
                                                         └─────────────┘
```

---

## Database Responsibilities

| Database | Stores | Accessed By |
|----------|--------|-------------|
| **MongoDB** | Rooms, Subscriptions, Messages (operational) | message-worker, broadcast-worker, notification-worker, room-service, room-worker, history-service, inbox-worker |
| **Cassandra** | Messages (time-series history) | message-worker (write), history-service (read) |

## Key Design Decisions

1. **Dual persistence**: Messages go to both MongoDB (operational queries) and Cassandra (time-series history with efficient pagination)
2. **Fanout separation**: message-worker handles persistence; broadcast-worker and notification-worker handle delivery independently from the same FANOUT stream
3. **Async invites**: room-service validates and publishes to ROOMS stream; room-worker processes asynchronously, enabling cross-site federation
4. **Outbox/Inbox federation**: Sites are loosely coupled. Each writes to its own OUTBOX; other sites pull via JetStream Sources into their INBOX
5. **Per-user NATS subjects**: Auth scoping ensures users can only publish to their own namespace while subscribing to rooms they belong to
