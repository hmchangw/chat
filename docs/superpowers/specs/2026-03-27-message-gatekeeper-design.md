# Message Gatekeeper Redesign

## Overview

Introduce a new `message-gatekeeper` microservice that sits between client message submissions and downstream processing. The gatekeeper consumes from the existing `MESSAGES` stream, validates the message (UUID format, content size, room membership), and publishes validated events to a new `MESSAGES_CANONICAL` stream. All downstream workers (message-worker, broadcast-worker, notification-worker) consume from this single source of truth, eliminating the FANOUT stream entirely.

## Motivation

- **Single validation boundary**: Currently, message-worker validates subscriptions, and broadcast-worker implicitly trusts FANOUT events. The gatekeeper centralizes validation so downstream workers can trust their input.
- **Simplified downstream workers**: message-worker becomes a pure Cassandra persistence service. broadcast-worker and notification-worker switch their source stream but keep their logic unchanged.
- **Client-generated message IDs**: Enables idempotent retries and offline-first clients. The gatekeeper validates UUID format and uses it for JetStream deduplication.
- **Eliminate FANOUT stream**: One fewer stream to manage. All downstream workers share a single source.

## Architecture

### Event Flow (New)

```
Client
  │
  ▼ publish SendMessageRequest
MESSAGES_{siteID} stream
  subject: chat.user.{username}.room.{roomID}.{siteID}.msg.send
  │
  ▼ consume
message-gatekeeper
  │  ├─ validate UUID, content, subscription
  │  ├─ reply success/failure to client
  │  └─ publish MessageEvent
  ▼
MESSAGES_CANONICAL_{siteID} stream
  subject: chat.msg.canonical.{siteID}.created
  │
  ├──────────────────┼──────────────────┐
  ▼                  ▼                  ▼
message-worker   broadcast-worker   notification-worker
(Cassandra)      (room events)      (user notifications)
```

### Event Flow (Old, for reference)

```
Client → MESSAGES → message-worker → FANOUT → broadcast-worker
                         │                  → notification-worker
                         └─ reply to client
```

## Stream Definitions

### New: MESSAGES_CANONICAL_{siteID}

| Field    | Value                           |
|----------|---------------------------------|
| Name     | `MESSAGES_CANONICAL_{siteID}`         |
| Subjects | `["chat.msg.canonical.{siteID}.>"]`  |

The `>` wildcard allows future action subjects (`.edited`, `.deleted`) alongside `.created`.

### Removed: FANOUT_{siteID}

The FANOUT stream, its subject builders (`Fanout`, `FanoutWildcard`), and all references are removed.

### Unchanged

- `MESSAGES_{siteID}` — still captures client message submissions
- `ROOMS_{siteID}`, `OUTBOX_{siteID}`, `INBOX_{siteID}` — unaffected

## Subject Definitions

### New Subject Builders (pkg/subject)

| Function                    | Output                               |
|-----------------------------|--------------------------------------|
| `MsgCanonicalCreated(siteID)`    | `chat.msg.canonical.{siteID}.created`     |
| `MsgCanonicalWildcard(siteID)`   | `chat.msg.canonical.{siteID}.>`           |

### Removed Subject Builders

| Function                    | Previously                           |
|-----------------------------|--------------------------------------|
| `Fanout(siteID, roomID)`    | `fanout.{siteID}.{roomID}`           |
| `FanoutWildcard(siteID)`    | `fanout.{siteID}.>`                  |

## Model Changes

### SendMessageRequest (modified)

```go
type SendMessageRequest struct {
    ID        string `json:"id"`        // Client-generated UUID
    Content   string `json:"content"`
    RequestID string `json:"requestId"`
}
```

- **Added**: `ID` — client-generated UUID, validated by gatekeeper
- **Removed**: `RoomID` — extracted from NATS subject instead

### Message (modified)

```go
type Message struct {
    ID        string    `json:"id"        bson:"_id"`
    RoomID    string    `json:"roomId"    bson:"roomId"`
    UserID    string    `json:"userId"    bson:"userId"`
    Username  string    `json:"username"  bson:"username"`
    Content   string    `json:"content"   bson:"content"`
    CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
```

