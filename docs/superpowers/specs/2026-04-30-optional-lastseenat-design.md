# Optional `LastSeenAt` on `model.Subscription`

## Goal

Make `Subscription.LastSeenAt` optional by changing its type from `time.Time`
to `*time.Time`, mirroring the pattern already used by the sibling
`HistorySharedSince` field (and by `ThreadSubscription.LastSeenAt`). A
subscription has no meaningful "last seen" timestamp until the user has
actually opened the room; today the field is silently zero-valued at insert,
which is misleading on the wire and in MongoDB.

## Motivation

- Production code never sets `Subscription.LastSeenAt` at creation time. All
  four creation sites (`room-service/handler.go`, `room-worker/handler.go`
  ×2, `inbox-worker/handler.go`) leave it at the Go zero value, which
  serializes to `"0001-01-01T00:00:00Z"` in JSON and an equally-meaningless
  zero `Date` in BSON.
- Consumers cannot distinguish "never seen" from "seen at the epoch".
- The codebase already models the analogous "never seen" state on
  `ThreadSubscription` as `*time.Time` with `omitempty`. Subscription should
  follow the same convention.

## Non-Goals

- Adding a "mark seen" code path. No such handler exists today and this
  change does not introduce one.
- Migrating existing MongoDB documents. The BSON decoder accepts a zero
  `Date` into `*time.Time` as a non-nil pointer to the zero time; consumers
  treating non-nil as "set" would still see the legacy zero value. No live
  consumer reads the field, so no migration is required.
- Touching `ThreadSubscription` — already correctly typed.

## Design

### Field type change

`pkg/model/subscription.go`:

```go
LastSeenAt *time.Time `json:"lastSeenAt,omitempty" bson:"lastSeenAt,omitempty"`
```

The `omitempty` on both tags matches `HistorySharedSince` in the same struct.
When `nil`, the field is dropped from JSON output and from BSON documents.

### Test updates

`pkg/model/model_test.go` — `TestSubscriptionJSON` currently uses a
`time.Time` literal. Update it to assign a `*time.Time` and verify the
round-trip. Add a second sub-test that round-trips with `LastSeenAt: nil` to
lock in the optional behavior.

### Consumer impact

No production code reads `Subscription.LastSeenAt` today, so the type
widening is a pure compile-time change at the model layer. The four
creation sites continue to compile unchanged because they never named the
field.

## Verification

- `make lint`
- `make test` (full unit suite — touches `pkg/model` and every service that
  imports it)
- Spot-check `make test SERVICE=room-service`,
  `make test SERVICE=room-worker`, `make test SERVICE=inbox-worker`,
  `make test SERVICE=message-gatekeeper`, `make test SERVICE=broadcast-worker`,
  `make test SERVICE=notification-worker`, `make test SERVICE=history-service`

## Rollout

Single PR on branch `claude/optional-lastseenAt-field-FJGwl`. No data
migration. No coordinated deploy required because the field has no live
readers.
