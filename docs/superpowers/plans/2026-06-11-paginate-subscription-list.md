# Paginate `subscription.list` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add offset/limit pagination (default page size 40, cap `MAX_SUBSCRIPTION_LIMIT`) to the user-service `subscription.list` NATS RPC for all three `type` values (`current`/`rooms`/`apps`), with `total` reporting the full filtered count.

**Architecture:** A new bounded page-request constructor in `pkg/mongoutil` feeds the existing `AggregatePaged` ($facet data+count) helper. The repo's three per-type pipelines unify into one paginate-able pipeline; the in-memory `favorite` filter and self-DM-first reorder move into the Mongo pipeline ($match + computed sort key). The handler threads an `OffsetPageRequest` through a changed `SubscriptionRepository` interface and enriches only the returned page.

**Tech Stack:** Go 1.25, mongo-driver/v2 aggregation, `go.uber.org/mock` (mockgen), testify, testcontainers (integration).

**Spec:** `docs/superpowers/specs/2026-06-11-paginate-subscription-list-design.md`

**Deviation from spec (intentional, justified):** the pipeline sort gains a final `_id: 1` tiebreak. Mongo's `$sort` is not stable; without a unique tiebreak, rows with equal `(favorite, name)` can swap between page requests, causing duplicated/skipped rows across pages. `_id` is unique, so it makes page boundaries deterministic. This only constrains previously-unspecified tie order.

---

## Verification commands (used throughout)

- Unit (single service): `make test SERVICE=user-service`
- Unit (pkg/mongoutil): `make test SERVICE=pkg/mongoutil` — if the Makefile's SERVICE filter doesn't accept pkg paths, run `make test` (full unit suite) instead.
- Integration: `make test-integration SERVICE=user-service` (requires Docker)
- Mocks: `make generate SERVICE=user-service`
- Lint: `make lint`

---

### Task 1: `pkg/mongoutil` — bounded page-request constructor

