# Read-Receipt RPC in room-service

## Summary

Add a new client-facing NATS request/reply RPC in `room-service` that returns
the list of users who have read a given message. The handler is sender-only:
only the message author may query the receipts. The list is computed from
local-site subscriptions whose `lastSeenAt >= message.createdAt`, with the
sender excluded. Each entry carries `userId` (Mongo `_id`), `account`,
`chineseName`, and `engName`.

The handler reads `messages_by_id` from Cassandra (room-service gains a
read-only Cassandra connection for this) and runs a single MongoDB
aggregation against `subscriptions` joined with `users` for the receipts.

## Subject

- Concrete: `chat.user.{account}.request.room.{roomID}.{siteID}.message.read-receipt`
- Wildcard: `chat.user.*.request.room.*.{siteID}.message.read-receipt`

Queue group: `room-service`. Subject parsing reuses
`subject.ParseUserRoomSubject`.

## Wire Format

`pkg/model/read_receipt.go` (new file):

```go
type ReadReceiptRequest struct {
    MessageID string `json:"messageId"`
}

type ReadReceiptEntry struct {
    UserID      string `json:"userId"`      // users._id (Mongo entity ID)
    Account     string `json:"account"`
    ChineseName string `json:"chineseName"`
    EngName     string `json:"engName"`
}

type ReadReceiptResponse struct {
    Readers []ReadReceiptEntry `json:"readers"`
}
```

Empty result is `{"readers": []}` (200 OK), not an error.

## Subject Builders

`pkg/subject/subject.go` (additions):

```go
func MessageReadReceipt(account, roomID, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.room.%s.%s.message.read-receipt", account, roomID, siteID)
}

func MessageReadReceiptWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.room.*.%s.message.read-receipt", siteID)
}
```

## Handler Flow

`room-service/handler.go`:

1. `RegisterCRUD` adds `nc.QueueSubscribe(subject.MessageReadReceiptWildcard(h.siteID), "room-service", h.natsMessageReadReceipt)`.
2. `natsMessageReadReceipt` wraps context, delegates to `handleMessageReadReceipt`, sanitizes errors via `natsutil.ReplyError`, success via `natsutil.ReplyJSON`.
3. `handleMessageReadReceipt(ctx, subj, data)`:
   1. Parse subject → `(requesterAccount, roomID)` via `subject.ParseUserRoomSubject`.
   2. `json.Unmarshal` request; reject empty `MessageID`.
   3. `h.store.GetSubscription(ctx, requesterAccount, roomID)`. `model.ErrSubscriptionNotFound` → `errNotRoomMember`.
   4. `msgRoomID, msgCreatedAt, msgSender, found, err := h.msgReader.GetMessageRoomAndCreatedAt(ctx, req.MessageID)`. `found=false` → `errMessageNotFound`.
   5. Cross-room guard: `msgRoomID != roomID` → `errMessageRoomMismatch`.
   6. Sender-only guard: `msgSender != requesterAccount` → `errNotMessageSender`.
   7. `h.store.ListReadReceipts(ctx, roomID, msgCreatedAt, msgSender, h.maxRoomSize)`.
   8. Reply `ReadReceiptResponse{Readers: rows}`.

Tracing attributes on the span: `room.id`, `message.id`, `site.id`. Logging:
structured slog with `roomId`, `messageId`, `requester`, `siteId`,
`readerCount`, `latency_ms`.

## Errors

Add to `room-service` errors (alongside `errNotRoomMember`):

- `errMessageNotFound` — Cassandra returned no row for `messageID`.
- `errMessageRoomMismatch` — message exists but belongs to another room.
- `errNotMessageSender` — requester is not the message author.

All routed through `sanitizeError` so internal details are not leaked to clients.

## Stores

### MessageReader (Cassandra, read-only)

`room-service/store.go` interface:

```go
type MessageReader interface {
    GetMessageRoomAndCreatedAt(ctx context.Context, messageID string) (
        roomID string, createdAt time.Time, senderAccount string, found bool, err error,
    )
}
```

`room-service/store_cassandra.go` implementation queries
`messages_by_id` selecting only `room_id, created_at, sender LIMIT 1`. The
`sender` column corresponds to the message author's account. Uses
`LocalQuorum` consistency (the `cassutil.Connect` default). Wrap non-nil
errors with `fmt.Errorf("get message %s: %w", messageID, err)`. Not-found is
`(found=false, err=nil)`.

### ListReadReceipts (MongoDB aggregation)

Add to `RoomStore`:

```go
type ReadReceiptRow struct {
    UserID      string `bson:"_id"`
    Account     string `bson:"account"`
    ChineseName string `bson:"chineseName"`
    EngName     string `bson:"engName"`
}

ListReadReceipts(ctx context.Context, roomID string, since time.Time,
    excludeAccount string, limit int) ([]ReadReceiptRow, error)
```

Pipeline against `subscriptions`:

