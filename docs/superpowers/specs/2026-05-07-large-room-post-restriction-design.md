# Large-Room Post Restriction — Design

**Date:** 2026-05-07
**Branch:** `claude/validate-message-sending-5HTd9`
**Status:** Draft, awaiting team-lead confirmation on Q3 (edits/deletes)

## Summary

In rooms with more than 500 members, only owners may send top-level messages.
Thread replies are exempt regardless of room size. Edits and deletes are
unaffected (author-only rule in `history-service` stands). The check is added
to `message-gatekeeper` as an admission gate before publishing to
`MESSAGES_CANONICAL`.

## Rule

A new top-level message is rejected when **all** of the following hold:

1. The send is **not** a thread reply (`req.ThreadParentMessageID == ""`).
2. The room's `userCount` is **strictly greater** than the configured
   threshold (default `500`).
3. The sender's subscription does **not** carry `model.RoleOwner`.

When rejected, the user receives a coded error and no message is published to
`MESSAGES_CANONICAL`.

## Architecture & data flow

Single change point: `message-gatekeeper`. No other service is modified.

The new check sits between `GetSubscription` and `resolveQuoteSnapshot` in
`processMessage`:

```
Subject parse → SiteID match → unmarshal → ID/content validation
    ↓
GetSubscription (existing)
    ↓
[NEW] LARGE-ROOM CHECK
    ↓
resolveQuoteSnapshot (existing)
    ↓
Build Message + publish to MESSAGES_CANONICAL
```

Why here:

- The subscription is the data source for the role bypass — must come after
  `GetSubscription`.
- Quote-resolution is a downstream Mongo/RPC cost we should avoid paying for
  messages that will be rejected.
- Canonical publish must come after — gatekeeper is the admission gate.

Producers that already bypass gatekeeper bypass this rule too (by design):
`room-worker` writes system messages (`Message.Type != ""`) directly to
`MESSAGES_CANONICAL` — they remain unaffected.

## Predicate logic (approach: owner fast-path)

```go
isThreadReply := req.ThreadParentMessageID != ""

if !isThreadReply && !canBypassLargeRoomCap(sub) {
    room, err := h.store.GetRoom(ctx, roomID)
    if err != nil {
        return nil, &infraError{cause: fmt.Errorf("get room %s for cap check: %w", roomID, err)}
    }
    if room.UserCount > h.largeRoomThreshold {
        slog.Info("send blocked",
            "reason",    "large_room_post_restricted",
            "account",   account,
            "roomID",    roomID,
            "userCount", room.UserCount,
            "threshold", h.largeRoomThreshold,
        )
        return nil, errLargeRoomPostRestricted
    }
}
```

The bypass predicate is a small named function — the single edit point for
the future bot phase:

```go
// canBypassLargeRoomCap reports whether the subscriber is exempt from the
// large-room post restriction. Today: owners only. Bots will be added in a
// future phase.
func canBypassLargeRoomCap(sub *model.Subscription) bool {
    return hasRole(sub.Roles, model.RoleOwner)
}
```

### Cost matrix

| Sender / message | Outcome | Added Mongo cost |
|---|---|---|
| Thread reply (any role, any room size) | Allowed | none |
| Top-level send by an owner | Allowed | none |
| Top-level send by a non-owner, room ≤ threshold | Allowed | +1 `findOne` |
| Top-level send by a non-owner, room > threshold | Rejected | +1 `findOne` |

Two of the four cases pay zero new cost — and they are the common-case paths.

### Error classification

`*infraError` (NACK + JetStream redelivery) for `GetRoom` failures. Plain
validation error (`errLargeRoomPostRestricted`, ACK + reply) for
non-owner-in-big-room. Mirrors how `GetSubscription` errors are classified
today.

## Store interface changes

`message-gatekeeper/store.go` — one new method:

```go
type Store interface {
    GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
    GetRoom(ctx context.Context, roomID string) (*model.Room, error) // NEW
}
```

`message-gatekeeper/store_mongo.go` — `MongoStore` gains a `rooms` collection
field and `GetRoom` method, structurally identical to
`room-worker/store_mongo.go`'s `GetRoom`. No new indexes — `rooms._id` is
already the primary key.

