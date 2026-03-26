# Architecture Diagram

## Multi-Site Topology

Each site is fully independent — its own NATS server (with JetStream), MongoDB, and Cassandra.
There is no NATS cluster or supercluster. Cross-site federation uses JetStream stream sourcing
(one-way replication from OUTBOX to remote INBOX).

```mermaid
graph TB
    subgraph "Site A"
        subgraph "NATS Server — Site A"
            direction TB
            NATS_A_CORE["NATS Core<br/>:4222"]
            NATS_A_MON["HTTP Monitor<br/>:8222"]
            NATS_A_AUTH["Auth Callout<br/>Endpoint"]

            subgraph "JetStream Engine — Site A"
                MESSAGES_A["MESSAGES_site-a<br/>chat.user.*.room.*.site-a.msg.>"]
                FANOUT_A["FANOUT_site-a<br/>fanout.site-a.>"]
                ROOMS_A["ROOMS_site-a<br/>chat.user.*.request.room.*.site-a.member.>"]
                OUTBOX_A["OUTBOX_site-a<br/>outbox.site-a.>"]
                INBOX_A["INBOX_site-a<br/>(no local subjects — sourced from remote)"]
            end

            RR_A["Core NATS Subjects<br/>Request/Reply + Pub/Sub"]
        end

        AS_A[auth-service]
        MW_A[message-worker]
        BW_A[broadcast-worker]
        NW_A[notification-worker]
        RS_A[room-service]
        RW_A[room-worker]
        HS_A[history-service]
        IW_A[inbox-worker]

        MONGO_A[(MongoDB)]
        CASS_A[(Cassandra)]

        %% Service → NATS connections
        AS_A --- NATS_A_AUTH
        MW_A --- MESSAGES_A
        MW_A --- FANOUT_A
        BW_A --- FANOUT_A
        NW_A --- FANOUT_A
        RS_A --- RR_A
        RS_A --- ROOMS_A
        RW_A --- ROOMS_A
        RW_A --- OUTBOX_A
        HS_A --- RR_A
        IW_A --- INBOX_A

        %% Service → DB connections
        MW_A --- MONGO_A
        MW_A --- CASS_A
        BW_A --- MONGO_A
        NW_A --- MONGO_A
        RS_A --- MONGO_A
        RW_A --- MONGO_A
        HS_A --- MONGO_A
        HS_A --- CASS_A
        IW_A --- MONGO_A
    end

    subgraph "Site B"
        subgraph "NATS Server — Site B"
            direction TB
            NATS_B_CORE["NATS Core<br/>:4222"]
            NATS_B_AUTH["Auth Callout<br/>Endpoint"]

            subgraph "JetStream Engine — Site B"
                OUTBOX_B["OUTBOX_site-b<br/>outbox.site-b.>"]
                INBOX_B["INBOX_site-b<br/>(sourced from Site A OUTBOX)"]
            end
        end

        AS_B[auth-service]
        IW_B[inbox-worker]
        SERVICES_B["... all other services"]

        MONGO_B[(MongoDB)]
        CASS_B[(Cassandra)]

        AS_B --- NATS_B_AUTH
        IW_B --- INBOX_B
        IW_B --- MONGO_B
        SERVICES_B --- NATS_B_CORE
        SERVICES_B --- MONGO_B
        SERVICES_B --- CASS_B
    end

    %% Cross-site federation (JetStream Source — NOT cluster routes)
    OUTBOX_A ==>|"JetStream Source<br/>(one-way replication)"| INBOX_B
    OUTBOX_B ==>|"JetStream Source<br/>(one-way replication)"| INBOX_A

    %% Clients
    CLIENT_A["Client (Site A user)"] -->|"connect(token)"| NATS_A_CORE
    CLIENT_B["Client (Site B user)"] -->|"connect(token)"| NATS_B_CORE

    style OUTBOX_A fill:#f96,stroke:#333
    style INBOX_B fill:#f96,stroke:#333
    style OUTBOX_B fill:#69f,stroke:#333
    style INBOX_A fill:#69f,stroke:#333
```

## NATS Auth Callout Flow

When a client connects, the NATS server delegates authentication to auth-service
via the auth_callout mechanism. The service verifies the SSO token and returns a
signed JWT that scopes the client's publish/subscribe permissions.

```mermaid
sequenceDiagram
    participant Client
    participant NATS as NATS Server
    participant AS as auth-service
    participant SSO as SSO/IdP (TODO)

    Note over Client: Holds SSO token from<br/>external identity provider

    Client->>NATS: CONNECT {token: "sso-token-xyz"}
    Note over NATS: Auth callout triggered —<br/>connection is held pending

    NATS->>AS: AuthorizationRequest<br/>{UserNkey, ConnectOptions.Token}

    AS->>SSO: Verify SSO token
    SSO-->>AS: username = "alice"

    Note over AS: Build UserClaims JWT:<br/>• Subject = UserNkey<br/>• Audience = "$G"<br/>• Expires = now + 2h<br/>• Pub.Allow: chat.user.alice.>, _INBOX.><br/>• Sub.Allow: chat.user.alice.>, chat.room.>, _INBOX.>

    AS->>AS: Sign JWT with account NKey

    AS-->>NATS: Signed User JWT

    Note over NATS: Apply JWT permissions<br/>to client connection

    NATS-->>Client: +OK (connected)

    Note over Client: Can now publish/subscribe<br/>within scoped permissions
```

### Permission Boundaries After Auth

```mermaid
graph LR
    subgraph "Client 'alice' — Allowed"
        direction TB
        PUB_OK["PUB chat.user.alice.room.R1.site-a.msg.send<br/>PUB chat.user.alice.request.rooms.create<br/>PUB chat.user.alice.request.rooms.list<br/>PUB _INBOX.>"]
        SUB_OK["SUB chat.user.alice.response.*<br/>SUB chat.user.alice.stream.msg<br/>SUB chat.user.alice.notification<br/>SUB chat.user.alice.event.><br/>SUB chat.room.R1.stream.msg<br/>SUB chat.room.R1.event.><br/>SUB _INBOX.>"]
    end

    subgraph "Client 'alice' — Denied"
        direction TB
        PUB_NO["PUB chat.user.bob.> ✗<br/>PUB fanout.> ✗<br/>PUB outbox.> ✗<br/>PUB chat.room.> ✗"]
        SUB_NO["SUB chat.user.bob.> ✗<br/>SUB fanout.> ✗<br/>SUB outbox.> ✗"]
    end

    style PUB_OK fill:#cfc,stroke:#393
    style SUB_OK fill:#cfc,stroke:#393
    style PUB_NO fill:#fcc,stroke:#933
    style SUB_NO fill:#fcc,stroke:#933
```

### Auth Security Model

| Layer | Mechanism | Scope |
|-------|-----------|-------|
| **Transport** | NATS auth_callout | Every connection must present a valid SSO token |
| **Identity** | SSO token → username | Verified by `TokenVerifier` (stub — TODO) |
| **NATS Permissions** | JWT `Pub.Allow` / `Sub.Allow` | User can only publish to own `chat.user.{self}.>` namespace |
| **Room-Level Access** | Application layer (subscription check) | Services verify user is a room member before processing |
| **Cross-Site** | JetStream Source (no direct client access) | Clients never touch OUTBOX/INBOX — only services publish there |

**Key design choice:** NATS permissions enforce *user identity isolation* (alice can't impersonate bob),
but *room-level authorization* is handled by the application (subscription checks in message-worker,
history-service, room-service). Any authenticated user can subscribe to `chat.room.>` at the NATS level,
but services reject requests from non-members.

## System Overview (Single Site)

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
