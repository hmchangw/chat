# Nullable RoomInfo Timestamps Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Change `model.RoomInfo.LastMsgAt` and `model.RoomInfo.LastMentionAllAt` from `int64` to `*int64` so the rooms-info batch RPC can unambiguously represent "no last message" (nil pointer → field absent from JSON) versus "present at epoch zero" (pointer to `0` → emitted).

**Architecture:** Single struct in `pkg/model/room.go`, single producer in `room-service/handler.go` (`aggregateRoomInfo`). Wire compatibility is preserved — both `int64` with `omitempty` and `*int64` with `omitempty` omit zero/nil and emit a bare JSON number otherwise. Spec lives at `docs/superpowers/specs/2026-04-23-nullable-room-info-timestamps-design.md`.

**Tech Stack:** Go 1.25, `encoding/json`, `testify`, `go.uber.org/mock`, `testcontainers-go` (integration). `make` targets only — never raw `go` commands per CLAUDE.md §2.

**Branch:** Work on `claude/nullable-room-timestamps-RQAqX` (already checked out). Push after each commit.

---

## File map

**Modify:**
- `pkg/model/room.go` — flip the two struct fields to `*int64`
- `room-service/handler.go` — update `aggregateRoomInfo` to assign pointers
- `pkg/model/model_test.go` — fix `TestRoomInfoJSON` happy path + `TestRoomsInfoBatchResponseJSON` for the new type; add a new subtest exercising nullable semantics
- `room-service/handler_test.go` — fix the `missing room` and `LastMsgAt set` cases; add a new case for "zero-time in Mongo → nil in response"
- `room-service/integration_test.go` — fix the three pointer/nil assertions in `TestRoomsInfoBatchRPC`

**Create:** none.

---

## Task 1: Flip the type and update all compile-affected code

This change is atomic at the Go compile level — flipping the struct field forces matching updates to the producer and every test that reads/writes these fields. The commit leaves the existing test semantics intact: places that asserted "zero int64" now assert "nil pointer", and places that compared to `UnixMilli()` now dereference through `require.NotNil`. No new behavior is added yet.

**Files:**
- Modify: `pkg/model/room.go:52-53`
- Modify: `room-service/handler.go:664-669`
- Modify: `pkg/model/model_test.go:1160-1161`, `pkg/model/model_test.go:1228`
- Modify: `room-service/handler_test.go:1433-1446`, `room-service/handler_test.go:1510-1525`
- Modify: `room-service/integration_test.go:1029`, `room-service/integration_test.go:1037`, `room-service/integration_test.go:1048`

- [ ] **Step 1: Update the struct in `pkg/model/room.go`**

Replace lines 52–53:

```go
	LastMsgAt        int64   `json:"lastMsgAt,omitempty"`
	LastMentionAllAt int64   `json:"lastMentionAllAt,omitempty"`
```

with:

```go
	LastMsgAt        *int64  `json:"lastMsgAt,omitempty"`
	LastMentionAllAt *int64  `json:"lastMentionAllAt,omitempty"`
```

(Keep the struct tags exactly as shown — `omitempty` is essential so nil pointers are absent from JSON.)

- [ ] **Step 2: Update the producer in `room-service/handler.go`**

Replace lines 664–669:

```go
		if !r.LastMsgAt.IsZero() {
			entry.LastMsgAt = r.LastMsgAt.UTC().UnixMilli()
		}
		if !r.LastMentionAllAt.IsZero() {
			entry.LastMentionAllAt = r.LastMentionAllAt.UTC().UnixMilli()
		}
```

with:

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

Note: declare a fresh `ms` in each `if` block so each pointer takes the address of its own local (do NOT reuse a single outer variable — that would alias both pointers to the same memory).

- [ ] **Step 3: Fix the `TestRoomInfoJSON` happy-path literals in `pkg/model/model_test.go`**

Find the block around lines 1152–1172 (the `"happy path with all fields"` subtest). Replace the current body:

```go
	t.Run("happy path with all fields", func(t *testing.T) {
		pk := "dGVzdC1wcml2YXRlLWtleS1iYXNlNjQ="
		kv := 7
		src := model.RoomInfo{
			RoomID:           "r1",
			Found:            true,
			SiteID:           "site-a",
			Name:             "general",
			LastMsgAt:        1735689600000,
			LastMentionAllAt: 1735693200000,
			PrivateKey:       &pk,
			KeyVersion:       &kv,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.RoomInfo
		require.NoError(t, json.Unmarshal(data, &dst))
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})
```

with:

```go
	t.Run("happy path with all fields", func(t *testing.T) {
		pk := "dGVzdC1wcml2YXRlLWtleS1iYXNlNjQ="
		kv := 7
		lastMsg := int64(1735689600000)
		lastMention := int64(1735693200000)
		src := model.RoomInfo{
			RoomID:           "r1",
			Found:            true,
			SiteID:           "site-a",
			Name:             "general",
			LastMsgAt:        &lastMsg,
			LastMentionAllAt: &lastMention,
			PrivateKey:       &pk,
			KeyVersion:       &kv,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.RoomInfo
		require.NoError(t, json.Unmarshal(data, &dst))
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})
```

`reflect.DeepEqual` correctly compares pointer targets for value equality, so the round-trip assertion still holds.

- [ ] **Step 4: Fix the `TestRoomsInfoBatchResponseJSON` literal in `pkg/model/model_test.go`**

Find the block around lines 1218–1245. Replace:

```go
func TestRoomsInfoBatchResponseJSON(t *testing.T) {
	pk := "dGVzdC1rZXk="
	kv := 3
	src := model.RoomsInfoBatchResponse{
		Rooms: []model.RoomInfo{
			{
				RoomID:     "r1",
				Found:      true,
				SiteID:     "site-a",
				Name:       "general",
				LastMsgAt:  1735689600000,
				PrivateKey: &pk,
				KeyVersion: &kv,
			},
			{
				RoomID: "r2",
				Found:  false,
			},
		},
	}
```

with:

```go
func TestRoomsInfoBatchResponseJSON(t *testing.T) {
	pk := "dGVzdC1rZXk="
	kv := 3
	lastMsg := int64(1735689600000)
	src := model.RoomsInfoBatchResponse{
		Rooms: []model.RoomInfo{
			{
				RoomID:     "r1",
				Found:      true,
				SiteID:     "site-a",
				Name:       "general",
				LastMsgAt:  &lastMsg,
				PrivateKey: &pk,
				KeyVersion: &kv,
			},
			{
				RoomID: "r2",
				Found:  false,
			},
		},
	}
```

- [ ] **Step 5: Fix the "missing room" assertion in `room-service/handler_test.go`**

Find the block around lines 1432–1447. Replace:

```go
		{
			name:    "missing room — Found=false, LastMsgAt=0",
			payload: mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r-missing"}}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r-missing"}).Return([]model.Room{}, nil)
			},
			setupKeys: func(k *MockRoomKeyStore) {
				k.EXPECT().GetMany(gomock.Any(), []string{"r-missing"}).Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp model.RoomsInfoBatchResponse) {
				require.Len(t, resp.Rooms, 1)
				assert.Equal(t, "r-missing", resp.Rooms[0].RoomID)
				assert.False(t, resp.Rooms[0].Found)
				assert.Equal(t, int64(0), resp.Rooms[0].LastMsgAt)
			},
		},
```

with:

```go
		{
			name:    "missing room → Found=false, LastMsgAt=nil",
			payload: mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r-missing"}}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r-missing"}).Return([]model.Room{}, nil)
			},
			setupKeys: func(k *MockRoomKeyStore) {
				k.EXPECT().GetMany(gomock.Any(), []string{"r-missing"}).Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp model.RoomsInfoBatchResponse) {
				require.Len(t, resp.Rooms, 1)
				assert.Equal(t, "r-missing", resp.Rooms[0].RoomID)
				assert.False(t, resp.Rooms[0].Found)
				assert.Nil(t, resp.Rooms[0].LastMsgAt)
				assert.Nil(t, resp.Rooms[0].LastMentionAllAt)
			},
		},
```

