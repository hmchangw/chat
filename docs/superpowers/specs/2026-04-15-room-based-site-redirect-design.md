# Room-Based Site Redirect for History Service

## Problem

The current site-routing in `history-service` embeds `{siteID}` as a wildcard parameter in NATS subject patterns. The `SiteProxy` middleware reads siteID from the subject and forwards to remote sites when it differs from the local config. This means the **client decides** which site to target.

The desired behavior is the opposite: the **server decides** by looking up the room in MongoDB and checking `room.SiteID`. This is more correct because the room's authoritative site is stored in the database, not inferred from the client's request.

## Changes

### 1. Subject Patterns — `pkg/subject/subject.go`

The 4 history-service pattern functions change from zero-arg with `{siteID}` placeholder to taking a `siteID string` argument that gets baked into the literal subject:

| Function | Before | After |
|----------|--------|-------|
| `MsgHistoryPattern` | `() string` → `...{siteID}.msg.history` | `(siteID string) string` → `...<siteID>.msg.history` |
| `MsgNextPattern` | `() string` → `...{siteID}.msg.next` | `(siteID string) string` → `...<siteID>.msg.next` |
| `MsgSurroundingPattern` | `() string` → `...{siteID}.msg.surrounding` | `(siteID string) string` → `...<siteID>.msg.surrounding` |
| `MsgGetPattern` | `() string` → `...{siteID}.msg.get` | `(siteID string) string` → `...<siteID>.msg.get` |

Each site subscribes only to subjects containing its own siteID. The natsrouter param extraction no longer yields a `siteID` param — position 6 is a literal string, not a `*` wildcard.

Update `pkg/subject/subject_test.go` to cover the new signatures.

### 2. RouterGroup — `pkg/natsrouter/group.go`

Add a Gin-style `RouterGroup` to `natsrouter`. The design follows Gin's `RouterGroup` closely:

- `RouterGroup` holds a `handlers` chain (middleware) and a reference to the parent `Registrar`.
- `Router.Group(mw ...HandlerFunc) *RouterGroup` creates a group inheriting the router as parent.
- `RouterGroup.Group(mw ...HandlerFunc) *RouterGroup` creates a nested sub-group with combined middleware.
- `RouterGroup.Use(mw ...HandlerFunc)` appends middleware to the group's chain.
- `RouterGroup` implements `Registrar` — in `addRoute` it prepends its middleware to the handler slice, then delegates to its parent's `addRoute`.

Resulting middleware chain for a route registered on a group:

```
router.middleware → group.middleware → handler
```

For nested groups:

```
router.middleware → outerGroup.middleware → innerGroup.middleware → handler
```

New files:
- `pkg/natsrouter/group.go` — `RouterGroup` type
- `pkg/natsrouter/group_test.go` — tests covering: group middleware execution order, nested groups, `Use()` after creation, group isolation (sibling groups don't share middleware)

### 3. Room Repository — `history-service/internal/mongorepo/room.go`

New `RoomRepo` following the existing `SubscriptionRepo` pattern:

```go
type RoomRepo struct {
    rooms *Collection[model.Room]
}

func NewRoomRepo(db *mongo.Database) *RoomRepo

func (r *RoomRepo) GetRoom(ctx context.Context, roomID string) (*model.Room, error)
```

- Collection name: `rooms`
- Uses `Collection[model.Room]` (generic collection wrapper already in `mongorepo/collection.go`)
- `GetRoom` queries by `_id` (roomID is the primary key)
- Returns `(nil, nil)` when not found (consistent with `SubscriptionRepo` pattern)
- Uses `model.Room` from `pkg/model/room.go`

### 4. Middleware Rewrite — `history-service/internal/middleware/siteproxy.go`

The middleware interface needs a room-lookup function. Define a consumer-side interface in the middleware package:

```go
type RoomFinder interface {
    GetRoom(ctx context.Context, roomID string) (*model.Room, error)
}
```

New middleware signature:

```go
func SiteProxy(localSiteID string, nc *nats.Conn, rooms RoomFinder) natsrouter.HandlerFunc
```

Behavior:

1. **Already forwarded?** — If `X-Forwarded-Site` header exists, proceed locally (`c.Next()`). Prevents infinite loops.
2. **Extract roomID** — `c.Param("roomID")`.
3. **Fetch room** — `rooms.GetRoom(ctx, roomID)`.
4. **Room not found** — Reply error `"room not found"`, abort.
5. **Local room** — `room.SiteID == localSiteID` → `c.Next()`.
6. **Remote room** — `room.SiteID != localSiteID` → forward:
   - Construct forward subject by replacing `localSiteID` with `room.SiteID` in `c.Msg.Subject` (single `strings.Replace`)
   - Copy headers, set `X-Forwarded-Site: localSiteID`
   - `nc.RequestMsg()` with 10s timeout
   - Relay response or reply `"remote site unavailable"` on error
   - Abort

### 5. Handler Registration — `history-service/internal/service/service.go`

`RegisterHandlers` changes to accept `siteID` for subject construction and a `*natsrouter.RouterGroup` for the 3 endpoints that need the room-site middleware:

```go
func (s *HistoryService) RegisterHandlers(r *natsrouter.Router, g *natsrouter.RouterGroup, siteID string) {
    natsrouter.Register(g, subject.MsgHistoryPattern(siteID), s.LoadHistory)
    natsrouter.Register(g, subject.MsgNextPattern(siteID), s.LoadNextMessages)
    natsrouter.Register(g, subject.MsgSurroundingPattern(siteID), s.LoadSurroundingMessages)
    natsrouter.Register(r, subject.MsgGetPattern(siteID), s.GetMessageByID)
}
```

Three endpoints go through the group (with room-site middleware), `GetMessageByID` goes directly on the router (no room lookup needed).

### 6. Wiring — `history-service/cmd/main.go`

- Create `mongorepo.NewRoomRepo(mongoClient.Database(cfg.Mongo.DB))`
- Remove global `router.Use(middleware.SiteProxy(...))`
- Create the group: `g := router.Group(middleware.SiteProxy(cfg.SiteID, nc.NatsConn(), roomRepo))`
- Pass both `router` and `g` to `svc.RegisterHandlers(router, g, cfg.SiteID)`

### 7. Handler Logic — No Changes

`messages.go` handlers are untouched. They don't read the `siteID` param — they only use `account` and `roomID` which remain as `{param}` placeholders.

## What Does NOT Change

- `pkg/subject` parse functions (`ParseUserRoomSubject`, `ParseUserRoomSiteSubject`)
- Other services
- Cassandra repo
- Handler logic in `messages.go`
- `natsrouter` core (context, params, register, errors, existing middleware)
- `mongorepo/collection.go`, `mongorepo/subscription.go`

## Files Changed

| File | Action |
|------|--------|
| `pkg/subject/subject.go` | Modify 4 pattern functions to take `siteID` param |
| `pkg/subject/subject_test.go` | Update tests for new signatures |
| `pkg/natsrouter/group.go` | **New** — `RouterGroup` type implementing `Registrar` |
| `pkg/natsrouter/group_test.go` | **New** — group tests |
| `history-service/internal/mongorepo/room.go` | **New** — `RoomRepo` |
| `history-service/internal/middleware/siteproxy.go` | Rewrite middleware logic + new `RoomFinder` interface |
| `history-service/internal/middleware/siteproxy_test.go` | Rewrite tests for new behavior |
| `history-service/internal/service/service.go` | Update `RegisterHandlers` signature |
| `history-service/cmd/main.go` | Wire `RoomRepo`, create group, update registration call |
