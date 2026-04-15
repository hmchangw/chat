# Multi-DB CRUD Tool — Design Spec

**Date:** 2026-04-15
**Branch:** `claude/multi-db-crud-tool-ED1x8`
**Status:** Design approved, pending implementation plan

## 1. Overview

A local developer tool that provides a browser-based UI for CRUD operations against MongoDB and Cassandra on a single page, with JSON export/import. Lives under `tools/multi-db-crud/` and follows the same packaging conventions as `tools/nats-debug/` — a flat Go service with a `//go:embed`'d static UI and a JSON API.

**Target user:** developers on this project working against local Mongo + Cassandra during development.

**Out of scope (v1):** authentication / RBAC, audit logging, production deployment, CSV or NDJSON formats, upsert imports, multi-keyspace switching within a single Cassandra connection, Cassandra updates that would change a primary key column, ALLOW FILTERING queries, SSE/websocket streaming.

## 2. Goals

- CRUD against arbitrary Mongo collections and Cassandra tables without code changes per collection/table.
- Optional pre-filled templates for known `pkg/model` types (hybrid schema awareness).
- Per-collection / per-table JSON export and insert-only JSON import.
- Both DBs visible simultaneously via a split-pane UI.
- Single binary, zero frontend build step, single `docker compose up` for local bring-up.

## 3. Non-goals

- Not a production admin console. No auth, no prod-safety features.
- Not a migration/schema-management tool.
- Not a general SQL/CQL query console.

## 4. File layout

```
tools/multi-db-crud/
  main.go                # config, Gin wiring, graceful shutdown, reaper start
  routes.go              # /api/... and /healthz registration
  handler.go             # HTTP handlers (orchestration + JSON responses)
  registry.go            # connectionID → Mongo/Cassandra session map + idle reaper
  mongo_ops.go           # CRUD + export + import for MongoDB
  cassandra_ops.go       # CRUD + export + import for Cassandra (PK-aware)
  templates.go           # reflect-based template generation from pkg/model
  static.go              # //go:embed static/*
  static/index.html      # single-page UI (vanilla HTML/JS/CSS)
  deploy/
    Dockerfile           # multi-stage alpine build
    docker-compose.yml   # tool + local Mongo + local Cassandra
    azure-pipelines.yml  # CI
  handler_test.go
  registry_test.go
  mongo_ops_test.go
  cassandra_ops_test.go
  templates_test.go
  integration_test.go    # //go:build integration
  mock_*_test.go         # mockgen output
  testdata/              # JSON fixtures, template golden files
  README.md
```

## 5. Configuration

Parsed via `caarlos0/env` into a typed `Config` struct in `main.go`.

| Env var | Default | Required | Description |
|---|---|---|---|
| `PORT` | `8091` | no | HTTP listener port |
| `IDLE_TIMEOUT_MINUTES` | `30` | no | How long an unused DB connection stays in the registry before the reaper closes it |
| `MAX_IMPORT_DOCS` | `10000` | no | Per-request safety cap on import size |
| `READ_ONLY` | `false` | no | When true, hides mutating UI controls and rejects non-GET API calls |
| `LOG_LEVEL` | `info` | no | slog level |

No DB URIs in env — they are entered in the UI per session.

## 6. Connection registry & lifecycle

### Types

```go
type connection struct {
    kind         string         // "mongo" | "cassandra"
    mongo        *mongo.Client  // nil for cassandra
    cass         *gocql.Session // nil for mongo
    cassKeyspace string         // cassandra only
    label        string         // user-provided nickname
    createdAt    time.Time
    lastUsed     atomic.Int64   // unix ms
}

type registry struct {
    mu          sync.RWMutex
    conns       map[string]*connection // opaque UUID keys
    idleTimeout time.Duration
    now         func() time.Time       // injectable for tests
}
```

### Operations