- [ ] **Step 6: Fix the "LastMsgAt set" assertion in `room-service/handler_test.go`**

Find the block around lines 1510–1525. Replace:

```go
		{
			name:    "LastMsgAt set → correct millis",
			payload: mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1"}}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r1"}).Return([]model.Room{
					{ID: "r1", Name: "general", SiteID: "site-a", LastMsgAt: now},
				}, nil)
			},
			setupKeys: func(k *MockRoomKeyStore) {
				k.EXPECT().GetMany(gomock.Any(), []string{"r1"}).Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp model.RoomsInfoBatchResponse) {
				require.Len(t, resp.Rooms, 1)
				assert.Equal(t, now.UTC().UnixMilli(), resp.Rooms[0].LastMsgAt)
			},
		},
```

with:

```go
		{
			name:    "LastMsgAt set → correct millis",
			payload: mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1"}}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r1"}).Return([]model.Room{
					{ID: "r1", Name: "general", SiteID: "site-a", LastMsgAt: now},
				}, nil)
			},
			setupKeys: func(k *MockRoomKeyStore) {
				k.EXPECT().GetMany(gomock.Any(), []string{"r1"}).Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp model.RoomsInfoBatchResponse) {
				require.Len(t, resp.Rooms, 1)
				require.NotNil(t, resp.Rooms[0].LastMsgAt)
				assert.Equal(t, now.UTC().UnixMilli(), *resp.Rooms[0].LastMsgAt)
			},
		},
```

- [ ] **Step 7: Fix the three assertions in `room-service/integration_test.go`**

Line 1029 — replace:

```go
	assert.Equal(t, lastMsg.UnixMilli(), resp.Rooms[0].LastMsgAt)
```

with:

```go
	require.NotNil(t, resp.Rooms[0].LastMsgAt)
	assert.Equal(t, lastMsg.UnixMilli(), *resp.Rooms[0].LastMsgAt)
```

Line 1037 — replace:

```go
	assert.Equal(t, int64(0), resp.Rooms[1].LastMsgAt)
```

with:

```go
	assert.Nil(t, resp.Rooms[1].LastMsgAt)
```

Line 1048 — replace:

```go
	assert.Equal(t, int64(0), resp.Rooms[3].LastMsgAt)
```

with:

```go
	assert.Nil(t, resp.Rooms[3].LastMsgAt)
```

(`require` and `assert` are already imported in this file; no new imports needed.)

- [ ] **Step 8: Run the unit test suite**

Run: `make test SERVICE=room-service && make test`

Expected: PASS on both. `make test` without `SERVICE=` covers `pkg/model` tests.

If either fails, read the failure, fix the test/producer, and re-run. Do not proceed until both are green.

- [ ] **Step 9: Run the room-service integration tests**

Run: `make test-integration SERVICE=room-service`

Expected: PASS. Requires Docker running locally (testcontainers pattern per CLAUDE.md §4).

If the machine has no Docker, note that and continue — the pre-commit hook does not gate on integration tests per `make lint` + `make test` conventions. But if Docker is available, run and confirm PASS before committing.

- [ ] **Step 10: Run the linter**

Run: `make lint`

Expected: clean. The pre-commit hook (see CLAUDE.md §5) runs both lint and tests, so if lint fails here it will also fail at commit time.

- [ ] **Step 11: Commit**

```bash
git add pkg/model/room.go room-service/handler.go pkg/model/model_test.go room-service/handler_test.go room-service/integration_test.go
git commit -m "$(cat <<'EOF'
refactor(model): make RoomInfo timestamps nullable

Change LastMsgAt and LastMentionAllAt on model.RoomInfo from int64 to
*int64 so the batch RPC can distinguish "no last message" from
"present at epoch 0". Wire format is unchanged: nil + omitempty omits
the field exactly like the previous int64(0) + omitempty did.

Update aggregateRoomInfo to assign &ms when the source time.Time is
non-zero. Update all existing unit and integration tests to compile
against the new pointer types; semantics of every existing case are
preserved.

https://claude.ai/code/session_01HsMbFL397VGMbojKDHZcZm
EOF
)"
```

