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

## Service Layout (flat, per `CLAUDE.md`)

```
search-service/
  main.go                 # config parsing, ES/Valkey/NATS wiring, natsrouter setup, graceful shutdown
  handler.go              # Handler struct + SearchMessages, SearchRooms methods; RegisterHandlers
  query_messages.go       # buildMessageQuery(req, account, restricted) -> json.RawMessage
  query_rooms.go          # buildRoomQuery(req, account) -> json.RawMessage
  response.go             # parse ES response JSON into typed *SearchMessagesResponse / *SearchRoomsResponse
  store.go                # SearchStore + RestrictedRoomCache interfaces + //go:generate mockgen directive
  store_es.go             # esStore: Search(indices, body), GetUserRoomDoc(account)
  store_valkey.go         # valkeyCache: GetRestricted, SetRestricted
  handler_test.go         # unit tests with mocked stores
  query_messages_test.go  # golden-file tests on buildMessageQuery
  query_rooms_test.go     # golden-file tests on buildRoomQuery
  response_test.go        # golden-file tests on ES response parsing
  integration_test.go     # //go:build integration — NATS + ES + Valkey testcontainers, including CCS test
  mock_store_test.go      # generated via `make generate`, never hand-edited
  testdata/
    es_messages_response.json
    es_spotlight_response.json
    es_user_room_doc.json
    query_messages_global.json
    query_messages_scoped.json
    query_rooms_all.json
  deploy/
    Dockerfile
    docker-compose.yml
    azure-pipelines.yml
```

### Rationale for file split

- `query_messages.go` / `query_rooms.go` are pulled out of `handler.go` — ES query construction is non-trivial (partitioning, terms-lookup, per-restricted-room clauses) and earns its own golden-file tests independent of handler orchestration.
- `response.go` isolates ES response parsing.
- `store.go` defines **two** interfaces (`SearchStore`, `RestrictedRoomCache`), mocked independently so unit tests partition cleanly (cache-hit / cache-miss / ES-error / parse-error / empty-results).

---

## Shared-code Additions (`pkg/`)

| Package | Change | What's added |
|---|---|---|
| `pkg/searchengine` | Extend | `Search(ctx, indices []string, body json.RawMessage) (json.RawMessage, error)` and `GetDoc(ctx, index, docID string) (json.RawMessage, bool, error)` on `SearchEngine` interface; http adapter implementation. |
| `pkg/subject` | Extend | `SearchMessages(account string) string`, `SearchRooms(account string) string`, `SearchMessagesPattern() string`, `SearchRoomsPattern() string`. Plus tests. |
| `pkg/model` | Extend | `SearchMessagesRequest`, `SearchMessagesResponse`, `MessageSearchHit`, `SearchRoomsRequest`, `SearchRoomsResponse`, `RoomSearchHit`. Plus `model_test.go` round-trip cases. |
| `pkg/valkeyutil` | New | `Connect(addr, password string) (*valkey.Client, error)` using `valkey-go` driver, plus context-bounded `GetJSON[T]`, `SetJSONWithTTL` helpers. Modeled on `pkg/mongoutil`. |

---

## Configuration

Standard nested-prefix pattern via `caarlos0/env`:

```go
type ESConfig struct {
    URL     string `env:"URL" required:"true"`
    Backend string `env:"BACKEND" envDefault:"elasticsearch"`
}

type ValkeyConfig struct {
    Addr     string `env:"ADDR"     required:"true"`
    Password string `env:"PASSWORD" envDefault:""`
}

type NATSConfig struct {
    URL       string `env:"URL" required:"true"`
    CredsFile string `env:"CREDS_FILE" envDefault:""`
}

type SearchConfig struct {
    DocCounts               int           `env:"DOC_COUNTS"                 envDefault:"25"`
    MaxDocCounts            int           `env:"MAX_DOC_COUNTS"             envDefault:"100"`
    RestrictedRoomsCacheTTL time.Duration `env:"RESTRICTED_ROOMS_CACHE_TTL" envDefault:"5m"`
    RecentWindow            time.Duration `env:"RECENT_WINDOW"              envDefault:"8760h"` // 1y
    RequestTimeout          time.Duration `env:"REQUEST_TIMEOUT"            envDefault:"10s"`
}

type Config struct {
    SiteID string       `env:"SITE_ID" envDefault:"site-local"`
    ES     ESConfig     `envPrefix:"SEARCH_"`
    Valkey ValkeyConfig `envPrefix:"VALKEY_"`
    NATS   NATSConfig   `envPrefix:"NATS_"`
    Search SearchConfig `envPrefix:"SEARCH_"`
}
```