**Files:**
- Modify: `pkg/mongoutil/pagination.go`
- Test: `pkg/mongoutil/pagination_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `pkg/mongoutil/pagination_test.go` (style matches the existing `TestNewOffsetPageRequest_*` tests):

```go
func TestNewOffsetPageRequestWithBounds_DefaultApplied(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, 0, 40, 1000)
	assert.Equal(t, int64(0), p.Offset)
	assert.Equal(t, int64(40), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_NegativeLimitDefaulted(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, -7, 40, 1000)
	assert.Equal(t, int64(40), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_InRangePassThrough(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(80, 25, 40, 1000)
	assert.Equal(t, int64(80), p.Offset)
	assert.Equal(t, int64(25), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_CapApplied(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(0, 5000, 40, 1000)
	assert.Equal(t, int64(1000), p.Limit)
}

func TestNewOffsetPageRequestWithBounds_NegativeOffsetClamped(t *testing.T) {
	p := NewOffsetPageRequestWithBounds(-5, 40, 40, 1000)
	assert.Equal(t, int64(0), p.Offset)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/mongoutil` (or `make test`)
Expected: compile error `undefined: NewOffsetPageRequestWithBounds`

- [ ] **Step 3: Implement**

In `pkg/mongoutil/pagination.go`, replace the existing `NewOffsetPageRequest` with a delegating pair (existing callers keep default 20 / cap 100 — `apps.list` behavior must not change):

```go
// NewOffsetPageRequestWithBounds validates offset+limit against caller-supplied
// bounds: limit <= 0 -> defaultLimit, limit > maxLimit -> maxLimit, negative offset -> 0.
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

// NewOffsetPageRequest validates offset+limit. Default limit 20, max 100, negative offset clamped to 0.
func NewOffsetPageRequest(offset, limit int) OffsetPageRequest {
	return NewOffsetPageRequestWithBounds(offset, limit, 20, 100)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/mongoutil` (or `make test`)
Expected: PASS, including all pre-existing `TestNewOffsetPageRequest_*` tests (proves `apps.list` defaults unchanged).

- [ ] **Step 5: Commit**

```bash
git add pkg/mongoutil/pagination.go pkg/mongoutil/pagination_test.go
git commit -m "feat(mongoutil): add NewOffsetPageRequestWithBounds for caller-defined page bounds"
```

---

### Task 2: user-service request model — `offset` / `limit` fields

**Files:**
- Modify: `user-service/models/subscription.go`
- Test: `user-service/models/subscription_test.go`

- [ ] **Step 1: Write the failing test**

In `user-service/models/subscription_test.go`, replace `TestSubscriptionListRequest_RoundTrip` with:

```go
func TestSubscriptionListRequest_RoundTrip(t *testing.T) {
	fav := true
	days := 7
	in := SubscriptionListRequest{Type: "rooms", Favorite: &fav, UpdatedWithinDays: &days, Offset: 40, Limit: 25}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out SubscriptionListRequest
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestSubscriptionListRequest_ZeroPageFieldsOmitted(t *testing.T) {
	b, err := json.Marshal(SubscriptionListRequest{Type: "rooms"})
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	_, hasOffset := raw["offset"]
	_, hasLimit := raw["limit"]
	require.False(t, hasOffset, "zero offset must be omitted on the wire")
	require.False(t, hasLimit, "zero limit must be omitted on the wire")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=user-service`
Expected: compile error — `unknown field Offset in struct literal`

- [ ] **Step 3: Implement**

In `user-service/models/subscription.go`, replace the `SubscriptionListRequest` struct (keep its doc comment style):

```go
// SubscriptionListRequest is the body of subscription.list.
// Type ∈ {current, rooms, apps}. UpdatedWithinDays nil ⇒ no age filter.
// Offset/Limit page the result: limit ≤ 0 ⇒ server default 40, capped at MAX_SUBSCRIPTION_LIMIT.
type SubscriptionListRequest struct {
	Type              string `json:"type"`
	Favorite          *bool  `json:"favorite,omitempty"`
	UpdatedWithinDays *int   `json:"updatedWithinDays,omitempty"`
	Offset            int    `json:"offset,omitempty"`
	Limit             int    `json:"limit,omitempty"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=user-service`
Expected: PASS (models package; other packages untouched so far)

- [ ] **Step 5: Commit**

```bash
git add user-service/models/subscription.go user-service/models/subscription_test.go
git commit -m "feat(user-service): add offset/limit to subscription.list request model"
```

---

### Task 3: paginate the pipeline end-to-end (repo + interface + mocks + handler)

This is one atomic signature change — the repo method, consumer interface, generated mocks, and handler must flip together or the tree doesn't compile. One commit at the end.

**Files:**
- Modify: `user-service/service/service.go` (interface)
- Modify: `user-service/service/subscriptions.go` (handler; delete `filterFavorites`, `moveSelfDMFront`)
- Modify: `user-service/mongorepo/subscriptions.go` (unify pipelines; delete `aggregateCurrent`)
- Regenerate: `user-service/service/mocks/mock_repository.go`
- Test: `user-service/service/subscriptions_test.go`
- Test: `user-service/mongorepo/subscriptions_test.go` (integration)

- [ ] **Step 1: Update the consumer interface**

In `user-service/service/service.go`, change the `AggregateSubscriptions` line of `SubscriptionRepository` to:

```go
	AggregateSubscriptions(ctx context.Context, account, listType string, withinDays *int, favorite bool, page mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.Subscription], error)
```

(`pkg/mongoutil` is already imported in `service.go` for `AppRepository`.)

- [ ] **Step 2: Rewrite the repo implementation**

In `user-service/mongorepo/subscriptions.go`, replace BOTH `AggregateSubscriptions` and `aggregateCurrent` (delete `aggregateCurrent` entirely) with:

```go
// AggregateSubscriptions returns one page of account's subscriptions by listType —
// current (active dm/channel + subscribed botDMs), rooms (dm+channel), apps
// (subscribed botDMs) — with Total = the full filtered count. favorite narrows to
// favorited rows and sorts the caller's self-DM first. The trailing _id sort key
// keeps page boundaries deterministic when (favorite, name) ties.
func (r *SubscriptionRepo) AggregateSubscriptions(ctx context.Context, account, listType string, withinDays *int, favorite bool, page mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.Subscription], error) {
	match := bson.M{"u.account": account}
	var windowCutoff *time.Time
	switch listType {
	case "current":
		match["$or"] = bson.A{
			bson.M{"roomType": bson.M{"$in": bson.A{"dm", "channel"}}, "muted": bson.M{"$ne": true}},
			bson.M{"roomType": "botDM", "muted": bson.M{"$ne": true}, "isSubscribed": true},
		}
	case "rooms":
		match["muted"] = bson.M{"$ne": true}
		match["roomType"] = bson.M{"$in": bson.A{"dm", "channel"}}
		if withinDays != nil {
			// Windows on whole-room activity (room.lastMsgAt) post-$lookup — subscriptions carry no updatedAt.
			cutoff := time.Now().UTC().AddDate(0, 0, -*withinDays)
			windowCutoff = &cutoff
		}
	case "apps":
		// withinDays is intentionally not applied to apps subscriptions.
		match["muted"] = bson.M{"$ne": true}
		match["roomType"] = "botDM"
		match["isSubscribed"] = true
	}
	if favorite {
		match["favorite"] = true
	}
	pipeline := bson.A{bson.M{"$match": match}}
	pipeline = append(pipeline, roomsEnrichStages(r.siteID, windowCutoff)...)
	if favorite {
		// Self-DM-first applies only to the favorite view. account is $literal-wrapped:
		// a $-prefixed value would otherwise be read as a field path in $eq.
		pipeline = append(pipeline,
			bson.M{"$addFields": bson.M{"_selfDm": bson.M{"$and": bson.A{
				bson.M{"$eq": bson.A{"$roomType", "dm"}},
				bson.M{"$eq": bson.A{"$name", bson.M{"$literal": account}}},
			}}}},
			bson.M{"$sort": bson.D{{Key: "_selfDm", Value: -1}, {Key: "favorite", Value: -1}, {Key: "name", Value: 1}, {Key: "_id", Value: 1}}},
			bson.M{"$project": bson.M{"_selfDm": 0}},
		)
	} else {
		pipeline = append(pipeline, bson.M{"$sort": bson.D{{Key: "favorite", Value: -1}, {Key: "name", Value: 1}, {Key: "_id", Value: 1}}})
	}
	return r.subscriptions.AggregatePaged(ctx, pipeline, page, mongoutil.WithAllowDiskUse())
}
```

- [ ] **Step 3: Rewrite the handler**

In `user-service/service/subscriptions.go`:

(a) Add the page-size constant next to `maxAccountNames`:

```go
// defaultSubPageSize is subscription.list's page size when the request omits limit.
const defaultSubPageSize = 40
```

(b) Replace `ListSubscriptions` with:

```go
func (s *UserService) ListSubscriptions(c *natsrouter.Context, req models.SubscriptionListRequest) (*models.SubscriptionListResponse, error) {
	if !validListTypes[req.Type] {
		return nil, errcode.BadRequest("unknown subscription type")
	}
	if req.UpdatedWithinDays != nil && *req.UpdatedWithinDays < 0 {
		// A negative window computes a FUTURE cutoff and silently returns empty.
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

(c) Delete the now-dead `filterFavorites` and `moveSelfDMFront` functions.

(d) Add `"github.com/hmchangw/chat/pkg/mongoutil"` to the imports.

- [ ] **Step 4: Regenerate mocks**

Run: `make generate SERVICE=user-service`
Expected: `user-service/service/mocks/mock_repository.go` regenerated with the new `AggregateSubscriptions` signature. Never hand-edit.

- [ ] **Step 5: Update unit tests (red on behavior, not just compile)**

In `user-service/service/subscriptions_test.go`:

(a) DELETE these tests (they cover the deleted in-memory helpers): `TestFilterFavorites`, `TestMoveSelfDMFront`, `TestMoveSelfDMFront_NoSelf`, `TestMoveSelfDMFront_Nil`, `TestMoveSelfDMFront_AlreadyFirst`, and the old `TestListSubscriptions_Favorite`. Keep the `ptrBool` helper (still used).

(b) REPLACE `TestListSubscriptions_Types` and `TestListSubscriptions_StoreError` with the new-signature versions, and ADD the page-bounds/total/favorite/enrichment tests:

```go
func TestListSubscriptions_Types(t *testing.T) {
	for _, typ := range []string{"current", "rooms", "apps"} {
		t.Run(typ, func(t *testing.T) {
			svc, subs, _, _, rooms, _ := newSvc(t)
			subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", typ, gomock.Any(), false, mongoutil.OffsetPageRequest{Offset: 0, Limit: 40}).
				Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{{ID: "s1"}}, Total: 1}, nil)
			rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: typ})
			require.NoError(t, err)
			assert.Equal(t, 1, resp.Total)
		})
	}
}

func TestListSubscriptions_PageBounds(t *testing.T) {
	cases := []struct {
		name     string
		offset   int
		limit    int
		wantOff  int64
		wantLim  int64
	}{
		{"omitted defaults to 40", 0, 0, 0, 40},
		{"explicit passthrough", 80, 25, 80, 25},
		{"limit capped at maxSubs", 0, 5000, 0, 1000},
		{"negatives clamped", -3, -9, 0, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false,
				mongoutil.OffsetPageRequest{Offset: tc.wantOff, Limit: tc.wantLim}).
				Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
			_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
				models.SubscriptionListRequest{Type: "rooms", Offset: tc.offset, Limit: tc.limit})
			require.NoError(t, err)
		})
	}
}

func TestListSubscriptions_TotalIsFullCount(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{
			Data:  []model.Subscription{{ID: "s1"}, {ID: "s2"}},
			Total: 57,
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "rooms"})
	require.NoError(t, err)
	assert.Equal(t, 57, resp.Total, "total must be the full filtered count, not the page length")
	assert.Len(t, resp.Subscriptions, 2)
}

func TestListSubscriptions_FavoriteForwarded(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), true, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"),
		models.SubscriptionListRequest{Type: "current", Favorite: ptrBool(true)})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Total)
}

