# Paginate `subscription.list` — Design

## Summary

Add offset/limit pagination to the user-service `subscription.list` NATS RPC, with a
default page size of **40**. The single `subscription.list` handler serves all three
subscription views (`type` ∈ `current` / `rooms` / `apps`), so one change paginates all
three uniformly. The separate `apps.list` catalog RPC (already paged, default 20) is **out
of scope** and untouched.

## Motivation

`subscription.list` currently fetches up to `MAX_SUBSCRIPTION_LIMIT` (default 1000)
subscriptions in a single reply, enriches every one via per-site room-service RPCs, and
returns the whole set. The initial sidebar bootstrap needs to load a bounded first page
instead of the entire subscription set.

## Scope

**In scope**

- `pkg/mongoutil`: additive bounded page-request constructor.
- `user-service`: request model, service handler, mongo repository, regenerated mocks.
- `docs/client-api.md` §3.4 `subscription.list`.

**Out of scope**

- `apps.list` (the app *catalog* RPC) — already paginated, a different surface.
- `subscription.getChannels`, `getDM`, `getByRoomID`, `count` — unchanged.
- **chat-frontend paging UI** — see "Known follow-up: sidebar truncation" below.

## Current behavior (baseline)

`service/subscriptions.go::ListSubscriptions`:

1. Validates `type` and `updatedWithinDays`.
2. `subs := AggregateSubscriptions(account, type, withinDays, maxSubs)` → `[]Subscription`
   (capped at `maxSubs`).
3. If `favorite == true`: `filterFavorites(subs)` then `moveSelfDMFront(subs, account)` —
   both **in-memory, in Go**.
4. `enrichWithRoomInfo(subs)` — per-site room-service enrichment over the full set.
5. Returns `{ Subscriptions: subs, Total: len(subs) }` — `Total` is the page length.

`mongorepo/subscriptions.go::AggregateSubscriptions` branches by type:
- `current` → `aggregateCurrent`: a `$facet` top-K optimization (each of the rooms/apps
  branches sorts + limits to `maxSubs` before a merge + global sort + limit).
- `rooms` / `apps` → single `$match` + enrich + `$sort` + `$limit` pipeline.

## Target design

### Request (`models/subscription.go`)

```go
type SubscriptionListRequest struct {
    Type              string `json:"type"`
    Favorite          *bool  `json:"favorite,omitempty"`
    UpdatedWithinDays *int   `json:"updatedWithinDays,omitempty"`
    Offset            int    `json:"offset,omitempty"` // default 0, negative clamped to 0
    Limit             int    `json:"limit,omitempty"`  // default 40, cap = MAX_SUBSCRIPTION_LIMIT
}
```

### Response (`models/subscription.go`)

Shape is unchanged: `{ subscriptions, total }`. **`total` semantics change**: it becomes the
**full filtered count across all pages**, not the length of the returned page (this matches
how `apps.list` already reports `total`). Verified safe: `chat-frontend`
(`fetchSidebarBuckets`) and `tools/loadgen` read only `subscriptions`, never `total`.

### Page-request constructor (`pkg/mongoutil/pagination.go`)

`NewOffsetPageRequest` hard-codes default 20 / cap 100 (used by `apps.list`). We need
default 40 / cap `MAX_SUBSCRIPTION_LIMIT`. Add an **additive** bounded constructor and have
the existing one delegate — `apps.list` behavior is untouched:

```go
// NewOffsetPageRequestWithBounds validates offset+limit against caller-supplied bounds.
// limit <= 0 -> defaultLimit; limit > maxLimit -> maxLimit; negative offset -> 0.
func NewOffsetPageRequestWithBounds(offset, limit, defaultLimit, maxLimit int) OffsetPageRequest {
    if offset < 0 {
        offset = 0
    }
    if limit <= 0 {
        limit = defaultLimit
    }
    if limit > maxLimit {
        limit = maxLimit
    }
    return OffsetPageRequest{Offset: int64(offset), Limit: int64(limit)}
}

func NewOffsetPageRequest(offset, limit int) OffsetPageRequest {
    return NewOffsetPageRequestWithBounds(offset, limit, 20, 100)
}
```

### Service handler (`service/subscriptions.go`)

```go
const defaultSubPageSize = 40

func (s *UserService) ListSubscriptions(c *natsrouter.Context, req models.SubscriptionListRequest) (*models.SubscriptionListResponse, error) {
    if !validListTypes[req.Type] {
        return nil, errcode.BadRequest("unknown subscription type")
    }
    if req.UpdatedWithinDays != nil && *req.UpdatedWithinDays < 0 {
        return nil, errcode.BadRequest("updatedWithinDays must be non-negative")
    }
    account := c.Param("account")
    c.WithLogValues("account", account)
    favorite := req.Favorite != nil && *req.Favorite
    page := mongoutil.NewOffsetPageRequestWithBounds(req.Offset, req.Limit, defaultSubPageSize, s.maxSubs)
    result, err := s.subs.AggregateSubscriptions(c, account, req.Type, req.UpdatedWithinDays, favorite, page)
    if err != nil {
        return nil, fmt.Errorf("list subscriptions: %w", err)
    }
    s.enrichWithRoomInfo(c, result.Data)
    return &models.SubscriptionListResponse{Subscriptions: result.Data, Total: int(result.Total)}, nil
}
```

`filterFavorites` and `moveSelfDMFront` are **deleted** — their behavior moves into the
pipeline (next section). Enrichment now runs on the ≤`limit` page → strictly fewer
room-service RPCs per call.

### Repository (`mongorepo/subscriptions.go`)

`SubscriptionRepository.AggregateSubscriptions` signature changes:

```go
// before
AggregateSubscriptions(ctx, account, listType string, withinDays *int, limit int) ([]model.Subscription, error)
// after
AggregateSubscriptions(ctx, account, listType string, withinDays *int, favorite bool, page mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.Subscription], error)
```

All three types collapse into **one** pipeline shape:

1. `$match` — per-type selector, plus `favorite: true` when `favorite` is set:
   - `current`: `{ u.account, $or: [ {roomType ∈ [dm,channel], muted≠true}, {roomType: botDM, muted≠true, isSubscribed: true} ] }`
   - `rooms`: `{ u.account, muted≠true, roomType ∈ [dm,channel] }`
   - `apps`: `{ u.account, muted≠true, roomType: botDM, isSubscribed: true }`
2. `roomsEnrichStages(siteID, windowCutoff)` — unchanged join/deleted-filter/`$addFields`.
   `windowCutoff` applies only to `rooms` + `withinDays` (as today).
3. Sort:
   - favorite set: `$addFields { _selfDm: { $and: [ {$eq:[roomType,"dm"]}, {$eq:[name, account]} ] } }`
     then `$sort { _selfDm: -1, favorite: -1, name: 1 }`, then `$project { _selfDm: 0 }`.
     This reproduces "self-DM first, then favorites by name" exactly, in Mongo.
   - favorite unset: `$sort { favorite: -1, name: 1 }` (unchanged).
4. `AggregatePaged(pipeline, page, WithAllowDiskUse())` → `OffsetPage[Subscription]`. Its
   `$facet` does `data: [$skip, $limit]` + `total: [$count]` over the same input → accurate
   full count + the requested page in one round trip.

`aggregateCurrent` and its internal `$facet` top-K optimization are **removed**. Rationale:
the top-K trick limits each branch before merge, which makes an exact paginated `total`
impossible; an accurate count requires touching the full matched set anyway. Per-user
subscription counts are bounded (logical cap `maxSubs` ≈ 1000), so a full `$sort` with
`allowDiskUse` is acceptable. This is required by the feature, not gratuitous refactoring.

### Mocks

`SubscriptionRepository` changed → regenerate `service/mocks/mock_repository.go` via
`make generate SERVICE=user-service`. Never hand-edit.

## Edge cases

| Input | Behavior |
|-------|----------|
| `limit` omitted / `0` / negative | default 40 |
| `limit` > `MAX_SUBSCRIPTION_LIMIT` | capped at `MAX_SUBSCRIPTION_LIMIT` |
| `offset` negative | clamped to 0 |
| `offset` ≥ total | empty `subscriptions`, `total` = full count |
| no matches | `subscriptions: []`, `total: 0` |
| `favorite: true`, none favorited | `subscriptions: []`, `total: 0` |
| unknown `type` | `bad_request` (unchanged) |
| negative `updatedWithinDays` | `bad_request` (unchanged) |

## Known follow-up: sidebar truncation (frontend)

**This change makes `subscription.list` default to 40 items per call when no `limit` is
sent.** `chat-frontend/src/api/fetchSidebarBuckets/index.ts` calls the RPC three times
(`{type:current,favorite:true}`, `{type:apps}`, `{type:rooms}`) **without** `offset`/`limit`,
so after this lands the sidebar will receive **at most 40 subscriptions per bucket** until
the frontend is updated to page through results ("load more"). This is a deliberate,
accepted consequence of the backend-only scope, **not** a bug. The frontend paging work is a
separate follow-up effort. `tools/loadgen` is unaffected (it ignores the reply body).

## Testing (TDD)

- **`pkg/mongoutil/pagination_test.go`**: `NewOffsetPageRequestWithBounds` — default applied,
  cap applied, negative offset clamped, in-range pass-through; confirm `NewOffsetPageRequest`
  still yields default 20 / cap 100.
- **`service/subscriptions_test.go`** (mocked repo): page request threaded through with
  defaults/caps; `favorite` flag forwarded; `Total` taken from `OffsetPage.Total` (not page
  length); `Data` enriched; validation errors unchanged. Remove tests for the deleted
  `filterFavorites` / `moveSelfDMFront`.
- **`mongorepo/subscriptions_test.go`** (integration, testcontainers Mongo): accurate `total`
  vs page slice across offsets; default-40 boundary; per-type `$match`; `favorite:true`
  filters in-DB; self-DM sorts first within favorites; `updatedWithinDays` window still
  applies to `rooms`; deleted-room filter preserved.

Coverage floor 80% (target 90% for handler + repo). `-race` on all unit tests.

## Documentation

`docs/client-api.md` §3.4 `subscription.list`:
- Add `offset` / `limit` rows to the request-body table (types, defaults, cap, clamping).
- Clarify `total` = full filtered count across all pages (not page length).
- Refresh the success example if needed; keep edits minimal (high-importance file).

## Files touched

| File | Change |
|------|--------|
| `pkg/mongoutil/pagination.go` | add `NewOffsetPageRequestWithBounds`; delegate existing |
| `pkg/mongoutil/pagination_test.go` | tests for the new constructor |
| `user-service/models/subscription.go` | add `Offset` / `Limit` to request |
| `user-service/service/service.go` | `AggregateSubscriptions` interface signature |
| `user-service/service/subscriptions.go` | rewrite handler; delete favorite helpers |
| `user-service/service/subscriptions_test.go` | update unit tests |
| `user-service/service/mocks/mock_repository.go` | regenerate |
| `user-service/mongorepo/subscriptions.go` | unify + paginate; remove `aggregateCurrent` |
| `user-service/mongorepo/subscriptions_test.go` | update/extend integration tests |
| `docs/client-api.md` | §3.4 request/response/notes |
