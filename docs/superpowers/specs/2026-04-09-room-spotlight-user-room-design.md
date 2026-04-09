# Design: Room Spotlight & User-Room Index via SUBSCRIPTIONS Stream

**Architecture Diagram:** [View in FigJam](https://www.figma.com/online-whiteboard/create-diagram/6412fb79-8a38-40f0-8061-59b3b90fd10d?utm_source=claude&utm_content=edit_in_figjam)

## Context

The search-sync-worker currently syncs messages to Elasticsearch. Two additional sync targets are needed:

1. **Spotlight search** — Users search for rooms they belong to by name (typeahead)
2. **User-room index** — Per-user list of room IDs used as a runtime filter for message search access control ("only search messages in rooms this user belongs to")

Previously, both were powered by Monstache (MongoDB CDC → ES). The new architecture replaces CDC with a JetStream-based SUBSCRIPTIONS stream that the search-sync-worker consumes via two new `Collection` implementations.

### What this work adds

1. A new `SUBSCRIPTIONS_{siteID}` JetStream stream for durable subscription lifecycle events
2. `SubscriptionChangeEvent` carrying both Subscription and Room
3. `spotlightCollection` — syncs per-subscription docs to `spotlight-*` index
4. `userRoomCollection` — maintains per-user `rooms` array in `user-room-{site}` index via scripted updates
5. `ActionUpdate` support in `pkg/searchengine` bulk API
6. `Collection.BuildAction` returns `[]BulkAction` (slice instead of single)

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Event source | New SUBSCRIPTIONS stream | Existing subscription events are pub/sub only (not durable). Need replayable stream for indexing. |
| Event publisher | room-worker (local) + inbox-worker (federated) | Both already create subscriptions and have Room loaded |
| Spotlight doc model | One doc per subscription | Filter by userAccount + search roomName. Simple create/delete lifecycle. |
| User-room update strategy | ES scripted update | No read-before-write. Painless script adds/removes roomId from array. Low volume (member changes). Executes on data node only. |
| Restricted rooms in ES | Not stored | Search service queries restricted rooms from DB + cache at query time |
| Room rename | Not supported | Rooms only have create/delete, no update |
| Collection.BuildAction return type | `[]BulkAction` | Enables collections that produce multiple actions per event (future extensibility). Message collection returns slice of one. |

---

## SUBSCRIPTIONS Stream

### Stream config

```go
func Subscriptions(siteID string) Config {
    return Config{
        Name:     fmt.Sprintf("SUBSCRIPTIONS_%s", siteID),
        Subjects: []string{fmt.Sprintf("chat.subscription.%s.>", siteID)},
    }
}
```

### Subjects

```text
chat.subscription.{siteID}.added     → member joined a room
chat.subscription.{siteID}.removed   → member left/removed from a room
```

### Event struct

```go
type SubscriptionChangeEvent struct {
    Subscription Subscription `json:"subscription"`
    Room         Room         `json:"room"`
    Action       string       `json:"action"` // "added" | "removed"
    Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}
```

The Room is included so downstream consumers (spotlight, user-room) have the room name and type without needing a DB lookup.

### Publishers

**room-worker** (local member add): Already has both Subscription and Room loaded in `processInvite`. Publishes `SubscriptionChangeEvent` to `chat.subscription.{siteID}.added` on the SUBSCRIPTIONS stream.

**inbox-worker** (federated member add): Receives enriched OutboxEvent payload containing the Room. After creating the local subscription, publishes `SubscriptionChangeEvent` to the local SUBSCRIPTIONS stream.

**Future**: Whoever handles member removal publishes with Action="removed" to `chat.subscription.{siteID}.removed`.

### Consumers on SUBSCRIPTIONS

| # | Consumer | Purpose |
|---|----------|---------|
| 1 | `spotlight-sync` | Sync subscription docs to spotlight index |
| 2 | `user-room-sync` | Maintain per-user rooms array |

---

## Spotlight Index (Task 1)

### Index template

- **Pattern:** `spotlight-*`
- **Index name:** `spotlight-{site}-v1-chat` (single index, not monthly)

```json
{
  "index_patterns": ["spotlight-*"],
  "template": {
    "settings": {
      "index": {
        "number_of_shards": 3,
        "number_of_replicas": 1
      },
      "analysis": {
        "analyzer": {
          "custom_analyzer": {
            "type": "custom",
            "tokenizer": "custom_tokenizer",
            "filter": ["lowercase"]
          }
        },
        "tokenizer": {
          "custom_tokenizer": {
            "type": "whitespace",
            "token_chars": ["letter", "digit", "punctuation", "symbol"]
          }
        }
      }
    },
    "mappings": {
      "dynamic": false,
      "properties": {
        "subscriptionId":  { "type": "keyword" },
        "userId":          { "type": "keyword" },
        "userAccount":     { "type": "keyword" },
        "roomId":          { "type": "keyword" },
        "roomName":        { "type": "search_as_you_type", "analyzer": "custom_analyzer" },
        "roomType":        { "type": "keyword" },
        "siteId":          { "type": "keyword" },
        "joinedAt":        { "type": "date" }
      }
    }
  }
}
```

### SpotlightSearchIndex struct

```go
type SpotlightSearchIndex struct {
    SubscriptionID string    `json:"subscriptionId" es:"keyword"`
    UserID         string    `json:"userId"         es:"keyword"`
    UserAccount    string    `json:"userAccount"    es:"keyword"`
    RoomID         string    `json:"roomId"         es:"keyword"`
    RoomName       string    `json:"roomName"       es:"search_as_you_type,custom_analyzer"`
    RoomType       string    `json:"roomType"       es:"keyword"`
    SiteID         string    `json:"siteId"         es:"keyword"`
    JoinedAt       time.Time `json:"joinedAt"       es:"date"`
}
```

### CUD mapping

| Action | ES Action | Doc ID | Detail |
|--------|-----------|--------|--------|
| `added` | `index` | subscription ID | Full spotlight doc |
| `removed` | `delete` | subscription ID | Delete by subscription ID |

### spotlightCollection

```go
type spotlightCollection struct {
    indexName string // e.g., "spotlight-site1-v1-chat"
}
```

- `StreamConfig` → `stream.Subscriptions(siteID)`
- `ConsumerName` → `"spotlight-sync"`
- `BuildAction` → unmarshal `SubscriptionChangeEvent`, return `[]BulkAction` with one index or delete action

### Search query pattern (future search service)

```json
{
  "query": {
    "bool": {
      "filter": { "term": { "userAccount": "alice" } },
      "must": {
        "multi_match": {
          "query": "eng",
          "type": "bool_prefix",
          "fields": ["roomName", "roomName._2gram", "roomName._3gram"]
        }
      }
    }
  }
}
```

---

## User-Room Index (Task 2)

### Index

- **Name:** `user-room-{site}` (single index, not templated)
- One doc per user. Doc ID = user account.

### Mapping

```json
{
  "properties": {
    "userAccount": { "type": "keyword" },
    "rooms":       { "type": "text", "fields": { "keyword": { "type": "keyword", "ignore_above": 256 } } },
    "createdAt":   { "type": "date" },
    "updatedAt":   { "type": "date" }
  }
}
```

### CUD mapping (scripted updates)

| Action | ES Action | Script |
|--------|-----------|--------|
| `added` (unrestricted) | `update` with upsert | Add roomId to `rooms` array if not present |
| `added` (restricted) | skip | `HistorySharedSince` is set — search service handles via DB+cache |
| `removed` | `update` | Remove roomId from `rooms` array |

**Upsert script (added):**
```painless
if (ctx._source.rooms == null) { ctx._source.rooms = []; }
if (!ctx._source.rooms.contains(params.rid)) { ctx._source.rooms.add(params.rid); }
ctx._source.updatedAt = params.now;
```

**Upsert doc (for new users):**
```json
{ "userAccount": "alice", "rooms": ["room-1"], "createdAt": "...", "updatedAt": "..." }
```

**Remove script (removed):**
```painless
if (ctx._source.rooms != null) {
  int idx = ctx._source.rooms.indexOf(params.rid);
  if (idx >= 0) { ctx._source.rooms.remove(idx); }
}
ctx._source.updatedAt = params.now;
```

### userRoomCollection

```go
type userRoomCollection struct {
    indexName string // e.g., "user-room-site1"
}
```

- `StreamConfig` → `stream.Subscriptions(siteID)`
- `ConsumerName` → `"user-room-sync"`
- `BuildAction` → unmarshal `SubscriptionChangeEvent`, skip if restricted, return `[]BulkAction` with one update action

### Message search access control (future search service)

The search service queries `user-room-{site}/_doc/{account}` to get the user's room list, then uses `terms` filter in the message search query:

```json
{
  "query": {
    "bool": {
      "filter": { "terms": { "roomId": ["room-1", "room-2", "room-3"] } },
      "must": { "match": { "content": "search query" } }
    }
  }
}
```

---

## pkg/searchengine Changes

### New ActionUpdate type

```go
const (
    ActionIndex  ActionType = "index"
    ActionDelete ActionType = "delete"
    ActionUpdate ActionType = "update"
)

type BulkAction struct {
    Action  ActionType
    Index   string
    DocID   string
    Version int64           // used as ES external version (0 = no versioning)
    Doc     json.RawMessage // index: full doc, update: script+params, delete: nil
}
```

For `ActionUpdate`, the `Doc` field contains the update body:

```json
{
  "script": {
    "source": "if (!ctx._source.rooms.contains(params.rid)) { ctx._source.rooms.add(params.rid); } ctx._source.updatedAt = params.now;",
    "params": { "rid": "room-1", "now": "2026-04-09T12:00:00Z" }
  },
  "upsert": { "userAccount": "alice", "rooms": ["room-1"], "createdAt": "...", "updatedAt": "..." }
}
```

### Bulk NDJSON for update

```json
{"update":{"_index":"user-room-site1","_id":"alice"}}
{"script":{"source":"...","params":{...}},"upsert":{...}}
```

### httpAdapter.Bulk changes

Add `ActionUpdate` case in the bulk body builder — same as `ActionIndex` but uses `"update"` key in the metadata line instead of `"index"`.

---

## Collection Interface Change

```go
type Collection interface {
    StreamConfig(siteID string) stream.Config
    ConsumerName() string
    TemplateName() string
    TemplateBody() json.RawMessage
    BuildAction(data []byte) ([]searchengine.BulkAction, error)  // changed: returns slice
}
```

`messageCollection.BuildAction` returns a slice of one. `spotlightCollection` returns a slice of one. `userRoomCollection` returns a slice of one (or empty for restricted rooms).

---

## Changes to Existing Services

### room-worker/handler.go

After creating the subscription and fetching the room (both already happen in `processInvite`), publish `SubscriptionChangeEvent` to the SUBSCRIPTIONS stream:

```go
changeEvt := model.SubscriptionChangeEvent{
    Subscription: sub,
    Room:         *room,
    Action:       "added",
    Timestamp:    now.UnixMilli(),
}
```

### room-worker/main.go

Create the SUBSCRIPTIONS stream at startup (idempotent).

### inbox-worker/handler.go

For `member_added` outbox events: the OutboxEvent payload is enriched to include Room. After creating the local subscription, publish `SubscriptionChangeEvent` to the local SUBSCRIPTIONS stream.

### pkg/stream/stream.go

Add `Subscriptions(siteID)` config.

### pkg/subject/subject.go

Add builders:
- `SubscriptionAdded(siteID string)` → `chat.subscription.{siteID}.added`
- `SubscriptionRemoved(siteID string)` → `chat.subscription.{siteID}.removed`

### pkg/model/event.go

Add `SubscriptionChangeEvent` struct.

### search-sync-worker/main.go

Start three `runConsumer` goroutines — one per collection (messages, spotlight, user-room). Each has its own consumer and handler.

---

## Implementation Order

1. `Collection.BuildAction` return type change (slice) + update messageCollection + handler + tests
2. `ActionUpdate` support in `pkg/searchengine` adapter + tests
3. `SubscriptionChangeEvent` model + subject builders + stream config
4. `spotlightCollection` implementation + tests
5. `userRoomCollection` implementation + tests
6. room-worker changes (publish to SUBSCRIPTIONS stream)
7. inbox-worker changes (enriched outbox payload + publish to SUBSCRIPTIONS stream)
8. search-sync-worker main.go (wire up all three collections)
9. Integration tests

---

## Future Work

- **Member removal handler** — room-service authorization + room-worker publishes Action="removed"
- **Search API service** — spotlight search + message search with user-room access control
- **Restricted room cache** — search service queries DB for HistorySharedSince, caches per user
