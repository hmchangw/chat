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

## Task 3: `pkg/roomkeystore.GetMany` — interface, fake, and valkeyStore unit tests

**Files:**
- Modify: `pkg/roomkeystore/roomkeystore.go`
- Test: `pkg/roomkeystore/roomkeystore_test.go`

- [ ] **Step 1: Write failing tests for `GetMany` and extend the fake**

In `pkg/roomkeystore/roomkeystore_test.go`, extend `fakeHashClient` with a call-count field and an `hgetallMany` method. Add the field to the struct literal:

```go
type fakeHashClient struct {
	store                map[string]map[string]string
	hsetErr              error
	hgetallErr           error
	hgetallCallCount     int // tracks number of hgetall calls made
	hgetallErrOnCall     int // if >0, hgetallErr fires only on this call number (1-based)
	hgetallManyCallCount int // tracks number of hgetallMany calls made
	rotatePipelineErr    error
	deletePipelineErr    error
}
```

Add the `hgetallMany` method (place it after the existing `hgetall` method so it reuses the same per-key error injection):

```go
func (f *fakeHashClient) hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error) {
	f.hgetallManyCallCount++
	out := make([]map[string]string, len(keys))
	for i, k := range keys {
		m, err := f.hgetall(ctx, k)
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}
```

Append the new test function to `pkg/roomkeystore/roomkeystore_test.go`:

```go
func TestValkeyStore_GetMany(t *testing.T) {
	pubKey1 := bytes.Repeat([]byte{0xAA}, 65)
	privKey1 := bytes.Repeat([]byte{0xBB}, 32)
	pubKey2 := bytes.Repeat([]byte{0xCC}, 65)
	privKey2 := bytes.Repeat([]byte{0xDD}, 32)

	tests := []struct {
		name          string
		fake          *fakeHashClient
		roomIDs       []string
		wantPresent   map[string]*VersionedKeyPair
		wantAbsent    []string
		wantManyCalls int
		wantErr       bool
		errContains   string
	}{
		{
			name:          "empty input — returns empty map, no error, hgetallMany not called",
			fake:          &fakeHashClient{},
			roomIDs:       []string{},
			wantManyCalls: 0,
		},
		{
			name: "all present — returns all versioned pairs",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey1, PrivateKey: privKey1})
				_, _ = s.Set(context.Background(), "room-2", RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2})
				return f
			}(),
			roomIDs: []string{"room-1", "room-2"},
			wantPresent: map[string]*VersionedKeyPair{
				"room-1": {Version: 0, KeyPair: RoomKeyPair{PublicKey: pubKey1, PrivateKey: privKey1}},
				"room-2": {Version: 0, KeyPair: RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2}},
			},
			wantManyCalls: 1,
		},
		{
			name:          "all absent — returns empty map, no error",
			fake:          &fakeHashClient{},
			roomIDs:       []string{"room-x", "room-y"},
			wantManyCalls: 1,
		},
		{
			name: "mixed — only present rooms appear in result",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey1, PrivateKey: privKey1})
				_, _ = s.Set(context.Background(), "room-3", RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2})
				return f
			}(),
			roomIDs: []string{"room-1", "room-2", "room-3"},
			wantPresent: map[string]*VersionedKeyPair{
				"room-1": {Version: 0, KeyPair: RoomKeyPair{PublicKey: pubKey1, PrivateKey: privKey1}},
				"room-3": {Version: 0, KeyPair: RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2}},
			},
			wantAbsent:    []string{"room-2"},
			wantManyCalls: 1,
		},
		{
			name: "decode error on one slot — returns wrapped error with roomID",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "AQID", "priv": "AQID", "ver": "0"},
					roomkey("room-2"): {"pub": "!!!bad!!!", "priv": "AQID", "ver": "0"},
				},
			},
			roomIDs:     []string{"room-1", "room-2"},
			wantErr:     true,
			errContains: "room-2",
		},
		{
			name: "version parse error on one slot — returns wrapped error with roomID",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "AQID", "priv": "AQID", "ver": "not-a-number"},
				},
			},
			roomIDs:     []string{"room-1"},
			wantErr:     true,
			errContains: "room-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.GetMany(context.Background(), tt.roomIDs)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got, "GetMany must return a non-nil map")
			assert.Equal(t, tt.wantManyCalls, tt.fake.hgetallManyCallCount,
				"hgetallManyCallCount mismatch")

			if len(tt.wantPresent) == 0 {
				assert.Empty(t, got)
			} else {
				assert.Len(t, got, len(tt.wantPresent))
				for roomID, want := range tt.wantPresent {
					gotPair, ok := got[roomID]
					require.True(t, ok, "expected room %q in result", roomID)
					require.NotNil(t, gotPair)
					assert.Equal(t, want.Version, gotPair.Version)
					assert.Equal(t, want.KeyPair.PublicKey, gotPair.KeyPair.PublicKey)
					assert.Equal(t, want.KeyPair.PrivateKey, gotPair.KeyPair.PrivateKey)
				}
			}
			for _, roomID := range tt.wantAbsent {
				_, ok := got[roomID]
				assert.False(t, ok, "room %q should not be in result", roomID)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/roomkeystore`