- `Connect(kind, uri, keyspace, label)` — open the underlying client, ping (Mongo `Ping`, Cassandra `SELECT release_version FROM system.local`), assign UUID, store, return `{id, label, kind, info}`. Fails fast on ping failure — does not store a broken connection.
- `Get(id)` — RLock, touch `lastUsed`, return `*connection` or `ErrNotFound`.
- `Close(id)` — full Lock, remove from map, close the underlying client/session.
- `List()` — returns connection metadata only (no URIs).
- `CloseAll()` — called on shutdown.

### Idle reaper

- Background goroutine launched from `main.go`, ticks every minute.
- For each connection where `now() - lastUsed > idleTimeout`, close and remove.
- Uses full `Lock` when deleting to avoid races with in-flight handlers.
- Stopped via `context.Context` during graceful shutdown.

### Graceful shutdown

- `pkg/shutdown.Wait` in `main.go`.
- Order: cancel reaper context → `registry.CloseAll()` (disconnect every Mongo client and Cassandra session) → `httpServer.Shutdown(ctx)`.
- Shutdown timeout: 25s (per project convention).

### Security note

- Connection URIs are not logged and not returned by `GET /api/connections`.
- Driver errors are sanitized before returning to the client — no credentials, no host:port leakage beyond what the user supplied.

## 7. API surface

All request/response bodies are JSON. `{id}` refers to the connection UUID.

### Connection management

| Method | Path | Purpose |
|---|---|---|
| `POST` | `/api/connect` | Body `{kind, uri, keyspace?, label}` → `{id, label, kind, info:{serverVersion,...}}`. Pings before returning. |
| `GET` | `/api/connections` | `[{id, kind, label, keyspace, createdAt}]` — no URIs. |
| `DELETE` | `/api/connections/{id}` | Close and remove. |

### MongoDB CRUD

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/mongo/{id}/collections` | `[{name, count}]` |
| `GET` | `/api/mongo/{id}/collections/{name}/docs?limit=50&skip=0&filter=<url-encoded JSON>` | `{total, docs:[...]}`. Empty filter means `{}`. |
| `POST` | `/api/mongo/{id}/collections/{name}/docs` | Insert one doc. Fails on `_id` conflict → 409. Returns the stored doc. |
| `PUT` | `/api/mongo/{id}/collections/{name}/docs/{docID}` | `ReplaceOne` by `_id`. Body is the full replacement doc. |
| `DELETE` | `/api/mongo/{id}/collections/{name}/docs/{docID}` | `DeleteOne` by `_id`. |

### Cassandra CRUD

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/cassandra/{id}/tables` | `[{name, columns:[{name, type, kind:"partition_key"|"clustering"|"regular"}]}]`. `kind` comes from `system_schema.columns`. |
| `GET` | `/api/cassandra/{id}/tables/{name}/rows?limit=50&pageState=...` | `{rows:[...], nextPageState:"..."}`. Uses gocql paging, no OFFSET. |
| `POST` | `/api/cassandra/{id}/tables/{name}/rows` | `INSERT ... IF NOT EXISTS`. 409 on PK collision. |
| `PUT` | `/api/cassandra/{id}/tables/{name}/rows` | `UPDATE ... WHERE <pk cols>`. Rejects any attempt to change a PK column → 400. |
| `DELETE` | `/api/cassandra/{id}/tables/{name}/rows` | Body carries PK values only → `DELETE ... WHERE <pk cols>`. |

Cassandra write endpoints take the full row (including PK values) in the body rather than encoding PKs in the URL, because multi-column clustering keys make URL tuples awkward.

### Export / Import

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/mongo/{id}/collections/{name}/export` | Streams a JSON array of documents. Chunked writes; never holds the entire collection in memory. |
| `GET` | `/api/cassandra/{id}/tables/{name}/export` | Same, iterating the gocql result set with paging. |
| `POST` | `/api/mongo/{id}/collections/{name}/import` | Body = JSON array. Insert-only. Returns `{inserted:N, failed:[{index, _id?, error}]}`. Enforces `MAX_IMPORT_DOCS`. |
| `POST` | `/api/cassandra/{id}/tables/{name}/import` | Same semantics, per-row `IF NOT EXISTS`. |

Mongo import uses individual `InsertOne` calls rather than `InsertMany{Ordered:false}` so each failure can be reported with a clean index + id pair. Acceptable for a dev tool capped at 10k docs.

### Templates

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/templates` | `[{kind:"room", description:"Room from pkg/model"}]` |
| `GET` | `/api/templates/{kind}` | `{template: {...zero-valued JSON derived from pkg/model.<Kind> via reflect}}` |