**Env var summary:**

| Variable | Default | Required | Purpose |
|---|---|---|---|
| `SITE_ID` | `site-local` | no | Service identity, used in logs/traces. |
| `SEARCH_URL` | — | **yes** | Elasticsearch HTTP URL. |
| `SEARCH_BACKEND` | `elasticsearch` | no | Backend selector for `pkg/searchengine`. |
| `VALKEY_ADDR` | — | **yes** | Valkey addr (e.g. `valkey:6379`). |
| `VALKEY_PASSWORD` | `""` | no | Optional password. |
| `NATS_URL` | — | **yes** | NATS URL. |
| `NATS_CREDS_FILE` | `""` | no | Optional creds file. |
| `SEARCH_DOC_COUNTS` | `25` | no | Default `size`. |
| `SEARCH_MAX_DOC_COUNTS` | `100` | no | Size cap; silently clamps on overflow. |
| `SEARCH_RESTRICTED_ROOMS_CACHE_TTL` | `5m` | no | Valkey TTL for per-user restricted-rooms map. |
| `SEARCH_RECENT_WINDOW` | `8760h` | no | Basic filter window (messages-in-last-year). |
| `SEARCH_REQUEST_TIMEOUT` | `10s` | no | Per-request upper bound. |

No remote-cluster env var. No CCS toggle. The `messages-*,*:messages-*` index pattern is a hardcoded constant in the query builders.

---

## Error Handling

All handler errors flow through `natsrouter.RouteError`.

| Condition | Return | Client sees |
|---|---|---|
| `searchText` empty | `ErrBadRequest("searchText is required")` | `{"error":"searchText is required","code":"bad_request"}` |
| `size < 0` / `offset < 0` | `ErrBadRequest("size and offset must be non-negative")` | — |
| `scope == "app"` | `ErrBadRequest("scope=app not supported in MVP")` | — |
| `scope` unknown value | `ErrBadRequest("unknown scope: <value>")` | — |
| Valkey + ES user-room both fail | `ErrInternal("unable to resolve room access")` | `{"error":"unable to resolve room access","code":"internal"}` |
| ES `_search` fails / times out | `ErrInternal("search backend unavailable")` | — |
| ES response parse error | `ErrInternal("unexpected search response")` | — |
| User with no subs (empty `rooms[]` + empty `restrictedRooms{}`) | Success with `{total:0, results:[]}` — NOT an error | — |
| `size > SEARCH_MAX_DOC_COUNTS` | Silently clamped — no error | — |

Cache-layer errors alone NEVER fail the request — log-and-fall-through to ES. Only when both cache AND ES prefetch fail does the request error.

Internal error messages exposed to clients are sanitized (never include stack traces, raw ES responses, or backend hostnames). Root causes logged at `ERROR` level.

---

## Observability

**Tracing** (`pkg/otelutil`):
- `otelutil.InitTracer(ctx, "search-service")` in `main.go`.
- `pkg/natsrouter` starts a span per request.
- Child spans: `cache.get`, `es.user_room_get` (on cache miss), `es.search`. Each tagged with `account`, `search.kind` (`messages` | `rooms`), `results.total`, `cache.hit`.

**Metrics** (Prometheus, `prometheus/client_golang`):

| Metric | Type | Labels |
|---|---|---|
| `search_service_requests_total` | counter | `kind, status` (`ok`/`bad_request`/`internal`) |
| `search_service_request_duration_seconds` | histogram | `kind` |
| `search_service_cache_hits_total` | counter | `kind` |
| `search_service_cache_misses_total` | counter | `kind` |
| `search_service_es_duration_seconds` | histogram | `op` (`search`/`user_room_get`) |

Exposed on `:9090/metrics` via a minimal `http.Server` in `main.go`.

**Logging** (`log/slog`, JSON):
- `INFO` per successful request: subject, account, kind, total, duration_ms, cache_hit, request_id.
- `ERROR` per failed request: subject, account, kind, error, duration_ms, request_id.
- Request ID propagated by `natsrouter.RequestID()` middleware.
- NEVER log `searchText` verbatim (privacy) — log length/hash if needed.

