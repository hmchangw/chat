# Design: Room Spotlight & User-Room Index via INBOX Aggregation

**Architecture Diagram:** [View in FigJam](https://www.figma.com/online-whiteboard/create-diagram/6412fb79-8a38-40f0-8061-59b3b90fd10d?utm_source=claude&utm_content=edit_in_figjam)

## Context

The search-sync-worker currently syncs messages to Elasticsearch. Two additional sync targets are needed:

1. **Spotlight search** — Users search for rooms they belong to by name (typeahead)
2. **User-room index** — Per-user list of room IDs used as a runtime filter for message search access control ("only search messages in rooms this user belongs to")

Previously, both were powered by Monstache (MongoDB CDC → ES). The new architecture replaces CDC with subscription events consumed from the existing `INBOX_{siteID}` JetStream stream via two new `Collection` implementations in search-sync-worker.

**Key insight:** no new stream is needed. The existing OUTBOX/INBOX infrastructure already handles durable cross-site event propagation. We extend it to also carry subscription lifecycle events using JetStream **SubjectTransforms** (NATS 2.10+) to unify the subject namespace on the destination site.

### What this work adds

1. `member_added` OutboxEvent payload is **enriched with Room** (so spotlight-sync and user-room-sync can index without a DB lookup)
2. Enhanced `INBOX_{siteID}` config with local subject acceptance and source-side SubjectTransforms that rewrite `outbox.{src}.to.{siteID}.>` → `chat.inbox.{siteID}.aggregate.>`
3. `spotlightCollection` — syncs per-subscription docs to `spotlight-*` index
4. `userRoomCollection` — maintains per-user `rooms` array in `user-room-{site}` index via scripted updates
5. `ActionUpdate` support in `pkg/searchengine` bulk API
6. `Collection.BuildAction` returns `[]BulkAction` (slice instead of single)
7. room-worker routes publishes using the **user's siteID** (not `sub.SiteID`)

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Event source | Reuse existing `INBOX_{siteID}` stream | No new stream needed. OUTBOX/INBOX already handles durable cross-site propagation with stream sourcing. |
| Event type | Reuse existing `member_added` OutboxEvent (enriched with Room) | Single event, single publish path. Avoids double-processing by inbox-worker. No new `SubscriptionChangeEvent` type. |
| Cross-site propagation | Existing OUTBOX/INBOX stream sourcing | Already tested/operational pattern. One cross-site path for both data (Mongo) and events (ES indexing). |
| Subject namespace split | `chat.inbox.{siteID}.>` (local) + `chat.inbox.{siteID}.aggregate.>` (federated via transform) | Two-prefix design lets inbox-worker filter to `aggregate.>` only, preventing duplicate Mongo writes for locally-originated events (which room-worker already wrote directly). |
| Subject transform | `outbox.{src}.to.{dst}.>` → `chat.inbox.{dst}.aggregate.>` via JetStream SubjectTransforms | Source messages rewritten on ingest. Note: aggregate prefix is **after** siteID, not before. |
| Local event publishing | room-worker publishes directly to `chat.inbox.{localSite}.{eventType}` | INBOX accepts local publishes via its own Subjects. Stored without the `aggregate` segment so inbox-worker's filter excludes them. |
| Federated event publishing | room-worker publishes to `outbox.{src}.to.{destSite}.{eventType}` (existing pattern) | Reuses the existing OUTBOX publish path. OutboxEvent payload enriched with Room. |
| Publisher routing key | **User's siteID** (not `sub.SiteID`) | `sub.SiteID` represents the room's origin site (per VJ's PR). Events must be routed to the user's home site so it appears in their spotlight/user-room index. |
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
2. **Source SubjectTransforms** — rewrite `outbox.{src}.to.{siteID}.>` → `chat.inbox.{siteID}.aggregate.>` when sourcing from remote OUTBOX streams

This creates two distinct prefixes on each INBOX stream:
- `chat.inbox.{siteID}.*` — events published locally (same-site invites)
- `chat.inbox.{siteID}.aggregate.*` — events sourced from remote OUTBOX streams (federated invites)

```go
func Inbox(siteID string, remoteSiteIDs []string) Config {
    sources := make([]StreamSource, 0, len(remoteSiteIDs))
    for _, remote := range remoteSiteIDs {
        sourcePattern := fmt.Sprintf("outbox.%s.to.%s.>", remote, siteID)
        destPattern := fmt.Sprintf("chat.inbox.%s.aggregate.>", siteID)
        sources = append(sources, StreamSource{
            Name:          fmt.Sprintf("OUTBOX_%s", remote),
            FilterSubject: sourcePattern,
            SubjectTransforms: []SubjectTransformConfig{
                {Source: sourcePattern, Destination: destPattern},
            },
        })
    }
    return Config{
        Name: fmt.Sprintf("INBOX_%s", siteID),
        Subjects: []string{
            fmt.Sprintf("chat.inbox.%s.>", siteID),            // includes both local and (after transform) aggregate
        },
        Sources: sources,
    }
}
```

**Requires NATS Server 2.10+** for SubjectTransforms support. `nats.go v1.50.0` (already in `go.mod`) supports the client-side config.

### Subject namespace

**Local events (published directly by room-worker for same-site invites):**
```text
chat.inbox.{siteID}.member_added    → member joined a room on this site
chat.inbox.{siteID}.member_removed  → member left a room on this site
```

**Federated events (sourced from remote OUTBOX, transformed to aggregate prefix):**
```text
chat.inbox.{siteID}.aggregate.member_added    → federated member_added from another site
chat.inbox.{siteID}.aggregate.member_removed  → federated member_removed from another site
```

**Raw OUTBOX subjects (used by federation, not seen by consumers directly):**
- `outbox.{srcSite}.to.{destSite}.member_added` → transformed to `chat.inbox.{destSite}.aggregate.member_added` on destination

### Event struct (reuse existing)

**No new event struct.** We reuse the existing `OutboxEvent` with `Type: "member_added"` — there's no separate `SubscriptionChangeEvent`. To give spotlight-sync and user-room-sync enough data to index without a Mongo lookup, the `member_added` payload is **changed** to carry the full `Subscription` alongside the `Room`:

```go
type MemberAddedPayload struct {
    Subscription Subscription `json:"subscription"` // full sub: ID, User, RoomID, SiteID, Role, HistorySharedSince, JoinedAt
    Room         Room         `json:"room"`         // full room: name, type, createdBy, etc.
}
```

This is a **breaking change** to the existing `member_added` payload shape (which was `InviteMemberRequest`). inbox-worker must be updated to unmarshal `MemberAddedPayload` and use `payload.Subscription` for Mongo writes (instead of constructing a subscription from `InviteMemberRequest`). See the inbox-worker migration notes below.

**Why `Subscription` instead of `InviteMemberRequest` + `Room`:**
- spotlight-sync needs `sub.ID`, `sub.User.Account`, `sub.JoinedAt` for the doc
- user-room-sync needs `sub.User.Account` and `sub.HistorySharedSince` (for restricted detection)
- inbox-worker needs the full subscription for its local Mongo write
- Passing `Subscription` once covers all three consumers without redundant fields

Avoiding a second event type means:
- room-worker publishes **one** event per invite, not two
- inbox-worker and search-sync-worker read the **same** event stream via independent consumers
- No risk of double-processing

### Publishers

**Critical:** Routing uses the **user's siteID**, NOT `sub.SiteID`. The subscription's `SiteID` field represents the room's origin site (per VJ's parallel PR), not where the user lives. To route the indexing event to the user's home site, room-worker must resolve the invitee's home site (this may be available directly on the invite request or looked up from the user account record — depends on VJ's PR).

```go
// room-worker has already built `sub` (Subscription) and fetched `room` (Room)
payload := model.MemberAddedPayload{
    Subscription: sub,
    Room:         *room,
}
payloadData, _ := json.Marshal(payload)

userSite := invitee.SiteID  // user's home site (resolved from invite request or account lookup)

outboxEvt := model.OutboxEvent{
    Type:       "member_added",
    SiteID:     h.siteID,
    DestSiteID: userSite,   // user's home site — not sub.SiteID (which is room's origin)
    Payload:    payloadData,
    Timestamp:  now.UnixMilli(),
}
outboxData, _ := json.Marshal(outboxEvt)

var subj string
if userSite == h.siteID {
    // Local invite — publish directly to local INBOX (bypasses OUTBOX)
    subj = subject.InboxMemberAdded(h.siteID)   // "chat.inbox.{siteID}.member_added"
} else {
    // Federated invite — publish to OUTBOX; remote INBOX sources + transforms
    subj = subject.Outbox(h.siteID, userSite, "member_added")
    // → "outbox.{srcSite}.to.{destSite}.member_added"
}
_, _ = h.js.Publish(ctx, subj, outboxData)
```

- **Local invite** (`userSite == h.siteID`): publishes directly to `chat.inbox.{localSite}.member_added`. Lands in local INBOX via its Subjects filter. inbox-worker's filter (`aggregate.>`) does NOT match → inbox-worker skips it (correct — room-worker already wrote Mongo).
- **Federated invite** (`userSite != h.siteID`): publishes to `outbox.{localSite}.to.{userSite}.member_added`. Lands in local OUTBOX. Remote INBOX sources it → SubjectTransform rewrites to `chat.inbox.{userSite}.aggregate.member_added`. Remote inbox-worker processes it (writes Mongo), remote search-sync-worker indexes it.

### Consumers on INBOX

The enhanced INBOX stream is shared by three consumers with independent durable names and subject filters:

| Durable consumer | FilterSubject(s) | What it sees | Purpose |
|---|---|---|---|
| **inbox-worker** | `chat.inbox.{siteID}.aggregate.>` | **Only federated events** — never local | Federated Mongo writes: upsert Room and create Subscription from `MemberAddedPayload` |
| **spotlight-sync** (new) | `chat.inbox.{siteID}.member_added` **+** `chat.inbox.{siteID}.aggregate.member_added` (+ `_removed` variants) | Both local and federated member events | Sync subscription docs to spotlight index |
| **user-room-sync** (new) | Same as spotlight-sync | Both local and federated member events | Maintain per-user rooms array |

For spotlight-sync and user-room-sync, use NATS 2.10+ `FilterSubjects` (plural) so a single consumer subscribes to both the local and aggregate prefixes:

```go
cons, err := js.CreateOrUpdateConsumer(ctx, "INBOX_"+siteID, jetstream.ConsumerConfig{
    Durable:   "spotlight-sync",
    AckPolicy: jetstream.AckExplicitPolicy,
    FilterSubjects: []string{
        fmt.Sprintf("chat.inbox.%s.member_added", siteID),              // local
        fmt.Sprintf("chat.inbox.%s.member_removed", siteID),
        fmt.Sprintf("chat.inbox.%s.aggregate.member_added", siteID),    // federated
        fmt.Sprintf("chat.inbox.%s.aggregate.member_removed", siteID),
    },
    BackOff: []time.Duration{1 * time.Second, 5 * time.Second, 30 * time.Second},
})
```

**Why this prevents double-processing:** inbox-worker's filter only matches `chat.inbox.{siteID}.aggregate.>`, so locally-published events (which don't contain `aggregate`) are invisible to it. Since room-worker already writes to the local Mongo for local invites, inbox-worker has nothing to do for those. For federated events, the SubjectTransform routes them to the `aggregate` prefix where inbox-worker (and search-sync-worker) both see them via independent JetStream consumer delivery.

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
- `FilterSubjects` (plural) → `["chat.inbox.{siteID}.member_added", "chat.inbox.{siteID}.member_removed", "chat.inbox.{siteID}.aggregate.member_added", "chat.inbox.{siteID}.aggregate.member_removed"]` — both local and federated member events
- `BuildAction` → unmarshal `OutboxEvent` → `MemberAddedPayload`; for `member_added` emit an `ActionIndex` with the SpotlightSearchIndex doc keyed by `sub.ID`; for `member_removed` emit an `ActionDelete` keyed by `sub.ID`. Returns `[]BulkAction` with one action.

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
- `FilterSubjects` (plural) → `["chat.inbox.{siteID}.member_added", "chat.inbox.{siteID}.member_removed", "chat.inbox.{siteID}.aggregate.member_added", "chat.inbox.{siteID}.aggregate.member_removed"]` — both local and federated member events
- `BuildAction` → unmarshal `OutboxEvent` → `MemberAddedPayload`; skip if `payload.Subscription.HistorySharedSince != nil` (restricted — search service handles via DB+cache at query time); otherwise emit an `ActionUpdate` adding/removing `sub.RoomID` from the user's `rooms` array, keyed by `sub.User.Account`. Returns a slice of one action (or empty if restricted).

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

After creating the subscription and fetching the room (both already happen in `processInvite`), build a single `OutboxEvent{Type: "member_added"}` with the **new `MemberAddedPayload`** (full `Subscription` + `Room`), and publish it based on the **user's siteID**:

```go
// sub and room are already available in processInvite
payload := model.MemberAddedPayload{
    Subscription: sub,
    Room:         *room,
}
payloadData, _ := json.Marshal(payload)

userSite := invitee.SiteID  // user's home site (resolved from invite request or account lookup)

outboxEvt := model.OutboxEvent{
    Type:       "member_added",
    SiteID:     h.siteID,
    DestSiteID: userSite,   // user's home site (not sub.SiteID — that's the room's origin)
    Payload:    payloadData,
    Timestamp:  now.UnixMilli(),
}
data, _ := json.Marshal(outboxEvt)

var subj string
if userSite == h.siteID {
    // Local invite — publish directly to local INBOX
    subj = subject.InboxMemberAdded(h.siteID)
    // → "chat.inbox.{siteID}.member_added"
} else {
    // Federated invite — publish to OUTBOX; remote INBOX sources and transforms to aggregate.>
    subj = subject.Outbox(h.siteID, userSite, "member_added")
    // → "outbox.{srcSite}.to.{destSite}.member_added"
}
_, _ = h.js.Publish(ctx, subj, data)
```

**Note on user siteID lookup:** `userSite` must be the user's home site, NOT the room's origin site (`sub.SiteID`). Depending on VJ's parallel PR, this may be available directly on `InviteMemberRequest`, or may need to be looked up from the user's account record. Not resolved in this spec.

**Single publish, single event:** room-worker does NOT publish a separate `SubscriptionChangeEvent`. The one `OutboxEvent{Type: "member_added"}` with `MemberAddedPayload` serves both inbox-worker (for federated Mongo writes on the destination site) and search-sync-worker (for spotlight/user-room indexing). No duplication, no double-processing.

### inbox-worker/main.go

Update the INBOX stream config to use `stream.Inbox(siteID, remoteSiteIDs)` which now includes:
- Subjects `chat.inbox.{siteID}.>` (accepts both local direct publishes and aggregate-prefixed transformed events)
- Sources with `SubjectTransforms` that rewrite `outbox.*.to.{siteID}.>` → `chat.inbox.{siteID}.aggregate.>`

Remote site IDs come from config (e.g., `REMOTE_SITE_IDS=site-b,site-c`).

### inbox-worker/handler.go

**Subject filter update required.** The durable consumer's `FilterSubject` must be updated to only see **federated** events (aggregate prefix):

```go
FilterSubject: fmt.Sprintf("chat.inbox.%s.aggregate.>", cfg.SiteID)
```

This ensures inbox-worker only processes events sourced from remote OUTBOX streams. Local events (published directly to `chat.inbox.{siteID}.{eventType}` without the `aggregate` segment) are invisible to inbox-worker, preventing duplicate Mongo writes — room-worker has already written those locally.

**Changes to `handleMemberAdded`:**
- Unmarshal the payload as `MemberAddedPayload` (full `Subscription` + `Room`) instead of `InviteMemberRequest`
- **Upsert the Room** into local Mongo first (so the federated user sees room metadata)
- **Write the Subscription** into local Mongo using `payload.Subscription` (no longer constructs a subscription from fields; uses the one from the payload as-is so the ID, HistorySharedSince, JoinedAt, etc. match the source site's values)
- Continue to publish `SubscriptionUpdateEvent` on the pub/sub subject for client notification (existing behavior)

### pkg/stream/stream.go

Update `Inbox(siteID string)` to `Inbox(siteID string, remoteSiteIDs []string)` with:
- `Subjects: []string{fmt.Sprintf("chat.inbox.%s.>", siteID)}`
- `Sources: [...]` for each remote site, with `FilterSubject: outbox.{remote}.to.{siteID}.>` and `SubjectTransforms` rewriting to `chat.inbox.{siteID}.aggregate.>`

### pkg/subject/subject.go

Add builders:
- `InboxMemberAdded(siteID string)` → `chat.inbox.{siteID}.member_added` (local publish target)
- `InboxMemberRemoved(siteID string)` → `chat.inbox.{siteID}.member_removed`
- `InboxAggregatePattern(siteID string)` → `chat.inbox.{siteID}.aggregate.>` (inbox-worker filter)

The existing `Outbox(srcSite, destSite, eventType)` builder is reused unchanged. Federated events use the same `outbox.{src}.to.{dst}.member_added` subject as today; the SubjectTransform handles the rewrite on the destination INBOX.

### pkg/model/event.go

Add a new `MemberAddedPayload` struct:

```go
type MemberAddedPayload struct {
    Subscription Subscription `json:"subscription"` // full subscription
    Room         Room         `json:"room"`         // full room metadata
}
```

This **replaces** `InviteMemberRequest` as the payload type for `OutboxEvent{Type: "member_added"}`. `InviteMemberRequest` remains as the NATS request type for the original invite API call; the `MemberAddedPayload` is what goes into the OutboxEvent payload after room-worker has created the subscription and fetched the room.

### search-sync-worker/main.go

Start three `runConsumer` goroutines — one per collection (messages, spotlight, user-room). The spotlight and user-room consumers bind to the local INBOX stream with `FilterSubjects` (plural) listing both the local and aggregate member subject patterns. inbox-worker continues to run independently with its own consumer on the same INBOX stream, filtered to `chat.inbox.{siteID}.aggregate.>` — it never sees local events.

---

## Implementation Order

1. `Collection.BuildAction` return type change (slice) + update messageCollection + handler + tests
2. `ActionUpdate` support in `pkg/searchengine` adapter + tests
3. Introduce `MemberAddedPayload{Subscription, Room}` in `pkg/model/event.go` (replaces `InviteMemberRequest` as the `member_added` OutboxEvent payload type) + new subject builders + `pkg/stream.Inbox()` enhanced config with SubjectTransforms
4. `spotlightCollection` implementation + tests (consumer on INBOX with `chat.inbox.{siteID}.member_added` + `chat.inbox.{siteID}.aggregate.member_added` filters)
5. `userRoomCollection` implementation + tests (same filter patterns)
6. inbox-worker migration:
   - Update consumer `FilterSubject` to `chat.inbox.{siteID}.aggregate.>`
   - Update `handleMemberAdded` to unmarshal `MemberAddedPayload` and upsert the Room in addition to writing the Subscription
7. room-worker changes: route publishes using the user's siteID; publish single enriched `member_added` OutboxEvent to either local INBOX (same-site) or OUTBOX (cross-site)
8. search-sync-worker main.go (wire up all three collections with distinct consumer filters)
9. Integration tests covering local and federated member_added events

---

## Future Work

- **Member removal handler** — room-service authorization + room-worker publishes Action="removed"
- **Search API service** — spotlight search + message search with user-room access control
- **Restricted room cache** — search service queries DB for HistorySharedSince, caches per user
