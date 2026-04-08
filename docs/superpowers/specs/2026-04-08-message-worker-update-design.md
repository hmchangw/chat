# Message Worker Update Design

## Overview

Update `message-worker` to write the full `messages_by_room` Cassandra schema (and `thread_messages_by_room` for thread replies), enriched with employee metadata from MongoDB, with NAK+backoff resilience for Cassandra failures.

## Context

The current `message-worker` consumes `MessageEvent` from the `MESSAGES_CANONICAL` stream and writes only 5 columns (`room_id`, `created_at`, `id`, `user_id`, `content`) to a legacy `messages` table. The production Cassandra schema (`messages_by_room`) has 22 columns including UDTs (`Participant`, `File`, `Card`, etc.). The worker needs to be updated to write the full schema and enrich the sender with employee metadata from MongoDB.

## Data Flow

```
MESSAGES_CANONICAL stream (MessageEvent)
        |
        v
  message-worker
        |
        v
  1. Unmarshal MessageEvent
        |
        v
  2. MongoDB: FindOne employee by UserAccount
     (soft failure: log warning, use UserAccount as fallback for names)
        |
        v
  3. Build sender Participant UDT from event fields + employee data
        |
        v
  4. Write to messages_by_room (always)
     Write to thread_messages_by_room (if ThreadParentMessageID is set)
        |
        v
  5. ACK on success / NAK with exponential backoff on Cassandra failure
```

## Input: MessageEvent from MESSAGES_CANONICAL

```go
// pkg/model/event.go
type MessageEvent struct {
    Message Message `json:"message"`
    SiteID  string  `json:"siteId"`
}

// pkg/model/message.go
type Message struct {
    ID                           string
    RoomID                       string
    UserID                       string
    UserAccount                  string
    Content                      string
    CreatedAt                    time.Time
    ThreadParentMessageID        string     // empty if not a thread reply
    ThreadParentMessageCreatedAt *time.Time // nil if not a thread reply
}
```

## Field Mapping: MessageEvent to Cassandra

### messages_by_room

| Cassandra column | Source | Notes |
|---|---|---|
| `room_id` | `evt.Message.RoomID` | Partition key |
| `created_at` | `evt.Message.CreatedAt` | Clustering key |
| `message_id` | `evt.Message.ID` | Clustering key |
| `sender` | Employee lookup + event | Participant UDT (see below) |
| `msg` | `evt.Message.Content` | |
| `thread_parent_id` | `evt.Message.ThreadParentMessageID` | Empty string if not a thread reply |
| `thread_parent_created_at` | `evt.Message.ThreadParentMessageCreatedAt` | Nil if not a thread reply |
| `site_id` | `evt.SiteID` | |
| `tshow` | `false` | Not available in event yet |
| `deleted` | `false` | New message is not deleted |
| `unread` | `true` | New message defaults to unread |
| `updated_at` | `evt.Message.CreatedAt` | Same as created_at on creation |
| `target_user` | `nil` | Not available in event yet |
| `mentions` | `nil` | Not available in event yet |
| `attachments` | `nil` | Not available in event yet |
| `file` | `nil` | Not available in event yet |
| `card` | `nil` | Not available in event yet |
| `card_action` | `nil` | Not available in event yet |
| `quoted_parent_message` | `nil` | Not available in event yet |
| `visible_to` | `""` | Not available in event yet |
| `reactions` | `nil` | Not available in event yet |
| `type` | `""` | Not available in event yet |
| `sys_msg_data` | `nil` | Not available in event yet |
| `edited_at` | `nil` | Not applicable on creation |

### sender Participant UDT

| UDT field | Source |
|---|---|
| `id` | `evt.Message.UserID` |
| `eng_name` | `employee.EngName` (fallback: `UserAccount`) |
| `company_name` | `employee.Name` (fallback: `UserAccount`) |
| `account` | `evt.Message.UserAccount` |
| `app_id` | `""` — not available |
| `app_name` | `""` — not available |
| `is_bot` | `false` — not available |

### thread_messages_by_room (only when ThreadParentMessageID is set)

Same columns as `messages_by_room`, plus:

| Cassandra column | Source |
|---|---|
| `thread_room_id` | `evt.Message.ThreadParentMessageID` |
| `thread_message_id` | `evt.Message.ID` |

## MongoDB Employee Lookup

Follows the established pattern from `broadcast-worker/store_mongo.go`.

- **Collection:** `employee`
- **Query:** `FindOne` by `accountName` matching `evt.Message.UserAccount`
- **Projection:** `accountName`, `name`, `engName`
- **Model:** `model.Employee{AccountName, Name, EngName}` (existing in `pkg/model/event.go`)
- **Failure handling:** Soft failure — log warning, continue with `UserAccount` as fallback for both `eng_name` and `company_name` in the Participant UDT. Message persistence must not be blocked by metadata lookup failure.

## Resilience: NAK with Exponential Backoff

When a Cassandra write fails, the message is NAK'd with an increasing delay to avoid hammering the database during an outage.

