# Read-Receipt RPC Performance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the per-subscription `$lookup` fan-out from the read-receipt RPC by making `ListReadReceipts` a lean covered query and resolving reader names through a shared, cached `userstore.UserStore`.

**Architecture:** `room-service/handleMessageReadReceipt` stops asking Mongo to join `subscriptions → users`. Instead `ListReadReceipts` returns the matched reader **accounts** (covered by a new `(roomId, lastSeenAt, u.account, u._id)` index, no document FETCH), and the handler resolves names via `userResolver.FindUsersByAccounts`. The resolver is the existing `broadcast-worker` LRU+TTL user cache, promoted to a shared `pkg/usercache` package so both services use one implementation. TTL-only staleness (no active invalidation — no user-updated event exists in the system).

**Tech Stack:** Go 1.25, MongoDB (`mongo-driver/v2`), `go.uber.org/mock` (mockgen), `stretchr/testify`, `testcontainers-go`, `caarlos0/env`.

**Spec:** `docs/superpowers/specs/2026-06-02-read-receipt-rpc-perf-design.md`

---

## File Structure

**Created:**
- `pkg/usercache/usercache.go` — `CachedUserStore` (moved from `broadcast-worker/usercache.go`), constructor renamed to `New`.
- `pkg/usercache/usercache_test.go` — moved tests.

**Modified:**
- `broadcast-worker/usercache.go` — **deleted** (moved to pkg).
- `broadcast-worker/usercache_test.go` — **deleted** (moved to pkg).
- `broadcast-worker/main.go:23,84` — import `pkg/usercache`, call `usercache.New`.
- `room-service/store.go` — add `UserResolver` interface; change `ListReadReceipts` return type; remove `ReadReceiptRow`.
- `room-service/store_mongo.go:88-93,917-964` — lean aggregation; covering-index migration.
- `room-service/handler.go:32-72,1279-1294` — `userResolver` field + constructor param; cached name resolution.
- `room-service/main.go:81,122` — construct `userstore` + `usercache`, inject; add config fields.
- `room-service/integration_test.go` — update 8 `NewHandler` call sites; add covered-query test.
- `room-service/handler_test.go:3150-3190` — rewrite read-receipt handler tests.
- `room-service/mock_store_test.go` — regenerated (`MockUserResolver`, new `ListReadReceipts` signature).
- `room-service/deploy/docker-compose.yml` — add `USER_CACHE_SIZE` / `USER_CACHE_TTL`.

**Unchanged (intentionally):** `pkg/model/read_receipt.go` (response schema identical) and `docs/client-api.md`.

---

## Task 1: Promote `CachedUserStore` to `pkg/usercache`

Pure move + rename refactor. The existing tests are the safety net.

**Files:**
- Create: `pkg/usercache/usercache.go`, `pkg/usercache/usercache_test.go`
- Delete: `broadcast-worker/usercache.go`, `broadcast-worker/usercache_test.go`

- [ ] **Step 1: Move the implementation file**

```bash
cd /home/user/chat
git mv broadcast-worker/usercache.go pkg/usercache/usercache.go
git mv broadcast-worker/usercache_test.go pkg/usercache/usercache_test.go
```

- [ ] **Step 2: Change the package clause in both files**

In `pkg/usercache/usercache.go` and `pkg/usercache/usercache_test.go`, change the first line:

```go
package usercache
```

(was `package main` in both).

- [ ] **Step 3: Rename the constructor `NewCachedUserStore` → `New`**

In `pkg/usercache/usercache.go`, rename the constructor and update its doc comment:

```go
// New returns a cache wrapping inner. maxSize and ttl must both be positive.
func New(inner userstore.UserStore, maxSize int, ttl time.Duration) *CachedUserStore {
	return &CachedUserStore{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		lru:     list.New(),
		index:   make(map[string]*list.Element, maxSize),
		now:     time.Now,
	}
}
```

In `pkg/usercache/usercache_test.go`, replace every `NewCachedUserStore(` with `New(`.

- [ ] **Step 4: Run the moved tests (verify green after the refactor)**

Run: `make test SERVICE=usercache`
If the Makefile's `SERVICE` filter doesn't resolve a `pkg/` path, run the full suite instead: `make test`
Expected: PASS — all moved `usercache` tests pass under the new package.

- [ ] **Step 5: Commit**