- **Added**: `Username` — needed by broadcast-worker (sender display), notification-worker (notification text), and Cassandra persistence (maps to `sender.user_name` in `Participant` UDT)

### MessageEvent (modified)

```go
type MessageEvent struct {
    Message Message `json:"message"`
    SiteID  string  `json:"siteId"`
}
```

- **Removed**: `RoomID` — already present in `Message.RoomID`, no need to duplicate

Published to `MESSAGES_CANONICAL` by the gatekeeper. The `Message` inside contains:
- `ID` — from client (validated UUID)
- `RoomID` — from NATS subject
- `UserID` — resolved from subscription lookup
- `Username` — resolved from subscription lookup
- `Content` — from request payload
- `CreatedAt` — set by gatekeeper (canonical server timestamp)

### Cassandra Mapping (message-worker internal)

The JetStream `Message` is a lean domain type. The message-worker maps it to the richer Cassandra `Participant` UDT when persisting:

| Message field | Cassandra column |
|---------------|-----------------|
| `ID`          | `message_id`    |
| `RoomID`      | `room_id`       |
| `UserID`      | `sender.id`     |
| `Username`    | `sender.user_name` |
| `Content`     | `msg`           |
| `CreatedAt`   | `created_at`    |

Other `Participant` fields (`eng_name`, `company_name`, `app_id`, `app_name`, `is_bot`) are not populated in the initial message send flow.

## Message Gatekeeper Service

### File Structure

```
message-gatekeeper/
├── main.go
├── handler.go
├── handler_test.go
├── store.go
├── store_mongo.go
├── mock_store_test.go
├── integration_test.go
└── deploy/
    ├── Dockerfile
    ├── docker-compose.yml
    └── azure-pipelines.yml
```

### Store Interface

```go
type Store interface {
    GetSubscription(ctx context.Context, username, roomID string) (*model.Subscription, error)
}
```

Single method. Returns the full `Subscription` to extract `UserID` for the message.

### Handler

```go
type Handler struct {
    store   Store
    publish func(ctx context.Context, subject string, data []byte, msgID string) error
    siteID  string
}
```

- `publish` is injected so tests can capture published data without a real NATS connection
- `siteID` used to verify the extracted siteID from the subject matches this instance's config

### Processing Flow

1. Parse subject via `subject.ParseUserRoomSiteSubject` to extract `username`, `roomID`, `siteID`
1a. **Validate siteID**: verify extracted `siteID` matches `handler.siteID` — reject if mismatched (guard against misconfigured streams)
2. Unmarshal payload to `SendMessageRequest`
3. **Validate ID**: `uuid.Parse(req.ID)` — must be a valid UUID
4. **Validate Content**: non-empty and `len([]byte(req.Content)) <= 20480` (20KB)
5. **Validate subscription**: `store.GetSubscription(ctx, username, roomID)` — confirms membership, retrieves `UserID`
6. **Build Message**: `{ID: req.ID, RoomID: roomID, UserID: sub.User.ID, Username: username, Content: req.Content, CreatedAt: time.Now()}`
7. **Publish to MESSAGES_CANONICAL**: subject `chat.msg.canonical.{siteID}.created`, payload `MessageEvent{Message, SiteID}`, header `Nats-Msg-Id: message.ID`
8. **Reply success**: `natsutil.ReplyJSON(msg, message)` to `chat.user.{username}.response.{requestID}`
9. On any validation failure: `natsutil.ReplyError(msg, "<description>")`, ack the JetStream message (validation failures must not redeliver)

### Consumer Pattern

High-throughput: `cons.Messages()` + channel-based semaphore (`chan struct{}` sized by `cfg.MaxWorkers`) + `sync.WaitGroup`. This is the hot path for all user messages.

### Config

```go
type Config struct {
    NatsURL    string `env:"NATS_URL"    envRequired:"true"`
    MongoURI   string `env:"MONGO_URI"   envRequired:"true"`
    MongoDB    string `env:"MONGO_DB"    envDefault:"chat"`
    SiteID     string `env:"SITE_ID"     envRequired:"true"`
    MaxWorkers int    `env:"MAX_WORKERS" envDefault:"100"`
}
```

## Downstream Service Refactors

### message-worker