Zero `time.Time` is rendered as `""` in template JSON to keep it unambiguous. The list of known kinds is derived from `pkg/model` types (`Room`, `Subscription`, `Message`, `User`, `Employee`, …) registered in `templates.go`.

### Housekeeping

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/healthz` | `{"status":"ok"}` |

### Cross-cutting

- Request-ID middleware: generates/propagates `X-Request-ID`, includes it in every slog line.
- slog JSON access log: method, path, status, latency, reqID, connectionID (when relevant).
- Read-only middleware: when `READ_ONLY=true`, any non-GET returns 403.
- Error responses: `{"error": "<sanitized message>", "code": "<short-code>"}` with appropriate HTTP status. Never echo raw driver errors.

## 8. UI behavior

Single `static/index.html` with vanilla HTML/JS/CSS, embedded via `//go:embed`.

### Layout

- Split-pane 50/50 flex layout: Mongo panel on the left, Cassandra panel on the right.
- `@media (max-width: 900px)` collapses the layout to a vertical stack.
- Both panels are rendered from a single `renderPanel(kind, rootEl)` function; they differ only in endpoint paths and a few kind-specific fields.

### Per-panel structure (top to bottom)

1. **Connect bar**: URI input, keyspace input (Cassandra only), label input, Connect button. When connected: green status pill showing label + server version, Disconnect button. Multiple connections per panel are supported via a connection-selector dropdown.
2. **Collection/Table picker**: dropdown of collections/tables for the active connection. Selecting one loads the grid below.
3. **Row grid**: first 50 rows tabulated. Columns derived from row keys (Mongo) or schema (Cassandra — PK columns get a subtle badge). Pagination at the bottom: Mongo skip/limit; Cassandra opaque `pageState` Next/Prev (Prev via a client-side stack of prior tokens).
4. **Row editor**: modal opened on row click, `+ New`, or `+ From template`. JSON textarea with client-side JSON.parse validation. For Cassandra, `+ New` pre-seeds the editor client-side with all columns from the table schema (PK columns marked required); this is distinct from the `/api/templates` endpoint (which serves `pkg/model`-derived docs for Mongo).
5. **Filter bar (Mongo only)**: JSON input for the Mongo query filter, applied on Apply. Cassandra has no filter UI in v1 — ad-hoc filtering would require `ALLOW FILTERING` which is deliberately not exposed.
6. **Import / Export row**: Export downloads the current collection/table as a `.json` file (streamed from server). Import opens a file picker, POSTs the JSON array, and shows an inline result panel with `Inserted: N. Failed: M.` and an expandable failure list.

### Templates UI

A `From template` dropdown next to `+ New` lists kinds from `GET /api/templates` (Mongo panel only — templates are `pkg/model`-derived and map naturally to Mongo collections). Picking one fetches the template and opens the editor pre-filled. Purely a convenience; user can edit freely before submit.

### Feedback & safety

- API errors surface as a top-right toast (auto-dismiss 5s), and also inline under the offending form when triggered by form submit.
- Delete and Import both show a confirm dialog.
- Read-only mode hides all mutating buttons (and the server rejects them as a defense in depth).

## 9. Testing strategy

Follows project TDD rules: Red-Green-Refactor; `-race` always; 80% minimum / 90%+ target for core logic.

### Unit tests (no real DBs)

