# Add Model Fields and Remove `Message.Unread`

**Date:** 2026-04-30
**Branch:** `claude/add-model-fields-njdU6`

## Goal

Two coordinated changes:

1. **Add fields** to two domain models in `pkg/model/`:
   - `Room.MinUserLastSeenAt` — earliest "last seen" timestamp across all
     subscribers of a room.
   - `Subscription.ThreadUnread` — set of `ThreadParentMessageID`s in this
     room that have unread replies for this user.
   - `Subscription.Alert` — stored materialization of
     `len(ThreadUnread) > 0`, maintained atomically with `ThreadUnread`
     mutations by the writer service.

2. **Remove the `Unread` column** from the Cassandra message model. Per-message
   unread state is being centralized at the subscription level via
   `ThreadUnread` / `Alert`; the `Message.Unread` column is no longer needed.

This spec covers model definitions, Cassandra column-list updates, schema
DDL (init scripts), the schema documentation, and the round-trip /
integration tests that exercise these shapes. Population logic for the new
subscription fields, Mongo indexes, handler logic, and prod Cassandra
`ALTER TABLE … DROP unread` are out of scope.

## Field Definitions

### `Room` (in `pkg/model/room.go`)

Add one field:

```go
MinUserLastSeenAt *time.Time `json:"minUserLastSeenAt,omitempty" bson:"minUserLastSeenAt,omitempty"`
```

- Pointer-to-time so absence is distinguishable from the zero time.
- `omitempty` on both tags so a nil value is omitted on the wire and in
  Mongo, matching the existing `LastMsgAt` and `LastMentionAllAt` pattern.

### `Subscription` (in `pkg/model/subscription.go`)

Add two fields:

```go
ThreadUnread []string `json:"threadUnread,omitempty" bson:"threadUnread,omitempty"`
Alert        bool     `json:"alert" bson:"alert"`
```

- `ThreadUnread` is a slice of `ThreadParentMessageID` strings. `omitempty`
  on both tags so empty/nil slices are not emitted, matching the project's
  preference for compact wire/storage representations.
- `Alert` is the stored materialization of `len(ThreadUnread) > 0`. It is
  always emitted (no `omitempty`) so legacy documents without the field
  decode to `false`, matching the existing `HasMention` pattern. The writer
  service is responsible for maintaining the invariant
  `Alert == (len(ThreadUnread) > 0)` atomically with every mutation; this
  spec does not implement that population path.

## Remove `Message.Unread` (Cassandra)

### Go struct

Drop the `Unread bool` field from `pkg/model/cassandra.Message` (currently
`pkg/model/cassandra/message.go:88`). The field has both a `json:"unread,omitempty"`
and `cql:"unread"` tag; both go away with the field.

### history-service column lists

The `cassrepo` package builds SELECT column lists as constants. Two need
the `unread` column dropped:

- `history-service/internal/cassrepo/messages_by_room.go` — drop `unread`
  from `baseColumns`.
- `history-service/internal/cassrepo/thread_messages.go` — drop `unread`
  from `threadMessageColumns`.

`structScan` is column-name driven, so the SELECT must not list a column
that no longer maps to a struct field. (Conversely, a column present in
Cassandra but absent from both the SELECT and the struct is harmless.)

### Local-dev schema (`docker-local/cassandra/init/*.cql`)

Drop the `unread BOOLEAN,` line from each:

- `10-table-messages_by_room.cql`
- `11-table-thread_messages_by_room.cql`
- `12-table-pinned_messages_by_room.cql`
- `13-table-messages_by_id.cql`

### Schema documentation

Update `docs/cassandra_message_model.md` to remove the four `unread BOOLEAN,`
lines (one per table block: `messages_by_room`, `thread_messages_by_room`,
`pinned_messages_by_room`, `messages_by_id`).

### Integration-test inline DDL and inserts

Several integration tests embed CREATE TABLE / INSERT statements that must
stay in sync with the production schema. Drop `unread` from each:

- `history-service/internal/cassrepo/integration_test.go` — three inline
  DDLs (lines ~49, ~78, ~108).
- `history-service/internal/cassrepo/thread_messages_integration_test.go` —
  one inline DDL (line ~192).
- `history-service/internal/cassrepo/messages_by_id_integration_test.go` —
  the `INSERT INTO messages_by_id (...)` statement (line ~71): drop the
  `unread` column name **and** one `?` placeholder; remove the corresponding
  positional argument from the `Exec` call.
- `history-service/internal/service/integration_test.go` — three inline
  DDLs (lines ~48, ~61, ~75).

