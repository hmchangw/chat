# Favorite-Toggle RPC in room-service

## Summary

A new client-facing NATS request/reply RPC, `favorite.toggle`, that flips
`Subscription.favorite` for the requester on a single room. The flag is a
per-user, per-room boolean intended as a sidebar render hint for a
"favorites" section — backend treats it as opaque. No notification gating,
no fan-out routing changes, no message filtering.

Architecturally mirrors `mute.toggle` (PR #217): same RPC shape, same store
pattern, same cross-site replication path. The mute and favorite flags live
side by side on `Subscription` and are mutated by parallel handlers; no
shared validation or coupling between them.

## Subject

- Concrete: `chat.user.{account}.request.room.{roomID}.{siteID}.favorite.toggle`
- Wildcard: `chat.user.*.request.room.*.{siteID}.favorite.toggle`

`{siteID}` is the room's origin site — the same site that owns the room
and that runs the `room-service` handling this RPC. Queue group:
`room-service`. Subject parsing reuses `subject.ParseUserRoomSubject`.

## Wire Format

`pkg/model/event.go`:

```go
// FavoriteToggleResponse is the sync reply for the favorite.toggle RPC.
type FavoriteToggleResponse struct {
    Status   string `json:"status"`   // always "ok" on success
    Favorite bool   `json:"favorite"` // post-flip value
}

// SubscriptionFavoriteToggledEvent is the OutboxEvent.Payload for type
// "subscription_favorite_toggled".
type SubscriptionFavoriteToggledEvent struct {
    Account   string `json:"account"   bson:"account"`
    RoomID    string `json:"roomId"    bson:"roomId"`
    Favorite  bool   `json:"favorite"  bson:"favorite"`
    Timestamp int64  `json:"timestamp" bson:"timestamp"` // UnixMilli UTC
}
```

Outbox event-type constant:

```go
const OutboxSubscriptionFavoriteToggled OutboxEventType = "subscription_favorite_toggled"
```

Request body: empty. The subject already carries `account` and `roomID`;
any body content sent by the client is ignored.

## Subscription model

`pkg/model/subscription.go` gains one field:

```go
Favorite bool `json:"favorite" bson:"favorite"`
```

Both tags are always present (no `omitempty`) — `false` is serialized
explicitly so clients can distinguish "not favorited" from "unknown".

The accompanying `SubscriptionUpdateEvent.Action` enum gains
`"favorite_toggled"` alongside the existing `"added"`, `"removed"`,
`"role_updated"`, `"mute_toggled"`.

## Subject Builders

`pkg/subject/subject.go`:

```go
func FavoriteToggle(account, roomID, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.room.%s.%s.favorite.toggle", account, roomID, siteID)
}

func FavoriteToggleWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.room.*.%s.favorite.toggle", siteID)
}
```

## Handler Flow

`room-service/handler.go`:

1. `Register` adds
   `nc.QueueSubscribe(subject.FavoriteToggleWildcard(h.siteID), "room-service", h.natsFavoriteToggle)`.
2. `natsFavoriteToggle` wraps context, delegates to `handleFavoriteToggle`,
   sanitises errors via `natsutil.ReplyError`, success via `m.Msg.Respond`.
3. `handleFavoriteToggle(ctx, subj, _)`:
   1. Parse subject → `(account, roomID)` via `subject.ParseUserRoomSubject`.
      Malformed subject → `fmt.Errorf("invalid favorite-toggle subject: %s", subj)`.
   2. Tracing: set `room.id` and `site.id` attributes on the active span.
   3. `sub, err := h.store.ToggleSubscriptionFavorite(ctx, roomID, account)`.
      - `errors.Is(err, model.ErrSubscriptionNotFound)` → `errNotRoomMember`.
      - Other errors → `fmt.Errorf("toggle subscription favorite: %w", err)`.
   4. Publish `SubscriptionUpdateEvent` on `subject.SubscriptionUpdate(account)`
      with `Action: "favorite_toggled"`, `Subscription: *sub`. **Publish
      failure here is non-fatal** — slog.Error and continue. Rationale: the
      DB write is the source of truth and other client sessions reconcile
      on their next subscription refetch.
   5. `userSiteID, err := h.store.GetUserSiteID(ctx, account)`.
      Error → `fmt.Errorf("get user siteId: %w", err)`.
   6. If `userSiteID != "" && userSiteID != h.siteID`:
      - Build `SubscriptionFavoriteToggledEvent` payload.
      - Wrap in `OutboxEvent{Type: OutboxSubscriptionFavoriteToggled, …}`.
      - Publish to `subject.Outbox(h.siteID, userSiteID, OutboxSubscriptionFavoriteToggled)`
        via `publishToStream` (JetStream OUTBOX). Failure here **is** fatal —
        client gets an error, federation will retry on next user action.
   7. Reply `FavoriteToggleResponse{Status: "ok", Favorite: sub.Favorite}`.

Idempotency: this is a **toggle**, not a set. Every successful call flips
the bit. Clients must debounce the user action; the handler does not
deduplicate. The same is true of `mute.toggle`.

## Errors

Reuses existing room-service sentinels — no new error variables:

- `errNotRoomMember` (already defined in `room-service/helper.go`,
  message `"only room members can list members"`) — returned when the
  requester has no subscription in the room.

All errors are routed through `sanitizeError` before reaching the client.

## Store

### Interface (`room-service/store.go`)

```go
// ToggleSubscriptionFavorite atomically flips favorite via a single
// FindOneAndUpdate. Returns the post-flip subscription, or
// model.ErrSubscriptionNotFound (wrapped) when no match.
ToggleSubscriptionFavorite(ctx context.Context, roomID, account string) (*model.Subscription, error)
```

`GetUserSiteID` already exists — reused as-is.

### Mongo implementation (`room-service/store_mongo.go`)

```go
filter := bson.M{"roomId": roomID, "u.account": account}
update := mongo.Pipeline{
    bson.D{{Key: "$set", Value: bson.M{
        "favorite": bson.M{"$not": bson.A{
            bson.M{"$ifNull": bson.A{"$favorite", false}},
        }},
    }}},
}
opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
```

Two key properties of this pipeline:

1. **Atomic** — single round-trip, no read-modify-write race.
2. **Legacy-safe** — `$ifNull: ["$favorite", false]` treats documents
   written before this change (no `favorite` key at all) as
   `false`, so the first toggle deterministically flips them to `true`.
   No migration script required.

`mongo.ErrNoDocuments` is wrapped with `model.ErrSubscriptionNotFound`.
All other Mongo errors are wrapped with context.

### Indexes

No new indexes. The existing `(roomId, "u.account")` compound index from
the `mute.toggle` work covers this lookup unchanged.

## Cross-Site Federation

Mirrors `subscription_mute_toggled`. The room's site is the write site;
the user's home site is the destination.

### Outbox publish (room-service)

Subject: `outbox.{roomSite}.to.{userSite}.subscription_favorite_toggled`.
Payload: `OutboxEvent` whose inner `Payload` is the JSON-encoded
`SubscriptionFavoriteToggledEvent`. JetStream stream:
`OUTBOX_{roomSite}` (already provisioned for mute and other cross-site
events).

### Inbox handler (inbox-worker)

`inbox-worker/handler.go`:

1. Dispatch switch gains
   `case "subscription_favorite_toggled": return h.handleSubscriptionFavoriteToggled(ctx, &evt)`.
2. `handleSubscriptionFavoriteToggled`:
   1. Unmarshal `evt.Payload` into `SubscriptionFavoriteToggledEvent`.
   2. Call `h.store.UpdateSubscriptionFavorite(ctx, e.RoomID, e.Account, e.Favorite)`.

`InboxStore` interface gains:

```go
UpdateSubscriptionFavorite(ctx context.Context, roomID, account string, favorite bool) error
```

Mongo implementation (`inbox-worker/main.go`):

```go
_, err := s.subCol.UpdateOne(ctx,
    bson.M{"roomId": roomID, "u.account": account},
    bson.M{"$set": bson.M{"favorite": favorite}},
)
```

Missing-subscription (zero documents matched) is a **silent no-op**, not
an error. Rationale: cross-site federation races — the user may have left
the room between the toggle on the room-site and the mirror on the
home-site. NACK'ing here would create a poison-pill redelivery loop with
no recovery path. Same pattern as `UpdateSubscriptionMute`.

### Stream ownership

No `bootstrap.go` change in either service. `OUTBOX_{site}` and
`INBOX_{site}` are owned by ops/IaC (CLAUDE.md §6 — "Stream bootstrap is
opt-in"). `inbox-worker` already owns INBOX creation when running in
dev with `BOOTSTRAP_STREAMS=true`.

## Behavioural rules

These are deliberate choices, not omissions:

1. **notification-worker does not consult `favorite`.** Favoriting a
   room does not affect push/email/banner delivery. (Compare: muting
   *also* does not yet gate notifications — both are render-only for now.
   If favorites ever need to gate behaviour, it's a separate change.)
2. **broadcast-worker does not route differently.** Favorites do not
   change which sessions receive a message.
3. **message-gatekeeper is untouched.** Validation does not consider
   subscription favourites.
4. **No bulk/list RPC.** Clients render the favorites section by
   filtering their existing in-memory subscription list on the
   `favorite` field. No new endpoint required.
5. **No DM/channel distinction.** Any subscription is favouritable —
   channel, DM, botDM, discussion. The handler doesn't branch on
   `RoomType`.

## Client API Doc

Per CLAUDE.md §5, the same PR updates `docs/client-api.md`:

- New section "Toggle Favorite" under user-scoped RPCs, sibling to
  "Toggle Mute". Documents subject, empty request body,
  `FavoriteToggleResponse`, error cases, triggered `subscription.update`
  event, and cross-site outbox-mirror behaviour.
- The existing "Subscription Update" event section's `action` enum
  documentation gains `"favorite_toggled"`, and the present-fields list
  for the embedded `Subscription` adds `favorite`.

## Testing (TDD)

### Subject builders (`pkg/subject/subject_test.go`)

- `TestFavoriteToggle` — concrete-subject builder.
- `TestFavoriteToggleWildcard` — wildcard form.
- `TestFavoriteToggle_ParseUserRoomSubject` — round-trip with the shared
  parser.

### Model round-trip (`pkg/model/model_test.go`)

- Extend the existing `Subscription` round-trip case to populate
  `Favorite: true`.
- Extend `TestSubscriptionJSON_ThreadUnreadOmittedAlertAlwaysPresent` to
  assert `"favorite"` is present in the JSON even when `false` (parallel
  to the existing `"muted"` assertion).
- `TestFavoriteToggleResponseJSON` — happy-path round-trip + raw-map
  assertion on `{status, favorite}`.
- `TestSubscriptionFavoriteToggledEventJSON` — round-trip.
- `TestOutboxSubscriptionFavoriteToggledConst` — exact-string
  `"subscription_favorite_toggled"`.

### Handler unit tests (`room-service/handler_test.go`)

Eight tests, parallel to the mute suite. Each builds a `Handler` with
mocked `RoomStore` and captured `publishCore` / `publishToStream`
closures.

| Test | Setup | Asserts |
|------|-------|---------|
| `TestHandler_FavoriteToggle_Success` | store returns sub with `Favorite: true`, `GetUserSiteID` returns `"site-a"` (same site) | Reply `{ok, true}`. One core publish on `chat.user.alice.event.subscription.update` with `Action: "favorite_toggled"`. **No** stream publish (`t.Fatal` guard in stub). |
| `TestHandler_FavoriteToggle_CrossSitePublishesOutbox` | store returns sub, `GetUserSiteID` returns `"site-b"` | Stream publish on `outbox.site-a.to.site-b.subscription_favorite_toggled`. `OutboxEvent` decoded carries `Type=OutboxSubscriptionFavoriteToggled`, `SiteID="site-a"`, `DestSiteID="site-b"`. Inner payload decodes to `SubscriptionFavoriteToggledEvent{Account, RoomID, Favorite, NonZero Timestamp}`. |
| `TestHandler_FavoriteToggle_NotRoomMember` | store returns `model.ErrSubscriptionNotFound` | `errors.Is(err, errNotRoomMember)`. No publishes. |
| `TestHandler_FavoriteToggle_InvalidSubject` | call handler with `"garbage.subject"` | Error contains `"invalid favorite-toggle subject"`. Store not called. |
| `TestHandler_FavoriteToggle_StoreError` | store returns `fmt.Errorf("db down")` | Error contains `"toggle subscription favorite"`. No publishes. |
| `TestHandler_FavoriteToggle_GetUserSiteIDError` | toggle ok, `GetUserSiteID` fails | Error contains `"get user siteId"`. Stream publish guarded by `t.Fatal`. |
| `TestHandler_FavoriteToggle_CrossSiteOutboxPublishFailure` | cross-site case, `publishToStream` returns error | Error contains `"publish favorite-toggled outbox"`. Client sees failure; favorite IS persisted (write happened before publish). |
| `TestHandler_FavoriteToggle_CorePublishFailureIsNonFatal` | same-site case, `publishCore` returns error | `require.NoError` — handler still replies `{ok, true}`. Documents the non-fatal contract. |

Mocks: `make generate SERVICE=room-service` regenerates `mock_store_test.go`
with `ToggleSubscriptionFavorite`. `GetUserSiteID` mock already exists.

### Inbox-worker unit tests (`inbox-worker/handler_test.go`)

Three tests on the same `stubInboxStore` used by the mute tests, extended
with a `UpdateSubscriptionFavorite` method that mutates the in-memory
subscription slice and silently no-ops on missing-sub.

- `TestHandler_SubscriptionFavoriteToggled` — happy path, asserts the
  store's subscription has `Favorite: true` after `HandleEvent`.
- `TestHandler_SubscriptionFavoriteToggled_MissingSubscriptionNoOp` —
  empty store, payload references unknown account, `HandleEvent` returns
  `nil`.
- `TestHandler_SubscriptionFavoriteToggled_MalformedPayload` —
  `Payload: []byte("not-json")`, `HandleEvent` returns error so JetStream
  redelivers (the wrapping NACK path).

### Integration test (`room-service/integration_test.go`)

Tagged `//go:build integration`. One test, `TestMongoStore_ToggleSubscriptionFavorite`:

1. Insert a raw BSON subscription with **no** `favorite` key at all —
   `bson.M` directly, bypassing the Go struct. This proves the legacy-doc
   path: documents written before this change must toggle cleanly.
2. First toggle → assert returned `Favorite == true` and `GetSubscription`
   reads back `true`. Confirms `$ifNull` branch.
3. Second toggle → assert `Favorite == false`.
4. Toggle on `(roomID="missing", account)` → assert
   `errors.Is(err, model.ErrSubscriptionNotFound)` and nil sub.

Uses `testutil.MongoDB(t, "room-svc-fav")` for an isolated DB per test.

### Coverage

The new handler and store methods each hit every branch the table above
enumerates. Expected ≥90% on the new code; project floor of 80% applies
to the whole package.

## Out of Scope

- Frontend changes. The frontend will read `favorite` off `Subscription`
  and render a sidebar section in a separate change; this PR is backend-only.
- Bulk-favorite RPC (e.g., "favourite all DMs"). Clients call
  `favorite.toggle` per-room.
- Default-favorite-on-join policy. Subscriptions are created with
  `favorite=false` implicitly (zero value); no constructor change.
- Persistence cross-device sort order. The flag is a boolean; ordering
  within the "favorites" section is the frontend's concern.
- Notification gating, broadcast routing changes, retention policy
  changes. None.
- Stream provisioning. `OUTBOX`/`INBOX` are pre-existing; no
  `bootstrap.go` change in either service.

## Risks

- **Toggle vs set semantics.** A retried RPC inverts state. Mute has
  this same property and clients debounce there; the favorite UI must do
  the same. Documented in `client-api.md`.
- **Cross-site mirror lag.** Between the room-site write and the
  home-site mirror, a "list my subscriptions" call against the home site
  will return the old value. Acceptable — the local subscription cache on
  the requester's session already reflects the new value (it received the
  `subscription.update` event synchronously), and other devices reconcile
  on next refetch.
- **Federation race silent no-op.** If a user leaves the room between
  the toggle and the inbox mirror, the mirror silently no-ops. The
  invariant — favorite makes no sense for a non-member — holds. No
  observable user impact.
- **No mute/favorite atomicity.** Mute and favorite are independent
  atomic toggles. Toggling both "simultaneously" from a client requires
  two RPCs that may interleave with other writers. Not a problem in
  practice (the fields don't constrain each other) but called out so a
  future "bulk subscription mutation" RPC doesn't accidentally assume
  the two were ever coupled.
