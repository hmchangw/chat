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

## Task 6: `room-service/store` — `ListRoomsByIDs`

**Files:**
- Modify: `room-service/store.go`
- Modify: `room-service/store_mongo.go`
- Test: `room-service/integration_test.go`

- [ ] **Step 1: Write failing integration test for `ListRoomsByIDs`**

Append to `room-service/integration_test.go`:

```go
func TestMongoStore_ListRoomsByIDs(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	seed := []model.Room{
		{ID: "r1", Name: "one", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now},
		{ID: "r2", Name: "two", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(1 * time.Second)},
		{ID: "r3", Name: "three", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(2 * time.Second)},
		{ID: "r4", Name: "four", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(3 * time.Second)},
		{ID: "r5", Name: "five", Type: model.RoomTypeGroup, SiteID: "site-a", LastMsgAt: now.Add(4 * time.Second)},
	}
	for i := range seed {
		if err := store.CreateRoom(ctx, &seed[i]); err != nil {
			t.Fatalf("seed CreateRoom %q: %v", seed[i].ID, err)
		}
	}

	t.Run("returns matches and skips missing", func(t *testing.T) {
		rooms, err := store.ListRoomsByIDs(ctx, []string{"r1", "r3", "r5", "missing"})
		if err != nil {
			t.Fatalf("ListRoomsByIDs: %v", err)
		}
		if len(rooms) != 3 {
			t.Fatalf("got %d rooms, want 3", len(rooms))
		}
		byID := map[string]model.Room{}
		for _, r := range rooms {
			byID[r.ID] = r
		}
		for _, id := range []string{"r1", "r3", "r5"} {
			r, ok := byID[id]
			if !ok {
				t.Errorf("expected roomID %q in result", id)
				continue
			}
			if r.LastMsgAt.IsZero() {
				t.Errorf("room %q: LastMsgAt is zero", id)
			}
		}
	})

	t.Run("empty slice returns nil without error", func(t *testing.T) {
		rooms, err := store.ListRoomsByIDs(ctx, nil)
		if err != nil {
			t.Fatalf("ListRoomsByIDs(nil): %v", err)
		}
		if rooms != nil {
			t.Errorf("got %v, want nil", rooms)
		}
	})
}
```

Add the `"time"` import at the top of `room-service/integration_test.go` if it is not already present.

- [ ] **Step 2: Run the integration test to confirm RED**

Run: `make test-integration SERVICE=room-service`
Expected: FAIL — `store.ListRoomsByIDs undefined (type *MongoStore has no field or method ListRoomsByIDs)`.

- [ ] **Step 3: Extend the `RoomStore` interface**

Edit `room-service/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	ListRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
}
```

- [ ] **Step 4: Implement `ListRoomsByIDs` on `MongoStore`**

Append to `room-service/store_mongo.go`:

```go
func (s *MongoStore) ListRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	cursor, err := s.rooms.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, fmt.Errorf("list rooms by ids: %w", err)
	}
	var rooms []model.Room
	if err := cursor.All(ctx, &rooms); err != nil {
		return nil, fmt.Errorf("list rooms by ids: decode: %w", err)
	}
	return rooms, nil
}
```

- [ ] **Step 5: Run the integration test to confirm GREEN**

Run: `make test-integration SERVICE=room-service`
Expected: PASS — both subtests green.

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/integration_test.go
git commit -m "feat(room-service): add ListRoomsByIDs Mongo store method"
```

---

## Task 7: Regenerate room-service mocks and add `RoomKeyStore` mockgen

**Files:**
- Modify: `room-service/store.go`
- Regenerate: `room-service/mock_store_test.go`
- Create: `room-service/mock_keystore_test.go`

- [ ] **Step 1: Add a second `//go:generate` directive for `RoomKeyStore`**

