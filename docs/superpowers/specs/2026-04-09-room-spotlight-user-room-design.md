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
| Event source | New SUBSCRIPTIONS stream per site | Existing subscription events are pub/sub only (not durable). Need replayable stream for indexing. |
| Cross-site propagation | JetStream stream sourcing (subject-filtered) | Decouples indexing federation from data federation (OUTBOX/INBOX). Same primitive INBOX already uses. |
| Event publisher | room-worker only | Single source of truth. Subject encodes destination via subscription's siteID; stream sourcing routes automatically. inbox-worker stays focused on Mongo replication. |
| Subject routing | `chat.subscription.{subscription.siteID}.{action}` | Invitee's siteID is the destination. Local events stay local; federated events flow via stream sourcing. |
| Spotlight doc model | One doc per subscription | Filter by userAccount + search roomName. Simple create/delete lifecycle. |
| User-room update strategy | ES scripted update | No read-before-write. Painless script adds/removes roomId from array. Low volume (member changes). Executes on data node only. |
| Restricted rooms in ES | Not stored | Search service queries restricted rooms from DB + cache at query time |
| Room rename | Not supported | Rooms only have create/delete, no update |
| Collection.BuildAction return type | `[]BulkAction` | Enables collections that produce multiple actions per event (future extensibility). Message collection returns slice of one. |

---

## SUBSCRIPTIONS Stream

### Stream config

Per-site SUBSCRIPTIONS stream that accepts any `chat.subscription.>` subject locally and sources events for its own siteID from other sites' SUBSCRIPTIONS streams. This uses JetStream stream sourcing — the same primitive used by `INBOX_{siteID}` to federate OUTBOX events.

```go
func Subscriptions(siteID string, remoteSiteIDs []string) Config {
    sources := make([]SourceConfig, 0, len(remoteSiteIDs))
    for _, remote := range remoteSiteIDs {
        sources = append(sources, SourceConfig{
            Name:          fmt.Sprintf("SUBSCRIPTIONS_%s", remote),
            FilterSubject: fmt.Sprintf("chat.subscription.%s.>", siteID),
        })
    }
    return Config{
        Name:     fmt.Sprintf("SUBSCRIPTIONS_%s", siteID),
        Subjects: []string{"chat.subscription.>"},
        Sources:  sources,
    }
}
```

Each site's stream:
1. **Accepts local publishes** for any `chat.subscription.>` subject
2. **Sources events destined for itself** from every other site's SUBSCRIPTIONS stream, filtered by its own siteID

### Subjects

```text
chat.subscription.{siteID}.added     → member joined a room
chat.subscription.{siteID}.removed   → member left/removed from a room
```

**Critical:** `{siteID}` is the **subscription's siteID** (the invitee's site), NOT the publisher's site. The subject encodes the destination; stream sourcing routes the event to the right site automatically.

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

**room-worker is the single publisher.** After creating the subscription (regardless of whether the invitee is local or remote), it publishes `SubscriptionChangeEvent` to `chat.subscription.{subscription.siteID}.added`.

- **Local invite** (invitee on same site): subject is `chat.subscription.{localSite}.added`, message stays in the local SUBSCRIPTIONS stream, local search-sync-worker consumes it.
- **Federated invite** (invitee on remote site): subject is `chat.subscription.{remoteSite}.added`, message lands in the publisher's local SUBSCRIPTIONS stream, then the remote site's SUBSCRIPTIONS stream sources it via its filter.

**inbox-worker does NOT publish SubscriptionChangeEvent.** It remains focused on its existing responsibility — writing the subscription to the local Mongo for federated invites. Federation of subscription state (Mongo) happens via the existing OUTBOX/INBOX pattern; federation of subscription events (for search indexing) happens via SUBSCRIPTIONS stream sourcing. These are two independent propagation paths with two different concerns.

**Future**: Whoever handles member removal publishes with Action="removed" to `chat.subscription.{subscription.siteID}.removed` — same rule.

### Consumers on SUBSCRIPTIONS

Each site's search-sync-worker consumes from its own local SUBSCRIPTIONS stream. Because the stream has broad subjects (`chat.subscription.>`), it stores both **locally-published events destined for other sites** (before stream sourcing propagates them) and **events sourced from remote sites destined for this site**. The consumer must filter to the local siteID to avoid processing events that aren't for this site.