```bash
git add pkg/usercache/ broadcast-worker/usercache.go broadcast-worker/usercache_test.go
git commit -m "Promote broadcast-worker user cache to pkg/usercache"
```

---

## Task 2: Rewire `broadcast-worker` to `pkg/usercache`

`broadcast-worker` no longer compiles after Task 1 (its `NewCachedUserStore` is gone). Fix the one call site and import.

**Files:**
- Modify: `broadcast-worker/main.go:23` (import), `broadcast-worker/main.go:84` (construction)

- [ ] **Step 1: Confirm the build is currently broken (expected)**

Run: `make build SERVICE=broadcast-worker`
Expected: FAIL — `undefined: NewCachedUserStore`.

- [ ] **Step 2: Add the `pkg/usercache` import**

In `broadcast-worker/main.go`, add to the `github.com/hmchangw/chat/...` import group (alphabetical, after `stream`, before `userstore`):

```go
	"github.com/hmchangw/chat/pkg/usercache"
```

- [ ] **Step 3: Update the construction call**

In `broadcast-worker/main.go:84`, change:

```go
		us = usercache.New(us, cfg.UserCacheSize, cfg.UserCacheTTL)
```

(was `us = NewCachedUserStore(us, cfg.UserCacheSize, cfg.UserCacheTTL)`).

- [ ] **Step 4: Build and test broadcast-worker**

Run: `make build SERVICE=broadcast-worker && make test SERVICE=broadcast-worker`
Expected: PASS — builds and existing tests green.

- [ ] **Step 5: Commit**

```bash
git add broadcast-worker/main.go
git commit -m "Rewire broadcast-worker to pkg/usercache"
```

---

## Task 3: room-service plumbing — `UserResolver` dependency + config

Adds the resolver dependency and wiring with **no behavior change yet** (the handler still uses the old path). Keeps the build green.

**Files:**
- Modify: `room-service/store.go` (add `UserResolver` interface)
- Modify: `room-service/handler.go:32-72` (field + constructor param)
- Modify: `room-service/main.go` (config fields + construction + injection)
- Modify: `room-service/integration_test.go` (8 `NewHandler` call sites)
- Modify: `room-service/deploy/docker-compose.yml`
- Regenerate: `room-service/mock_store_test.go`

- [ ] **Step 1: Add the `UserResolver` interface to `store.go`**

In `room-service/store.go`, after the `RoomStore` interface block (it already imports `pkg/model`), add:

```go
// UserResolver resolves user directory records (names) by account, for
// building read-receipt responses. Implemented by pkg/usercache.CachedUserStore
// (and pkg/userstore.UserStore underneath). Defined here so mockgen generates a
// MockUserResolver for handler tests.
type UserResolver interface {
	FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error)
}
```

- [ ] **Step 2: Add the `userResolver` field and constructor param to `handler.go`**

In `room-service/handler.go`, add the field to the `Handler` struct (after `msgReader`):

```go
	msgReader         MessageReader
	userResolver      UserResolver
```

Change the `NewHandler` signature to accept `userResolver UserResolver` immediately after `msgReader MessageReader`:

```go
func NewHandler(store RoomStore, keyStore RoomKeyStore, memberListClient MemberListClient, msgReader MessageReader, userResolver UserResolver, siteID string, maxRoomSize, maxBatchSize int, memberListTimeout time.Duration, publishToStream func(context.Context, string, []byte) error, publishCore func(context.Context, string, []byte) error) *Handler {
	return &Handler{
		store:             store,
		keyStore:          keyStore,
		memberListClient:  memberListClient,
		msgReader:         msgReader,
		userResolver:      userResolver,
		siteID:            siteID,
		maxRoomSize:       maxRoomSize,
		maxBatchSize:      maxBatchSize,
		memberListTimeout: memberListTimeout,
		publishToStream:   publishToStream,
		publishCore:       publishCore,
	}
}
```

- [ ] **Step 3: Add config fields in `main.go`**

In `room-service/main.go` `config` struct, after `MemberListTimeout`:

```go
	UserCacheSize     int             `env:"USER_CACHE_SIZE"           envDefault:"10000"`
	UserCacheTTL      time.Duration   `env:"USER_CACHE_TTL"            envDefault:"5m"`
```

- [ ] **Step 4: Construct and inject the resolver in `main.go`**

In `room-service/main.go`, add the imports (in the `github.com/hmchangw/chat/...` group):