Edit `room-service/store.go`. The top of the file now reads:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore
//go:generate mockgen -destination=mock_keystore_test.go -package=main github.com/hmchangw/chat/pkg/roomkeystore RoomKeyStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	ListRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
}
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=room-service`
Expected:
- `room-service/mock_store_test.go` rewritten — now includes `ListRoomsByIDs` method on `MockRoomStore` plus its recorder.
- `room-service/mock_keystore_test.go` created — contains `MockRoomKeyStore` with methods `Set`, `Get`, `GetMany`, `GetByVersion`, `Rotate`, `Delete` and a `NewMockRoomKeyStore(ctrl)` constructor.

Verify both files exist and reference the right interfaces:

```bash
ls room-service/mock_store_test.go room-service/mock_keystore_test.go
```

- [ ] **Step 3: Run existing unit tests to confirm nothing broke**

Run: `make test SERVICE=room-service`
Expected: PASS — existing `TestHandler_CreateRoom`, `TestHandler_InviteOwner_Success`, `TestHandler_InviteMember_Rejected`, `TestHandler_InviteExceedsMaxSize` still green. The regenerated `MockRoomStore` satisfies the expanded interface.

- [ ] **Step 4: Commit**

```bash
git add room-service/store.go room-service/mock_store_test.go room-service/mock_keystore_test.go
git commit -m "chore(room-service): add ListRoomsByIDs and regenerate mocks"
```

---

## Task 8: Handler struct, constructor, and registration for batch RPC

**Files:**
- Modify: `room-service/handler.go`
- Modify: `room-service/main.go` (minimal no-op patch so the package still compiles; real Valkey wiring arrives in Task 10)

- [ ] **Step 1: Edit `handler.go` — add import, expand struct, update constructor, register subscription, stub method**

Replace `room-service/handler.go` with:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/google/uuid"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/subject"
)

type Handler struct {
	store           RoomStore
	keyStore        roomkeystore.RoomKeyStore
	siteID          string
	maxRoomSize     int
	maxBatchSize    int
	publishToStream func(ctx context.Context, data []byte) error
}

func NewHandler(
	store RoomStore,
	keyStore roomkeystore.RoomKeyStore,
	siteID string,
	maxRoomSize, maxBatchSize int,
	publishToStream func(context.Context, []byte) error,
) *Handler {
	return &Handler{
		store:           store,
		keyStore:        keyStore,
		siteID:          siteID,
		maxRoomSize:     maxRoomSize,
		maxBatchSize:    maxBatchSize,
		publishToStream: publishToStream,
	}
}

// RegisterCRUD registers NATS request/reply handlers for room CRUD with queue group.
func (h *Handler) RegisterCRUD(nc *otelnats.Conn) error {
	const queue = "room-service"
	if _, err := nc.QueueSubscribe(subject.RoomsCreateWildcard(), queue, h.natsCreateRoom); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.RoomsListWildcard(), queue, h.natsListRooms); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.RoomsGetWildcard(), queue, h.natsGetRoom); err != nil {
		return err
	}
	if _, err := nc.QueueSubscribe(subject.RoomsInfoBatchWildcard(h.siteID), queue, h.natsRoomsInfoBatch); err != nil {
		return err
	}
	return nil
}

func (h *Handler) natsCreateRoom(m otelnats.Msg) {
	resp, err := h.handleCreateRoom(m.Context(), m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) natsListRooms(m otelnats.Msg) {
	rooms, err := h.store.ListRooms(m.Context())
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	natsutil.ReplyJSON(m.Msg, model.ListRoomsResponse{Rooms: rooms})
}

func (h *Handler) natsGetRoom(m otelnats.Msg) {
	parts := strings.Split(m.Msg.Subject, ".")
	roomID := parts[len(parts)-1]
	room, err := h.store.GetRoom(m.Context(), roomID)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	natsutil.ReplyJSON(m.Msg, room)
}

// NatsHandleInvite handles invite authorization requests.
func (h *Handler) NatsHandleInvite(m otelnats.Msg) {
	resp, err := h.handleInvite(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) handleCreateRoom(ctx context.Context, data []byte) ([]byte, error) {
	var req model.CreateRoomRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	now := time.Now().UTC()
	room := model.Room{
		ID:        uuid.New().String(),
		Name:      req.Name,
		Type:      req.Type,
		CreatedBy: req.CreatedBy,
		SiteID:    req.SiteID,
		UserCount: 1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateRoom(ctx, &room); err != nil {
		return nil, fmt.Errorf("create room: %w", err)
	}

	sub := model.Subscription{
		ID:                 uuid.New().String(),
		User:               model.SubscriptionUser{ID: req.CreatedBy, Account: req.CreatedByAccount},
		RoomID:             room.ID,
		SiteID:             req.SiteID,
		Role:               model.RoleOwner,
		HistorySharedSince: &now,
		JoinedAt:           now,
	}
	if err := h.store.CreateSubscription(ctx, &sub); err != nil {
		slog.Warn("create owner subscription failed", "error", err)
	}

	return json.Marshal(room)
}

func (h *Handler) handleInvite(ctx context.Context, subj string, data []byte) ([]byte, error) {
	inviterAccount, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid invite subject: %s", subj)
	}

	sub, err := h.store.GetSubscription(ctx, inviterAccount, roomID)
	if err != nil {
		return nil, fmt.Errorf("inviter not found: %w", err)
	}
	if sub.Role != model.RoleOwner {
		return nil, fmt.Errorf("only owners can invite members")
	}

	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("room not found: %w", err)
	}
	if room.UserCount >= h.maxRoomSize {
		return nil, fmt.Errorf("room is at maximum capacity (%d)", h.maxRoomSize)
	}

	var req model.InviteMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	req.Timestamp = time.Now().UTC().UnixMilli()

	timestampedData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal invite request: %w", err)
	}

	if err := h.publishToStream(ctx, timestampedData); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "ok"})
}

// natsRoomsInfoBatch is the batch room-info RPC dispatcher. The real implementation
// is added in Task 9; this placeholder keeps the package compiling.
func (h *Handler) natsRoomsInfoBatch(m otelnats.Msg) {
	natsutil.ReplyError(m.Msg, "not implemented")
}
```