1. `$match`: `{ roomId, lastSeenAt: { $gte: since }, "u.account": { $ne: excludeAccount } }`.
2. `$lookup`: from `users`, `localField: "u._id"`, `foreignField: "_id"`, `as: "user"`.
3. `$unwind: "$user"` (`preserveNullAndEmptyArrays: false`).
4. `$project`: `{ _id: "$user._id", account: "$user.account", chineseName: "$user.chineseName", engName: "$user.engName" }`.
5. `$limit: limit` (`h.maxRoomSize`).

The handler maps `ReadReceiptRow` → `ReadReceiptEntry` (`UserID` ← `_id`).

### Index

`subscriptions` should have a compound index on `(roomId, lastSeenAt)`. The
`EnsureIndexes` step in `room-service/store_mongo.go` adds it if missing.

## Wiring (`main.go`)

- Extend `Config` with `Cassandra` block (`CASSANDRA_HOSTS` required CSV,
  `CASSANDRA_KEYSPACE` required, `CASSANDRA_USERNAME`, `CASSANDRA_PASSWORD`).
  Mirrors `message-worker/main.go`.
- After Mongo connect: `cassSession, err := cassutil.Connect(...)`.
- Build `cassReader := NewCassMessageReader(cassSession)` (named so `*cassMessageReader`
  satisfies `MessageReader`).
- `NewHandler(...)` gains a `msgReader MessageReader` parameter; pass `cassReader`.
- Graceful shutdown order: `nc.Drain()` → mongo disconnect → `cassutil.Close(cassSession)`.

## Local Dev

- `room-service/deploy/docker-compose.yml`: add Cassandra service + `depends_on`,
  match `message-worker/deploy/docker-compose.yml`. Reuse
  `docker-local/cassandra/init/*.cql` for schema bootstrap.
- No Dockerfile change (env-only).
- `azure-pipelines.yml`: add Cassandra to integration-test services if that
  pipeline runs `make test-integration SERVICE=room-service`.

## Client API Doc

Per CLAUDE.md Section 5, update `docs/client-api.md` in the same PR. Add a
section documenting the subject, request body, response shape, error cases,
and "no events triggered" note.

## Testing (TDD)

### Subject builders (`pkg/subject/subject_test.go`)

- `TestMessageReadReceipt` — concrete builder.
- `TestMessageReadReceiptWildcard` — wildcard form.
- `TestMessageReadReceipt_ParseUserRoomSubject` — round-trip.

### Model round-trip (`pkg/model/model_test.go`)

Add `ReadReceiptRequest`, `ReadReceiptEntry`, `ReadReceiptResponse` to the
generic `roundTrip` table.

### Handler unit tests (`room-service/handler_test.go`)

`TestHandler_handleMessageReadReceipt` table-driven cases:

| Case | Setup | Expected outcome |
|------|-------|------------------|
| happy path | sub exists, message found, requester==sender, 3 readers returned by mock | 3 entries, sender excluded by store layer |
| empty readers | sub exists, message found, store returns `[]` | `Readers: []` |
| invalid subject | malformed `subj` | parse error |
| empty messageID | `{}` body | validation error |
| not a room member | `GetSubscription` → `ErrSubscriptionNotFound` | `errNotRoomMember`; no message lookup |
| message not found | reader → `found=false` | `errMessageNotFound` |
| message in another room | reader → `roomID="other"` | `errMessageRoomMismatch` |
| not the sender | `senderAccount != requesterAccount` | `errNotMessageSender` |
| store error on subscription | mongo error | wrapped error, no further calls |
| store error on message lookup | gocql error | wrapped error |
| store error on aggregation | mongo error | wrapped error |

Mocks: `make generate SERVICE=room-service` regenerates `mock_store_test.go`
with the new `ListReadReceipts` and a new mock for `MessageReader`.

### Integration tests (`room-service/integration_test.go`)

Tagged `//go:build integration`.

1. **Mongo aggregation** — `mongodb` testcontainer; seed users + subscriptions
   with varied `lastSeenAt`; assert `ListReadReceipts` filter, lookup, and
   sender-exclusion semantics.
2. **Cassandra reader** — `cassandra` testcontainer; apply DDL from
   `docker-local/cassandra/init`; insert one row; assert
   `GetMessageRoomAndCreatedAt` returns the expected tuple. Second case
   asserts `found=false` for an unknown id.

### Coverage

≥80% for the package (CLAUDE.md mandate). Aim ≥90% on the new handler.

## Out of Scope

- Write side: does not modify `lastSeenAt`. Use the existing `message.read` RPC.
- Cross-site outbox: the read-receipt query is local-site-only by design.
- Notifications, pub/sub events, push.
- Pagination.

## Risks

- Without a `(roomId, lastSeenAt)` index on `subscriptions`, the aggregation
  scans all subs for the room. The plan adds it via `EnsureIndexes`.
- A reader who leaves the room between reading and query disappears from the
  result. Acceptable: this RPC reflects current readers, not history.
- Cassandra connection adds a new failure mode at room-service startup. Same
  fail-fast policy as `message-worker` and `history-service`.