```go
	"github.com/hmchangw/chat/pkg/usercache"
	"github.com/hmchangw/chat/pkg/userstore"
```

After `db := mongoClient.Database(cfg.MongoDB)` (line 81), build the resolver:

```go
	var userResolver UserResolver = userstore.NewMongoStore(db.Collection("users"))
	if cfg.UserCacheSize > 0 && cfg.UserCacheTTL > 0 {
		userResolver = usercache.New(userResolver, cfg.UserCacheSize, cfg.UserCacheTTL)
		slog.Info("user-cache enabled", "size", cfg.UserCacheSize, "ttl", cfg.UserCacheTTL)
	}
```

Then pass `userResolver` into the `NewHandler` call (line 122), as the new 5th argument after `cassReader`:

```go
	handler := NewHandler(store, keyStore, memberListClient, cassReader, userResolver, cfg.SiteID, cfg.MaxRoomSize, cfg.MaxBatchSize, cfg.MemberListTimeout,
```

> Note: `pkg/usercache.CachedUserStore` and `pkg/userstore.mongoStore` both satisfy the local `UserResolver` interface (it is a subset of `userstore.UserStore`).

- [ ] **Step 5: Update the 8 `NewHandler` call sites in `integration_test.go`**

In `room-service/integration_test.go`, insert `nil` as the new 5th argument (after the `msgReader` argument, which is `nil` in all but none of these) in each `NewHandler(...)` call. The read-receipt integration test added in Task 5 will pass a real resolver; for now pass `nil` everywhere. Example — line 1084 becomes:

```go
	handler := NewHandler(store, keyStore, nil, nil, nil, "site-a", 1000, 500, 5*time.Second, publish, func(context.Context, string, []byte) error { return nil })
```

Apply the same `nil` insertion at lines 1148, 1190, 1245, 1265, 1325, 1372, 1568 (each already passes `nil` for `msgReader`; add one more `nil` right after it).

- [ ] **Step 6: Regenerate mocks**

Run: `make generate SERVICE=room-service`
Expected: `room-service/mock_store_test.go` now contains a `MockUserResolver` type and an updated `ListReadReceipts` mock matches the (still unchanged) signature.

- [ ] **Step 7: Add env vars to docker-compose**

In `room-service/deploy/docker-compose.yml`, under the room-service `environment:` block, add:

```yaml
      USER_CACHE_SIZE: "10000"
      USER_CACHE_TTL: "5m"
```

- [ ] **Step 8: Build and test**

Run: `make build SERVICE=room-service && make test SERVICE=room-service`
Expected: PASS — compiles with the new dependency wired but unused in the handler body; existing tests green.

- [ ] **Step 9: Commit**

```bash
git add room-service/store.go room-service/handler.go room-service/main.go room-service/integration_test.go room-service/mock_store_test.go room-service/deploy/docker-compose.yml
git commit -m "Wire UserResolver dependency into room-service handler"
```

---

## Task 4: Lean `ListReadReceipts` + cached name resolution

The behavioral change. TDD: write the handler test against the new shape first.

**Files:**
- Modify: `room-service/store.go` (signature + remove `ReadReceiptRow`)
- Modify: `room-service/store_mongo.go:917-964` (lean query)
- Modify: `room-service/handler.go:1279-1294` (resolve via `userResolver`)
- Modify: `room-service/handler_test.go:3150-3190` (rewrite tests)
- Regenerate: `room-service/mock_store_test.go`

- [ ] **Step 1: Write the failing handler test**

In `room-service/handler_test.go`, replace the existing read-receipt success/empty tests (around lines 3150-3190) with table-driven tests using the new shape. The handler builds the response from `userResolver.FindUsersByAccounts`, not from the store rows:

```go
func TestHandler_handleMessageReadReceipt_Resolution(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	const (
		roomID    = "room-1"
		requester = "alice"
		msgID     = "msg-1"
	)
	created := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	subj := subject.MessageReadReceipt(requester, roomID, "site-a")
	body, _ := json.Marshal(model.ReadReceiptRequest{MessageID: msgID})

	t.Run("resolves reader names via userResolver", func(t *testing.T) {
		store := NewMockRoomStore(ctrl)
		resolver := NewMockUserResolver(ctrl)
		store.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
			Return(&model.Subscription{}, nil)
		store.EXPECT().ListReadReceipts(gomock.Any(), roomID, created, requester, 1000).
			Return([]string{"bob", "carol"}, nil)
		resolver.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob", "carol"}).
			Return([]model.User{
				{ID: "u-bob", Account: "bob", ChineseName: "鮑伯", EngName: "Bob"},
				{ID: "u-carol", Account: "carol", ChineseName: "卡蘿", EngName: "Carol"},
			}, nil)

		h := &Handler{store: store, userResolver: resolver, siteID: "site-a", maxRoomSize: 1000,
			msgReader: stubMsgReader{roomID: roomID, created: created, sender: requester, found: true}}

		out, err := h.handleMessageReadReceipt(context.Background(), subj, body)
		require.NoError(t, err)
		var resp model.ReadReceiptResponse
		require.NoError(t, json.Unmarshal(out, &resp))
		require.Len(t, resp.Readers, 2)
		assert.Equal(t, "u-bob", resp.Readers[0].UserID)
		assert.Equal(t, "Carol", resp.Readers[1].EngName)
	})

	t.Run("empty reader set skips resolver", func(t *testing.T) {
		store := NewMockRoomStore(ctrl)
		resolver := NewMockUserResolver(ctrl)
		store.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
			Return(&model.Subscription{}, nil)
		store.EXPECT().ListReadReceipts(gomock.Any(), roomID, created, requester, 1000).
			Return([]string{}, nil)
		// resolver.FindUsersByAccounts must NOT be called.

		h := &Handler{store: store, userResolver: resolver, siteID: "site-a", maxRoomSize: 1000,
			msgReader: stubMsgReader{roomID: roomID, created: created, sender: requester, found: true}}

		out, err := h.handleMessageReadReceipt(context.Background(), subj, body)
		require.NoError(t, err)
		var resp model.ReadReceiptResponse
		require.NoError(t, json.Unmarshal(out, &resp))
		assert.Empty(t, resp.Readers)
	})

	t.Run("resolver error propagates", func(t *testing.T) {
		store := NewMockRoomStore(ctrl)
		resolver := NewMockUserResolver(ctrl)
		store.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
			Return(&model.Subscription{}, nil)
		store.EXPECT().ListReadReceipts(gomock.Any(), roomID, created, requester, 1000).
			Return([]string{"bob"}, nil)
		resolver.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"bob"}).
			Return(nil, errors.New("mongo down"))

		h := &Handler{store: store, userResolver: resolver, siteID: "site-a", maxRoomSize: 1000,
			msgReader: stubMsgReader{roomID: roomID, created: created, sender: requester, found: true}}

		_, err := h.handleMessageReadReceipt(context.Background(), subj, body)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolve reader names")
	})
}
```

> If `stubMsgReader` does not already exist in the test file, define a minimal stub satisfying `MessageReader.GetMessageRoomAndCreatedAt` near the top of `handler_test.go`:
> ```go
> type stubMsgReader struct {
> 	roomID  string
> 	created time.Time
> 	sender  string
> 	found   bool
> }
>
> func (s stubMsgReader) GetMessageRoomAndCreatedAt(_ context.Context, _ string) (string, time.Time, string, bool, error) {
> 	return s.roomID, s.created, s.sender, s.found, nil
> }
> ```
> (Check whether the existing read-receipt tests already provide a message-reader stub/mock; reuse it instead of redefining.)

- [ ] **Step 2: Update the interface, remove `ReadReceiptRow`, regenerate mocks**

In `room-service/store.go`: change the `ListReadReceipts` signature and delete the now-unused `ReadReceiptRow` struct (lines 36-41):

```go
	ListReadReceipts(ctx context.Context, roomID string, since time.Time, excludeAccount string, limit int) ([]string, error)
```

Run: `make generate SERVICE=room-service`
Expected: `mock_store_test.go` `ListReadReceipts` mock now returns `[]string`; `MockUserResolver` present.

- [ ] **Step 3: Run the test to verify it fails**

Run: `make test SERVICE=room-service`
Expected: FAIL — `store_mongo.go` still returns `[]ReadReceiptRow` (compile error) and the handler still builds from rows.

- [ ] **Step 4: Implement the lean query in `store_mongo.go`**

Replace the entire `ListReadReceipts` body (lines 917-964) with:

```go
func (s *MongoStore) ListReadReceipts(
	ctx context.Context,
	roomID string,
	since time.Time,
	excludeAccount string,
	limit int,
) ([]string, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"roomId":     roomID,
			"lastSeenAt": bson.M{"$gte": since},
			"u.account":  bson.M{"$ne": excludeAccount},
		}}},
		{{Key: "$limit", Value: int64(limit)}},
		{{Key: "$project", Value: bson.M{"_id": 0, "account": "$u.account"}}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate read receipts for room %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)

	accounts := make([]string, 0)
	for cursor.Next(ctx) {
		var row struct {
			Account string `bson:"account"`
		}
		if err := cursor.Decode(&row); err != nil {
			return nil, fmt.Errorf("decode read-receipt row for room %q: %w", roomID, err)
		}
		accounts = append(accounts, row.Account)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate read receipts for room %q: %w", roomID, err)
	}
	return accounts, nil
}
```

- [ ] **Step 5: Implement cached resolution in the handler**

In `room-service/handler.go`, replace lines 1279-1294 (from `rows, err := h.store.ListReadReceipts(...)` through the final `return json.Marshal(...)`) with:

```go
	accounts, err := h.store.ListReadReceipts(ctx, roomID, msgCreatedAt, msgSender, h.maxRoomSize)
	if err != nil {
		return nil, fmt.Errorf("list read receipts: %w", err)
	}

	entries := make([]model.ReadReceiptEntry, 0, len(accounts))
	if len(accounts) > 0 {
		users, err := h.userResolver.FindUsersByAccounts(ctx, accounts)
		if err != nil {
			return nil, fmt.Errorf("resolve reader names: %w", err)
		}
		for i := range users {
			u := users[i]
			entries = append(entries, model.ReadReceiptEntry{
				UserID:      u.ID,
				Account:     u.Account,
				ChineseName: u.ChineseName,
				EngName:     u.EngName,
			})
		}
	}

	return json.Marshal(model.ReadReceiptResponse{Readers: entries})
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make test SERVICE=room-service`
Expected: PASS — new resolution tests green; no remaining references to `ReadReceiptRow`.

- [ ] **Step 7: Lint**

Run: `make lint`
Expected: PASS (no unused imports/symbols; `ReadReceiptRow` fully removed).

- [ ] **Step 8: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/handler.go room-service/handler_test.go room-service/mock_store_test.go
git commit -m "Make ListReadReceipts lean and resolve reader names via cache"
```

---

## Task 5: Covering index migration + integration test

Add `(roomId, lastSeenAt, u.account, u._id)`, drop the redundant `(roomId, lastSeenAt)`, and verify the lean query returns correct readers against real Mongo.

**Files:**
- Modify: `room-service/store_mongo.go:88-93` (index migration)
- Modify: `room-service/integration_test.go` (new test)

- [ ] **Step 1: Write the failing integration test**

In `room-service/integration_test.go`, add (the package already uses `testutil.MongoDB` and the `//go:build integration` tag):

```go
func TestMongoStore_ListReadReceipts_Lean(t *testing.T) {
	db := testutil.MongoDB(t, "readreceipt")
	store := NewMongoStore(db)
	require.NoError(t, store.EnsureIndexes(context.Background()))

	ctx := context.Background()
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	older := since.Add(-time.Hour)
	newer := since.Add(time.Hour)

	subs := db.Collection("subscriptions")
	_, err := subs.InsertMany(ctx, []any{
		bson.M{"_id": "s1", "roomId": "r1", "lastSeenAt": newer, "u": bson.M{"_id": "u-bob", "account": "bob"}},
		bson.M{"_id": "s2", "roomId": "r1", "lastSeenAt": newer, "u": bson.M{"_id": "u-sndr", "account": "sender"}},
		bson.M{"_id": "s3", "roomId": "r1", "lastSeenAt": older, "u": bson.M{"_id": "u-old", "account": "stale"}},
		bson.M{"_id": "s4", "roomId": "r2", "lastSeenAt": newer, "u": bson.M{"_id": "u-oth", "account": "other"}},
	})
	require.NoError(t, err)

	got, err := store.ListReadReceipts(ctx, "r1", since, "sender", 1000)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"bob"}, got) // sender excluded, stale below since, r2 excluded

	// The covering index exists and the redundant 2-field index is gone.
	names := indexNames(t, ctx, subs)
	assert.Contains(t, names, "roomId_1_lastSeenAt_1_u.account_1_u._id_1")
	assert.NotContains(t, names, "roomId_1_lastSeenAt_1")
}

func indexNames(t *testing.T, ctx context.Context, col *mongo.Collection) []string {
	t.Helper()
	cur, err := col.Indexes().List(ctx)
	require.NoError(t, err)
	var idx []struct {
		Name string `bson:"name"`
	}
	require.NoError(t, cur.All(ctx, &idx))
	names := make([]string, 0, len(idx))
	for _, i := range idx {
		names = append(names, i.Name)
	}
	return names
}
```