- [ ] **Step 2: Minimal no-op patch to `main.go` so the package still builds**

Edit `room-service/main.go`. Locate the `NewHandler` call and update it to pass `nil` for the key store and `500` for the batch-size cap (real Valkey wiring arrives in Task 10):

```go
store := NewMongoStore(db)
handler := NewHandler(store, nil, cfg.SiteID, cfg.MaxRoomSize, 500, func(ctx context.Context, data []byte) error {
    _, err := js.Publish(ctx, subject.MemberInviteWildcard(cfg.SiteID), data)
    return err
})
```

No other changes to `main.go` in this task.

- [ ] **Step 3: Verify compilation**

Run: `go build ./room-service/`
Expected: no errors.

- [ ] **Step 4: Run existing unit tests**

Run: `make test SERVICE=room-service`
Expected: PASS — the four pre-existing `TestHandler_*` tests remain green. The existing direct `&Handler{...}` struct literals in `handler_test.go` still compile — new fields `keyStore` and `maxBatchSize` zero-value cleanly.

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add room-service/handler.go room-service/main.go
git commit -m "feat(room-service): wire RoomKeyStore + maxBatchSize into Handler and register batch-info subscription"
```

---

## Task 9: Handler implementation — `handleRoomsInfoBatch` + `aggregateRoomInfo` + unit tests

**Files:**
- Modify: `room-service/handler.go`
- Test: `room-service/handler_test.go`
- Modify: `go.mod`, `go.sum` (run `go mod tidy` to pull `golang.org/x/sync` in as a direct dependency)

- [ ] **Step 1: Write the failing table-driven test `TestHandler_handleRoomsInfoBatch`**

Append to `room-service/handler_test.go`. Add the imports `"errors"`, `"strings"`, `"time"`, `"github.com/hmchangw/chat/pkg/roomkeystore"` at the top of the file if not already present:

```go
func TestHandler_handleRoomsInfoBatch(t *testing.T) {
	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	nowMs := now.UnixMilli()

	type setup struct {
		store    *MockRoomStore
		keyStore *MockRoomKeyStore
	}

	tests := []struct {
		name         string
		maxBatchSize int
		payload      []byte
		setup        func(s setup)
		wantErr      string
		assertResp   func(t *testing.T, resp []byte)
	}{
		{
			name:         "happy path preserves order and mixes keyed/unkeyed entries",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1", "r2", "r3"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1", "r2", "r3"}).
					Return([]model.Room{
						{ID: "r1", SiteID: "site-a", Name: "one", LastMsgAt: now},
						{ID: "r2", SiteID: "site-a", Name: "two"},
						{ID: "r3", SiteID: "site-a", Name: "three", LastMsgAt: now.Add(1 * time.Second)},
					}, nil)
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1", "r2", "r3"}).
					Return(map[string]*roomkeystore.VersionedKeyPair{
						"r1": {Version: 7, KeyPair: roomkeystore.RoomKeyPair{PrivateKey: []byte("priv-r1")}},
						"r3": {Version: 2, KeyPair: roomkeystore.RoomKeyPair{PrivateKey: []byte("priv-r3")}},
					}, nil)
			},
			assertResp: func(t *testing.T, resp []byte) {
				var got model.RoomsInfoBatchResponse
				if err := json.Unmarshal(resp, &got); err != nil {
					t.Fatalf("unmarshal response: %v", err)
				}
				if len(got.Rooms) != 3 {
					t.Fatalf("len(Rooms) = %d, want 3", len(got.Rooms))
				}
				if got.Rooms[0].RoomID != "r1" || !got.Rooms[0].Found || got.Rooms[0].Name != "one" || got.Rooms[0].LastMsgAt != nowMs {
					t.Errorf("entry 0 = %+v", got.Rooms[0])
				}
				if got.Rooms[0].PrivateKey == nil || got.Rooms[0].KeyVersion == nil || *got.Rooms[0].KeyVersion != 7 {
					t.Errorf("entry 0 key fields = priv=%v ver=%v", got.Rooms[0].PrivateKey, got.Rooms[0].KeyVersion)
				}
				if got.Rooms[1].RoomID != "r2" || !got.Rooms[1].Found || got.Rooms[1].PrivateKey != nil || got.Rooms[1].KeyVersion != nil {
					t.Errorf("entry 1 = %+v", got.Rooms[1])
				}
				if got.Rooms[1].LastMsgAt != 0 {
					t.Errorf("entry 1 LastMsgAt = %d, want 0", got.Rooms[1].LastMsgAt)
				}
				if got.Rooms[2].RoomID != "r3" || got.Rooms[2].KeyVersion == nil || *got.Rooms[2].KeyVersion != 2 {
					t.Errorf("entry 2 = %+v", got.Rooms[2])
				}
			},
		},
		{
			name:         "missing room yields Found=false and LastMsgAt=0",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1", "missing", "r3"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1", "missing", "r3"}).
					Return([]model.Room{
						{ID: "r1", SiteID: "site-a", Name: "one", LastMsgAt: now},
						{ID: "r3", SiteID: "site-a", Name: "three"},
					}, nil)
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1", "missing", "r3"}).
					Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp []byte) {
				var got model.RoomsInfoBatchResponse
				if err := json.Unmarshal(resp, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if len(got.Rooms) != 3 {
					t.Fatalf("len(Rooms) = %d, want 3", len(got.Rooms))
				}
				if got.Rooms[1].RoomID != "missing" || got.Rooms[1].Found || got.Rooms[1].LastMsgAt != 0 {
					t.Errorf("entry 1 = %+v, want RoomID=missing Found=false LastMsgAt=0", got.Rooms[1])
				}
				if !strings.Contains(string(resp), `"lastMsgAt":0`) {
					t.Errorf("expected lastMsgAt:0 present in payload, got %s", string(resp))
				}
			},
		},
		{
			name:         "empty RoomIDs returns validation error",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{}}),
			setup:        func(_ setup) {},
			wantErr:      "must not be empty",
		},
		{
			name:         "oversized batch returns validation error",
			maxBatchSize: 2,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1", "r2", "r3"}}),
			setup:        func(_ setup) {},
			wantErr:      "exceeds limit",
		},
		{
			name:         "invalid JSON returns parse error",
			maxBatchSize: 500,
			payload:      []byte("{not-json"),
			setup:        func(_ setup) {},
			wantErr:      "invalid request",
		},
		{
			name:         "Mongo error is wrapped",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1"}).
					Return(nil, errors.New("boom"))
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1"}).
					Return(map[string]*roomkeystore.VersionedKeyPair{}, nil).
					AnyTimes()
			},
			wantErr: "list rooms by ids",
		},
		{
			name:         "Valkey error is wrapped",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1"}).
					Return([]model.Room{{ID: "r1"}}, nil).
					AnyTimes()
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1"}).
					Return(nil, errors.New("valkey down"))
			},
			wantErr: "get room keys",
		},
		{
			name:         "duplicate IDs yield duplicate entries",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1", "r1"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1", "r1"}).
					Return([]model.Room{{ID: "r1", Name: "one", SiteID: "site-a", LastMsgAt: now}}, nil)
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1", "r1"}).
					Return(map[string]*roomkeystore.VersionedKeyPair{
						"r1": {Version: 1, KeyPair: roomkeystore.RoomKeyPair{PrivateKey: []byte("priv")}},
					}, nil)
			},
			assertResp: func(t *testing.T, resp []byte) {
				var got model.RoomsInfoBatchResponse
				if err := json.Unmarshal(resp, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if len(got.Rooms) != 2 {
					t.Fatalf("len(Rooms) = %d, want 2", len(got.Rooms))
				}
				if got.Rooms[0].RoomID != "r1" || got.Rooms[1].RoomID != "r1" {
					t.Errorf("expected both entries RoomID=r1, got %+v", got.Rooms)
				}
				if got.Rooms[0].KeyVersion == nil || got.Rooms[1].KeyVersion == nil {
					t.Errorf("expected both entries keyed, got %+v", got.Rooms)
				}
			},
		},
		{
			name:         "LastMsgAt set marshals to UTC millis",
			maxBatchSize: 500,
			payload:      mustJSON(t, model.RoomsInfoBatchRequest{RoomIDs: []string{"r1"}}),
			setup: func(s setup) {
				s.store.EXPECT().
					ListRoomsByIDs(gomock.Any(), []string{"r1"}).
					Return([]model.Room{{ID: "r1", Name: "one", SiteID: "site-a", LastMsgAt: now}}, nil)
				s.keyStore.EXPECT().
					GetMany(gomock.Any(), []string{"r1"}).
					Return(map[string]*roomkeystore.VersionedKeyPair{}, nil)
			},
			assertResp: func(t *testing.T, resp []byte) {
				var got model.RoomsInfoBatchResponse
				if err := json.Unmarshal(resp, &got); err != nil {
					t.Fatalf("unmarshal: %v", err)
				}
				if got.Rooms[0].LastMsgAt != nowMs {
					t.Errorf("LastMsgAt = %d, want %d", got.Rooms[0].LastMsgAt, nowMs)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockRoomStore(ctrl)
			keyStore := NewMockRoomKeyStore(ctrl)
			tc.setup(setup{store: store, keyStore: keyStore})

			h := &Handler{
				store:        store,
				keyStore:     keyStore,
				siteID:       "site-a",
				maxRoomSize:  1000,
				maxBatchSize: tc.maxBatchSize,
			}

			resp, err := h.handleRoomsInfoBatch(context.Background(), tc.payload)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.assertResp != nil {
				tc.assertResp(t, resp)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
```

- [ ] **Step 2: Run tests to confirm RED**

Run: `make test SERVICE=room-service`
Expected: FAIL — `h.handleRoomsInfoBatch undefined (type *Handler has no field or method handleRoomsInfoBatch)`.

- [ ] **Step 3: Implement `natsRoomsInfoBatch`, `handleRoomsInfoBatch`, `aggregateRoomInfo`**

Add `"encoding/base64"` and `"golang.org/x/sync/errgroup"` to the `handler.go` import block.

Replace the placeholder `natsRoomsInfoBatch` at the bottom of `handler.go` with:

```go
func (h *Handler) natsRoomsInfoBatch(m otelnats.Msg) {
	resp, err := h.handleRoomsInfoBatch(m.Context(), m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, err.Error())
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to message", "error", err)
	}
}

func (h *Handler) handleRoomsInfoBatch(ctx context.Context, data []byte) ([]byte, error) {
	var req model.RoomsInfoBatchRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}
	if len(req.RoomIDs) == 0 {
		return nil, fmt.Errorf("roomIds must not be empty")
	}
	if len(req.RoomIDs) > h.maxBatchSize {
		return nil, fmt.Errorf("batch size %d exceeds limit %d", len(req.RoomIDs), h.maxBatchSize)
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var (
		rooms []model.Room
		keys  map[string]*roomkeystore.VersionedKeyPair
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		r, err := h.store.ListRoomsByIDs(gctx, req.RoomIDs)
		if err != nil {
			return fmt.Errorf("list rooms by ids: %w", err)
		}
		rooms = r
		return nil
	})
	g.Go(func() error {
		k, err := h.keyStore.GetMany(gctx, req.RoomIDs)
		if err != nil {
			return fmt.Errorf("get room keys: %w", err)
		}
		keys = k
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}

	return json.Marshal(model.RoomsInfoBatchResponse{
		Rooms: h.aggregateRoomInfo(req.RoomIDs, rooms, keys),
	})
}

func (h *Handler) aggregateRoomInfo(ids []string, rooms []model.Room, keys map[string]*roomkeystore.VersionedKeyPair) []model.RoomInfo {
	byID := make(map[string]*model.Room, len(rooms))
	for i := range rooms {
		byID[rooms[i].ID] = &rooms[i]
	}
	out := make([]model.RoomInfo, len(ids))
	for i, id := range ids {
		entry := model.RoomInfo{RoomID: id}
		r, ok := byID[id]
		if !ok {
			out[i] = entry
			continue
		}
		entry.Found = true
		entry.SiteID = r.SiteID
		entry.Name = r.Name
		if !r.LastMsgAt.IsZero() {
			entry.LastMsgAt = r.LastMsgAt.UTC().UnixMilli()
		}
		if kp, ok := keys[id]; ok && kp != nil {
			enc := base64.StdEncoding.EncodeToString(kp.KeyPair.PrivateKey)
			ver := kp.Version
			entry.PrivateKey = &enc
			entry.KeyVersion = &ver
		}
		out[i] = entry
	}
	return out
}
```

- [ ] **Step 4: Pull `golang.org/x/sync` in as a direct dependency**

Run: `go mod tidy`
Expected: `go.mod` now has `golang.org/x/sync` under direct `require` (or its `// indirect` marker removed); `go.sum` updated.

- [ ] **Step 5: Run tests to confirm GREEN**

Run: `make test SERVICE=room-service`
Expected: PASS — all nine subtests under `TestHandler_handleRoomsInfoBatch` green, plus all pre-existing `TestHandler_*` tests still green.

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add room-service/handler.go room-service/handler_test.go go.mod go.sum
git commit -m "feat(room-service): implement rooms info batch RPC handler"
```

---

## Task 10: Wire Valkey and MaxBatchSize in `room-service/main.go`

**Files:**
- Modify: `room-service/main.go`

- [ ] **Step 1: Update `config` struct and wire Valkey keystore**

Edit `room-service/main.go` to the following full contents:

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/subject"
)

type config struct {
	NatsURL      string `env:"NATS_URL"       envDefault:"nats://localhost:4222"`
	SiteID       string `env:"SITE_ID"        envDefault:"site-local"`
	MongoURI     string `env:"MONGO_URI"      envDefault:"mongodb://localhost:27017"`
	MongoDB      string `env:"MONGO_DB"       envDefault:"chat"`
	MaxRoomSize  int    `env:"MAX_ROOM_SIZE"  envDefault:"1000"`
	MaxBatchSize int    `env:"MAX_BATCH_SIZE" envDefault:"500"`

	ValkeyAddr        string        `env:"VALKEY_ADDR,required"`
	ValkeyPassword    string        `env:"VALKEY_PASSWORD" envDefault:""`
	ValkeyGracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	tracerShutdown, err := otelutil.InitTracer(ctx, "room-service")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	nc, err := otelnats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := oteljetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)

	keyStore, err := roomkeystore.NewValkeyStore(roomkeystore.Config{
		Addr:        cfg.ValkeyAddr,
		Password:    cfg.ValkeyPassword,
		GracePeriod: cfg.ValkeyGracePeriod,
	})
	if err != nil {
		slog.Error("valkey connect failed", "error", err)
		os.Exit(1)
	}

	streamCfg := stream.Rooms(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	}); err != nil {
		slog.Error("create stream failed", "error", err)
		os.Exit(1)
	}

	store := NewMongoStore(db)
	handler := NewHandler(store, keyStore, cfg.SiteID, cfg.MaxRoomSize, cfg.MaxBatchSize, func(ctx context.Context, data []byte) error {
		_, err := js.Publish(ctx, subject.MemberInviteWildcard(cfg.SiteID), data)
		return err
	})

	if err := handler.RegisterCRUD(nc); err != nil {
		slog.Error("register CRUD handlers failed", "error", err)
		os.Exit(1)
	}

	inviteSubj := subject.MemberInviteWildcard(cfg.SiteID)
	if _, err := nc.QueueSubscribe(inviteSubj, "room-service", handler.NatsHandleInvite); err != nil {
		slog.Error("subscribe invite failed", "error", err)
		os.Exit(1)
	}

	slog.Info("room-service running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}
```

- [ ] **Step 2: Verify the binary compiles**

```bash
go build ./room-service/
```

Expected: no output, exit code 0.

- [ ] **Step 3: Run unit tests for room-service**

```bash
make test SERVICE=room-service
```

Expected: `ok  github.com/hmchangw/chat/room-service` — all unit tests PASS with `-race`.

- [ ] **Step 4: Run lint**

```bash
make lint
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add room-service/main.go
git commit -m "feat(room-service): wire Valkey and MaxBatchSize config"
```

---

<!-- TASKS 11-12 WILL BE APPENDED IN SUBSEQUENT COMMITS -->
