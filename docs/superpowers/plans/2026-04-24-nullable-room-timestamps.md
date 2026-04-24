# Nullable Room Timestamps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change `model.Room.LastMsgAt` and `model.Room.LastMentionAllAt` from `time.Time` to `*time.Time` with `json:",omitempty"` and `bson:",omitempty"` so freshly created rooms no longer store zero-time values in Mongo, and downstream code can distinguish "no last message" (nil) from "there was a message at time X" (non-nil pointer).

**Architecture:** One struct in `pkg/model/room.go`, one production reader (`aggregateRoomInfo` in `room-service/handler.go`), one generic test helper (`roundTrip` in `pkg/model/model_test.go`) that needs a small rewrite so it works with pointer-field types. Write path `UpdateRoomOnNewMessage` in `broadcast-worker/store_mongo.go` uses raw `bson.M` and is untouched. Spec: `docs/superpowers/specs/2026-04-24-nullable-room-timestamps-design.md`.

**Tech Stack:** Go 1.25, MongoDB (`go.mongodb.org/mongo-driver/v2` bson encoding), `encoding/json`, `testify`, `go.uber.org/mock`, `testcontainers-go` (integration). `make` targets only — never raw `go` commands per CLAUDE.md §2.

**Branch:** Continue on `claude/nullable-room-timestamps-RQAqX` (already checked out, already has the RoomInfo nullable-timestamp work committed). Push after each task commit.

---

## File map

**Modify:**
- `pkg/model/room.go` — flip the two `time.Time` fields to `*time.Time`; add `,omitempty` to both tags (json + bson)
- `room-service/handler.go` — add nil guards in front of the existing `.IsZero()` checks in `aggregateRoomInfo`
- `pkg/model/model_test.go` — rewrite the generic `roundTrip` helper; fix `TestRoomJSON` to use pointer literals; add `TestRoomJSON_NilTimestampsOmitted` (Task 2)
- `broadcast-worker/integration_test.go` — three `assert.WithinDuration` sites need `require.NotNil` + deref
- `room-service/handler_test.go` — one `LastMsgAt: now` seed becomes `LastMsgAt: &now`
- `room-service/integration_test.go` — six seed literals become pointer form; one `.IsZero()` read gains a nil check

**Create:** none.

---

## Why Task 1 is atomic (no intermediate commits)

Flipping `Room.LastMsgAt` from `time.Time` to `*time.Time` is a breaking type change at the Go compile level. Every producer, consumer, and test that touches the field stops compiling until *all* sites match the new type. There is no ordering of the sub-changes that leaves the tree green partway through. The commit must therefore contain the full set of matching changes. The steps below are sized as 2–5 minute units for review and undo purposes, but only the final commit + push step creates a publishable state.

---

## Task 1: Flip `Room` timestamps to `*time.Time` (atomic commit)

**Files:**
- Modify: `pkg/model/room.go:20,22`
- Modify: `room-service/handler.go:664-670`
- Modify: `pkg/model/model_test.go:31-42` (`TestRoomJSON`), `pkg/model/model_test.go:1280-1293` (`roundTrip` helper)
- Modify: `broadcast-worker/integration_test.go:128,162,270`
- Modify: `room-service/handler_test.go:1516`
- Modify: `room-service/integration_test.go:928-932,958,988,990`

- [ ] **Step 1: Flip the struct fields in `pkg/model/room.go`**

Replace lines 20 and 22. Current:

```go
	LastMsgAt        time.Time `json:"lastMsgAt" bson:"lastMsgAt"`
	LastMsgID        string    `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt time.Time `json:"lastMentionAllAt" bson:"lastMentionAllAt"`
```

New:

```go
	LastMsgAt        *time.Time `json:"lastMsgAt,omitempty" bson:"lastMsgAt,omitempty"`
	LastMsgID        string     `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt *time.Time `json:"lastMentionAllAt,omitempty" bson:"lastMentionAllAt,omitempty"`
```

