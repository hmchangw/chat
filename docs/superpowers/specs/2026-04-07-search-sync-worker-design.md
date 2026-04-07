# Design: JetStream to Elasticsearch Search Sync

## Context

The chat system currently syncs messages to Cassandra for history but has no full-text search capability. Previously, Elasticsearch indexing was done via Monstache (Mongo CDC). The new architecture replaces CDC with JetStream — a `search-sync-worker` consumes from `MESSAGES_CANONICAL` and bulk-indexes to Elasticsearch with monthly indices.

### Why JetStream over CDC?

1. **Decoupled from DB internals** — no dependency on MongoDB oplog format
2. **Durable consumers** with ack/nack and replay
3. **We define the message schema** — not derived from DB change events
4. **Backfill by replaying** stream from offset/time
5. **Add more consumers** without touching the source

### What this work adds

1. Message update/delete operations through the existing gatekeeper pipeline
2. A new `search-sync-worker` service with batch-flush ES bulk indexing
3. External versioning (`version_type: external`) to handle redeliveries and multi-pod ordering

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| ES index routing key | `CreatedAt` (immutable) | Determines `{prefix}-YYYY-MM` index; never changes even on update |
| Version source | `time.Now().UnixMilli()` stamped as `Timestamp` by gatekeeper | search-sync-worker uses `Timestamp` as ES external version; later event = higher ts |
| Update strategy | Full document replace (`index` action) | Simpler, idempotent; ES does full reindex internally anyway |
| Consumer type | Pull consumer with batch flush | Flow control, backpressure, batch processing for ES bulk API |
| Durable consumer name | `search-sync-worker` | 4th consumer on MESSAGES_CANONICAL (alongside message-worker, broadcast-worker, notification-worker) |
| ES client | `elastic/go-elasticsearch` v8 | Official client, close to REST API, good for bulk operations |
| Index prefix | Configurable via `MSG_INDEX_PREFIX` env | e.g., `messages-site1-v1`; enables versioned index rollover |
| Index template | `dynamic: false`, upserted on startup | Explicit schema control; unknown fields stored but not indexed |
| Template verification | First-message check | Catches stale mapping when template changed but prefix not bumped |
| Index lifecycle | Separate `index-manager` batch job (future) | Reindexing, version rollover, old index cleanup — not in the hot path |

---

## CRUD Event Payload Design

### Event Envelope

All CUD operations publish to `MESSAGES_CANONICAL` using the existing `MessageEvent` struct, extended with two new fields:

```go
type EventType string

const (
    EventCreated EventType = "created"
    EventUpdated EventType = "updated"
    EventDeleted EventType = "deleted"
)

type MessageEvent struct {
    Event     EventType `json:"event"`             // NEW — "created" | "updated" | "deleted"
    Timestamp int64     `json:"ts"`                // NEW — time.Now().UnixMilli(), set by gatekeeper
    Message   Message   `json:"message"`           // existing
    SiteID    string    `json:"siteId"`            // existing
}
```

**Why extend instead of new struct:** Go's `json.Unmarshal` silently ignores unknown fields. Existing workers that don't care about `Event` and `Timestamp` keep working unchanged — zero migration, deploy in any order.

**Why `Timestamp` not `Version`:** The field is a timestamp (`time.Now().UnixMilli()`), not an abstract version counter. The search-sync-worker uses it as the ES external version — higher timestamp = newer write. The index-manager uses `updatedAt` or `createdAt` millis for the same purpose.

### Event payloads

**Create:**
```json
{
  "event": "created",
  "ts": 1737964678390,
  "message": { "id": "msg_abc", "roomId": "...", "userId": "...", "username": "...", "content": "...", "createdAt": "2026-01-15T10:30:00Z" },
  "siteId": "site1"
}
```

**Update:**
```json
{
  "event": "updated",
  "ts": 1737964699000,
  "message": { "id": "msg_abc", "roomId": "...", "userId": "...", "username": "...", "content": "updated content", "createdAt": "2026-01-15T10:30:00Z", "updatedAt": "2026-01-15T10:35:00Z" },
  "siteId": "site1"
}
```

**Delete:**
```json
{
  "event": "deleted",
  "ts": 1737964710000,
  "message": { "id": "msg_abc", "roomId": "...", "createdAt": "2026-01-15T10:30:00Z" },
  "siteId": "site1"
}
```

