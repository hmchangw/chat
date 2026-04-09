# message-worker: MongoDB User Lookup & Cassandra Schema Update

**Date:** 2026-04-09
**Status:** Approved

## Summary

Add MongoDB user lookup to `message-worker` so the sender `Participant` is enriched with `EngName` and `ChineseName` before persisting. Update the Cassandra store to write to the correct tables (`messages_by_room` and `messages_by_id`) with the proper `Participant` UDT. Create the shared `pkg/userstore` package (per the approved user model spec). Update `pkg/model/user.go` to match the approved user model spec.

## Motivation

- `message-worker` currently writes to a stale `messages` table with a schema that does not match the canonical Cassandra data model
- The `sender` field in both target tables is a `Participant` UDT that requires display names (`eng_name`, `company_name`) — unavailable without a user lookup
- `pkg/model/user.go` still has the old `Name` field; the approved spec (`2026-04-09-user-model-broadcast-lookup-design.md`) defines the updated shape but has not been implemented
- `pkg/userstore` does not exist yet; multiple services will need MongoDB user lookups

## Scope

This spec covers:
1. Updating `pkg/model/user.go` (per approved spec)
2. Creating `pkg/userstore` (per approved spec, extended with `FindUserByID`)
3. Updating `message-worker` to add MongoDB wiring, user lookup, and correct Cassandra writes

It does **not** cover the `broadcast-worker` migration from `employee` to `users` collection — that remains a separate task per the approved user model spec.

## Design

### 1. `pkg/model/user.go` — update

Remove `Name`. Add `EngName`, `ChineseName`, `EmployeeID`:

```go
type User struct {
    ID          string `json:"id"           bson:"_id"`
    Account     string `json:"account"      bson:"account"`
    SiteID      string `json:"siteId"       bson:"siteId"`
    EngName     string `json:"engName"      bson:"engName"`
    ChineseName string `json:"chineseName"  bson:"chineseName"`
    EmployeeID  string `json:"employeeId"   bson:"employeeId"`
}
```

### 2. `pkg/userstore/userstore.go` — new package

Exports a concrete `mongoStore` struct and `NewMongoStore` constructor. No exported interface — consumers define their own interface subset per CLAUDE.md.

```go
package userstore

// ErrUserNotFound is returned by FindUserByID when no user matches the given ID.
var ErrUserNotFound = errors.New("user not found")

type mongoStore struct {
    col *mongo.Collection
}

func NewMongoStore(col *mongo.Collection) *mongoStore

// FindUsersByAccounts returns users matching any of the given account names.
// Used by broadcast-worker (future).
func (s *mongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)

// FindUserByID returns the user with the given ID.
// Returns ErrUserNotFound (wrapped) if no document matches.
// Used by message-worker.
func (s *mongoStore) FindUserByID(ctx context.Context, id string) (*model.User, error)
```

- Both methods project only `_id`, `account`, `engName`, `chineseName` — enough for display name enrichment
- `FindUsersByAccounts` queries `{"account": {"$in": accounts}}`
- `FindUserByID` queries `{"_id": id}`, wraps `mongo.ErrNoDocuments` as `ErrUserNotFound`

#### `pkg/userstore/integration_test.go` (`//go:build integration`)
Integration tests using a real MongoDB via testcontainers. Covers:
- `FindUserByID`: found, not found (`ErrUserNotFound`), empty id
- `FindUsersByAccounts`: all found, partial match, empty slice

### 3. `message-worker` changes

#### `store.go` — two local interfaces

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store,UserStore

type Store interface {
    SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant) error
}

type UserStore interface {
    FindUserByID(ctx context.Context, id string) (*model.User, error)
}
```

`cassParticipant` is an unexported Cassandra-specific struct defined in `store_cassandra.go`. It is the concrete type passed from handler to store; it is not part of the interface signature from the caller's perspective — but since Go interfaces match on exact types, `cassParticipant` must be exported or the interface must use a different type. See note below.

**Note on `cassParticipant` visibility:** Because `Store.SaveMessage` is called from `handler.go` (same package `main`), `cassParticipant` can remain unexported within the `main` package. This is valid since all files share `package main`.

#### `store_cassandra.go` — rewritten

Defines unexported `cassParticipant` struct:

```go
type cassParticipant struct {
    ID          string `cql:"id"`
    EngName     string `cql:"eng_name"`
    CompanyName string `cql:"company_name"` // ChineseName
    Account     string `cql:"account"`
    AppID       string `cql:"app_id"`
    AppName     string `cql:"app_name"`
    IsBot       bool   `cql:"is_bot"`
}
```

`SaveMessage` runs two inserts sequentially, populating only the fields available at this stage. All other columns (`mentions`, `attachments`, `file`, `card`, etc.) are omitted and default to `null` in Cassandra:

```go
INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id)
VALUES (?, ?, ?, ?, ?, ?)

INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id)
VALUES (?, ?, ?, ?, ?)
```

Columns populated: `room_id`, `created_at`, `message_id`, `sender` (UDT), `msg` (`Message.Content`), `site_id`.
Columns left null for now: `mentions`, `attachments`, `file`, `card`, `card_action`, `quoted_parent_message`, `target_user`, `visible_to`, `unread`, `reactions`, `deleted`, `type`, `sys_msg_data`, `tshow`, `thread_parent_id`, `thread_parent_created_at`, `edited_at`, `updated_at`.

Both use `gocql` UDT marshalling via the `cassParticipant` struct. If either insert fails, the error is returned immediately (no partial rollback — Cassandra has no transactions; the message will be retried via NAK and JetStream redelivery).

Fields `site_id` comes from `model.MessageEvent.SiteID`, not `model.Message` — so `SaveMessage` receives the full event or `siteID` is passed separately. **Decision:** pass `siteID string` as an additional parameter to keep the signature explicit:

```go
SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant, siteID string) error
```

#### `handler.go` — updated

`Handler` struct:
```go
type Handler struct {
    store     Store
    userStore UserStore
}
```

`processMessage` flow:
1. Unmarshal `model.MessageEvent`
2. `user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)` — if error → `fmt.Errorf("lookup user %s: %w", ..., err)` → NAK
3. Build `cassParticipant` from `user` fields + `evt.Message.UserAccount`
4. `h.store.SaveMessage(ctx, evt.Message, sender, evt.SiteID)` — if error → NAK
5. ACK

#### `main.go` — updated config and wiring

New config fields:
```go
MongoURI string `env:"MONGO_URI,required"`
MongoDB  string `env:"MONGO_DB" envDefault:"chat"`
```

Startup wiring (after Cassandra connect, before handler):
```go
mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
// fail fast on error
db := mongoClient.Database(cfg.MongoDB)
userStore := userstore.NewMongoStore(db.Collection("users"))
handler := NewHandler(store, userStore)
```

Shutdown (after `nc.Drain()`):
```go
func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
```

#### `handler_test.go` — updated

Table-driven tests covering all scenarios:

| Scenario | UserStore | Store | Expected |
|---|---|---|---|
| Happy path | returns user | SaveMessage OK | ACK |
| User not found | returns ErrUserNotFound | not called | NAK |
| User DB error | returns generic error | not called | NAK |
| Save error | returns user | SaveMessage error | NAK |
| Malformed JSON | not called | not called | NAK |

#### `integration_test.go` — updated

- `setupMongo(t)` — starts MongoDB testcontainer, seeds a `users` document
- `setupCassandra(t)` — creates `messages_by_room` and `messages_by_id` tables (plus UDT) in `chat_test` keyspace
- `TestCassandraStore_SaveMessage` — verifies rows in both tables with correct `sender` fields

## Error Handling

- User not found → `userstore.ErrUserNotFound` wrapped; handler logs and NAKs (JetStream will redeliver)
- MongoDB connection failure at startup → log + `os.Exit(1)`
- Cassandra insert failure → return error → NAK; no partial rollback (idempotent retry acceptable since message_id is part of primary key)

## Testing

- Unit tests: `make test SERVICE=message-worker` and `make test` (pkg/userstore)
- Integration tests: `make test-integration SERVICE=message-worker` and `make test-integration SERVICE=pkg/userstore`
- Run `make generate SERVICE=message-worker` after store interface changes to regenerate mocks

## Unchanged

- `broadcast-worker` — not migrated in this task; still uses `FindEmployeesByAccountNames` on its own `Store`
- `pkg/model/employee.go` — kept as-is
- All other services
