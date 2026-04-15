# Room Info Batch RPC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a NATS request/reply RPC on `room-service` that returns aggregated room info (Mongo `lastMsgAt`, name, siteId + Valkey `privateKey`) for a batch of room IDs, in a single request.

**Architecture:** One handler on subject `chat.user.{account}.request.rooms.{siteID}.info.batch`. Validates → parallel fan-out to Mongo (one `find({_id: {$in: ids}})`) and Valkey (one pipelined `HGETALL` per room) via `errgroup` → merges by roomID preserving input order → JSON reply. Pipelining is added to `pkg/roomkeystore` as a new `GetMany` method.

**Tech Stack:** Go 1.25, NATS core request/reply, MongoDB (v2 driver), Valkey via `github.com/redis/go-redis/v9`, `golang.org/x/sync/errgroup`, `go.uber.org/mock`, `testify`, `testcontainers-go`.

**Spec reference:** `docs/superpowers/specs/2026-04-14-room-info-batch-rpc-design.md`

---

## File Structure

| Path | Disposition | Responsibility |
|---|---|---|
| `pkg/subject/subject.go` | Modify | Add `RoomsInfoBatch`, `RoomsInfoBatchWildcard`, `RoomsInfoBatchPattern` builders. |
| `pkg/subject/subject_test.go` | Modify | Unit tests for the three new builders. |
| `pkg/model/room.go` | Modify | Add `RoomsInfoBatchRequest`, `RoomInfo`, `RoomsInfoBatchResponse`. |
| `pkg/model/model_test.go` | Modify | JSON roundtrip + omit-behavior tests for the new types. |
| `pkg/roomkeystore/roomkeystore.go` | Modify | Add `GetMany` to `RoomKeyStore`; add `hgetallMany` to `hashCommander`; implement `GetMany` on `valkeyStore`. |
| `pkg/roomkeystore/adapter.go` | Modify | Implement pipelined `hgetallMany` on the redis adapter. |
| `pkg/roomkeystore/roomkeystore_test.go` | Modify | Extend fake with `hgetallMany`; unit tests for `GetMany`. |
| `pkg/roomkeystore/integration_test.go` | Modify | `TestValkeyStore_GetMany` against testcontainers Valkey. |
| `room-service/store.go` | Modify | Add `ListRoomsByIDs` to `RoomStore` interface. |
| `room-service/store_mongo.go` | Modify | Implement `ListRoomsByIDs` via `$in`. |
| `room-service/mock_store_test.go` | Regenerate | `make generate SERVICE=room-service` after interface change. |
| `room-service/mock_keystore_test.go` | Create | New mockgen output for `roomkeystore.RoomKeyStore`. |
| `room-service/handler.go` | Modify | Inject `RoomKeyStore` + `maxBatchSize`; register new subscription; add `natsRoomsInfoBatch`, `handleRoomsInfoBatch`, `aggregateRoomInfo`. |
| `room-service/handler_test.go` | Modify | Unit tests for the new handler paths with mocks. |
| `room-service/main.go` | Modify | Add `MaxBatchSize` + Valkey config; wire `NewValkeyStore`; pass into `NewHandler`. |
| `room-service/deploy/docker-compose.yml` | Modify | Add Valkey service; add Valkey env vars. |
| `room-service/integration_test.go` | Modify | `TestRoomsInfoBatchRPC` end-to-end (Mongo + Valkey + NATS via testcontainers). |

---

## Tasks

- Task 1: `pkg/subject` builders — add `RoomsInfoBatch`, `RoomsInfoBatchWildcard`, `RoomsInfoBatchPattern`.
- Task 2: `pkg/model` types — add `RoomsInfoBatchRequest`, `RoomInfo`, `RoomsInfoBatchResponse`.
- Task 3: `pkg/roomkeystore.GetMany` — extend interface, fake, and valkeyStore; unit tests.
- Task 4: `pkg/roomkeystore.GetMany` — pipelined redis adapter implementation.
- Task 5: `pkg/roomkeystore.GetMany` — integration test with testcontainers Valkey.
- Task 6: `room-service/store` — add `ListRoomsByIDs` interface method + Mongo impl + integration test.
- Task 7: Regenerate room-service mocks and add `RoomKeyStore` mockgen.
- Task 8: `room-service/handler` — add `RoomKeyStore` + `maxBatchSize` deps; register new subscription.
- Task 9: `room-service/handler` — implement `handleRoomsInfoBatch` + `aggregateRoomInfo`; unit tests.
- Task 10: `room-service/main.go` — config additions, wire Valkey, update `NewHandler` call.
- Task 11: `room-service/deploy/docker-compose.yml` — add Valkey service + env vars.
- Task 12: `room-service/integration_test.go` — end-to-end `TestRoomsInfoBatchRPC`.

---

## Task 1: `pkg/subject` builders

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write failing tests for the three new builders**

Append to `pkg/subject/subject_test.go` inside the existing `TestSubjectBuilders` slice:

```go
{"RoomsInfoBatch", subject.RoomsInfoBatch("alice", "site-a"),
    "chat.user.alice.request.rooms.site-a.info.batch"},
```

Append to the existing `TestWildcardPatterns` slice:

```go
{"RoomsInfoBatchWild", subject.RoomsInfoBatchWildcard("site-a"),
    "chat.user.*.request.rooms.site-a.info.batch"},
```

Append a new test function at the end of the file:

```go
func TestRoomsInfoBatchPattern(t *testing.T) {
    got := subject.RoomsInfoBatchPattern("site-a")
    want := "chat.user.{account}.request.rooms.site-a.info.batch"
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/subject`
Expected: FAIL — `undefined: subject.RoomsInfoBatch`, etc.

