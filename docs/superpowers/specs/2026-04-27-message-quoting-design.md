# Message Quoting Design

Date: 2026-04-27
Status: Approved (pending implementation plan)

## Goal

Let a user reply to a chat message with a "quote" — the new message renders alongside a snapshot of the parent message it's quoting. The snapshot must be visible to every user in the room (it ships through real-time delivery, push notifications, and persisted history).

## Scope

- Same-room quoting only.
- Thread reply and quote are independent — a single message may carry both, neither, or only one.
- MVP snapshot fields: `messageId`, `roomId`, `sender`, `createdAt`, `msg`, `mentions`. `attachments` and `messageLink` are out of scope for MVP.
- Soft-fail policy: any failure to resolve the parent (not found, deleted, RPC error, timeout) drops the quote silently and lets the message ship without it. Failures are logged at WARN.

## Non-goals

- No editing of the snapshot after the fact.
- No cross-room or cross-site quoting.
- No new feature flag — gated by clients sending the new request field.
- No schema migration — Cassandra columns already exist.

## Architecture

```
client ──(SendMessageRequest{quotedParentMessageId})──> MESSAGES stream
                                                            │
                                                            ▼
                                  message-gatekeeper
                                  ├─ existing validation (UUID, content, sub)
                                  ├─ if quotedParentMessageId != "":
                                  │   ├─ NATS request to history-service
                                  │   │   subject: chat.user.{account}.request.room.{roomID}.{siteID}.msg.get
                                  │   │   payload: {"messageId": "..."}
                                  │   ├─ on success: project to *cassandra.QuotedParentMessage,
                                  │   │              set msg.QuotedParentMessage
                                  │   └─ on any error: warn, drop quote, proceed
                                  └─ publish MessageEvent → MESSAGES_CANONICAL
                                                            │
              ┌─────────────────────────────────┬───────────┴───────────────────────┐
              ▼                                 ▼                                   ▼
        message-worker                    broadcast-worker                  notification-worker
        persists snapshot in              ships snapshot to clients         no change in MVP
        quoted_parent_message             via existing fan-out              (snapshot rides on Message)
        column on insert
```

The canonical event is the single source of truth. Once gatekeeper has built the snapshot, no downstream worker re-resolves it.

## Why call history-service via RPC instead of querying Cassandra in gatekeeper?

- `history-service.GetMessageByID` already implements lookup, room match (via subject param), subscription check, and access-window enforcement. The access-window check is desirable for quoting — a user who can't see the parent shouldn't be able to surface its content in a quote.
- Keeps gatekeeper's dependency surface unchanged (Mongo only). Cassandra reads stay owned by history-service.
- Trade-off accepted: synchronous NATS hop on every quoted send, bounded by a 2-second timeout. On timeout or any RPC error, soft-fail drops the quote.

The user-scoped subject is published on by gatekeeper acting on behalf of the sender — the sender's `account` and `roomID` come from the inbound MESSAGES subject. natsrouter parses params from the subject regardless of publisher identity, and all auth checks pass because the sender genuinely is subscribed.

## Wire-format changes (`pkg/model`)

### `pkg/model/message.go`

```go
import "github.com/hmchangw/chat/pkg/model/cassandra"

type SendMessageRequest struct {
    ID                           string `json:"id"`
    Content                      string `json:"content"`
    RequestID                    string `json:"requestId"`
    ThreadParentMessageID        string `json:"threadParentMessageId,omitempty"`
    ThreadParentMessageCreatedAt *int64 `json:"threadParentMessageCreatedAt,omitempty"`
    QuotedParentMessageID        string `json:"quotedParentMessageId,omitempty"` // NEW
}

type Message struct {
    // ...existing fields...
    QuotedParentMessage *cassandra.QuotedParentMessage `json:"quotedParentMessage,omitempty" bson:"quotedParentMessage,omitempty"` // NEW
}
```

### Why reuse `cassandra.QuotedParentMessage` directly

It's already defined in the shared `pkg/model/cassandra` package, has both `json` and `cql` tags, and was created for this exact use. history-service already aliases it as `models.QuotedParentMessage`. Defining a parallel `model.QuotedParentMessage` would duplicate fields and introduce conversion code on the message-worker side.