Adding a method to `Store` invalidates the generated mock; regenerate via
`make generate SERVICE=message-gatekeeper`.

## Configuration

One new field on `message-gatekeeper`'s `Config` struct in `main.go`:

```go
LargeRoomThreshold int `env:"LARGE_ROOM_THRESHOLD" envDefault:"500"`
```

- Type `int` (matches `Room.UserCount`).
- Default `500` — the product rule of record.
- Not `required` — non-critical config with a sensible default, matching
  `CLAUDE.md` Section 6.
- Threaded into `Handler` via constructor injection alongside `siteID`:
  ```go
  type Handler struct {
      ...
      largeRoomThreshold int  // NEW
  }
  ```

Operational note: setting `LARGE_ROOM_THRESHOLD` to a very large value
(e.g. `999999999`) effectively disables the rule without a code change. This
is the rollback knob.

## Error contract & wire format

### `pkg/model/error.go` — backward-compatible field addition

```go
type ErrorResponse struct {
    Error string `json:"error"`
    Code  string `json:"code,omitempty"`  // NEW
}
```

`omitempty` is load-bearing: every existing `MarshalError(string)` call site
keeps producing identical bytes (`{"error": "..."}`).

### `pkg/natsutil/reply.go` — new helper

```go
// MarshalErrorWithCode encodes an error message and code as a JSON ErrorResponse.
func MarshalErrorWithCode(errMsg, code string) []byte {
    data, _ := json.Marshal(model.ErrorResponse{Error: errMsg, Code: code})
    return data
}
```

### `message-gatekeeper` — typed error sentinel

A small unexported type in `store.go` (alongside `errNotSubscribed`):

```go
type codedError struct {
    Code    string
    Message string
}

func (e *codedError) Error() string { return e.Message }

var errLargeRoomPostRestricted = &codedError{
    Code:    "large_room_post_restricted",
    Message: "only owners can post in this room",
}
```

The handler's validation-error branch dispatches via `errors.As`:

```go
var ce *codedError
var replyData []byte
if errors.As(err, &ce) {
    replyData = natsutil.MarshalErrorWithCode(ce.Message, ce.Code)
} else {
    replyData = natsutil.MarshalError(err.Error())
}
h.sendReply(ctx, account, msg.Data(), replyData)
```

All other validation errors (subscription missing, content empty, etc.) flow
through the existing `MarshalError(err.Error())` path unchanged.

### Wire examples

Reject (new rule fires):
```json
{"error": "only owners can post in this room", "code": "large_room_post_restricted"}
```

Existing reject (unchanged):
```json
{"error": "user alice is not subscribed to room R"}
```

## Logging

A single new log line on the rejection branch:

```go
slog.Info("send blocked",
    "reason",    "large_room_post_restricted",
    "account",   account,
    "roomID",    roomID,
    "userCount", room.UserCount,
    "threshold", h.largeRoomThreshold,
)
```

- Level `Info`: this is expected behavior, not an anomaly.
- `reason` mirrors the wire `code` so log queries and frontend dispatch share
  the same vocabulary; promotion to a Prometheus counter label is mechanical.
- No log on bypass paths (would be enormous volume in busy 500+ rooms for zero
  diagnostic value).
- No log of `req.Content` — `CLAUDE.md` Section 3 forbids logging full message
  bodies.
- `requestID` is already on the structured log scope from request-id
  middleware; no manual threading needed.
- Existing log paths cover `GetRoom` infrastructure failures and ACK/NACK
  failures — no new lines added there.

## Testing strategy

TDD per `CLAUDE.md` Section 4. Three layers:

### `pkg/model`
Round-trip tests for `ErrorResponse` covering both shapes (with and without
`Code`), asserting `omitempty` keeps the wire format identical for the
no-code case.

### `pkg/natsutil`
Unit test for `MarshalErrorWithCode` mirroring the existing `MarshalError`
test style.

### `message-gatekeeper`
Extend the existing table-driven `TestHandler_ProcessMessage` with the cases
below. Regenerate mocks first (`make generate SERVICE=message-gatekeeper`).