func TestListSubscriptions_FavoriteFalseNotForwarded(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
		models.SubscriptionListRequest{Type: "current", Favorite: ptrBool(false)})
	require.NoError(t, err)
}

func TestListSubscriptions_EnrichesPage(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{
			Data:  []model.Subscription{{ID: "s1", SiteID: "site-a", RoomID: "r1", Name: "Stale"}},
			Total: 1,
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "rooms"})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "Renamed", resp.Subscriptions[0].Name, "page rows must be room-info-enriched")
}
```

REPLACE `TestListSubscriptions_StoreError` body with:

```go
func TestListSubscriptions_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{}, errors.New("db down"))
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "current"})
	requireCode(t, err, errcode.CodeInternal)
}
```

(c) Add `"github.com/hmchangw/chat/pkg/mongoutil"` to the test file's imports.

- [ ] **Step 6: Run unit tests**

Run: `make test SERVICE=user-service`
Expected: PASS. If anything fails, fix the implementation (not the tests) unless the test itself is wrong.

- [ ] **Step 7: Update + extend the integration tests**

In `user-service/mongorepo/subscriptions_test.go`, inside `TestAggregateSubscriptions_Integration`:

(a) Add `"github.com/hmchangw/chat/pkg/mongoutil"` to imports, and a helper just above `TestAggregateSubscriptions_Integration`:

```go
// pg builds an explicit page for integration calls (no defaulting — the
// service-level constructor owns defaults).
func pg(offset, limit int64) mongoutil.OffsetPageRequest {
	return mongoutil.OffsetPageRequest{Offset: offset, Limit: limit}
}
```

(b) Extend the `rooms` seed with a favorited self-DM (room + sub) so the favorite/self-DM pipeline is exercised. Add to the `seed(t, db, "rooms", ...)` call:

```go
bson.M{"_id": "r-self", "name": "alice", "siteId": "site-a", "userCount": 1},
```

and to the `seed(t, db, "subscriptions", ...)` call:

```go
// favorited self-DM (name == account): must sort FIRST in the favorite view,
// even though "Eng" < "alice" by name.
bson.M{"_id": "sub-self", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-self",
	"name": "alice", "roomType": "dm", "siteId": "site-a", "favorite": true, "updatedAt": now, "createdAt": now},