- [ ] **Step 12: Push**

```bash
git push -u origin claude/nullable-room-timestamps-RQAqX
```

Retry on network failure with exponential backoff (2s, 4s, 8s, 16s), up to 4 attempts.

---

## Task 2: Add a `pkg/model` test that pins down nullable semantics

This test proves the new capability: a `nil` `LastMsgAt` is omitted from JSON, and a pointer to `0` is emitted as `0`. The struct change in Task 1 already enables this behavior — this task's test makes the guarantee explicit and future-proof against a regression to `int64`.

**Files:**
- Modify: `pkg/model/model_test.go` (add subtest to `TestRoomInfoJSON`)

- [ ] **Step 1: Add the failing subtest**

Inside `TestRoomInfoJSON` in `pkg/model/model_test.go`, add the following subtest at the end of the function body (after the `"found=true with nil PrivateKey omits zero-valued optional fields"` subtest, before the closing `}` of `TestRoomInfoJSON`):

```go
	t.Run("nil LastMsgAt omitted; pointer to zero LastMentionAllAt emitted as 0", func(t *testing.T) {
		zero := int64(0)
		src := model.RoomInfo{
			RoomID:           "r1",
			Found:            true,
			LastMsgAt:        nil,
			LastMentionAllAt: &zero,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))

		_, hasLastMsg := raw["lastMsgAt"]
		assert.False(t, hasLastMsg, "nil LastMsgAt must be omitted from JSON")

		lastMention, hasMention := raw["lastMentionAllAt"]
		require.True(t, hasMention, "non-nil LastMentionAllAt must be present even when value is 0")
		assert.Equal(t, float64(0), lastMention, "zero value must round-trip as JSON number 0")

		var dst model.RoomInfo
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Nil(t, dst.LastMsgAt, "absent JSON field must unmarshal to nil pointer")
		require.NotNil(t, dst.LastMentionAllAt)
		assert.Equal(t, int64(0), *dst.LastMentionAllAt)
	})
```

Note: `map[string]any` decodes JSON numbers as `float64` — that's why the comparison is `float64(0)`, not `int64(0)`.

- [ ] **Step 2: Run the new subtest**

Run: `make test SERVICE=pkg/model` if the Makefile accepts that, otherwise: `make test` and grep for the subtest name.

Actually, check the Makefile: `SERVICE=<name>` maps to a service directory. `pkg/model` is a package, not a service. Run instead:

```bash
make test
```

Expected: PASS (all tests in the repo, including the new subtest). The new subtest PASSES immediately because Task 1 already implemented the pointer semantics — we're locking the behavior in, not driving new code.

If the subtest fails, the most likely cause is a typo in the JSON key names or forgetting `omitempty` during Task 1. Inspect `pkg/model/room.go` and fix.

- [ ] **Step 3: Run the linter**

Run: `make lint`

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add pkg/model/model_test.go
git commit -m "$(cat <<'EOF'
test(model): assert nullable semantics for RoomInfo timestamps

Pin down the guarantee that nil LastMsgAt is omitted from JSON while
a pointer to int64(0) is emitted as the number 0. Distinguishes
"no last message" from "present at epoch 0".