Notes:
- The middle field `LastMsgID` is unchanged in content — its column positions shift because surrounding field types widened, re-indent to keep columns aligned.
- Both `json:",omitempty"` and `bson:",omitempty"` are required. Without `bson:",omitempty"`, nil pointers write explicit BSON `null`, not absent — which defeats the purpose of the change.

- [ ] **Step 2: Add nil guards in `room-service/handler.go` `aggregateRoomInfo`**

Replace lines 664–670 (currently reads the RoomInfo pointer logic from the prior nullable-RoomInfo change). Current:

```go
		if !r.LastMsgAt.IsZero() {
			ms := r.LastMsgAt.UTC().UnixMilli()
			entry.LastMsgAt = &ms
		}
		if !r.LastMentionAllAt.IsZero() {
			ms := r.LastMentionAllAt.UTC().UnixMilli()
			entry.LastMentionAllAt = &ms
		}
```

New:

```go
		if r.LastMsgAt != nil && !r.LastMsgAt.IsZero() {
			ms := r.LastMsgAt.UTC().UnixMilli()
			entry.LastMsgAt = &ms
		}
		if r.LastMentionAllAt != nil && !r.LastMentionAllAt.IsZero() {
			ms := r.LastMentionAllAt.UTC().UnixMilli()
			entry.LastMentionAllAt = &ms
		}
```

Both checks are needed. The nil check handles post-change rooms created without the field; the `.IsZero()` check handles legacy Mongo documents that stored `ISODate("0001-01-01T00:00:00Z")` before this change landed.

- [ ] **Step 3: Rewrite the `roundTrip` helper in `pkg/model/model_test.go`**

Find the function at lines 1280–1293 (end of the file). Current:

```go
// roundTrip marshals src to JSON, unmarshals into dst, and compares.
func roundTrip[T comparable](t *testing.T, src *T, dst *T) {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *dst != *src {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
	}
}
```

New:

```go
// roundTrip marshals src to JSON, unmarshals into dst, and compares.
func roundTrip[T any](t *testing.T, src *T, dst *T) {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*src, *dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
	}
}
```

Changes: constraint `comparable` → `any`; comparison `*dst != *src` → `!reflect.DeepEqual(*src, *dst)`. The `reflect` package is already imported at the top of the file (used by `TestRoomEvent`, among others). No other caller of `roundTrip` needs changes — `reflect.DeepEqual` is strictly more permissive than `==` for their value types.

- [ ] **Step 4: Fix `TestRoomJSON` literals in `pkg/model/model_test.go`**

Find lines 31–42. Current:

```go
func TestRoomJSON(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		LastMsgAt:        time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LastMsgID:        "m1",
		LastMentionAllAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}
```

New:

```go
func TestRoomJSON(t *testing.T) {
	lastMsg := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	lastMention := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		LastMsgAt:        &lastMsg,
		LastMsgID:        "m1",
		LastMentionAllAt: &lastMention,
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}
```

Two named locals (`lastMsg`, `lastMention`) are used instead of inline `&time.Date(...)` because Go does not allow taking the address of a function call result.

- [ ] **Step 5: Fix `broadcast-worker/integration_test.go` — three assertion sites**

Find the three sites (`grep -n "WithinDuration.*room.LastMsgAt\|WithinDuration.*room.LastMentionAllAt" broadcast-worker/integration_test.go` confirms lines ~128, ~162, ~270). Each site currently reads:

```go
	assert.WithinDuration(t, msgTime, room.LastMsgAt, time.Millisecond)
```

or

```go
	assert.WithinDuration(t, msgTime, room.LastMentionAllAt, time.Millisecond)
```

Transform each site identically by prepending a non-nil check and dereferencing:

```go
	require.NotNil(t, room.LastMsgAt)
	assert.WithinDuration(t, msgTime, *room.LastMsgAt, time.Millisecond)
```