### Key properties

- `createdAt` is **immutable** — set at creation, determines which monthly index (`{prefix}-YYYY-MM`) the doc lives in forever
- `updatedAt` is **mutable** — lives inside message payload, changes on edit
- For delete, FE sends `messageId` + `createdAt` (it already has the message object rendered on screen)
- `ts` is stamped by gatekeeper, not FE — FE doesn't know about versioning; search-sync-worker uses it as ES external version

---

## Versioning Strategy

### External versioning from day one

The `message-gatekeeper` stamps `ts: time.Now().UnixMilli()` on **every** CUD event. The search-sync-worker uses this `Timestamp` field as the ES external version.

**Why from day one (even with single pod):**
- Protects against JetStream redelivery (ack timeout, pod restart)
- Protects against partial bulk failures followed by redelivery
- No refactoring needed when scaling to multiple pods later
- ES `version_type: external` auto-rejects stale writes

**How it works:**
- Events are processed in order through gatekeeper
- Later event = higher timestamp = higher version
- ES rejects any write with version ≤ current → stale writes silently dropped
- Version conflict (409) is treated as success by the worker (ack, not nak)

---

## Consumer Design

### Batch flush pattern (different from existing workers)

Existing workers (message-worker, broadcast-worker) use a **semaphore + goroutine-per-message** pattern. The search-sync-worker uses a **batch-flush** pattern instead:

```text
┌──────────────────────────────────────────────────────────┐
│                   Batch Consumer Loop                     │
│                                                          │
│  cons.Fetch(n, FetchMaxWait(1s))                        │
│       │                                                  │
│       ▼                                                  │
│  ┌─────────┐    buffer full (500)?    ┌──────────────┐  │
│  │ Buffer   │──── YES ───────────────►│   Flush()    │  │
│  │          │                         │              │  │
│  │          │    5s elapsed?           │  • Build     │  │
│  │          │──── YES ───────────────►│    bulk req  │  │
│  │          │                         │  • POST to   │  │
│  │          │                         │    ES _bulk  │  │
│  └─────────┘                         │  • Ack/Nak   │  │
│                                      │    per item   │  │
│                                      └──────────────┘  │
└──────────────────────────────────────────────────────────┘
```

**Flush triggers** (whichever comes first):
- Buffer reaches 500 messages (batch size threshold)
- 5 seconds elapsed since last flush (time threshold)

**Why both?**
- High throughput → batch size triggers fast flushes
- Low throughput → time threshold ensures no stale data waiting forever
- Idle → no-op (empty buffer)

### Why `Fetch` instead of `Messages()` iterator

The `Messages()` iterator blocks on `Next()`, making it impossible to implement timer-based flushing in a single goroutine. Using `cons.Fetch(n, FetchMaxWait(1s))` allows multiplexing between fetching, timer flushing, and stop signals in a single `select` loop.

### CUD → ES Bulk Action Mapping

| Event | ES Action | Body |
|-------|-----------|------|
| `created` | `index` (with external version) | Full document JSON |
| `updated` | `index` (with external version) | Full document JSON (full replace) |
| `deleted` | `delete` (with external version) | No body |

Single bulk request — ES `_bulk` API accepts mixed indices in one request, so each action specifies its own `_index`.

### After flush — per-item ack/nak

Each buffered message holds a reference to its `jetstream.Msg` for individual ack/nak. After the `_bulk` response, we match each item in the response to its corresponding buffered message by index position:

- **Item succeeded (2xx)** → `msg.Ack()`
- **Item version conflict (409)** → `msg.Ack()` (stale write correctly rejected, not an error)
- **Item failed (other)** → `msg.Nak()` (redelivery with consumer backoff)
- **Total bulk request failure** (ES unreachable) → `msg.Nak()` all items

This is **per-item tracking**, not all-or-nothing. If 490 of 500 succeed and 10 fail, only the 10 failures are nak'd for redelivery.

---

## Multi-Pod Design

### Consumer config

```go
jetstream.ConsumerConfig{
    Durable:   "search-sync-worker",
    AckPolicy: jetstream.AckExplicitPolicy,
    BackOff:   []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
}
```