https://claude.ai/code/session_01HsMbFL397VGMbojKDHZcZm
EOF
)"
```

- [ ] **Step 5: Push**

```bash
git push origin claude/nullable-room-timestamps-RQAqX
```

---

## Task 3: Add a `room-service` handler test for zero-time → nil

This covers the producer branch: when Mongo stores `time.Time{}` (the zero value) in `Room.LastMsgAt`, the batched response entry must have `LastMsgAt == nil`, not a pointer to `0`. This is the producer-side guarantee that complements Task 2's wire-format guarantee.

**Files:**
- Modify: `room-service/handler_test.go` (add table case to the rooms-info batch test)

- [ ] **Step 1: Add the new table case**

Open `room-service/handler_test.go`. Locate the table in the rooms-info batch test (the `tests := []struct { ... }{ ... }` slice that contains the `"missing room → Found=false, LastMsgAt=nil"` and `"LastMsgAt set → correct millis"` cases — around lines 1383–1526).

Immediately after the `"LastMsgAt set → correct millis"` case (after its closing `},` around line 1525, before the outer closing `}` of the slice at line 1526), add:

```go
		{
			name:    "LastMsgAt zero-time in Mongo → nil in response",
			payload: mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r-zero"}}),
			setupStore: func(s *MockRoomStore) {
				s.EXPECT().ListRoomsByIDs(gomock.Any(), []string{"r-zero"}).Return([]model.Room{
					{ID: "r-zero", Name: "quiet", SiteID: "site-a"},
				}, nil)
			},
			setupKeys: func(k *MockRoomKeyStore) {
				k.EXPECT().GetMany(gomock.Any(), []string{"r-zero"}).Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp model.RoomsInfoBatchResponse) {
				require.Len(t, resp.Rooms, 1)
				assert.Equal(t, "r-zero", resp.Rooms[0].RoomID)
				assert.True(t, resp.Rooms[0].Found)
				assert.Nil(t, resp.Rooms[0].LastMsgAt, "zero-time Room.LastMsgAt must produce nil RoomInfo.LastMsgAt")
				assert.Nil(t, resp.Rooms[0].LastMentionAllAt, "zero-time Room.LastMentionAllAt must produce nil RoomInfo.LastMentionAllAt")
			},
		},
```

The `model.Room` literal deliberately omits `LastMsgAt` and `LastMentionAllAt` so both are the zero `time.Time`, which is exactly the condition `aggregateRoomInfo` checks with `.IsZero()`.

- [ ] **Step 2: Run the test**

Run: `make test SERVICE=room-service`

Expected: PASS (all cases in the table, including the new one). This case is green because Task 1's `aggregateRoomInfo` already handles zero-time correctly by leaving the pointer nil.

If it fails, the most likely cause is that Step 2 of Task 1 assigned a pointer even when `IsZero()` was true. Re-check `room-service/handler.go` against Task 1 Step 2.

- [ ] **Step 3: Run the linter**

Run: `make lint`

Expected: clean.

- [ ] **Step 4: Commit**

```bash
git add room-service/handler_test.go
git commit -m "$(cat <<'EOF'
test(room-service): cover zero-time → nil RoomInfo timestamp

Add a handler table case that seeds a room with the zero time.Time and
asserts aggregateRoomInfo leaves the response LastMsgAt /
LastMentionAllAt pointers nil, matching the nullable wire semantics.

https://claude.ai/code/session_01HsMbFL397VGMbojKDHZcZm
EOF
)"
```

- [ ] **Step 5: Push**

```bash
git push origin claude/nullable-room-timestamps-RQAqX
```

---

## Final verification

After all three tasks are committed and pushed:

- [ ] `make lint` — clean
- [ ] `make test` — PASS
- [ ] `make test-integration SERVICE=room-service` — PASS (requires Docker)
- [ ] `git log --oneline -5` — shows the three commits from this plan on top of the spec commit, in order: type flip → model nullable-semantics test → handler zero-time test

---

## Notes for the implementer

- **Do NOT** run raw `go test` / `go build`. CLAUDE.md §2 mandates `make` targets (they wire in `-race`, lint config, etc.).
- **Do NOT** `make generate`. No store interface changes; mocks are unaffected. (Running it anyway is harmless but wastes time.)
- **Do NOT** edit `mock_store_test.go` or any other generated mock file.
- **Do NOT** touch `model.Room`, `model.ThreadRoom`, `pkg/model/event.go`, `broadcast-worker`, or `message-worker`. The spec's "Out of scope" section enumerates these explicitly.
- **If `make lint` flags** anything outside these files, leave it — this plan is not a general-cleanup PR per CLAUDE.md §5 "keep changes minimal and focused".
- **If a pre-commit hook fails**, per CLAUDE.md §5 and the Git Safety Protocol at the top of the session: fix the underlying issue and create a NEW commit; never `--amend` or `--no-verify`.
