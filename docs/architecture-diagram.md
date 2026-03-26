# Architecture Diagram

## System Overview

```mermaid
graph TB
    subgraph Clients
        C1[Client A]
        C2[Client B]
        C3[Client C - Remote Site]
    end

    subgraph "NATS Server"
        AUTH[Auth Callout]
        subgraph "JetStream Streams"
            MESSAGES["MESSAGES_{siteID}"]
            FANOUT["FANOUT_{siteID}"]
            ROOMS["ROOMS_{siteID}"]
            OUTBOX["OUTBOX_{siteID}"]
            INBOX["INBOX_{siteID}"]
        end
        RR["Request/Reply Subjects"]
    end

    subgraph "Services"
        AS[auth-service]
        MW[message-worker]
        BW[broadcast-worker]
        NW[notification-worker]
        RS[room-service]
        RW[room-worker]
        HS[history-service]
        IW[inbox-worker]
    end

    subgraph "Databases"
        MONGO[(MongoDB)]
        CASS[(Cassandra)]
    end

    subgraph "Remote Site"
        REMOTE_OUTBOX["Remote OUTBOX"]
    end

    %% Auth flow
    C1 & C2 & C3 -->|connect| AUTH
    AUTH --- AS

    %% Message flow
    C1 -->|publish msg| MESSAGES
    MESSAGES -->|consume| MW
    MW -->|publish event| FANOUT
    MW -->|reply| C1
    FANOUT -->|consume| BW
    FANOUT -->|consume| NW
    BW -->|publish to room/user stream| C1 & C2
    NW -->|publish notification| C2

    %% Room flow
    C1 -->|request/reply| RR
    RR --- RS
    RR --- HS
    RS -->|publish invite| ROOMS
    ROOMS -->|consume| RW

    %% Federation
    RW -->|cross-site event| OUTBOX
    REMOTE_OUTBOX -.->|JetStream source| INBOX
    INBOX -->|consume| IW

    %% Database connections
    MW --- MONGO
    MW --- CASS
    BW --- MONGO
    NW --- MONGO
    RS --- MONGO
    RW --- MONGO
    HS --- MONGO
    HS --- CASS
    IW --- MONGO
```

## Message Flow (Detailed)

```mermaid
sequenceDiagram
    participant Client as Client
    participant NATS as NATS (MESSAGES stream)
    participant MW as message-worker
    participant FANOUT as NATS (FANOUT stream)
    participant BW as broadcast-worker
    participant NW as notification-worker
    participant Mongo as MongoDB
    participant Cass as Cassandra
    participant Members as Room Members

    Client->>NATS: chat.user.{uid}.room.{rid}.{site}.msg.send
    NATS->>MW: consume message
    MW->>Mongo: verify subscription
    MW->>Cass: save message
    MW->>Mongo: update room lastMessageAt
    MW->>FANOUT: fanout.{siteID}.{roomID} (MessageEvent)
    MW-->>Client: chat.user.{uid}.response.{reqID}

    par Broadcast
        FANOUT->>BW: consume fanout event
        BW->>Mongo: get room (type, members)
        alt Group Room
            BW->>Members: chat.room.{rid}.stream.msg
        else DM Room
            BW->>Members: chat.user.{uid}.stream.msg (per member)
        end
        BW->>Members: chat.room.{rid}.event.metadata.update
    and Notifications
        FANOUT->>NW: consume fanout event
        NW->>Mongo: list room subscriptions
        NW->>Members: chat.user.{uid}.notification (each non-sender)
    end
```

## Room Invitation & Federation Flow

```mermaid
sequenceDiagram
    participant Client as Inviter
    participant RS as room-service
    participant ROOMS as NATS (ROOMS stream)
    participant RW as room-worker
    participant OUTBOX as NATS (OUTBOX stream)
    participant INBOX as Remote INBOX stream
    participant IW as inbox-worker (remote)
    participant Mongo as MongoDB

    Client->>RS: request invite (NATS req/reply)
    RS->>Mongo: verify inviter is owner
    RS->>Mongo: check room capacity
    RS->>ROOMS: publish InviteMemberRequest

    ROOMS->>RW: consume invite
    RW->>Mongo: create subscription
    RW->>Mongo: increment userCount
    RW-->>Client: subscription.update event
    RW-->>Client: room.metadata.update event

    alt Cross-Site Invitee
        RW->>OUTBOX: outbox.{site}.to.{dest}.member_added
        Note over OUTBOX,INBOX: JetStream Source replication
        OUTBOX-->>INBOX: replicated event
        INBOX->>IW: consume OutboxEvent
        IW->>Mongo: create remote subscription
        IW->>IW: publish subscription.update
    end
```

## Service-Database Matrix

| Service | MongoDB | Cassandra | NATS Pattern |
|---------|---------|-----------|-------------|
| **auth-service** | - | - | Auth callout |
| **message-worker** | subscriptions, rooms | messages | Consumer (MESSAGES) |
| **broadcast-worker** | subscriptions, rooms | - | Consumer (FANOUT) |
| **notification-worker** | subscriptions | - | Consumer (FANOUT) |
| **room-service** | rooms, subscriptions | - | Request/Reply (Queue) |
| **room-worker** | rooms, subscriptions | - | Consumer (ROOMS) |
| **history-service** | subscriptions | messages | Request/Reply (Queue) |
| **inbox-worker** | rooms, subscriptions | - | Consumer (INBOX) |

## JetStream Streams

| Stream | Subject Pattern | Consumers |
|--------|----------------|-----------|
| `MESSAGES_{siteID}` | `chat.user.*.room.*.{siteID}.msg.>` | message-worker |
| `FANOUT_{siteID}` | `fanout.{siteID}.>` | broadcast-worker, notification-worker |
| `ROOMS_{siteID}` | `chat.user.*.request.room.*.{siteID}.member.>` | room-worker |
| `OUTBOX_{siteID}` | `outbox.{siteID}.>` | Remote INBOX (via Source) |
| `INBOX_{siteID}` | *(sourced from remote OUTBOX)* | inbox-worker |