```

(c) Update every existing `r.AggregateSubscriptions(ctx, "alice", <type>, <days>, 100)` call to the new signature and `.Data` access. The mapping is mechanical — `(ctx, "alice", typ, days, 100)` becomes `(ctx, "alice", typ, days, false, pg(0, 100))` and `subs` becomes `page.Data`:

```go
page, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, false, pg(0, 100))
require.NoError(t, err)
subs := page.Data
```

Existing membership assertions stay as-is (the new `sub-self` row only ADDS to the rooms/current sets; no existing `assert.False` references it). The `"favorite sorts before non-favorite then by name"` subtest still expects `subs[0].ID == "sub-eng"` — `sub-self` is also favorited, but among favorites the name sort puts `"Eng"` before `"alice"` (BSON binary order, uppercase < lowercase), so `sub-eng` stays first. Update the `"limit caps results"` subtest to `pg(0, 1)` + assert `len(page.Data) == 1`.

(d) ADD these subtests at the end of `TestAggregateSubscriptions_Integration`:

```go
t.Run("pages slice deterministically and total is the full count", func(t *testing.T) {
	// rooms view, favorite desc then name asc then _id. BOTH favorites lead
	// (sub-eng "Eng" < sub-self "alice" in BSON binary order), then non-favorites:
	// [sub-eng(fav), sub-self(fav), sub-old("EngOld"), sub-xsite("Remote"), sub-dm("bob")]
	first, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, false, pg(0, 2))
	require.NoError(t, err)
	require.Len(t, first.Data, 2)
	assert.Equal(t, int64(5), first.Total)
	assert.Equal(t, "sub-eng", first.Data[0].ID)
	assert.Equal(t, "sub-self", first.Data[1].ID)

	second, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, false, pg(2, 2))
	require.NoError(t, err)
	require.Len(t, second.Data, 2)
	assert.Equal(t, int64(5), second.Total, "total must not change across pages")
	assert.Equal(t, "sub-old", second.Data[0].ID)
	assert.Equal(t, "sub-xsite", second.Data[1].ID)

	last, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, false, pg(4, 2))
	require.NoError(t, err)
	require.Len(t, last.Data, 1)
	assert.Equal(t, "sub-dm", last.Data[0].ID)
})