- **`handler_test.go`** — table-driven tests per handler, using mocked `mongoOps`, `cassandraOps`, and `registryAPI` interfaces (mockgen output in `mock_*_test.go`). Coverage includes: valid input; malformed JSON; missing/unknown connection ID; ops errors surfaced correctly; insert-only 409 path; read-only mode blocks non-GET.
- **`registry_test.go`** — Connect/Get/Close happy and error paths; idle reaper eviction using an injected `now func() time.Time` (no sleeps); concurrent access under `-race`.
- **`templates_test.go`** — reflect walker produces expected zero-valued JSON for each `pkg/model` type. Golden files in `testdata/templates/<kind>.json`. Covers nested structs, slices, pointers, `time.Time` (`""`).
- **`mongo_ops_test.go` / `cassandra_ops_test.go`** — thin unit tests for logic that doesn't need a live driver: Cassandra PK column extraction from a fake `system_schema.columns` row set; import failure aggregation; export streaming against a fake cursor.

### Integration tests (`//go:build integration`)

`integration_test.go` using testcontainers-go with the official `mongodb` and `cassandra` modules. Helpers `setupMongo(t)` and `setupCassandra(t)` register `t.Cleanup`.

Scenarios:

- End-to-end: connect → list collections/tables → insert → list → update → delete → export → import.
- Cassandra PK rejection: PUT a row with a changed PK column → 400.
- Insert-only enforcement: double insert same `_id` (Mongo) / same PK (Cassandra) → 409.
- Export/import round-trip: seed → export → wipe → import → contents match modulo ordering.
- Idle reaper: shortened `IDLE_TIMEOUT_MINUTES`, verify connection disappears.

Test database / keyspace names: `multi_db_crud_test`.

### What is not tested

- The static HTML/JS. Manually smoke-tested via `docker compose up`; smoke-test steps documented in `README.md`.

### Coverage targets

- Handler, registry, ops, templates: **90%+**.
- `main.go` wiring: best effort (standard project practice).

## 10. Deployment artifacts

- **`deploy/Dockerfile`**: multi-stage, `golang:1.25-alpine` builder → `alpine:3.21` runtime. Build context = repo root (to access `pkg/` and `go.mod`).
- **`deploy/docker-compose.yml`**: brings up the tool + a local `mongo:latest` + a local `cassandra:4.1`, exposing 8091 / 27017 / 9042.
- **`deploy/azure-pipelines.yml`**: same shape as other services' CI pipelines.
- **`README.md`**: quickstart (docker compose + direct binary), env var table, usage screenshots or descriptions, manual smoke-test checklist.

## 11. Risks & open questions

- **Reflect-based templates for complex nested types** (e.g. `Message` with attachments, mentions): initial implementation handles primitive fields, nested structs, slices, maps, pointers, and `time.Time`. If a `pkg/model` type introduces something the walker can't handle, the template endpoint returns a best-effort doc and logs a warning rather than erroring.
- **Large collections / tables**: export streams; import is capped at `MAX_IMPORT_DOCS`. Export has no size cap — acceptable given the local-dev scope, but documented in the README.
- **Cassandra schema diversity**: user-defined types, collections (list/set/map), counters, static columns. v1 supports all regular primitive columns and standard collections; counters and UDTs are best-effort and may return raw gocql-stringified values in exports. Documented as a known limitation.
- **Concurrent edits**: no optimistic locking / versioning. Two users editing the same doc in two tabs will clobber each other. Acceptable at local-dev scope.

## 12. Implementation sequence (preview)

Full implementation plan to be produced by `writing-plans`. Rough ordering:

1. Scaffold `tools/multi-db-crud/` with `main.go`, `routes.go`, `handler.go`, `static.go`, `/healthz`.
2. Registry + idle reaper with unit tests.
3. Mongo ops (CRUD, export, import) + handler wiring + unit tests.
4. Cassandra ops (schema introspection, PK-aware CRUD, export, import) + handler wiring + unit tests.
5. Templates generator + unit tests with golden files.
6. Integration tests with testcontainers.
7. Static UI: connect bars, pickers, grids, row editor modal, filter bar, import/export, templates dropdown, toasts, read-only mode.
8. `deploy/Dockerfile`, `deploy/docker-compose.yml`, `deploy/azure-pipelines.yml`, `README.md`.