---

## `docker-local` Additions

### `docker-local/compose.deps.yaml`

**Valkey:**

```yaml
valkey:
  image: valkey/valkey:8-alpine
  container_name: valkey
  ports:
    - "6379:6379"
  command: ["valkey-server", "--save", "", "--appendonly", "no"]
  healthcheck:
    test: ["CMD", "valkey-cli", "ping"]
    interval: 5s
    timeout: 3s
    retries: 5
```

**Kibana:**

```yaml
kibana:
  image: docker.elastic.co/kibana/kibana:8.17.0
  container_name: kibana
  ports:
    - "5601:5601"
  environment:
    - ELASTICSEARCH_HOSTS=http://elasticsearch:9200
    - XPACK_SECURITY_ENABLED=false
  depends_on:
    elasticsearch:
      condition: service_healthy
  healthcheck:
    test: ["CMD-SHELL", "curl -s http://localhost:5601/api/status | grep -q 'available'"]
    interval: 10s
    timeout: 5s
    retries: 10
```

### `docker-local/compose.services.yaml`

Add `search-service` with `depends_on: { valkey: healthy, elasticsearch: healthy, nats: healthy }` and the appropriate env vars wired to the other compose services.

---

## Testing Strategy

Minimum 80% coverage per `CLAUDE.md`; target 90%+ on handlers and query builders.

### Unit tests

**`handler_test.go` — `SearchMessages`:**
- Valid request, cache hit, unrestricted-only user → terms-lookup clause, no per-room clauses.
- Valid request, cache miss → `store.GetUserRoomDoc` called, cache populated, search executed with combined clauses.
- Valid request, cache error → fall-through to ES succeeds.
- Valid request, cache AND ES prefetch error → `ErrInternal`.
- Valid request, ES search error → `ErrInternal`.
- Scoped (`roomIds` set) with mix of restricted/unrestricted → partition correct.
- Empty `searchText` → `ErrBadRequest`.
- Negative `size`/`offset` → `ErrBadRequest`.
- `size > maxDocCounts` → silently clamped.
- Empty `rooms[]` + empty `restrictedRooms{}` → `{total:0, results:[]}`.
- User-room doc not found → empty-map cached, `{total:0, results:[]}`.

**`handler_test.go` — `SearchRooms`:**
- Each scope value (`all`, `channel`, `dm`) → correct filter.
- `scope="app"` → `ErrBadRequest`.
- Unknown scope → `ErrBadRequest`.
- Empty results → `{total:0, results:[]}`.

**`query_messages_test.go` / `query_rooms_test.go`:** golden-file byte-for-byte comparisons against `testdata/query_*.json` for every major case (global, scoped, unrestricted-only, restricted-only, mixed, empty, each scope value).

**`response_test.go`:** golden-file tests on `es_messages_response.json`, `es_spotlight_response.json`, `es_empty.json`. Also malformed-JSON error case.

### Integration tests (`//go:build integration`)

Using `testcontainers-go` (NATS, Elasticsearch, Valkey modules):

- `TestSearchService_SearchMessages_HappyPath` — seed messages + user-room doc, send request, assert shape.
- `TestSearchService_SearchMessages_Restricted` — user-room doc with entries in both `rooms[]` and `restrictedRooms{}`; assert filtering honors HSS bound.
- `TestSearchService_SearchMessages_ScopedRoomIds` — request subset mix, assert partition.
- `TestSearchService_SearchMessages_CacheBehavior` — first request triggers prefetch + set, second hits cache only.
- **`TestSearchService_SearchMessages_CCS_CrossCluster`** — two ES containers on same docker network, `PUT /_cluster/settings { "persistent": { "cluster.remote.remote1.seeds": ["es-remote1:9300"] } }` against local; seed distinct messages in each; assert merged results.
- `TestSearchService_SearchRooms_HappyPath` — seed spotlight docs, assert shape.
- `TestSearchService_SearchRooms_Scope_{Channel,DM,All}` — seed docs with mixed `roomType`, assert filtering.
- `TestSearchService_SearchRooms_AppRejected` — `scope=app` → `ErrBadRequest`.