- [ ] **Step 2: Run the integration test to verify it fails**

Run: `make test-integration SERVICE=room-service`
Expected: FAIL — the covering index doesn't exist yet and `roomId_1_lastSeenAt_1` is still present.

- [ ] **Step 3: Implement the index migration**

In `room-service/store_mongo.go`, replace the `(roomId, lastSeenAt)` index block (lines 89-93) with the covering index plus a tolerant drop of the old one (`store_mongo.go` already imports `errors`):

```go
	// Covering index for the read-receipt query: serves roomId(eq) +
	// lastSeenAt(range), evaluates the u.account residual on index keys, and
	// covers the u.account projection — no document FETCH.
	if _, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "roomId", Value: 1},
			{Key: "lastSeenAt", Value: 1},
			{Key: "u.account", Value: 1},
			{Key: "u._id", Value: 1},
		},
	}); err != nil {
		return fmt.Errorf("ensure subscriptions (roomId,lastSeenAt,u.account,u._id) index: %w", err)
	}
	// Drop the now-redundant (roomId, lastSeenAt) index — its key is a strict
	// prefix of the covering index above (and still serves
	// MinSubscriptionLastSeenByRoomID via that prefix). Tolerate IndexNotFound
	// (code 27) so EnsureIndexes stays idempotent on fresh databases.
	if _, err := s.subscriptions.Indexes().DropOne(ctx, "roomId_1_lastSeenAt_1"); err != nil {
		var ce mongo.CommandError
		if !errors.As(err, &ce) || ce.Code != 27 {
			return fmt.Errorf("drop redundant subscriptions (roomId,lastSeenAt) index: %w", err)
		}
	}
```

- [ ] **Step 4: Run the integration test to verify it passes**

Run: `make test-integration SERVICE=room-service`
Expected: PASS — `ListReadReceipts` returns `["bob"]`; covering index present; old index dropped.

- [ ] **Step 5: Confirm covered-query behavior (manual, optional)**

The committed `tools/loadgen/explain-read-receipts.js` measures the covered query against real data. Coverage (no FETCH) is verified there rather than in the integration test to avoid coupling assertions to explain-plan shape across Mongo versions.

- [ ] **Step 6: Commit**

```bash
git add room-service/store_mongo.go room-service/integration_test.go
git commit -m "Add covering index for read-receipt query, drop redundant index"
```

---

## Final Verification

- [ ] **Full unit suite + lint:**

Run: `make test && make lint`
Expected: PASS.

- [ ] **Integration suite for the two touched services:**

Run: `make test-integration SERVICE=room-service && make test-integration SERVICE=broadcast-worker`
Expected: PASS.

- [ ] **SAST (blocking CI gate):**

Run: `make sast`
Expected: PASS (no medium+ findings introduced).

- [ ] **Confirm no client-API drift:** `docs/client-api.md` is unchanged because the read-receipt request/response schema, errors, and triggered events are identical.

---

## Self-Review Notes (author)

- **Spec coverage:** lean query (Task 4) ✓; cached resolution (Task 4) ✓; promote `CachedUserStore` to `pkg/usercache` (Tasks 1-2) ✓; covering index + drop old (Task 5) ✓; config `USER_CACHE_SIZE`/`USER_CACHE_TTL` (Task 3) ✓; schema/doc unchanged (Final Verification) ✓; TTL-only / no invalidation event (design non-goal, nothing to build) ✓.
- **Open decisions (resolved per approval):** drop the old index — done in Task 5; per-account cache keying — inherited unchanged from the promoted cache; `USER_CACHE_TTL` default 5m — Task 3.
- **Type consistency:** `UserResolver.FindUsersByAccounts` matches `userstore.UserStore`/`usercache.CachedUserStore` exactly; `ListReadReceipts` returns `[]string` consistently across `store.go`, `store_mongo.go`, the regenerated mock, and both call sites (handler + tests); `usercache.New` used in both `broadcast-worker` and `room-service`.