### Horizontal scaling

- Multiple pods share the same durable consumer name
- JetStream auto-distributes messages across pods
- No subject partitioning needed
- Horizontal throughput scaling out of the box

### Out-of-order risk across pods

- Same `msgId` CUD events could land on different pods
- Solved by external versioning — ES rejects writes with version lower than current
- No data inconsistency possible

---

## ES Index Template Management

### Custom analyzer: underscore-aware search

**Problem:** The `standard` tokenizer treats `_` as a word boundary. Searching for `word_` does NOT match `word_sentence` — only `word` or `sentence` match individually.

**Fix:** Use a `pattern` tokenizer that splits on whitespace and common delimiters but **preserves underscores** as part of tokens:

The full analysis pipeline:
1. **char_filter: `html_strip`** — strips HTML tags first (e.g., `<b>word_sentence</b>` → `word_sentence`)
2. **tokenizer: `underscore_preserving`** — pattern tokenizer splits on whitespace/punctuation, keeps `_` intact
3. **filter: `underscore_subword`** — `word_delimiter_graph` with `preserve_original: true` — splits on `_` to generate sub-tokens but keeps the original token too
4. **filter: `cjk_bigram` + `lowercase`** — CJK language support + lowercasing

For `word_sentence`, this produces three tokens: `word_sentence` (original), `word`, `sentence`.

| Query | Match? | Why |
|-------|--------|-----|
| `word_sentence` | Yes | Exact match on original token |
| `word_*` (wildcard) | Yes | Prefix wildcard on original token |
| `word_` (prefix) | Yes | Prefix of original token |
| `word` | Yes | Matches subword token |
| `sentence` | Yes | Matches subword token |

### Template definition (`dynamic: false`)

Mapping derived from Cassandra `messages_by_room` schema + history-service Message model:

```json
{
  "index_patterns": ["messages-*"],
  "template": {
    "settings": {
      "index": {
        "number_of_shards": 4,
        "number_of_replicas": 2,
        "refresh_interval": "30s"
      },
      "analysis": {
        "analyzer": {
          "custom_analyzer": {
            "type": "custom",
            "tokenizer": "underscore_preserving",
            "filter": ["underscore_subword", "cjk_bigram", "lowercase"],
            "char_filter": ["html_strip"]
          }
        },
        "tokenizer": {
          "underscore_preserving": {
            "type": "pattern",
            "pattern": "[\\s,;!?()\\[\\]{}\"'<>]+"
          }
        },
        "filter": {
          "underscore_subword": {
            "type": "word_delimiter_graph",
            "split_on_case_change": false,
            "split_on_numerics": false,
            "preserve_original": true
          }
        },
        "char_filter": {
          "html_strip": {
            "type": "html_strip"
          }
        }
      }
    },
    "mappings": {
      "dynamic": false,
      "properties": {
        "messageId":              { "type": "keyword" },
        "roomId":                 { "type": "keyword" },
        "siteId":                 { "type": "keyword" },
        "senderUsername":         { "type": "keyword" },
        "senderEngName":          { "type": "keyword" },
        "msg":                    { "type": "text", "analyzer": "custom_analyzer" },
        "attachmentText":         { "type": "text", "analyzer": "custom_analyzer" },
        "cardData":               { "type": "text", "analyzer": "custom_analyzer" },
        "createdAt":              { "type": "date" },
        "updatedAt":              { "type": "date" },
        "editedAt":               { "type": "date" },
        "threadParentId":         { "type": "keyword" },
        "threadParentCreatedAt":  { "type": "date" },
        "tshow":                  { "type": "boolean" },
        "visibleTo":              { "type": "keyword" },
        "deleted":                { "type": "boolean" },
        "type":                   { "type": "keyword" }
      }
    }
  }
}
```

### Field mapping