- [ ] **Step 3: Implement the three builders**

Append to `pkg/subject/subject.go` (after the existing `RoomsGet` builder section for specific builders, and alongside the wildcard section for the wildcard):

```go
// RoomsInfoBatch is the request/reply subject for batch room info lookups.
func RoomsInfoBatch(account, siteID string) string {
    return fmt.Sprintf("chat.user.%s.request.rooms.%s.info.batch", account, siteID)
}

// RoomsInfoBatchWildcard is the per-site subscription pattern for room-service.
func RoomsInfoBatchWildcard(siteID string) string {
    return fmt.Sprintf("chat.user.*.request.rooms.%s.info.batch", siteID)
}

// RoomsInfoBatchPattern is the natsrouter-style pattern with {account} placeholder.
func RoomsInfoBatchPattern(siteID string) string {
    return fmt.Sprintf("chat.user.{account}.request.rooms.%s.info.batch", siteID)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/subject`
Expected: PASS.

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add RoomsInfoBatch subject builders"
```

---

## Task 2: `pkg/model` types

**Files:**
- Modify: `pkg/model/room.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests**

Append to `pkg/model/model_test.go`:

```go
func TestRoomsInfoBatchRequestJSON(t *testing.T) {
	src := model.RoomsInfoBatchRequest{
		RoomIDs: []string{"r1", "r2", "r3"},
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.RoomsInfoBatchRequest
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}

func TestRoomInfoJSON(t *testing.T) {
	t.Run("happy path with all fields", func(t *testing.T) {
		pk := "dGVzdC1wcml2YXRlLWtleS1iYXNlNjQ="
		kv := 7
		src := model.RoomInfo{
			RoomID:     "r1",
			Found:      true,
			SiteID:     "site-a",
			Name:       "general",
			LastMsgAt:  1735689600000,
			PrivateKey: &pk,
			KeyVersion: &kv,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)
		var dst model.RoomInfo
		require.NoError(t, json.Unmarshal(data, &dst))
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})

	t.Run("found=false omits optional fields but keeps lastMsgAt", func(t *testing.T) {
		src := model.RoomInfo{
			RoomID: "r1",
			Found:  false,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))

		assert.Contains(t, raw, "roomId")
		assert.Equal(t, "r1", raw["roomId"])

		foundVal, foundPresent := raw["found"]
		assert.True(t, foundPresent, "found must be present")
		assert.Equal(t, false, foundVal)

		lastMsgAtVal, lastMsgAtPresent := raw["lastMsgAt"]
		assert.True(t, lastMsgAtPresent, "lastMsgAt must be present even when zero")
		assert.Equal(t, float64(0), lastMsgAtVal)

		for _, key := range []string{"siteId", "name", "privateKey", "keyVersion", "error"} {
			_, present := raw[key]
			assert.False(t, present, "%q should be omitted", key)
		}
	})

	t.Run("found=true with nil PrivateKey omits privateKey but keeps lastMsgAt", func(t *testing.T) {
		src := model.RoomInfo{
			RoomID:    "r1",
			Found:     true,
			SiteID:    "site-a",
			Name:      "general",
			LastMsgAt: 0,
		}
		data, err := json.Marshal(&src)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))

		lastMsgAtVal, lastMsgAtPresent := raw["lastMsgAt"]
		assert.True(t, lastMsgAtPresent, "lastMsgAt must be present even when zero")
		assert.Equal(t, float64(0), lastMsgAtVal)

		_, pkPresent := raw["privateKey"]
		assert.False(t, pkPresent, "privateKey should be omitted when nil")
		_, kvPresent := raw["keyVersion"]
		assert.False(t, kvPresent, "keyVersion should be omitted when nil")
	})
}

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
				RoomID:    "r2",
				Found:     false,
				LastMsgAt: 0,
			},
		},
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.RoomsInfoBatchResponse
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`
Expected: FAIL — `undefined: model.RoomsInfoBatchRequest`, `undefined: model.RoomInfo`, `undefined: model.RoomsInfoBatchResponse`.

- [ ] **Step 3: Implement the types**

Append to `pkg/model/room.go`:

```go
// RoomsInfoBatchRequest is the NATS request body for the batch room info RPC.
type RoomsInfoBatchRequest struct {
	RoomIDs []string `json:"roomIds"`
}

// RoomInfo is a single aggregated room record: Mongo metadata + Valkey key.
// LastMsgAt has no omitempty — it is always emitted so callers can distinguish
// "found, never messaged" (lastMsgAt: 0) from "not found" (found: false).
type RoomInfo struct {
	RoomID     string  `json:"roomId"`
	Found      bool    `json:"found"`
	SiteID     string  `json:"siteId,omitempty"`
	Name       string  `json:"name,omitempty"`
	LastMsgAt  int64   `json:"lastMsgAt"`
	PrivateKey *string `json:"privateKey,omitempty"`
	KeyVersion *int    `json:"keyVersion,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// RoomsInfoBatchResponse contains one entry per requested roomID, in input order.
type RoomsInfoBatchResponse struct {
	Rooms []RoomInfo `json:"rooms"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`
Expected: PASS.

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add pkg/model/room.go pkg/model/model_test.go
git commit -m "feat(model): add RoomsInfoBatchRequest/RoomInfo/RoomsInfoBatchResponse types"
```

---

<!-- TASKS 3-12 WILL BE APPENDED IN SUBSEQUENT COMMITS -->