The `Sender` and `Mentions` inside the snapshot are `cassandra.Participant` (id, engName, companyName, account, appId, appName, isBot) — a slightly different shape than the top-level `model.Participant` used by `Message.Mentions` (userId, account, chineseName, engName). Both shapes are already part of the wire vocabulary (history-service ships `cassandra.Participant` directly to clients), so this isn't introducing a new shape.

### `pkg/model/model_test.go`

Add a JSON round-trip case for `Message` carrying a populated `QuotedParentMessage`.

## Subject helper (`pkg/subject`)

### `pkg/subject/subject.go`

```go
// MsgGet returns the concrete subject for issuing a GetMessageByID request.
// The natsrouter pattern variant MsgGetPattern is used by history-service
// to register the handler.
func MsgGet(account, roomID, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.get", account, roomID, siteID)
}
```

Project rule (CLAUDE.md): use `pkg/subject` builders, never raw `fmt.Sprintf` at call sites.

## Gatekeeper changes (`message-gatekeeper`)

### `store.go` — new interface (kept separate from existing `Store`)

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ParentMessageFetcher

type ParentMessageFetcher interface {
    // FetchQuotedParent issues an RPC to history-service and returns a snapshot
    // suitable for embedding on a quoting message. Returns an error for any
    // condition that should drop the quote (not found, RPC timeout, etc.).
    FetchQuotedParent(ctx context.Context, account, roomID, siteID, messageID string) (*cassandra.QuotedParentMessage, error)
}
```

### `fetcher_history.go` — new file

Implements `ParentMessageFetcher`:

1. Build subject via `subject.MsgGet(account, roomID, siteID)`.
2. Marshal the request as `{"messageId": "..."}` (a small local struct in gatekeeper, mirroring `history-service/internal/models.GetMessageByIDRequest`; we duplicate the wire shape rather than cross-import a service-internal package).
3. `nc.Request(subj, data, 2*time.Second)`.
4. Decode the natsrouter response envelope:
   - Success → `*cassandra.Message` body. Project to `*cassandra.QuotedParentMessage` by copying `MessageID`, `RoomID`, `Sender`, `CreatedAt`, `Msg`, `Mentions`. Leave `Attachments` and `MessageLink` zero.
   - Error envelope (NotFound, Forbidden, Internal) → return wrapped error.
5. NATS error (no responder, timeout) → return wrapped error.

The 2-second timeout is the standard NATS Go client default and is hardcoded inline at the single call site. No env var is added — promote to config later if ops needs to tune it.

### `handler.go` — one new branch in `processMessage`

After existing validation (UUID, content size, thread-pair, subscription), and before constructing the `Message`:

```go
var quotedSnapshot *cassandra.QuotedParentMessage
if req.QuotedParentMessageID != "" {
    snap, err := h.parentFetcher.FetchQuotedParent(ctx, account, roomID, siteID, req.QuotedParentMessageID)
    if err != nil {
        slog.Warn("quoted parent unavailable, dropping quote",
            "quotedParentMessageId", req.QuotedParentMessageID,
            "roomId", roomID,
            "error", err)
    } else {
        quotedSnapshot = snap
    }
}