### Production Cassandra (out of scope)

CLAUDE.md specifies that production Cassandra schema is owned by ops/IaC.
Existing prod tables retain the `unread` column physically until ops issues
an `ALTER TABLE … DROP unread`. gocql `structScan` is column-name driven,
so reading from a table that still has the column is safe — the column is
simply not scanned into any struct field. The application code change in
this spec is therefore forward-compatible with prod tables that have not
yet had the column dropped.

## Backward Compatibility

- **Mongo (`Room`, `Subscription`):** documents that pre-date these fields
  decode cleanly. `MinUserLastSeenAt` becomes `nil`, `ThreadUnread` becomes
  `nil`, `Alert` becomes `false`. No migration required.
- **JSON consumers:** new optional fields (`omitempty`) are absent from the
  wire when zero-valued. New `alert` field is always present (defaults to
  `false`); pre-existing consumers that ignore unknown fields are unaffected.
- **Cassandra (`Message`):** dropping the `Unread` field from the Go struct
  is read-safe against prod tables that still carry the column (column-name
  scanning ignores extra columns). Writes via existing INSERT/UPDATE
  statements that listed `unread` must drop it from both the column list
  and the placeholder list — this spec covers the integration-test INSERT
  in `messages_by_id_integration_test.go`; any production write paths that
  set `unread` must be audited as part of implementation (none expected
  given recent moves to subscription-level unread state, but the
  implementation plan should grep for `unread` in writer code paths).

## Testing (TDD)

Follow Red → Green → Refactor.

### Model tests (`pkg/model/model_test.go`)

Red phase:

1. **Extend `TestRoomJSON`** — populate `MinUserLastSeenAt` and verify it
   round-trips through JSON.
2. **Extend `TestRoomJSON_NilTimestampsOmitted`** — assert
   `minUserLastSeenAt` is omitted from JSON when nil and decodes back to nil.
3. **Extend `TestSubscriptionJSON`** — populate `ThreadUnread` with a couple
   of parent message IDs and set `Alert: true`; verify round-trip equality.
4. **New subtest** under `TestSubscriptionJSON` (or a sibling
   `TestSubscriptionJSON_*`):
   - `threadUnread` is omitted from JSON when nil/empty.
   - `alert` is present in JSON even when `false` (no `omitempty`).

Green phase: add the three fields exactly as defined above. Re-run
`make test SERVICE=pkg/model` (or `make test`) and confirm all pass with
`-race`.

### Cassandra-model tests (`pkg/model/cassandra/message_test.go`)

Red phase: remove the three `Unread` references (lines ~153, ~182, ~218).
Tests should compile and pass without them; the field's removal is verified
by the absence of compile errors after the struct change.

### history-service tests

- Run `make test SERVICE=history-service` to confirm `cassrepo` unit/handler
  tests still pass after the column-list updates.
- Run `make test-integration SERVICE=history-service` (Docker required) to
  confirm the integration-test DDL / INSERT updates align with the new
  schema. The Cassandra container will rebuild from the updated init
  scripts.

### Coverage

These are pure data-shape changes to a heavily-tested model package and a
column-list shrink in `cassrepo`. The new tests give meaningful coverage
of every new field's serialization behavior on both populated and
zero-value paths. Existing integration tests cover the Cassandra
read/write paths after the column drop.

## Out of Scope

- Population logic: when/how `MinUserLastSeenAt` is computed, who mutates
  `ThreadUnread`, who toggles `Alert`. (The writer service owns these and
  is designed in a separate spec.)
- Mongo indexes on the new fields.
- NATS event payloads, handler logic, broadcast/notification flows.
- Production Cassandra `ALTER TABLE … DROP unread` (ops-owned).
- Any non-test write paths that currently set `unread` in Cassandra. The
  implementation plan should grep `cassandra.*Unread` and `INSERT.*unread`
  in non-test code; this spec asserts no such writers are expected.

## Workflow

1. Red: add failing tests in `pkg/model/model_test.go`.
2. Green: add the three model fields.
3. Remove `Unread` from `pkg/model/cassandra.Message` and its tests.
4. Update `cassrepo` column-list constants.
5. Update integration-test inline DDLs and the messages_by_id INSERT.
6. Update `docker-local/cassandra/init/*.cql` (4 files).
7. Update `docs/cassandra_message_model.md`.
8. Run `make lint`, `make test`, `make test-integration SERVICE=history-service`.
9. Commit on `claude/add-model-fields-njdU6`.
10. Push to origin with `-u`.