| Source field | ES field | ES type | Notes |
|---|---|---|---|
| `message_id` | `messageId` | keyword | Unique message ID |
| `room_id` | `roomId` | keyword | Room filter |
| `site_id` | `siteId` | keyword | Site filter |
| `sender.user_name` | `senderUsername` | keyword | Flattened from Participant |
| `sender.eng_name` | `senderEngName` | keyword | Flattened from Participant |
| `msg` | `msg` | text | Message body — primary search field |
| _(preprocessed)_ | `attachmentText` | text | Extracted text from attachments |
| _(preprocessed)_ | `cardData` | text | Extracted text from card data |
| `created_at` | `createdAt` | date | Immutable, determines monthly index |
| `updated_at` | `updatedAt` | date | Last update timestamp |
| `edited_at` | `editedAt` | date | Message edit timestamp |
| `thread_parent_id` | `threadParentId` | keyword | Parent thread message ID |
| `thread_parent_created_at` | `threadParentCreatedAt` | date | Thread parent context |
| `tshow` | `tshow` | boolean | Thread msg shown in channel |
| `visible_to` | `visibleTo` | keyword | Visibility restriction |
| `deleted` | `deleted` | boolean | Soft-delete flag |
| `type` | `type` | keyword | System message type |

**Not indexed** (Cassandra only): `mentions`, `attachments` (binary), `file`, `card` (raw), `card_action`, `target_user`, `unread`, `reactions`, `sys_msg_data`

### Startup behavior

1. Worker upserts index template `messages_template` for `messages-*` pattern (idempotent)
2. Sets `templateVerified = false`

### First-message verification

On the **first** message processed:

1. Derive target index: `{prefix}-YYYY-MM`
2. Check if the index already exists:
   - **Index doesn't exist** → ES creates it from template when first doc is indexed → mapping matches by definition → `templateVerified = true`
   - **Index exists, mapping matches** → `templateVerified = true`
   - **Index exists, mapping doesn't match** → **FATAL**: log error and exit — "Index template updated but MSG_INDEX_PREFIX not bumped"
3. All subsequent messages skip the check

### FE search pattern

Clients search using `messages-site1-*` — catches all versions (v1, v2) during migration.

### Version rollover flow (via separate index-manager, future work)

1. Developer adds new field to embedded template mapping
2. Bumps `MSG_INDEX_PREFIX` from `messages-site1-v1` to `msgs-site1-v2`
3. Deploy worker → upserts template for v2, starts writing to v2 indices
4. Run `index-manager reindex --from=messages-site1-v1 --to=msgs-site1-v2 --months=6`
5. FE searches `messages-site1-*` — gets both v1 and v2 during migration
6. Run `index-manager cleanup --prefix=messages-site1-v1` to delete old indices

---

## Subject & Stream Changes

### New FE publish subjects

```text
chat.user.{account}.room.{roomID}.{siteID}.msg.send    → create (existing)
chat.user.{account}.room.{roomID}.{siteID}.msg.update   → update (new)
chat.user.{account}.room.{roomID}.{siteID}.msg.delete   → delete (new)
```

### New canonical subjects

```text
chat.msg.canonical.{siteID}.created   (existing)
chat.msg.canonical.{siteID}.updated   (new)
chat.msg.canonical.{siteID}.deleted   (new)
```

### No stream config changes needed

- `MESSAGES_{siteID}` captures `chat.user.*.room.*.{siteID}.msg.> (user token = account)` — already includes `.update` and `.delete`
- `MESSAGES_CANONICAL_{siteID}` captures `chat.msg.canonical.{siteID}.>` — already includes `.updated` and `.deleted`

### Consumers on MESSAGES_CANONICAL (after this work)

| # | Consumer | Handles | Purpose |
|---|----------|---------|---------|
| 1 | `message-worker` | created only (unchanged) | Persist to Cassandra |
| 2 | `broadcast-worker` | created only (unchanged) | Broadcast to room members |
| 3 | `notification-worker` | created only (unchanged) | Send notifications |
| 4 | **`search-sync-worker`** | **created, updated, deleted** | **Bulk index to Elasticsearch** |

Existing workers require **no code changes** — they ignore the new `Event` and `Timestamp` fields on `MessageEvent` and continue processing `created` events via their existing filter subjects.

---

## Search Engine Adapter Pattern (`pkg/searchengine/`)

### Why an adapter?

Both Elasticsearch and OpenSearch share the same REST API (`_bulk`, `_search`, `_index_template`, `_mapping`). We use an adapter pattern so swapping backends is a config change only.

### Architecture layers

