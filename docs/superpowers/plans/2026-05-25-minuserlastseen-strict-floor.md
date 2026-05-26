# `minUserLastSeenAt` Strict "Everyone Read" Floor — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change `MinSubscriptionLastSeenByRoomID` so the room read-floor (`rooms.minUserLastSeenAt`) is set only when **every** subscription in the room has been read, and is `nil` when **any** member (bots included) has not yet read.

**Architecture:** A single MongoDB aggregation over the `subscriptions` collection computes, per room, the total subscription count, the count of subscriptions with a usable `lastSeenAt` (`> zero`), and the MIN of `lastSeenAt`. The Go method returns `&min` only when `total > 0 && readCount == total`, else `nil`. The caller in `handler.go` is unchanged — a `nil` result still `$unset`s the field. Docs and the integration test are updated to the new semantics.

**Tech Stack:** Go 1.25, `go.mongodb.org/mongo-driver/v2` aggregation pipelines, `testcontainers-go` (MongoDB integration tests), `stretchr/testify`.

---

## Background & Context (read before starting)

The method lives in `room-service`. Current (OLD) behavior: it MINs only over subscriptions that already have `lastSeenAt > zero`, so a never-read member is *excluded* and does not affect the floor. NEW behavior: a never-read member must force the floor to `nil` ("not everyone has read").

Key facts an implementer needs:
- **Schema:** each `subscriptions` document has `roomId string` and an optional `lastSeenAt` (BSON Date). A "never read" subscription either lacks the field, has it `null`, or carries the BSON zero-date (legacy). All three must count as "not read".
- **`$gt` against zero:** the existing code uses `lastSeenAt: {$gt: zeroTime}` as the definition of "has read". In aggregation, a missing field path resolves to null, and BSON sorts `null` *before* dates, so `$gt: ["$lastSeenAt", zeroTime]` is `false` for missing/null and `false` for the zero-date itself. We reuse exactly this predicate so the new code's notion of "read" matches the old one.
- **`$min` ignores missing/null** values. When `readCount == total`, every doc has `lastSeenAt > zero`, so `min` is the true minimum. When `readCount < total` we return `nil` and never read `min`, so it doesn't matter that a zero-date could sneak into `$min` in that case.
- **No `isBot` filtering.** Bots count as ordinary subscriptions. A bot never reads, so a botDM room (one human + one bot) resolves to `nil`. This is intended.
- **Caller is unchanged.** `room-service/handler.go:1103-1109` calls `MinSubscriptionLastSeenByRoomID` then passes the result straight to `UpdateRoomMinUserLastSeenAt`, which `$unset`s on `nil` and `$set`s otherwise. Do **not** change the handler.
- **Handler unit tests are unaffected.** `room-service/handler_test.go` mocks the store and only asserts the wiring (Min… is called, its result is forwarded to Update…). The method signature is unchanged, so these tests stay green and must not be edited.

### Known limitation to document (do NOT fix in this plan)
Adding a member (in `room-worker`) does **not** recompute the floor, and the mark-read handler early-returns (skips recompute) when the caller is already caught up (`room-service/handler.go:1099`). Consequence under the new strict rule: a newly-invited, never-read member will not flip an existing non-`nil` floor to `nil` until some recompute is triggered (e.g. that member reads, or another member reads while content exists). The user-triggered mark-read path itself stays correct. This is an accepted follow-up, recorded in the docs (Task 3), not fixed here.

---

## File Structure

- **Modify** `room-service/store_mongo.go` (method `MinSubscriptionLastSeenByRoomID`, ~lines 835-876) — replace the aggregation + decode logic with the strict-floor version, and rewrite its doc comment.
- **Modify** `room-service/store.go` (interface doc comment, ~lines 93-99) — rewrite to the new semantics.
- **Modify** `room-service/integration_test.go` (`TestMongoStore_MinSubscriptionLastSeenByRoomID_Integration`, ~lines 1646-1692) — update fixtures/assertions to the new semantics (all-read ⇒ min; any-unread ⇒ nil; none-read ⇒ nil; empty ⇒ nil).
- **Modify** `docs/client-api.md` (Mark Read RPC bullets ~lines 651-652; history-response field row ~line 958) — rewrite to the new semantics and add the botDM note + the documented limitation.