msg := model.Message{
    // ...existing fields...
    QuotedParentMessage: quotedSnapshot,
}
```

No UUID validation on `QuotedParentMessageID` — if the client sends garbage, the RPC's NotFound path handles it via soft-fail.

### `main.go` — wiring

Construct the fetcher with the existing `nc *nats.Conn` and pass it to `NewHandler`. No new dependencies, no new config.

### Tests (`message-gatekeeper/handler_test.go` + `fetcher_history_test.go`)

Handler table-driven cases via `MockParentMessageFetcher`:
- Quote field unset → fetcher not called.
- Quote field set, fetcher returns snapshot → snapshot embedded on canonical event.
- Quote field set, fetcher returns error → quote dropped, warn logged, message still published.
- Thread + quote both set → both fields propagate.

Fetcher tests against an in-process NATS server:
- History responds with success → returns projected snapshot.
- History responds with error envelope → returns error.
- No responder / timeout → returns error.

## Message-worker changes (`message-worker`)

### `store_cassandra.go` — extend INSERT statements

`SaveMessage`: bind `quoted_parent_message` in both `messages_by_room` and `messages_by_id` INSERTs, value = `msg.QuotedParentMessage` (a `*cassandra.QuotedParentMessage`). gocql marshals nil as null UDT and a populated struct as the UDT via the existing `cql:` tags.

`SaveThreadMessage`: bind `quoted_parent_message` in both `messages_by_id` and `thread_messages_by_room` INSERTs, same binding.

No conversion code — `evt.Message.QuotedParentMessage` is already the storage type.

### Tests

- `handler_test.go`: extend the handler table — when the canonical event carries a snapshot, the mocked store receives the same pointer.
- `store_cassandra_test.go` (`//go:build integration`): add a case that calls `SaveMessage` (and `SaveThreadMessage`) with a populated snapshot, then reads `messages_by_id` / `messages_by_room` / `thread_messages_by_room` and asserts the UDT round-trips intact.

## What is NOT changed

- `history-service` — zero changes, we call its existing RPC.
- `broadcast-worker` — zero changes, `Message` propagates the new field automatically.
- `notification-worker` — zero changes for MVP.
- Cassandra schema (`docs/cassandra_message_model.md`, `docker-local/cassandra/init/*.cql`) — `quoted_parent_message` column already exists in all four tables.
- `cassandra.QuotedParentMessage` struct — used as-is.
- Gatekeeper's existing `Store` interface, Mongo store, subscription validation, content validation, thread validation — all untouched.

## Soft-fail policy (full enumeration)

| Failure mode | Gatekeeper behavior |
|---|---|
| Client sends invalid UUID for quotedParentMessageId | RPC returns NotFound → quote dropped, message ships |
| Parent message not found in Cassandra | RPC returns NotFound → quote dropped, message ships |
| Parent in a different room | RPC returns NotFound (room param in subject doesn't match) → quote dropped, message ships |
| Parent has `deleted = true` | history-service may return the row or NotFound (verify during impl); either way, quote dropped on error path or honored on success path |
| Sender's `historySharedSince` is after parent's createdAt | RPC returns Forbidden → quote dropped, message ships |
| history-service unreachable | NATS no-responder error → quote dropped, message ships |
| history-service slow (>2s) | NATS timeout → quote dropped, message ships |
| Any other error | Wrapped error returned → quote dropped, message ships |

In every case the user's message lands in the room — only the quote decoration is lost, and a WARN log is emitted for operational visibility.

## TDD ordering

Per CLAUDE.md, every change lands as Red → Green → Refactor → Commit:

1. `pkg/model` field additions + round-trip test.
2. `pkg/subject.MsgGet` helper + test.
3. `message-gatekeeper` `ParentMessageFetcher` interface + handler branch + mock + handler tests.
4. `message-gatekeeper` `fetcher_history.go` + fetcher tests against in-process NATS.
5. `message-gatekeeper` `main.go` wiring.
6. `message-worker` INSERT extensions + handler test extensions.
7. `message-worker` integration test for round-trip persistence.

## Backward compatibility & rollout

- All new fields are `omitempty` — old clients and old service binaries see no behavior change.
- Old gatekeeper deployed against new model: just never sets the field; canonical event has nil snapshot; everything works.
- Old message-worker deployed against new event: gocql binds nil UDT; INSERTs without the new column are still valid (Cassandra ignores unspecified columns). However, message-worker SHOULD be deployed before frontend starts sending quotes, so snapshots are persisted from day one.
- No DB migration. No new env vars. No feature flag.

## Observability

One new log line: `slog.Warn("quoted parent unavailable, dropping quote", ...)`. No new metrics in MVP.

## Open questions for implementation

- Confirm history-service's behavior when the parent has `deleted = true` — does `findMessage` return it or treat it as not-found? Adjust the soft-fail table once verified.
- Confirm gocql version in the repo correctly marshals a nil `*cassandra.QuotedParentMessage` as a null UDT in INSERT bindings (expected behavior, but verify via the integration test).
