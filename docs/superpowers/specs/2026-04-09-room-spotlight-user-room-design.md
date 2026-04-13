# Design: Room Spotlight & User-Room Index via INBOX Aggregation

**Architecture Diagram:** [View in FigJam](https://www.figma.com/online-whiteboard/create-diagram/6412fb79-8a38-40f0-8061-59b3b90fd10d?utm_source=claude&utm_content=edit_in_figjam)

## Context

The search-sync-worker currently syncs messages to Elasticsearch. Two additional sync targets are needed:

1. **Spotlight search** — Users search for rooms they belong to by name (typeahead)
2. **User-room index** — Per-user list of room IDs used as a runtime filter for message search access control ("only search messages in rooms this user belongs to")

Previously, both were powered by Monstache (MongoDB CDC → ES). The new architecture replaces CDC with subscription events consumed from the existing `INBOX_{siteID}` JetStream stream via two new `Collection` implementations in search-sync-worker.

**Key insight:** no new stream is needed. The existing OUTBOX/INBOX infrastructure already handles durable cross-site event propagation. We extend it to also carry subscription lifecycle events using JetStream **SubjectTransforms** (NATS 2.10+) to unify the subject namespace on the destination site.

### What this work adds

1. `SubscriptionChangeEvent` payload type carrying both Subscription and Room
2. Enhanced `INBOX_{siteID}` config with local subject acceptance and source-side SubjectTransforms to unify subjects into `chat.inbox.{siteID}.>`
3. `spotlightCollection` — syncs per-subscription docs to `spotlight-*` index
4. `userRoomCollection` — maintains per-user `rooms` array in `user-room-{site}` index via scripted updates
5. `ActionUpdate` support in `pkg/searchengine` bulk API
6. `Collection.BuildAction` returns `[]BulkAction` (slice instead of single)

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Event source | Reuse existing `INBOX_{siteID}` stream | No new stream needed. OUTBOX/INBOX already handles durable cross-site propagation with stream sourcing. |
| Cross-site propagation | Existing OUTBOX/INBOX stream sourcing | Already tested/operational pattern. One cross-site path for both data (Mongo) and events (ES indexing). |
| Subject unification | JetStream SubjectTransforms on INBOX sources | Source messages rewritten from `outbox.{src}.to.{dst}.>` → `chat.inbox.{dst}.>` when stored in destination INBOX. Consumers see a single unified namespace regardless of origin. |
| Local event publishing | room-worker publishes directly to `chat.inbox.{localSite}.>` | INBOX accepts local publishes via its own Subjects. Same destination format as federated events after transform. |
| Federated event publishing | room-worker publishes to `outbox.{src}.to.{dst}.>` (existing pattern) | Reuses the existing OUTBOX publish path. The OutboxEvent payload is enriched to carry Room metadata. |
| Consumer coexistence | inbox-worker and search-sync-worker consume the same INBOX stream with different durable consumer names | JetStream delivers each event to each durable consumer independently. No conflict. |
| Spotlight doc model | One doc per subscription | Filter by userAccount + search roomName. Simple create/delete lifecycle. |
| User-room update strategy | ES scripted update | No read-before-write. Painless script adds/removes roomId from array. Low volume (member changes). Executes on data node only. |
| Restricted rooms in ES | Not stored | Search service queries restricted rooms from DB + cache at query time |
| Room rename | Not supported | Rooms only have create/delete, no update |
| Collection.BuildAction return type | `[]BulkAction` | Enables collections that produce multiple actions per event (future extensibility). Message collection returns slice of one. |

---

## INBOX Stream (Enhanced)

### Stream config

The existing `INBOX_{siteID}` stream is enhanced with:
1. **Local subjects** (`chat.inbox.{siteID}.>`) — accept local publishes from room-worker for same-site events
2. **Source SubjectTransforms** — rewrite `outbox.{src}.to.{siteID}.>` → `chat.inbox.{siteID}.>` when sourcing from remote OUTBOX streams

The net effect: regardless of whether an event originated from a local publish or was sourced from a remote OUTBOX, it lands in this site's INBOX under the unified `chat.inbox.{siteID}.>` namespace.

```go
func Inbox(siteID string, remoteSiteIDs []string) Config {
    sources := make([]StreamSource, 0, len(remoteSiteIDs))
    for _, remote := range remoteSiteIDs {
        sourcePattern := fmt.Sprintf("outbox.%s.to.%s.>", remote, siteID)
        destPattern := fmt.Sprintf("chat.inbox.%s.>", siteID)
        sources = append(sources, StreamSource{
            Name:          fmt.Sprintf("OUTBOX_%s", remote),
            FilterSubject: sourcePattern,
            SubjectTransforms: []SubjectTransformConfig{
                {Source: sourcePattern, Destination: destPattern},
            },
        })
    }
    return Config{
        Name:     fmt.Sprintf("INBOX_%s", siteID),
        Subjects: []string{fmt.Sprintf("chat.inbox.%s.>", siteID)},
        Sources:  sources,
    }
}
```

**Requires NATS Server 2.10+** for SubjectTransforms support. `nats.go v1.50.0` (already in `go.mod`) supports the client-side config.

### Subject namespace

**Unified on INBOX (what consumers see):**
```text
chat.inbox.{siteID}.subscription.added     → member joined a room
chat.inbox.{siteID}.subscription.removed   → member left/removed from a room
chat.inbox.{siteID}.member.added            → existing member_added event (enriched with Room)
chat.inbox.{siteID}.room.sync               → existing room_sync event
```

**Raw subjects published by room-worker / room-service:**
- **Local events** (same-site): directly published to `chat.inbox.{localSite}.subscription.added`
- **Federated events** (cross-site): published to `outbox.{localSite}.to.{destSite}.subscription.added`, which gets transformed to `chat.inbox.{destSite}.subscription.added` on the destination site via SubjectTransforms

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

**room-worker is the single publisher** for subscription change events. After creating the subscription in local Mongo, it publishes once based on the subscription's siteID:

```go
// Determine the target subject based on subscription's siteID
var subj string
if sub.SiteID == h.siteID {
    // Local invite — publish directly to local INBOX
    subj = subject.InboxSubscriptionAdded(h.siteID)
} else {
    // Federated invite — publish to OUTBOX for cross-site propagation
    subj = subject.Outbox(h.siteID, sub.SiteID, "subscription.added")
}
_, _ = h.js.Publish(ctx, subj, data)
```

- **Local invite** (`sub.SiteID == h.siteID`): publishes to `chat.inbox.{localSite}.subscription.added`, which matches INBOX's local Subjects filter and is stored directly.
- **Federated invite** (`sub.SiteID != h.siteID`): publishes to `outbox.{localSite}.to.{remoteSite}.subscription.added`, which matches the local OUTBOX stream. The remote INBOX sources it via its SubjectTransform, storing it as `chat.inbox.{remoteSite}.subscription.added`.

**inbox-worker is NOT affected.** It continues to consume INBOX for `member_added` and `room_sync` events. The new subscription events go to a different consumer (search-sync-worker) via JetStream's independent-consumer semantics. inbox-worker simply ignores events whose subjects don't match its durable consumer's filter.

> **Cross-cutting dependency on inbox-worker design:** for **local invites**, room-worker creates the subscription in the local Mongo directly — inbox-worker should NOT also process the same event (it would cause a duplicate Mongo write). To prevent this, the inbox-worker design must ensure its consumer filter excludes locally-published events. One option is to split the INBOX subject namespace into two prefixes: `chat.inbox.{siteID}.>` for direct local publishes and `chat.inbox.aggregate.{siteID}.>` for events sourced from remote OUTBOX (via SubjectTransforms). inbox-worker then filters on the `aggregate` namespace only, while spotlight-sync and user-room-sync use NATS 2.10+ `FilterSubjects` (plural) to subscribe to both. The exact namespace separation is part of the inbox-worker design — this spec only depends on it being in place.

### Consumers on INBOX

The enhanced INBOX stream is shared by multiple consumers with independent durable names and subject filters:

| Durable consumer | FilterSubject | Purpose |
|---|---|---|
| `inbox-worker` (existing) | `chat.inbox.{siteID}.member.>` + `chat.inbox.{siteID}.room.>` | Federated Mongo writes for members/rooms (existing) |
| `spotlight-sync` (new) | `chat.inbox.{siteID}.subscription.>` | Sync subscription docs to spotlight index |
| `user-room-sync` (new) | `chat.inbox.{siteID}.subscription.>` | Maintain per-user rooms array |

**Note on inbox-worker migration:** The existing inbox-worker currently reads `member_added` / `room_sync` `OutboxEvent`s directly from OUTBOX-pattern subjects (`outbox.*.to.{siteID}.>`). After enabling SubjectTransforms, the subjects on INBOX change to `chat.inbox.{siteID}.*`. inbox-worker's consumer `FilterSubject` must be updated accordingly. This is a minor config change.

**Why this works without conflict:** JetStream delivers each message to each durable consumer independently. inbox-worker's consumer sees only `chat.inbox.{siteID}.member.>` and `chat.inbox.{siteID}.room.>` events. search-sync-worker's consumers see only `chat.inbox.{siteID}.subscription.>` events. There's no overlap, no duplicate processing.

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

- `StreamConfig` → `stream.Inbox(siteID, remoteSiteIDs)` (the enhanced INBOX)
- `ConsumerName` → `"spotlight-sync"`
- `FilterSubject` → `chat.inbox.{siteID}.subscription.>`
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

- `StreamConfig` → `stream.Inbox(siteID, remoteSiteIDs)` (the enhanced INBOX)
- `ConsumerName` → `"user-room-sync"`
- `FilterSubject` → `chat.inbox.{siteID}.subscription.>`
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

After creating the subscription and fetching the room (both already happen in `processInvite`), publish `SubscriptionChangeEvent`. **The target subject depends on whether the subscription is local or federated**:

```go
changeEvt := model.SubscriptionChangeEvent{
    Subscription: sub,
    Room:         *room,
    Action:       "added",
    Timestamp:    now.UnixMilli(),
}
data, _ := json.Marshal(changeEvt)

var subj string
if sub.SiteID == h.siteID {
    // Local invite — publish directly to local INBOX
    subj = subject.InboxSubscriptionAdded(h.siteID)
} else {
    // Federated invite — publish to OUTBOX; remote INBOX sources and transforms the subject
    subj = subject.OutboxSubscriptionAdded(h.siteID, sub.SiteID)
}
_, _ = h.js.Publish(ctx, subj, data)
```

The federated case uses the existing OUTBOX pattern. The remote site's INBOX sources the event via stream sourcing and the SubjectTransform rewrites it into the unified `chat.inbox.{destSite}.subscription.added` namespace.

### inbox-worker/main.go

Update the INBOX stream config to use `stream.Inbox(siteID, remoteSiteIDs)` which now includes:
- Local subjects `chat.inbox.{siteID}.>` (accepts local publishes)
- Sources with `SubjectTransforms` that rewrite `outbox.*.to.{siteID}.>` → `chat.inbox.{siteID}.>`

Remote site IDs come from config (e.g., `REMOTE_SITE_IDS=site-b,site-c`).

### inbox-worker/handler.go

**Subject filter update required.** The durable consumer's `FilterSubject` must be updated from the old OUTBOX-pattern (`outbox.*.to.{siteID}.>`) to the new unified namespace:

```go
FilterSubject: "chat.inbox." + cfg.SiteID + ".member.>,chat.inbox." + cfg.SiteID + ".room.>"
```

(Or use two separate consumers — one per event type.)

The handler logic that decodes `OutboxEvent` and routes to `handleMemberAdded` / `handleRoomSync` remains unchanged. Only the subject filter changes.

### pkg/stream/stream.go

Update `Inbox(siteID string)` to `Inbox(siteID string, remoteSiteIDs []string)` with:
- `Subjects: []string{fmt.Sprintf("chat.inbox.%s.>", siteID)}`
- `Sources: [...]` with `FilterSubject` and `SubjectTransforms` for each remote site

### pkg/subject/subject.go

Add builders:
- `InboxSubscriptionAdded(siteID string)` → `chat.inbox.{siteID}.subscription.added`
- `InboxSubscriptionRemoved(siteID string)` → `chat.inbox.{siteID}.subscription.removed`
- `OutboxSubscriptionAdded(srcSiteID, destSiteID string)` → `outbox.{srcSiteID}.to.{destSiteID}.subscription.added`
- `OutboxSubscriptionRemoved(srcSiteID, destSiteID string)` → `outbox.{srcSiteID}.to.{destSiteID}.subscription.removed`

Also update existing member/room builders to use new `chat.inbox.{siteID}.member.added` / `chat.inbox.{siteID}.room.sync` on the INBOX side, and keep `outbox.{src}.to.{dst}.member_added` / `outbox.{src}.to.{dst}.room_sync` on the OUTBOX side (with corresponding `SubjectTransforms`).

### pkg/model/event.go

Add `SubscriptionChangeEvent` struct.

### search-sync-worker/main.go

Start three `runConsumer` goroutines — one per collection (messages, spotlight, user-room). The spotlight and user-room consumers bind to the local INBOX stream with `FilterSubject: "chat.inbox.{siteID}.subscription.>"`. inbox-worker continues to run independently with its own consumer on the same INBOX stream.

---

## Implementation Order

1. `Collection.BuildAction` return type change (slice) + update messageCollection + handler + tests
2. `ActionUpdate` support in `pkg/searchengine` adapter + tests
3. `SubscriptionChangeEvent` model + new subject builders + `pkg/stream.Inbox()` enhanced config with SubjectTransforms
4. `spotlightCollection` implementation + tests (consumer on INBOX with `chat.inbox.{siteID}.subscription.>` filter)
5. `userRoomCollection` implementation + tests (consumer on INBOX with `chat.inbox.{siteID}.subscription.>` filter)
6. inbox-worker migration: update consumer FilterSubject to new `chat.inbox.{siteID}.{member,room}.>` namespace; update existing room-worker OUTBOX publishes to new subject format
7. room-worker changes (publish `SubscriptionChangeEvent` to local INBOX for local invites, OUTBOX for federated)
8. search-sync-worker main.go (wire up all three collections with distinct consumer filters)
9. Integration tests covering local and federated subscription events

---

## Future Work

- **Member removal handler** — room-service authorization + room-worker publishes Action="removed"
- **Search API service** — spotlight search + message search with user-room access control
- **Restricted room cache** — search service queries DB for HistorySharedSince, caches per user