```text
┌─────────────────────────────────────────────────────────────────┐
│  SearchEngine interface (domain operations)                      │
│  Bulk(), Search(), UpsertTemplate(), GetIndexMapping(), Ping()  │
└───────────────────────────┬─────────────────────────────────────┘
                            │ implements
┌───────────────────────────▼─────────────────────────────────────┐
│  httpAdapter (backend-agnostic)                                  │
│  Builds raw HTTP requests + parses JSON responses               │
└───────────────────────────┬─────────────────────────────────────┘
                            │ calls
┌───────────────────────────▼─────────────────────────────────────┐
│  Transporter interface                                           │
│  Perform(req *http.Request) (*http.Response, error)             │
│  Both ES and OS clients satisfy this out of the box             │
└─────────────────────────────────────────────────────────────────┘
```

### Layer 1 — Transporter

```go
// Perform is the only method needed. Both elastic/go-elasticsearch
// and opensearch-go clients have this method — no wrapper needed.
type Transporter interface {
    Perform(req *http.Request) (*http.Response, error)
}
```

### Layer 2 — SearchEngine interface

```go
type SearchEngine interface {
    Ping(ctx context.Context) error
    Bulk(ctx context.Context, actions []BulkAction) ([]BulkResult, error)
    UpsertTemplate(ctx context.Context, name, pattern string, mapping json.RawMessage) error
    GetIndexMapping(ctx context.Context, index string) (json.RawMessage, error)
    Search(ctx context.Context, indices []string, query json.RawMessage) (json.RawMessage, error)
}
```

### Layer 3 — httpAdapter

Implements all `SearchEngine` methods using raw HTTP + JSON on top of `Transporter`. No ES/OS SDK-specific types leak — pure HTTP. This is what makes it backend-agnostic.

### Layer 4 — Factory function

```go
func New(backend, url string) (SearchEngine, error) {
    var transport Transporter
    switch backend {
    case "elasticsearch":
        client, _ := elasticsearch.NewClient(elasticsearch.Config{Addresses: []string{url}})
        transport = client // satisfies Transporter
    case "opensearch":
        client, _ := opensearch.NewClient(opensearch.Config{Addresses: []string{url}})
        transport = client // satisfies Transporter
    }
    adapter := newAdapter(transport, url)
    adapter.Ping(ctx) // verify connectivity
    return adapter, nil
}
```

**Swapping ES ↔ OpenSearch is a config change only:**
```yaml
SEARCH_BACKEND=opensearch
SEARCH_URL=http://opensearch:9200
```

---

## Service Configuration

```go
type config struct {
    NatsURL        string `env:"NATS_URL,required"`
    SiteID         string `env:"SITE_ID,required"`
    SearchURL      string `env:"SEARCH_URL,required"`                        // e.g. "http://localhost:9200"
    SearchBackend  string `env:"SEARCH_BACKEND" envDefault:"elasticsearch"` // "elasticsearch" | "opensearch"
    MsgIndexPrefix string `env:"MSG_INDEX_PREFIX,required"`                  // e.g. "messages-site1-v1"
    BatchSize      int    `env:"BATCH_SIZE"      envDefault:"500"`
    FlushInterval  int    `env:"FLUSH_INTERVAL"  envDefault:"5"`             // seconds
}
```

---

## Implementation Order

1. Model changes — add `Event`, `Timestamp` fields to `MessageEvent`; add `UpdatedAt` to `Message`; add request types (`pkg/model/`)
2. Subject additions — `pkg/subject/subject.go` (update `{username}` → `{account}`)
3. Gatekeeper CUD — handler routing, processUpdate, processDelete, timestamp stamping
4. `pkg/searchengine` — Transporter interface, SearchEngine interface, httpAdapter, factory
5. `search-sync-worker` service — store, handler, batch consumer, main
6. Docker/deploy — Dockerfile, docker-compose, pipeline
7. Integration tests — end-to-end with testcontainers

**No existing worker changes needed** — `MessageEvent` is backward compatible. Existing workers ignore the new `Event` and `Timestamp` fields. Full CUD handling in downstream workers is future work.

---

## Future Work

- **index-manager** batch job/CLI — template version rollover, reindexing, old index cleanup
- **Full CUD in downstream workers** — Cassandra updates/deletes, broadcast edit/delete to rooms
- **Search API service** — HTTP service for FE search queries against ES/OpenSearch