```go
cons, err := js.CreateOrUpdateConsumer(ctx, "SUBSCRIPTIONS_"+siteID, jetstream.ConsumerConfig{
    Durable:       "spotlight-sync",              // or "user-room-sync"
    AckPolicy:     jetstream.AckExplicitPolicy,
    FilterSubject: fmt.Sprintf("chat.subscription.%s.>", siteID), // only local-site events
    BackOff:       []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
})
```

| # | Consumer | FilterSubject | Purpose |
|---|----------|---------------|---------|
| 1 | `spotlight-sync` | `chat.subscription.{siteID}.>` | Sync subscription docs to spotlight index |
| 2 | `user-room-sync` | `chat.subscription.{siteID}.>` | Maintain per-user rooms array |

**Why the filter is required:** If Alice on site-A invites Bob on site-B, room-worker on site-A publishes to `chat.subscription.site-B.added`. This matches site-A's stream (broad subjects) and gets stored there. Site-B's stream sources it via its filter. But site-A's consumer would *also* see it — without `FilterSubject`, site-A would incorrectly try to index Bob's subscription into site-A's spotlight/user-room, which is wrong (Bob searches site-B's spotlight, not site-A's).

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

After creating the subscription and fetching the room (both already happen in `processInvite`), publish `SubscriptionChangeEvent` to the SUBSCRIPTIONS stream. **The subject uses the subscription's siteID, not the worker's local siteID.** This ensures the event is routed to the correct destination site via stream sourcing.

```go
changeEvt := model.SubscriptionChangeEvent{
    Subscription: sub,
    Room:         *room,
    Action:       "added",
    Timestamp:    now.UnixMilli(),
}
data, _ := json.Marshal(changeEvt)
// Subject uses the subscription's siteID (the invitee's site), not h.siteID
subj := subject.SubscriptionAdded(sub.SiteID)
_, _ = h.js.Publish(ctx, subj, data)
```

This works for both local and federated invites:
- Local invite: `sub.SiteID == h.siteID`, event stays in local stream
- Federated invite: `sub.SiteID != h.siteID`, event lands in local stream then gets sourced by the remote site's SUBSCRIPTIONS stream

### room-worker/main.go

Create the SUBSCRIPTIONS stream at startup with sources for all known remote site IDs (idempotent). Remote site IDs come from config (e.g., `REMOTE_SITE_IDS=site-b,site-c`).

### inbox-worker

**No changes needed.** inbox-worker continues to handle federated Mongo writes via the OUTBOX/INBOX pattern. It does NOT publish to SUBSCRIPTIONS — that's room-worker's job, and stream sourcing handles propagation.

### pkg/stream/stream.go

Add `Subscriptions(siteID, remoteSiteIDs)` config with stream sourcing.

### pkg/subject/subject.go

Add builders:
- `SubscriptionAdded(siteID string)` → `chat.subscription.{siteID}.added`
- `SubscriptionRemoved(siteID string)` → `chat.subscription.{siteID}.removed`

### pkg/model/event.go

Add `SubscriptionChangeEvent` struct.

### search-sync-worker/main.go

Start three `runConsumer` goroutines — one per collection (messages, spotlight, user-room). Each has its own consumer and handler. The spotlight and user-room consumers bind to the local SUBSCRIPTIONS stream; stream sourcing ensures they only see events for subscriptions on their own site.

---

## Implementation Order

1. `Collection.BuildAction` return type change (slice) + update messageCollection + handler + tests
2. `ActionUpdate` support in `pkg/searchengine` adapter + tests
3. `SubscriptionChangeEvent` model + subject builders + stream config (with sources)
4. `spotlightCollection` implementation + tests
5. `userRoomCollection` implementation + tests
6. room-worker changes (publish to SUBSCRIPTIONS stream using subscription's siteID)
7. search-sync-worker main.go (wire up all three collections)
8. Integration tests

---

## Future Work

- **Member removal handler** — room-service authorization + room-worker publishes Action="removed"
- **Search API service** — spotlight search + message search with user-room access control
- **Restricted room cache** — search service queries DB for HistorySharedSince, caches per user