t.Run("offset beyond end yields empty page with full total", func(t *testing.T) {
	page, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, false, pg(100, 2))
	require.NoError(t, err)
	assert.Empty(t, page.Data)
	assert.NotNil(t, page.Data, "empty page must be a non-nil slice")
	assert.Equal(t, int64(5), page.Total)
})

t.Run("favorite filters in-DB and self-DM sorts first", func(t *testing.T) {
	page, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, true, pg(0, 40))
	require.NoError(t, err)
	require.Len(t, page.Data, 2, "only the two favorited rows")
	assert.Equal(t, int64(2), page.Total)
	assert.Equal(t, "sub-self", page.Data[0].ID, "favorited self-DM first despite name order")
	assert.Equal(t, "sub-eng", page.Data[1].ID)
})

t.Run("favorite view with no favorites is empty", func(t *testing.T) {
	page, err := r.AggregateSubscriptions(ctx, "bob", "rooms", nil, true, pg(0, 40))
	require.NoError(t, err)
	assert.Empty(t, page.Data)
	assert.Equal(t, int64(0), page.Total)
})
```

(e) Update the OTHER integration files' compile dependencies: none — only `subscriptions_test.go` calls `AggregateSubscriptions`. The compile-time assertion `var _ service.SubscriptionRepository = (*SubscriptionRepo)(nil)` in `setup_test.go` now verifies the new signature for free.

- [ ] **Step 8: Run integration tests**

Run: `make test-integration SERVICE=user-service`
Expected: PASS (requires Docker; containers come from `pkg/testutil`).

- [ ] **Step 9: Full unit suite + lint**

Run: `make test && make lint`
Expected: PASS / no findings. The full unit run catches any other package that compiled against the old interface.

- [ ] **Step 10: Commit**

```bash
git add user-service/service/service.go user-service/service/subscriptions.go \
        user-service/service/subscriptions_test.go user-service/service/mocks/mock_repository.go \
        user-service/mongorepo/subscriptions.go user-service/mongorepo/subscriptions_test.go