| Aspect | Before | After |
|--------|--------|-------|
| Source stream | `MESSAGES_{siteID}` | `MESSAGES_CANONICAL_{siteID}` |
| Payload | `SendMessageRequest` | `MessageEvent` |
| Subscription validation | Yes (MongoDB) | No |
| UUID generation | Yes | No |
| CreatedAt assignment | Yes | No |
| FANOUT publishing | Yes | No |
| Client reply | Yes | No |
| Cassandra persistence | Yes | Yes |

**Simplified handler flow:**
1. Unmarshal `MessageEvent` from MESSAGES_CANONICAL
2. Save `event.Message` to Cassandra
3. Ack

**Store interface shrinks:**
```go
type Store interface {
    SaveMessage(ctx context.Context, message model.Message) error
}
```

- `GetSubscription` removed (no validation)

**Removed dependencies:**
- MongoDB connection removed entirely
- NATS publish function injection removed (no FANOUT)

### broadcast-worker

| Aspect | Before | After |
|--------|--------|-------|
| Source stream | `FANOUT_{siteID}` | `MESSAGES_CANONICAL_{siteID}` |
| Payload | `MessageEvent` | `MessageEvent` (same) |
| Consumer name | Updated to reflect new stream |
| All broadcast logic | Unchanged | Unchanged |

Store interface unchanged — still needs rooms and subscriptions from MongoDB for mention processing and event delivery.

### notification-worker

| Aspect | Before | After |
|--------|--------|-------|
| Source stream | `FANOUT_{siteID}` | `MESSAGES_CANONICAL_{siteID}` |
| Payload | `MessageEvent` | `MessageEvent` (same) |
| Consumer name | Updated to reflect new stream |
| All notification logic | Unchanged | Unchanged |

Store interface unchanged — still needs subscriptions from MongoDB for subscriber lookup.

## FANOUT Removal Checklist

1. Remove `FANOUT_{siteID}` stream config from `pkg/stream/stream.go`
2. Remove `Fanout(siteID, roomID)` from `pkg/subject`
3. Remove `FanoutWildcard(siteID)` from `pkg/subject`
4. Remove all FANOUT publishing code from message-worker
5. Remove FANOUT consumer setup from broadcast-worker and notification-worker
6. Grep for any remaining `fanout` or `FANOUT` references and clean up
7. Update CLAUDE.md: event flow, stream list, subject naming, service descriptions

## CLAUDE.md Updates

- **Event flow**: Update to reflect gatekeeper in the pipeline
- **JetStream Streams**: Add `MESSAGES_CANONICAL_{siteID}`, remove `FANOUT_{siteID}`
- **Subject Naming**: Add `chat.msg.canonical.{siteID}.created` pattern
- **Service descriptions**: Add message-gatekeeper, update message-worker description

## Error Handling

| Error | Gatekeeper Action |
|-------|-------------------|
| Invalid UUID | Reply error, ack message |
| Empty content | Reply error, ack message |
| Content > 20KB | Reply error, ack message |
| User not in room | Reply error, ack message |
| MongoDB unavailable | Nack/retry (transient) |
| MESSAGES_CANONICAL publish fails | Nack/retry (transient) |
| SiteID mismatch | Log error, ack (misconfigured) |
| Subject parse failure | Log error, ack (malformed) |
| JSON unmarshal failure | Log error, ack (malformed) |

Validation failures are terminal — ack the message to prevent redelivery. Infrastructure failures (DB down, publish failure) are transient — nack to allow retry.

## Testing Strategy

### message-gatekeeper

- **Unit tests** (handler_test.go): Table-driven tests covering:
  - Valid message (happy path)
  - Invalid UUID format
  - Empty content
  - Content exceeding 20KB
  - User not in room (subscription not found)
  - Store error (MongoDB failure)
  - Publish error
  - Malformed JSON payload
  - SiteID mismatch
  - Subject parse failure
- **Integration tests**: testcontainers with MongoDB + NATS

### message-worker (updated)

- **Unit tests**: Update to use `MessageEvent` payload instead of `SendMessageRequest`
- Remove tests for subscription validation, FANOUT publishing, and client reply
- Keep Cassandra persistence tests

### broadcast-worker / notification-worker

- **Unit tests**: Minimal changes — update stream/consumer references in test setup
- Logic tests unchanged since `MessageEvent` payload structure is the same