No new files. No new dependencies.

---

## Task 1: Update the integration test to the new strict semantics (Red)

**Files:**
- Test: `room-service/integration_test.go` — replace the body of `TestMongoStore_MinSubscriptionLastSeenByRoomID_Integration` (currently ~lines 1646-1692).

- [ ] **Step 1: Rewrite the test to assert the strict floor**

Replace the entire existing `TestMongoStore_MinSubscriptionLastSeenByRoomID_Integration` function (from its `func` line through its closing `}`) with:

```go
func TestMongoStore_MinSubscriptionLastSeenByRoomID_Integration(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := NewMongoStore(db)

	earliest := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	mid := earliest.Add(15 * time.Minute)
	latest := earliest.Add(45 * time.Minute)

	// Room "all-read": every subscription has a usable lastSeenAt, so the floor
	// is the MIN across all of them.
	mustInsertSub(t, db, &model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "all-read", JoinedAt: earliest, LastSeenAt: &mid,
	})
	mustInsertSub(t, db, &model.Subscription{
		ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "all-read", JoinedAt: earliest, LastSeenAt: &latest,
	})

	got, err := store.MinSubscriptionLastSeenByRoomID(ctx, "all-read")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.WithinDuration(t, mid, *got, time.Second)

	// Room "one-unread": two members have read, one was invited but has never
	// opened the room. Under the strict floor a single never-read member forces
	// nil — "not everyone has read".
	mustInsertSub(t, db, &model.Subscription{
		ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"},
		RoomID: "one-unread", JoinedAt: earliest, LastSeenAt: &mid,
	})
	mustInsertSub(t, db, &model.Subscription{
		ID: "s4", User: model.SubscriptionUser{ID: "u4", Account: "dave"},
		RoomID: "one-unread", JoinedAt: earliest, LastSeenAt: &latest,
	})
	// Never-read sub: joined but never opened the room.
	mustInsertSub(t, db, &model.Subscription{
		ID: "s5", User: model.SubscriptionUser{ID: "u5", Account: "erin"},
		RoomID: "one-unread", JoinedAt: earliest,
	})

	got, err = store.MinSubscriptionLastSeenByRoomID(ctx, "one-unread")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Room "none-read": a single sub that has never been read → nil.
	mustInsertSub(t, db, &model.Subscription{
		ID: "s6", User: model.SubscriptionUser{ID: "u6", Account: "frank"},
		RoomID: "none-read", JoinedAt: earliest,
	})
	got, err = store.MinSubscriptionLastSeenByRoomID(ctx, "none-read")
	require.NoError(t, err)
	assert.Nil(t, got)

	// Room with no subscriptions at all → nil.
	got, err = store.MinSubscriptionLastSeenByRoomID(ctx, "empty")
	require.NoError(t, err)
	assert.Nil(t, got)
}
```

- [ ] **Step 2: Run the test and confirm it FAILS against the current implementation**

Run: `make test-integration SERVICE=room-service`

Expected: the new `one-unread` assertion FAILS — the OLD implementation excludes the never-read sub and returns `mid` (non-nil) for `one-unread`, but the test asserts `Nil`. (`all-read`, `none-read`, and `empty` already pass under the old code; `one-unread` is the behavior change.)

If Docker / testcontainers is unavailable in this environment, you cannot run this step here. In that case: state explicitly that the integration test could not be executed locally, leave the test in place as the specification of intended behavior, and rely on CI (`make test-integration`) to verify. Do NOT claim the test passed without running it.

- [ ] **Step 3: Commit the failing test**

```bash
git add room-service/integration_test.go
git commit -m "test: assert strict everyone-read floor for MinSubscriptionLastSeenByRoomID"
```

---

## Task 2: Implement the strict-floor aggregation (Green)

**Files:**
- Modify: `room-service/store_mongo.go` — method `MinSubscriptionLastSeenByRoomID` (~lines 835-876).
- Modify: `room-service/store.go` — interface doc comment (~lines 93-99).

- [ ] **Step 1: Replace the implementation and its doc comment**