git commit -m "feat(user-service): paginate subscription.list with default page size 40

All three type views (current/rooms/apps) share one paginated pipeline.
favorite filtering and self-DM-first ordering move from in-memory Go into
the Mongo pipeline so page slices and totals stay correct; total now
reports the full filtered count via AggregatePaged's \$facet."
```

---

### Task 4: client API docs + final verification

**Files:**
- Modify: `docs/client-api.md` (§3.4 `subscription.list` only — high-importance file, keep edits surgical)

- [ ] **Step 1: Update the request-body table**

In `docs/client-api.md` under `#### subscription.list`, add two rows to the request table after `updatedWithinDays`:

```markdown
| `offset`            | number  | no       | 0-based index of the first row to return. Negative values are clamped to `0`. Default `0`. |
| `limit`             | number  | no       | Page size. Omitted, `0`, or negative → server default **40**. Values above the server cap (`MAX_SUBSCRIPTION_LIMIT`, default 1000) are clamped to the cap. |
```

And replace the request example to show paging:

```json
{ "type": "current", "favorite": true, "offset": 0, "limit": 40 }
```

- [ ] **Step 2: Update the success-response table + add a pagination note**

Change the `total` row of the success-response table to:

```markdown
| `total`         | number            | Total count of subscriptions matching the filters across **all** pages (not the returned page length). |
```

Directly under the success-response table (before "Enrichment behavior"), add:

```markdown
**Pagination:** the reply contains at most `limit` rows starting at `offset` under a stable
`favorite` → `name` → `_id` ordering (the favorite view additionally sorts the caller's
self-DM first). An `offset` at or past the end yields an empty `subscriptions` array with
the unchanged full `total`. Clients that need the complete list must page until
`offset + len(subscriptions) >= total`.
```

- [ ] **Step 3: Verify the docs build context didn't break**

Run: `grep -n "offset" docs/client-api.md | sed -n '1,10p'` and visually confirm the §3.4 subscription.list rows render as a well-formed table (pipe-aligned, no broken markdown).
Expected: the two new rows appear inside the `subscription.list` request table.

- [ ] **Step 4: Full gate — tests, lint, SAST**

Run: `make test && make lint && make sast`
Expected: all PASS / no medium+ findings.

Run coverage for the touched packages (sanctioned by CLAUDE.md §4):

```bash
go test -race -coverprofile=/tmp/cov-svc.out ./user-service/service/ && go tool cover -func=/tmp/cov-svc.out | tail -1
go test -race -coverprofile=/tmp/cov-mongoutil.out ./pkg/mongoutil/ && go tool cover -func=/tmp/cov-mongoutil.out | tail -1
```

Expected: total coverage ≥ 80% for both (handler package target 90%+).

- [ ] **Step 5: Commit**

```bash
git add docs/client-api.md
git commit -m "docs(client-api): document subscription.list pagination"
```

---

## Out of scope (do NOT touch)

- `apps.list` / `AppRepo.ListApps` — already paginated at default 20/cap 100; behavior must not change (Task 1 keeps `NewOffsetPageRequest` delegating with 20/100).
- `subscription.getChannels`, `getDM`, `getByRoomID`, `count` — unchanged signatures and behavior.
- `chat-frontend` — will receive at most 40 rows per sidebar bucket after this lands; that truncation is a documented, accepted follow-up (spec §"Known follow-up"), NOT a bug to fix here.
- `tools/loadgen` — its `subscriptionListRequest` mirror struct needs no new fields (omitted fields = server defaults).