Expected: FAIL — `store.GetMany undefined`; package won't compile until Step 3.

- [ ] **Step 3: Add `GetMany` to the interface and implement on `valkeyStore`**

In `pkg/roomkeystore/roomkeystore.go`, extend the `RoomKeyStore` interface:

```go
// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetMany(ctx context.Context, roomIDs []string) (map[string]*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
	Delete(ctx context.Context, roomID string) error
}
```

Extend the `hashCommander` interface:

```go
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}
```

Add the `GetMany` implementation (append after the existing `Get` method):

```go
// GetMany retrieves the current key pair for each roomID in a single pipelined call.
// Absent rooms are omitted from the result map. Returns an empty (non-nil) map when
// roomIDs is empty without issuing any network call.
func (s *valkeyStore) GetMany(ctx context.Context, roomIDs []string) (map[string]*VersionedKeyPair, error) {
	result := make(map[string]*VersionedKeyPair, len(roomIDs))
	if len(roomIDs) == 0 {
		return result, nil
	}
	keys := make([]string, len(roomIDs))
	for i, id := range roomIDs {
		keys[i] = roomkey(id)
	}
	hashes, err := s.client.hgetallMany(ctx, keys)
	if err != nil {
		return nil, fmt.Errorf("get many room keys: %w", err)
	}
	for i, fields := range hashes {
		roomID := roomIDs[i]
		if len(fields) == 0 {
			continue
		}
		ver, err := strconv.Atoi(fields["ver"])
		if err != nil {
			return nil, fmt.Errorf("get many room keys: parse version for room %s: %w", roomID, err)
		}
		pair, err := decodeKeyPair(fields)
		if err != nil {
			return nil, fmt.Errorf("get many room keys: decode for room %s: %w", roomID, err)
		}
		result[roomID] = &VersionedKeyPair{Version: ver, KeyPair: *pair}
	}
	return result, nil
}
```

- [ ] **Step 4: Verify package compiles and unit tests pass**

Because `redisAdapter` does not yet implement `hgetallMany` (that arrives in Task 4), add a temporary stub in `adapter.go` to keep the package compiling during this task:

```go
func (a *redisAdapter) hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error) {
	return nil, fmt.Errorf("not implemented")
}
```

Run: `make test SERVICE=pkg/roomkeystore`
Expected: PASS — all existing tests plus `TestValkeyStore_GetMany` subtests pass. (The stub is never called in unit tests because the fake is used.)

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: no warnings.

- [ ] **Step 6: Commit**

```bash
git add pkg/roomkeystore/roomkeystore.go pkg/roomkeystore/roomkeystore_test.go pkg/roomkeystore/adapter.go
git commit -m "feat(roomkeystore): add GetMany to interface, fake, and valkeyStore"
```

---

## Task 4: `pkg/roomkeystore.GetMany` — pipelined redis adapter implementation

**Files:**
- Modify: `pkg/roomkeystore/adapter.go`

- [ ] **Step 1: Replace the stub with the pipelined implementation**

In `pkg/roomkeystore/adapter.go`, replace the temporary stub from Task 3 with the real `hgetallMany`. Place it directly after the existing `hgetall` method:

```go
// hgetallMany issues HGETALL for every key in a single pipeline and returns
// one map per input key (in the same order). A missing hash yields an empty
// map rather than an error, matching go-redis v9 HGetAll semantics.
func (a *redisAdapter) hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	pipe := a.c.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.HGetAll(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(keys))
	for i, c := range cmds {
		m, err := c.Result()
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}
```

- [ ] **Step 2: Run unit tests**

Run: `make test SERVICE=pkg/roomkeystore`
Expected: PASS — all existing tests plus `TestValkeyStore_GetMany` still pass (unit tests use the fake; this step verifies `redisAdapter` still satisfies `hashCommander`).

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: no warnings.

- [ ] **Step 4: Commit**

```bash
git add pkg/roomkeystore/adapter.go
git commit -m "feat(roomkeystore): implement pipelined hgetallMany on redis adapter"
```

---

## Task 5: `pkg/roomkeystore.GetMany` — integration test

**Files:**
- Test: `pkg/roomkeystore/integration_test.go`

- [ ] **Step 1: Append the integration test**

Append to `pkg/roomkeystore/integration_test.go` (file already has `//go:build integration`):

```go
func TestValkeyStore_GetMany(t *testing.T) {
	store := setupValkey(t, time.Hour)
	ctx := context.Background()

	pub1 := bytes.Repeat([]byte{0x01}, 65)
	priv1 := bytes.Repeat([]byte{0x02}, 32)
	pub2 := bytes.Repeat([]byte{0x03}, 65)
	priv2 := bytes.Repeat([]byte{0x04}, 32)
	pub3 := bytes.Repeat([]byte{0x05}, 65)
	priv3 := bytes.Repeat([]byte{0x06}, 32)

	// Seed three rooms with distinct key pairs.
	_, err := store.Set(ctx, "room-1", RoomKeyPair{PublicKey: pub1, PrivateKey: priv1})
	require.NoError(t, err)
	_, err = store.Set(ctx, "room-2", RoomKeyPair{PublicKey: pub2, PrivateKey: priv2})
	require.NoError(t, err)
	_, err = store.Set(ctx, "room-3", RoomKeyPair{PublicKey: pub3, PrivateKey: priv3})
	require.NoError(t, err)

	// Request a superset that includes one missing room.
	got, err := store.GetMany(ctx, []string{"room-1", "room-2", "room-3", "room-missing"})
	require.NoError(t, err)
	require.Len(t, got, 3, "missing room must be omitted from result")

	require.Contains(t, got, "room-1")
	assert.Equal(t, 0, got["room-1"].Version)
	assert.Equal(t, pub1, got["room-1"].KeyPair.PublicKey)
	assert.Equal(t, priv1, got["room-1"].KeyPair.PrivateKey)

	require.Contains(t, got, "room-2")
	assert.Equal(t, 0, got["room-2"].Version)
	assert.Equal(t, pub2, got["room-2"].KeyPair.PublicKey)
	assert.Equal(t, priv2, got["room-2"].KeyPair.PrivateKey)

	require.Contains(t, got, "room-3")
	assert.Equal(t, 0, got["room-3"].Version)
	assert.Equal(t, pub3, got["room-3"].KeyPair.PublicKey)
	assert.Equal(t, priv3, got["room-3"].KeyPair.PrivateKey)

	_, missing := got["room-missing"]
	assert.False(t, missing, "room-missing must not be present in result")

	// Empty input must return an empty, non-nil map without error.
	empty, err := store.GetMany(ctx, []string{})
	require.NoError(t, err)
	require.NotNil(t, empty)
	assert.Empty(t, empty)
}
```

- [ ] **Step 2: Run the integration test**

Run: `make test-integration SERVICE=pkg/roomkeystore`
Expected: PASS — all existing integration tests plus `TestValkeyStore_GetMany` pass against a real Valkey container.

- [ ] **Step 3: Run lint**

Run: `make lint`
Expected: no warnings.

- [ ] **Step 4: Commit**

```bash
git add pkg/roomkeystore/integration_test.go
git commit -m "test(roomkeystore): add GetMany integration test with testcontainers Valkey"
```

---

<!-- TASKS 6-12 WILL BE APPENDED IN SUBSEQUENT COMMITS -->