**Test helpers:**
- `setupES(t)`, `setupValkey(t)`, `setupNATS(t)` — each returns connected client, registers `t.Cleanup`.
- `setupCCS(t, localClient, remoteHost)` — NEW helper for the CCS test: applies persistent cluster settings.
- `seedMessages(t, esClient, docs []MessageSearchIndex)`, `seedUserRoomDoc(t, esClient, doc userRoomDoc)`, `seedSpotlightDocs(t, esClient, docs []SpotlightSearchIndex)` — fixture helpers.

### Coverage gate

- `make test SERVICE=search-service` — unit tests with `-race`, >=80% line coverage.
- `make test-integration SERVICE=search-service` — integration including CCS test.

---

## Known MVP Parity Gaps (documented follow-ups)

| Gap | Impact | Follow-up needed |
|---|---|---|
| `attachment_text`, `card_data`, `msg` not in message index — only `content` searched | Messages with non-text content not discoverable | Extend `MessageSearchIndex` in search-sync-worker + message-worker event schema. |
| `hidden` field not indexed | Hidden messages leak into results | Extend message index. |
| `t=tcard` / `visibleTo` filter logic dropped | User-specific card visibility not enforced | Extend message index; restore filter. |
| `tshow` field not indexed | Restricted users miss "also shown in channel" thread replies posted before their HSS | Extend message-gatekeeper + message index. |
| `archived`, `prid`, `botDM` not indexed for room search | Archived/thread-sub/bot-DM rooms not filtered | Extend spotlight index + event payload. |
| `fname`, `sidebarname` not in spotlight — multi-match on `roomName` only | Old display-name-aware search reduced | Extend spotlight index. |
| `ls`, `_updatedAt`, `fname` sort not available | Room search sort is score+joinedAt only | Extend spotlight index + indexed fields. |
| `scope=app` (bot DM) rejected | Clients lose that scope | Index `botDM`, re-enable. |
| No push invalidation of Valkey cache | Up to 5 min window where a user unsubscribed from a restricted room still sees their messages in search | Subscribe to `chat.user.{account}.event.subscriptions.changed` and delete cache entry. |
| Spotlight doesn't index restricted rooms | Restricted rooms missing from room search | Separate event-level flag or strategy; see companion sync-worker spec. |

---

## Companion Spec Dependency

This spec depends on `2026-04-21-search-service-sync-worker-extension-design.md`, which extends `search-sync-worker` to populate `restrictedRooms` in the user-room ES doc, and changes `pkg/model.InboxMemberEvent` / `MemberAddEvent` `HistorySharedSince` from `int64` → `*int64`.

Order of implementation:
1. **Companion spec first** — sync-worker schema, painless scripts, publisher-side pointer changes.
2. **This spec** — search-service consuming the new schema, plus `pkg/searchengine` / `pkg/subject` / `pkg/model` / `pkg/valkeyutil` additions, plus docker-local additions.

Backfill consideration: existing user-room docs lack `restrictedRooms`. Reindex strategy (drop+re-consume) is owned by the companion spec.

---

## Decision Log

- **Why `natsrouter` over raw `nc.QueueSubscribe`?** Typed handlers, error taxonomy, middleware (RequestID, Recovery, Logging), and param extraction are already built. Same approach as `history-service`.
- **Why flat layout despite `history-service` using `cmd/` + `internal/`?** User preference; matches the `CLAUDE.md` rule that governs new services.
- **Why CCS via `*:` wildcard without an enable flag?** Works uniformly whether remote clusters are configured or not. One fewer config knob, one fewer failure mode.
- **Why `messages-*,*:messages-*` and not just `*:messages-*`?** `*:` matches only remote-cluster aliases, not local. Local must be included explicitly. Without the local prefix, a user on site-a would miss their own site's messages.
- **Why Valkey over in-memory cache?** Shared across replicas; survives pod restarts; 5-min TTL bounds staleness. The lazy-populate pattern is simple enough that in-memory was viable too, but Valkey is a small incremental ops cost for significant multi-replica correctness wins.
- **Why offset+size pagination, not cursor?** Old HTTP APIs used offset+size; clients already built against it; score-sorted results don't cursor cleanly anyway.
- **Why NOT index `tshow`/`hidden`/`archived`/etc. in this spec?** Each requires message-domain or room-domain event-shape changes that cascade through gatekeeper/worker/sync-worker. Keeping this spec focused on the service + minimal sync-worker extension.
- **Why drop highlighting?** Measurable ES query cost; clients typically do their own client-side highlighting anyway; can be added post-MVP with no API break.