| Case | Roles | Room.UserCount | ThreadParent | GetRoom expected? | Outcome |
|---|---|---|---|---|---|
| owner sends in big room | `[owner]` | 600 | "" | no (fast-path) | success |
| owner sends in small room | `[owner]` | 50 | "" | no | success |
| member sends in small room | `[member]` | 50 | "" | yes | success |
| member sends in big room | `[member]` | 600 | "" | yes | reject `large_room_post_restricted`, no canonical publish |
| boundary: count == threshold | `[member]` | 500 | "" | yes | success (strict `>`) |
| boundary: count == threshold+1 | `[member]` | 501 | "" | yes | reject |
| member thread reply in big room | `[member]` | 600 | non-empty | no | success |
| owner thread reply in big room | `[owner]` | 600 | non-empty | no | success |
| GetRoom returns Mongo error | `[member]` | n/a | "" | yes (returns err) | infraError → NACK |
| GetRoom returns ErrNoDocuments | `[member]` | n/a | "" | yes | infraError → NACK |
| Custom threshold (env=2), 3-person room | `[member]` | 3 | "" | yes | reject |

Reject-case assertions check the wire payload:
```go
assert.Equal(t, `{"error":"only owners can post in this room","code":"large_room_post_restricted"}`, string(replyData))
```

Negative assertions on fast-paths:
- `MockStore.EXPECT().GetRoom(...).Times(0)` proves owners and thread replies
  do not incur the fetch.
- `published` slice empty on reject cases proves no canonical publish
  happens when blocked.

### Predicate unit test

```go
func TestCanBypassLargeRoomCap(t *testing.T) { /* table over role combinations */ }
```

When the bot phase lands, this test grows by one or two cases — the entire
test diff for the rule extension.

### Integration tests
Not added. The rule has no real Mongo/NATS dependency that requires
integration coverage; unit tests with mocked store cover all branches.

### Coverage target
≥80% required, target 90%+ for handlers (per `CLAUDE.md` Section 4).

## Out of scope

| Item | Why |
|---|---|
| Edit & delete authorization | Q3 — author-only rule in `history-service` stands. |
| System-message admission | Q4 — `room-worker` writes directly to `MESSAGES_CANONICAL`. |
| Bot identification / `RoleBot` / promoting `isBot` to `pkg/` | Separate phase. Gatekeeper is role-only here. |
| Admin role | `RoleAdmin` does not exist in the codebase today. |
| `Room.UserCount` lifecycle | Maintained by `room-worker.ReconcileUserCount`; this spec only reads. |
| Per-room threshold override | YAGNI; single env-var threshold. |
| Federation outbox/inbox flow | Q6 — gate runs on the room's home site by subject routing. |
| Cross-service refactor of error handling | New `Code` field on `model.ErrorResponse` is the only shared change. |
| Prometheus metrics | Q8 — logs only with structured `reason` label. |
| Frontend UX changes | Backward-compatible; existing clients keep reading `error`. |
| Schema migrations | None — uses existing `Subscription.Roles` and `Room.UserCount`. |
| Stream / subject / event schema | Unchanged. |
| Production rollout audit | Q10 — pre-production. |

## Future follow-ups this spec sets up for

- **Bot-phase integration**: `canBypassLargeRoomCap(sub)` is the single edit
  point. Adding `RoleBot` to `pkg/model/subscription.go`'s `Role` constants is
  trivially supported by the existing `Roles []Role` field.
- **Edit/delete extension** (re-opening Q3): the `codedError` shape and
  `large_room_post_restricted` code are reusable from `history-service`. The
  check would live in `EditMessage` / `DeleteMessage`, not gatekeeper.
- **Metrics promotion**: same `reason` label key; one new
  `prometheus.CounterVec` registration plus one `.Inc()` call.

## Open question

**Q3 — edits and deletes scope** is parked awaiting team-lead confirmation.
This spec assumes the answer is "rule does not apply to edits/deletes"
(author-only stands). If the team later decides edits/deletes should also be
gated, that becomes a separate spec against `history-service` that reuses the
predicate vocabulary defined here.
