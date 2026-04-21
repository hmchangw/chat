# Design: Search Service (NATS Request/Reply)

**Status:** Draft
**Companion spec:** `2026-04-21-search-service-sync-worker-extension-design.md` (prerequisite — user-room restricted-rooms schema)

## Context

Previously the chat system exposed two HTTP APIs for search:
- `GET /api/v2/search/:username/messages` — global or room-scoped message search
- `GET /api/v2/search/:username/rooms` — room typeahead (spotlight)

Both backed by Elasticsearch indexes that were kept in sync via a Mongo change-stream processor (monstache-style CDC).

The new architecture replaces both, with:
- A new `search-service` exposing NATS request/reply endpoints (not HTTP).
- Index hydration via `search-sync-worker` consuming INBOX events (PR #109), not CDC.
- Restricted-room access stored in the ES `user-room` doc itself (see companion spec), removing the query-time Mongo dependency.
- Cross-cluster search (CCS) support on the messages index so a user's search reaches messages posted on remote sites.

This spec covers the `search-service` itself. The `user-room` schema extension is a prerequisite covered by the companion spec.

---

## Goals

1. Two NATS request/reply endpoints: `search.messages` and `search.rooms`, scoped under `chat.user.{account}.request.…`.
2. Cross-cluster search across `messages-*` via ES CCS wildcard alias — no service-side fan-out logic, no config flag.
3. Per-user restricted-rooms cache (Valkey, 5-min TTL, lazy-populated from ES `user-room` doc).
4. Pure read path — no Mongo, no Cassandra.
5. Reuse `pkg/natsrouter` for typed handlers, middleware, and error taxonomy.
6. Flat service layout per `CLAUDE.md` (`main.go`, `handler.go`, `store.go`, etc. at repo root).
7. Support MVP-level parity with the old HTTP APIs; document parity gaps explicitly.

## Non-Goals

- Highlighting in search results (client does its own).
- `scope=app` (bot DM) room search — deferred until `botDM` field is indexed.
- Thread-reply "also shown in channel" (`tshow`) visibility — `tshow` not in the message index.
- Sort by `ls` (last-seen), `_updatedAt`, `fname` on room search — fields not indexed.
- `archived`, `hidden`, `prid`, `tcard`, `visibleTo` filters — fields not indexed.
- Push invalidation of the restricted-rooms cache on subscription change — TTL-only invalidation for MVP.
- Frontend UI integration (separate follow-on spec).

---

## Key Design Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Transport | NATS request/reply, core NATS `QueueSubscribe` | Matches existing request/reply services (`room-service`, `history-service`). No HTTP gateway needed. |
| Router | Reuse `pkg/natsrouter` | Gin-style pattern routing, typed handlers, error taxonomy — exact fit. |
| Subject scheme | `chat.user.{account}.request.search.messages` / `…search.rooms` | Matches existing `chat.user.{account}.request.…` convention. |
| Layout | Flat: `search-service/main.go`, `handler.go`, `store.go`, etc. | Matches `CLAUDE.md` conventions. |
| Pagination | `offset` + `size` (capped at `SEARCH_MAX_DOC_COUNTS=100`, default `SEARCH_DOC_COUNTS=25`) | Matches old HTTP API; natural fit for ES `from + size`. |
| Total count | Returned (`track_total_hits: true`) | Clients need accurate page counts; offset-based requires it. |
| Result payload | Raw ES `_source` parsed into typed Go structs | Simplest MVP; no reshaping layer. |
| Highlighting | Not included in MVP | Keep queries cheap; clients handle rendering. |
| Cross-cluster | ES CCS wildcard alias `messages-*,*:messages-*` — messages only | Spotlight and user-room docs always live on the user's home site. Messages can live on remote sites where rooms are hosted. |
| CCS config flag | None | Wildcard alias works whether remote clusters are configured or not; local-only deployments silently resolve the `*:` segment to zero matches. |
| Restricted-rooms source | ES `user-room` doc (single local GET on cache miss) | Sync-worker companion spec writes `restrictedRooms` into the doc; no Mongo read at query time. |
| Restricted-rooms cache | Valkey, 5-min TTL, lazy-populate | Shared across replicas; graceful degradation on cache failure. |
| Room-search restricted handling | Not shown (spotlight MVP skips `hss != nil`) | Per companion spec MVP. Documented gap. |

---

## Architecture

```
┌────────┐  NATS request    ┌────────────────┐   Valkey GET/SET    ┌────────┐
│ client │ ───────────────→ │ search-service │ ←──────────────────→│ Valkey │
└────────┘ ←─────────────── │  (natsrouter)  │                     └────────┘
              response       └───────┬────────┘
                                     │
                              ES HTTP (1 or 2 hops):
                                     │
                              ┌──────┴───────┐
                              │              │
                     GET /user-room/_doc   POST messages-*,*:messages-*/_search
                        (cache miss)         POST spotlight/_search
                                             │
                                             ▼
                                    ┌──────────────────────┐
                                    │ local Elasticsearch  │
                                    │ (CCS fans to remote  │
                                    │  clusters for        │
                                    │  messages-* only)    │
                                    └──────────────────────┘
```

### Data flow — `search.messages` (global)

1. Valkey `GET searchservice:restrictedrooms:{account}`.
2. **Miss:** ES `GET /user-room/_doc/{account}` → extract `restrictedRooms` map → Valkey `SET … TTL=5m`.
3. Build query body:
   - `multi_match` on `content` (bool_prefix + AND).
   - Basic filter: `range createdAt gte now-1y`.
   - Room-access clause (`bool should`, `minimum_should_match: 1`):
     - Unrestricted: `terms` lookup on `roomId` via `{ "index": "user-room", "id": "{account}", "path": "rooms" }`.
     - Restricted: one `bool must` clause per entry in `restrictedRooms` — see "Query Construction" below.
4. ES `POST messages-*,*:messages-*/_search?ignore_unavailable=true&allow_no_indices=true`.
5. Parse response → typed `SearchMessagesResponse`.
6. `natsrouter` JSON reply.

### Data flow — `search.messages` (scoped by `roomIds`)

Same as global, except the room-access clause partitions the requested `roomIds` into unrestricted vs restricted using the cached map:
- Unrestricted subset → `terms roomId: [...]` (inline list, no ES lookup needed).
- Restricted subset → per-room `bool must` clauses.

### Data flow — `search.rooms`

1. Build query body:
   - `multi_match` on `roomName` (bool_prefix + AND).
   - Filter: `term userAccount: {account}`.
   - Plus one conditional filter based on `req.scope`:
     - `"channel"` → `term roomType: p`
     - `"dm"` → `term roomType: d`
     - `"all"` → (no additional filter)
     - `"app"` → handler returns `ErrBadRequest("scope=app not supported in MVP")`
2. ES `POST spotlight/_search?ignore_unavailable=true&allow_no_indices=true` (local-only, no CCS).
3. Parse response → `SearchRoomsResponse`.
4. `natsrouter` JSON reply.

### Cross-cluster scope

| Index | Pattern | Why |
|---|---|---|
| `messages-*` | `messages-*,*:messages-*` | Messages live on the site where they were posted; CCS needed to find cross-site rooms' messages. |
| `spotlight` | `spotlight` | Spotlight docs live on the user's home site (where sync-worker runs for them). Local-only. |
| `user-room` | `user-room` | Same — user's home site only. |

`*:` is a wildcard over configured remote-cluster aliases. If no remote clusters are configured on local ES, the `*:messages-*` segment resolves to zero matches and the query proceeds against local `messages-*` only. No service-side toggle required.

---

## Query Construction

### Message search (final JSON shape)

```json
{
  "from": "{offset}",
  "size": "{size}",
  "track_total_hits": true,
  "query": {
    "bool": {
      "must": [
        {
          "multi_match": {
            "query":    "{searchText}",
            "type":     "bool_prefix",
            "operator": "AND",
            "fields":   ["content"]
          }
        }
      ],
      "filter": [
        { "range": { "createdAt": { "gte": "now-1y" } } },
        {
          "bool": {
            "should":               "{...room-access-clauses...}",
            "minimum_should_match": 1
          }
        }
      ]
    }
  },
  "sort": [ "_score", { "createdAt": "desc" } ]
}
```

**Room-access clauses — global search:**

```json
[
  { "terms": {
      "roomId": { "index": "user-room", "id": "{account}", "path": "rooms" }
  }},
  { "bool": { "must": [                            // per restrictedRooms entry (repeat)
      { "term":  { "roomId": "{rid}" } },
      { "range": { "createdAt": { "gte": "{hssISO8601}" } } }
  ]}},
  { "bool": { "must": [                            // Clause B-reduced (thread-reply path)
      { "term":   { "roomId": "{rid}" } },
      { "exists": { "field": "threadParentMessageId" } },
      { "range":  { "threadParentMessageCreatedAt": { "gte": "{hssISO8601}" } } }
  ]}}
]
```

For each restricted room, emit **both** Clause A (parent/regular message after HSS) and Clause B-reduced (thread reply whose parent is after HSS). The B1 `tshow=true` branch is dropped — `tshow` is not indexed.

**Room-access clauses — scoped search (`req.roomIds != nil`):**

Partition `req.roomIds` using the cached `restrictedRooms` map:
- Unrestricted subset → single `{ "terms": { "roomId": [...] } }` clause (inline list, no ES terms-lookup).
- Restricted subset → same Clause A + Clause B-reduced pair per rid as above.

### Room search (final JSON shape)

```json
{
  "from": "{offset}",
  "size": "{size}",
  "track_total_hits": true,
  "query": {
    "bool": {
      "must": [
        {
          "multi_match": {
            "query":    "{searchText}",
            "type":     "bool_prefix",
            "operator": "AND",
            "fields":   ["roomName"]
          }
        }
      ],
      "filter": [
        { "term": { "userAccount": "{account}" } }
        /* plus ONE of (from req.scope):
           "channel" → { "term": { "roomType": "p" } }
           "dm"      → { "term": { "roomType": "d" } }
           "all"     → nothing
           "app"     → handler rejects with ErrBadRequest
        */
      ]
    }
  },
  "sort": [ "_score", { "joinedAt": "desc" } ]
}
```

### Restricted-rooms cache lookup (pseudocode)

```go
func (h *Handler) loadRestricted(ctx context.Context, account string) (map[string]int64, error) {
    cached, hit, err := h.cache.GetRestricted(ctx, account)
    if err != nil {
        slog.Warn("valkey read failed; falling through to ES", "err", err, "account", account)
        // fall through — do NOT fail search on cache error
    }
    if hit {
        return cached, nil
    }
    doc, found, err := h.store.GetUserRoomDoc(ctx, account)
    if err != nil {
        return nil, fmt.Errorf("loading user-room doc: %w", err)
    }
    if !found {
        // new user with no subs; cache empty to prevent miss-storm
        _ = h.cache.SetRestricted(ctx, account, map[string]int64{}, h.cacheTTL)
        return map[string]int64{}, nil
    }
    _ = h.cache.SetRestricted(ctx, account, doc.RestrictedRooms, h.cacheTTL)
    return doc.RestrictedRooms, nil
}
```

Cache-layer failures degrade gracefully: log + fall through to ES prefetch. Only if both cache AND ES prefetch fail does the request error.

---

## Wire Schemas (`pkg/model`)

### Requests

```go
type SearchMessagesRequest struct {
    SearchText string   `json:"searchText"`
    RoomIds    []string `json:"roomIds,omitempty"` // absent/empty = global
    Size       int      `json:"size,omitempty"`
    Offset     int      `json:"offset,omitempty"`
}

type SearchRoomsRequest struct {
    SearchText string `json:"searchText"`
    Scope      string `json:"scope,omitempty"` // "all" | "channel" | "dm" | "app"
    Size       int    `json:"size,omitempty"`
    Offset     int    `json:"offset,omitempty"`
}
```

### Responses

```go
type SearchMessagesResponse struct {
    Total   int64             `json:"total"`
    Results []MessageSearchHit `json:"results"`
}

type MessageSearchHit struct {
    MessageID             string     `json:"messageId"`
    RoomID                string     `json:"roomId"`
    SiteID                string     `json:"siteId"`
    UserID                string     `json:"userId"`
    UserAccount           string     `json:"userAccount"`
    Content               string     `json:"content"`
    CreatedAt             time.Time  `json:"createdAt"`
    ThreadParentMessageID string     `json:"threadParentMessageId,omitempty"`
    ThreadParentCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty"`
}

type SearchRoomsResponse struct {
    Total   int64           `json:"total"`
    Results []RoomSearchHit `json:"results"`
}

type RoomSearchHit struct {
    RoomID      string    `json:"roomId"`
    RoomName    string    `json:"roomName"`
    RoomType    string    `json:"roomType"`
    UserAccount string    `json:"userAccount"`
    SiteID      string    `json:"siteId"`
    JoinedAt    time.Time `json:"joinedAt"`
}
```

All types get `model_test.go` round-trip coverage (nil/empty/full cases).

---

(Continued in follow-up commit for layout, config, testing, ops.)