or (for the LastMentionAllAt case):

```go
	require.NotNil(t, room.LastMentionAllAt)
	assert.WithinDuration(t, msgTime, *room.LastMentionAllAt, time.Millisecond)
```

Confirm `require` is already imported (it is — the file uses `require.NoError` throughout).

- [ ] **Step 6: Fix `room-service/handler_test.go` seed literal**

Find the `"LastMsgAt set → correct millis"` case (around line 1511). The inner `model.Room` literal currently reads:

```go
			s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r1"}).Return([]model.Room{
				{ID: "r1", Name: "general", SiteID: "site-a", LastMsgAt: now},
			}, nil)
```

Change the seed value:

```go
			s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r1"}).Return([]model.Room{
				{ID: "r1", Name: "general", SiteID: "site-a", LastMsgAt: &now},
			}, nil)
```

The `now` variable is a local `time.Time` already in scope (declared near line 1381 of the test). Taking `&now` is valid Go. The existing assertion (`require.NotNil(t, resp.Rooms[0].LastMsgAt); assert.Equal(..., *resp.Rooms[0].LastMsgAt)`) is already pointer-aware and does not need changes.

The other two cases in this file (`"missing room → Found=false, LastMsgAt=nil"` and `"LastMsgAt zero-time in Mongo → nil in response"`) do not set `LastMsgAt` in their seeded `model.Room` literals — the field defaults to the zero value of its type, which is now `nil` instead of `time.Time{}`. No change needed there.

- [ ] **Step 7: Fix `room-service/integration_test.go` seed literals (first block — `TestMongoStore_ListRoomsByIDs`)**

Find lines ~926–933. Current:

```go
	now := time.Now().UTC().Truncate(time.Millisecond)
	seed := []model.Room{
		{ID: "r1", Name: "one", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now},
		{ID: "r2", Name: "two", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(1 * time.Second)},
		{ID: "r3", Name: "three", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(2 * time.Second)},
		{ID: "r4", Name: "four", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(3 * time.Second)},
		{ID: "r5", Name: "five", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(4 * time.Second)},
	}
```

New — hoist each distinct time to a local so `&` is valid:

```go
	now := time.Now().UTC().Truncate(time.Millisecond)
	t1 := now
	t2 := now.Add(1 * time.Second)
	t3 := now.Add(2 * time.Second)
	t4 := now.Add(3 * time.Second)
	t5 := now.Add(4 * time.Second)
	seed := []model.Room{
		{ID: "r1", Name: "one", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &t1},
		{ID: "r2", Name: "two", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &t2},
		{ID: "r3", Name: "three", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &t3},
		{ID: "r4", Name: "four", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &t4},
		{ID: "r5", Name: "five", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &t5},
	}
```

Do not try to write `LastMsgAt: &now.Add(1 * time.Second)` — taking the address of a method-call result is a Go compile error.

- [ ] **Step 8: Fix `room-service/integration_test.go` `.IsZero()` read (same test)**

Find line ~958 inside the same `TestMongoStore_ListRoomsByIDs`. Current:

```go
			if r.LastMsgAt.IsZero() {
				t.Errorf("room %q: LastMsgAt is zero", id)
			}
```

New — guard against nil first:

```go
			if r.LastMsgAt == nil || r.LastMsgAt.IsZero() {
				t.Errorf("room %q: LastMsgAt is zero or nil", id)
			}
```

The test intent is "these seeded rooms should have a real non-zero LastMsgAt" — a nil pointer is a failure for the same reason a zero time was, so a unified error path is correct.

- [ ] **Step 9: Fix `room-service/integration_test.go` seed literals (second block — `TestRoomsInfoBatchRPC`)**

Find lines ~986–991. Current:

```go
	lastMsg := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	rooms := []model.Room{
		{ID: "r1", Name: "room-1", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: lastMsg},
		{ID: "r2", Name: "room-2", Type: model.RoomTypeGroup, SiteID: "site-a"},
		{ID: "r3", Name: "room-3", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: lastMsg.Add(-time.Hour)},
	}
```

New — hoist pointers:

```go
	lastMsg := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	earlier := lastMsg.Add(-time.Hour)
	rooms := []model.Room{
		{ID: "r1", Name: "room-1", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &lastMsg},
		{ID: "r2", Name: "room-2", Type: model.RoomTypeGroup, SiteID: "site-a"},
		{ID: "r3", Name: "room-3", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: &earlier},
	}
```

`r2` deliberately leaves `LastMsgAt` unset — it defaults to nil, which is the condition the test's `assert.Nil(t, resp.Rooms[1].LastMsgAt)` (line ~1038) already expects. The later assertion at line ~1030 (`require.NotNil(t, resp.Rooms[0].LastMsgAt); assert.Equal(t, lastMsg.UnixMilli(), *resp.Rooms[0].LastMsgAt)`) continues to pass because `r1`'s pointer now resolves to a non-nil time with value `lastMsg`.

- [ ] **Step 10: Run the unit test suite**

Run: `make test SERVICE=room-service && make test`

Expected: PASS on both. `make test` without `SERVICE=` covers `pkg/model` (`TestRoomJSON` via the rewritten `roundTrip` helper) and every other package.

If anything fails:
- `pkg/model` fail → most likely typo in the struct tag or forgot to update `TestRoomJSON`. Re-check Steps 1, 3, 4.
- `room-service` fail → re-check Steps 2, 6, 7, 8, 9.
- Other service fails → should not happen; no other service reads these fields. If it does, stop and read the failure.

Do not proceed until both runs are green.

- [ ] **Step 11: Run the room-service integration tests**

Run: `make test-integration SERVICE=room-service`

Expected: PASS. Requires Docker running locally (testcontainers pattern per CLAUDE.md §4). If the local environment has no Docker, note that in the final commit message and continue — Task 1's compile changes will have been exercised by `go vet` / `go build` via `make lint` + `make test` already, and CI will run integration.

- [ ] **Step 12: Run the broadcast-worker integration tests**

Run: `make test-integration SERVICE=broadcast-worker`

Expected: PASS. Same Docker caveat applies.

- [ ] **Step 13: Run the linter**

Run: `make lint`

Expected: `0 issues.`. The pre-commit hook runs both lint and tests per CLAUDE.md §5, so a lint failure here will block the commit in Step 14.

- [ ] **Step 14: Commit**

```bash
git add pkg/model/room.go room-service/handler.go pkg/model/model_test.go broadcast-worker/integration_test.go room-service/handler_test.go room-service/integration_test.go
git commit -m "$(cat <<'EOF'
refactor(model): make Room timestamps nullable

Change LastMsgAt and LastMentionAllAt on model.Room from time.Time to
*time.Time with both json and bson omitempty so freshly created rooms
no longer store a zero-time value in Mongo. Update aggregateRoomInfo
in room-service to add a nil guard in front of the existing IsZero
check — needed to handle both nil (new rooms) and zero-time
(pre-existing Mongo data).

Rewrite the generic roundTrip test helper in pkg/model/model_test.go
to use reflect.DeepEqual (dropping the comparable constraint) so it
supports structs with pointer fields. No existing caller regresses
since DeepEqual is strictly more permissive than == for value types.

Update all test seed literals and assertions across pkg/model,
broadcast-worker, and room-service to use pointer form.

https://claude.ai/code/session_01HsMbFL397VGMbojKDHZcZm
EOF
)"
```

- [ ] **Step 15: Push**

```bash
git push -u origin claude/nullable-room-timestamps-RQAqX
```

Retry on network failure with exponential backoff (2s, 4s, 8s, 16s), up to 4 attempts.

---