In `room-service/store_mongo.go`, replace the doc comment and full body of `MinSubscriptionLastSeenByRoomID` (from the `// MinSubscriptionLastSeenByRoomID returns…` comment through the method's closing `}`) with:

```go
// MinSubscriptionLastSeenByRoomID returns the room's strict read floor: the
// minimum lastSeenAt across ALL of the room's subscriptions, but only when
// EVERY subscription has a usable lastSeenAt (> zero). If any subscription has
// no usable lastSeenAt — missing, null, or the BSON zero date, i.e. a member
// who was invited but has never opened the room — it returns nil, meaning "not
// everyone has read yet". It also returns nil for a room with no subscriptions.
// Bots are ordinary subscriptions and are counted: a botDM room (the bot never
// reads) therefore always resolves to nil. The caller $unsets
// rooms.minUserLastSeenAt on a nil result.
func (s *MongoStore) MinSubscriptionLastSeenByRoomID(ctx context.Context, roomID string) (*time.Time, error) {
	zeroTime := time.Time{}
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"roomId": roomID}}},
		// total counts every subscription in the room. readCount counts those
		// with a usable lastSeenAt — $gt:zeroTime is false for missing/null
		// (BSON null sorts before dates) and for the legacy zero date, so it
		// matches our definition of "has read". min is only consumed when
		// readCount == total, where every doc has lastSeenAt > zero, so $min
		// (which ignores missing/null) yields the true minimum.
		{{Key: "$group", Value: bson.M{
			"_id":   nil,
			"total": bson.M{"$sum": 1},
			"readCount": bson.M{"$sum": bson.M{"$cond": bson.A{
				bson.M{"$gt": bson.A{"$lastSeenAt", zeroTime}}, 1, 0,
			}}},
			"min": bson.M{"$min": "$lastSeenAt"},
		}}},
	}
	cursor, err := s.subscriptions.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate min lastSeenAt for room %q: %w", roomID, err)
	}
	defer cursor.Close(ctx)
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("iterate min lastSeenAt for room %q: %w", roomID, err)
		}
		return nil, nil // no subscriptions in the room
	}
	var result struct {
		Total     int       `bson:"total"`
		ReadCount int       `bson:"readCount"`
		Min       time.Time `bson:"min"`
	}
	if err := cursor.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode min lastSeenAt for room %q: %w", roomID, err)
	}
	// Strict floor: only return the MIN when every subscription has read.
	if result.Total == 0 || result.ReadCount != result.Total {
		return nil, nil
	}
	return &result.Min, nil
}
```

- [ ] **Step 2: Update the interface doc comment in `store.go`**

In `room-service/store.go`, replace the comment block above `MinSubscriptionLastSeenByRoomID(ctx context.Context, roomID string) (*time.Time, error)` (currently the lines beginning `// MinSubscriptionLastSeenByRoomID returns the minimum lastSeenAt across` through `// lastSeenAt.`) with:

```go
	// MinSubscriptionLastSeenByRoomID returns the room's strict read floor:
	// the MIN(lastSeenAt) across ALL of the room's subscriptions, but only
	// when every subscription has a usable lastSeenAt (> zero). Returns nil if
	// any subscription has never been read (missing/null/zero lastSeenAt) or if
	// the room has no subscriptions. Bots are counted, so a botDM room always
	// resolves to nil.
```

- [ ] **Step 3: Verify it compiles, lints, and the unit suite is green**

Run: `make lint && make test SERVICE=room-service`

Expected: no lint errors; unit tests PASS. (`handler_test.go` mocks the store, so its recompute-wiring tests are unaffected by this logic change.)

- [ ] **Step 4: Run the integration test and confirm it now PASSES**

Run: `make test-integration SERVICE=room-service`

Expected: `TestMongoStore_MinSubscriptionLastSeenByRoomID_Integration` PASSES — `all-read` ⇒ `mid`, `one-unread` ⇒ `nil`, `none-read` ⇒ `nil`, `empty` ⇒ `nil`.

If Docker / testcontainers is unavailable here, say so explicitly and defer this verification to CI; do not claim a pass you did not observe.

- [ ] **Step 5: Commit the implementation**

```bash
git add room-service/store_mongo.go room-service/store.go
git commit -m "feat: make room read-floor require every member to have read"
```

---

## Task 3: Update `docs/client-api.md` to the new semantics

**Files:**
- Modify: `docs/client-api.md` — Mark Read RPC bullets (~lines 651-652) and the history-response field row (~line 958).

- [ ] **Step 1: Rewrite the "JoinedAt fallback" and "Room-floor recompute" bullets**

In `docs/client-api.md`, replace the bullet that currently begins `- **No \`JoinedAt\` fallback for the early-return:**` and the bullet that begins `- **Room-floor recompute (\`Room.MinUserLastSeenAt\`):**` (the two consecutive bullets, ~lines 651-652) with:

```markdown
- **No `JoinedAt` fallback for the early-return:** if `subscription.lastSeenAt` is null (the user was invited but has never opened the room), the handler does **not** treat `joinedAt` as a synthetic read position — being invited isn't reading. The room-floor recompute runs in this case so a member who has just read for the first time is reflected in the floor.
- **Room-floor recompute (`Room.MinUserLastSeenAt`):** the room's read floor (surfaced as `minUserLastSeenAt` in history responses) is a **strict "everyone has read" marker**: `MIN(lastSeenAt)` across **all** of the room's subscriptions, set **only when every subscription has a usable `lastSeenAt`**. If **any** member has never read the room (no/zero `lastSeenAt` — e.g. invited but never opened), the floor is `$unset` (null). Bots are counted like any other member, so a **botDM room — where the bot never reads — always has a null floor**. Reading a room can advance the floor (or, if this was the last unread member, raise it from null to a value).
- **Recompute trigger & a known gap:** the floor is recomputed only on this Mark Read path, and only when the caller was not already past `room.lastMsgAt` (the early-return above). Adding a member does not itself recompute the floor, so a newly-invited, never-read member will not flip an existing non-null floor to null until the next recompute is triggered (e.g. that member reads, or another member reads while the room has content).
```

- [ ] **Step 2: Rewrite the `minUserLastSeenAt` field description in the history response table**

In `docs/client-api.md`, replace the table row that currently begins `| \`minUserLastSeenAt\` | number |` (~line 958) with:

```markdown
| `minUserLastSeenAt` | number | Optional. UTC milliseconds since Unix epoch. The room's **strict read floor** — `MIN(lastSeenAt)` across all subscribers, present **only when every member has read** the room. Absent (null) when any member has not read yet (so botDM rooms, where the bot never reads, never set it), when the most recent read is already past `room.lastMsgAt` (recompute is skipped), or when the value cannot be retrieved (best-effort; messages still load). See the Message Read RPC for how this floor is recomputed. |
```

- [ ] **Step 3: Sanity-check the rendered Markdown**

Run: `grep -n "strict" docs/client-api.md`

Expected: matches appear in both the Mark Read RPC section and the history-response field row, confirming both edits landed.

- [ ] **Step 4: Commit the docs**

```bash
git add docs/client-api.md
git commit -m "docs: document strict everyone-read semantics for minUserLastSeenAt"
```

---

## Task 4: Final verification & push

- [ ] **Step 1: Full local gate**

Run: `make lint && make test SERVICE=room-service`

Expected: lint clean, unit tests PASS.

- [ ] **Step 2: Integration gate (if Docker available)**

Run: `make test-integration SERVICE=room-service`

Expected: PASS. If Docker is unavailable locally, state that explicitly and rely on CI.

- [ ] **Step 3: Push the branch**

```bash
git push -u origin claude/client-api-doc-review-Rz70X
```

Do **not** open a pull request unless the user explicitly asks for one.

---

## Self-Review Notes (author checklist — already applied)

- **Spec coverage:** strict floor implemented (Task 2), bots counted / botDM ⇒ nil (Task 2 + Task 3 docs), nil on any-unread and on zero-subs (Task 2, asserted in Task 1), caller unchanged (documented, not edited), docs updated (Task 3), integration test updated (Task 1). The add-member gap is documented as an explicit out-of-scope limitation (Task 3, Step 1).
- **Placeholder scan:** every code/edit step contains the literal code or exact command — no TBD/TODO/"handle edge cases".
- **Type consistency:** the decode struct fields (`Total`, `ReadCount`, `Min`) match the aggregation output keys (`total`, `readCount`, `min`); the method signature `MinSubscriptionLastSeenByRoomID(ctx, roomID) (*time.Time, error)` is unchanged across `store.go`, `store_mongo.go`, the mock, the handler, and both test files, so no mock regeneration is needed.