**Backoff schedule** (based on JetStream delivery attempt count via message metadata):
- Attempt 1: NAK with 1s delay
- Attempt 2: 2s delay
- Attempt 3: 4s delay
- Attempt 4: 8s delay
- Attempt 5: 16s delay
- Attempt 6+: capped at 30s delay

**Implementation:** Use `msg.NakWithDelay(delay)` with delay calculated from `msg.Headers().Get("Nats-Num-Delivered")` or message metadata. No `MaxDeliver` limit — messages retry indefinitely until Cassandra recovers.

**MongoDB failure does NOT trigger NAK** — the message is written to Cassandra with incomplete sender metadata. Only Cassandra write failures cause retry.

## Operational Playbook: Worst-Case Cassandra Outage

If Cassandra is down for an extended period, unacked messages accumulate in the MESSAGES_CANONICAL stream. Operators have several manual levers:

### Preventive
- **Monitor stream usage** — NATS HTTP monitoring port (8222) exposes stream stats. Alert on percentage full (warn at 70%, critical at 90%).
- **Increase stream limits on the fly** — `MaxBytes` and `MaxMsgs` can be updated via `js.CreateOrUpdateStream()` without restarting. NATS applies changes immediately.

### During an outage
- **Pause the consumer** — Stop message-worker so messages accumulate in the stream without redelivery churn. Once Cassandra is back, restart the worker and it drains from where it left off.
- **Scale stream storage** — If on disk-backed JetStream (`FileStorage`), expand the volume and update `MaxBytes`.

### Last resort
- **Mirror/copy the stream** — Create a secondary stream that sources from the original, effectively doubling buffer capacity.
- **Export messages** — Use `nats` CLI to dump stream contents to a file for manual replay later.

## Interfaces

### Store (Cassandra operations)

```go
type Store interface {
    SaveMessage(ctx context.Context, msg CassandraMessage) error
    SaveThreadMessage(ctx context.Context, msg CassandraMessage) error
}
```

`CassandraMessage` is a struct internal to `message-worker` that maps 1:1 to the `messages_by_room` columns plus the extra thread columns. It uses the UDT types from `history-service/internal/models/types.go` (Participant, File, Card, CardAction).

### MetadataStore (MongoDB operations)

```go
type MetadataStore interface {
    FindEmployeeByAccount(ctx context.Context, account string) (*model.Employee, error)
}
```

Single method, single query. Uses the existing `model.Employee` type and the `employee` MongoDB collection, consistent with `broadcast-worker`.

## Config Changes

```go
type config struct {
    NatsURL           string `env:"NATS_URL,required"`
    SiteID            string `env:"SITE_ID,required"`
    CassandraHosts    string `env:"CASSANDRA_HOSTS"    envDefault:"localhost"`
    CassandraKeyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
    MongoURI          string `env:"MONGO_URI"          envDefault:"mongodb://localhost:27017"`
    MongoDB           string `env:"MONGO_DB"           envDefault:"chat"`
    MaxWorkers        int    `env:"MAX_WORKERS"        envDefault:"100"`
}
```

New fields: `MongoURI`, `MongoDB` — same env var names and defaults as broadcast-worker and notification-worker.

## Files Changed

| File | Change |
|---|---|
| `message-worker/main.go` | Add MongoDB connection, new config fields, pass MetadataStore to handler, add MongoDB disconnect to shutdown |
| `message-worker/handler.go` | Add MetadataStore dependency, employee lookup with soft failure, build CassandraMessage, NAK with backoff logic, thread message detection |
| `message-worker/store.go` | Update Store interface (SaveMessage + SaveThreadMessage), add MetadataStore interface, add CassandraMessage struct |
| `message-worker/store_cassandra.go` | Rewrite SaveMessage for full `messages_by_room` schema, add SaveThreadMessage for `thread_messages_by_room` |
| `message-worker/store_mongo.go` | New file — MongoMetadataStore implementation |
| `message-worker/handler_test.go` | Update for new handler signature, add test cases for: thread messages, employee lookup failure (soft), Cassandra failure with backoff |
| `message-worker/integration_test.go` | Update schema setup and assertions for new table structure |
| `message-worker/deploy/docker-compose.yml` | Add MongoDB service |

## Future Enrichment (Not In Scope)

The following `messages_by_room` fields are written as nil/empty because the canonical stream does not carry them yet. Each will require either enriching `MessageEvent` upstream or adding additional lookups in this worker:

- `target_user` — Participant UDT for DM target
- `mentions` — Set of mentioned Participant UDTs
- `attachments` — Binary attachment list
- `file` — File UDT
- `card` — Card UDT
- `card_action` — CardAction UDT
- `quoted_parent_message` — QuotedParentMessage UDT
- `visible_to` — Visibility restriction
- `reactions` — Reaction map
- `type` — Message type (system message, etc.)
- `sys_msg_data` — System message payload
- `tshow` — Thread "also send to channel" flag
