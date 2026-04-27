# Room Members List — Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in display enrichment to the existing `member.list` NATS endpoint. When the caller sends `{"enrich":true}`, each returned `RoomMember.Member` carries display fields: `engName`/`chineseName`/`isOwner` for individuals and `sectName`/`memberCount` for orgs. When absent or `false`, behavior is unchanged from the already-shipped bare path.

**Architecture:** One `RoomStore.ListRoomMembers(ctx, roomID, limit, offset, enrich)` method replaces the existing zero-arg `enrich` version. The Mongo implementation threads the flag through the existing `getRoomMembers` (aggregation) and `getRoomSubscriptions` (fallback) helpers; when `enrich=true` the aggregation appends `$lookup`/`$set` stages and the fallback runs a batched user lookup. Display fields live on `RoomMemberEntry` with `bson:"-"` (never persisted) + `json:",omitempty"` (elided from the wire when zero).

**Tech Stack:** Go 1.25.8, NATS (request/reply via `otelnats`), MongoDB (`go.mongodb.org/mongo-driver/v2`), `go.uber.org/mock` for mocks, `stretchr/testify`, `testcontainers-go`.

**Reference spec:** `docs/superpowers/specs/2026-04-20-room-members-list-design.md`

**Starting state:** bare `member.list` endpoint is already implemented and tested on branch `claude/add-room-member-feature-5FPbS`. This plan only covers the enrichment delta.

---

## File Structure (delta from current state)

| File | Role | Change |
|------|------|--------|
| `pkg/model/member.go` | Request/response + entry types | Add `Enrich bool` to `ListRoomMembersRequest`; add 5 display fields to `RoomMemberEntry` with `bson:"-"` + `json:",omitempty"` |
| `pkg/model/model_test.go` | Model round-trip tests | Add a round-trip test for a populated-display-fields `RoomMemberEntry` (JSON includes them; BSON marshal excludes them) |
| `room-service/store.go` | `RoomStore` interface | Change `ListRoomMembers` signature to add `enrich bool` |
| `room-service/store_mongo.go` | Mongo store impl | Thread `enrich` through helpers; extend `getRoomMembers` with enrichment stages; extend `getRoomSubscriptions` with batched user lookup |
| `room-service/mock_store_test.go` | Generated mock (never edit) | Regenerated via `make generate SERVICE=room-service` |
| `room-service/handler.go` | Handler | Pass `req.Enrich` through to `store.ListRoomMembers` |
| `room-service/handler_test.go` | Handler unit tests | Update existing mock expectations to pass `enrich=false`; add new subtests asserting `enrich=true` plumbs through and the returned display fields round-trip to JSON |
| `room-service/integration_test.go` | Store integration tests | Update existing call sites with `enrich=false`; add enrichment integration cases (individual display names + isOwner, org sectName + memberCount, enrichment over both paths) |

No changes to `main.go`, `helper.go`, `pkg/subject`, or any other service.

---

## Part 1 — Model changes

Two additive changes to `pkg/model`: one field on the request, five display fields on `RoomMemberEntry`. All tagged for safety so persistence and existing JSON wire format remain unchanged.

### Task 1.1 — Add `Enrich` flag to `ListRoomMembersRequest`

**Files:**
- Modify: `pkg/model/member.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1 — Write failing test**

Append to `pkg/model/model_test.go`, inside `TestListRoomMembersRequestJSON` (add a new `t.Run`):

```go
t.Run("with enrich true", func(t *testing.T) {
    r := model.ListRoomMembersRequest{Enrich: true}
    data, err := json.Marshal(&r)
    require.NoError(t, err)
    assert.Equal(t, `{"enrich":true}`, string(data))

    var dst model.ListRoomMembersRequest
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.True(t, dst.Enrich)
})
```

- [ ] **Step 2 — Run test; verify it fails**

```bash
go test ./pkg/model/... -run TestListRoomMembersRequestJSON -v
```

Expected: compile error `unknown field Enrich in struct literal of type model.ListRoomMembersRequest`.

- [ ] **Step 3 — Add the field**

Edit `pkg/model/member.go`. Current:

```go
type ListRoomMembersRequest struct {
    Limit  *int `json:"limit,omitempty"`
    Offset *int `json:"offset,omitempty"`
}
```

Replace with:

```go
type ListRoomMembersRequest struct {
    Limit  *int `json:"limit,omitempty"`
    Offset *int `json:"offset,omitempty"`
    Enrich bool `json:"enrich,omitempty"`
}
```

- [ ] **Step 4 — Run test; verify it passes**

```bash
go test ./pkg/model/... -run TestListRoomMembersRequestJSON -v
```

Expected: PASS for all three subtests (`with limit and offset`, `omitempty when nil`, `with enrich true`).

- [ ] **Step 5 — Commit**

```bash
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(model): add Enrich flag to ListRoomMembersRequest"
```

---

### Task 1.2 — Add display fields to `RoomMemberEntry`

**Files:**
- Modify: `pkg/model/member.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1 — Write failing tests**

Append two new test functions to `pkg/model/model_test.go` (before the `roundTrip` helper at the bottom):

```go
func TestRoomMemberEntry_DisplayFields_JSON(t *testing.T) {
    entry := model.RoomMemberEntry{
        ID: "u1", Type: model.RoomMemberIndividual, Account: "alice",
        EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
    }
    data, err := json.Marshal(&entry)
    require.NoError(t, err)

    // JSON carries all fields, including the display ones.
    var got map[string]any
    require.NoError(t, json.Unmarshal(data, &got))
    assert.Equal(t, "u1", got["id"])
    assert.Equal(t, "individual", got["type"])
    assert.Equal(t, "alice", got["account"])
    assert.Equal(t, "Alice Wang", got["engName"])
    assert.Equal(t, "愛麗絲", got["chineseName"])
    assert.Equal(t, true, got["isOwner"])
}

func TestRoomMemberEntry_DisplayFields_OmittedWhenZero(t *testing.T) {
    entry := model.RoomMemberEntry{
        ID: "u1", Type: model.RoomMemberIndividual, Account: "alice",
    }
    data, err := json.Marshal(&entry)
    require.NoError(t, err)
    var got map[string]any
    require.NoError(t, json.Unmarshal(data, &got))
    for _, k := range []string{"engName", "chineseName", "isOwner", "sectName", "memberCount"} {
        _, present := got[k]
        assert.False(t, present, "display field %q should be omitted when zero", k)
    }
}

func TestRoomMemberEntry_DisplayFields_NotPersistedToBSON(t *testing.T) {
    entry := model.RoomMemberEntry{
        ID: "org-1", Type: model.RoomMemberOrg,
        SectName: "Engineering", MemberCount: 42,
    }
    data, err := bson.Marshal(&entry)
    require.NoError(t, err)

    var got bson.M
    require.NoError(t, bson.Unmarshal(data, &got))
    assert.Equal(t, "org-1", got["id"])
    assert.Equal(t, "org", got["type"])
    for _, k := range []string{"engName", "chineseName", "isOwner", "sectName", "memberCount"} {
        _, present := got[k]
        assert.False(t, present, "display field %q must not be persisted to BSON", k)
    }
}
```

Add the `bson` import at the top of `pkg/model/model_test.go` if it is not already present:

```go
import (
    ...
    "go.mongodb.org/mongo-driver/v2/bson"
    ...
)
```

- [ ] **Step 2 — Run tests; verify they fail**

```bash
go test ./pkg/model/... -run "TestRoomMemberEntry_DisplayFields" -v
```

Expected: compile error `unknown field EngName in struct literal of type model.RoomMemberEntry`.

- [ ] **Step 3 — Add the display fields**

Edit `pkg/model/member.go`. Current:

```go
type RoomMemberEntry struct {
    ID      string         `json:"id"                bson:"id"`
    Type    RoomMemberType `json:"type"              bson:"type"`
    Account string         `json:"account,omitempty" bson:"account,omitempty"`
}
```

Replace with:

```go
type RoomMemberEntry struct {
    ID      string         `json:"id"                bson:"id"`
    Type    RoomMemberType `json:"type"              bson:"type"`
    Account string         `json:"account,omitempty" bson:"account,omitempty"`

    // Display fields — never persisted (bson:"-"); populated only when
    // ListRoomMembers is called with enrich=true. Elided from JSON when zero.
    EngName     string `json:"engName,omitempty"     bson:"-"`
    ChineseName string `json:"chineseName,omitempty" bson:"-"`
    IsOwner     bool   `json:"isOwner,omitempty"     bson:"-"`
    SectName    string `json:"sectName,omitempty"    bson:"-"`
    MemberCount int    `json:"memberCount,omitempty" bson:"-"`
}
```

- [ ] **Step 4 — Run tests; verify they pass**

```bash
go test ./pkg/model/... -run "TestRoomMemberEntry_DisplayFields" -v
```

Expected: PASS for all three tests.

- [ ] **Step 5 — Run full pkg/model suite to confirm no regressions**

```bash
go test -race ./pkg/model/...
```

Expected: full pkg/model suite PASS.

- [ ] **Step 6 — Commit**

```bash
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(model): add display fields to RoomMemberEntry (bson:\"-\", json omitempty)"
```

---

## Part 2 — Store interface signature change + mock regen

Change `RoomStore.ListRoomMembers` to take an `enrich bool` parameter. This is a **breaking signature change** — touching the interface, the Mongo implementation's public method (we thread the flag through to helpers in Part 4), the mock, and every existing call site. The aggregation/fallback bodies still ignore `enrich` in this part (a `_ = enrich` guard keeps lint green); the real enrichment logic comes in Part 4.

### Task 2.1 — Change interface and implementation signatures

**Files:**
- Modify: `room-service/store.go`
- Modify: `room-service/store_mongo.go`

- [ ] **Step 1 — Change interface signature**

Edit `room-service/store.go`. Replace the existing line:

```go
    ListRoomMembers(ctx context.Context, roomID string, limit, offset *int) ([]model.RoomMember, error)
```

with:

```go
    // ListRoomMembers returns the members of roomID. When enrich=true, the
    // returned RoomMember.Member entries carry display fields populated via
    // $lookup stages against users and subscriptions. When enrich=false,
    // display fields are left zero.
    ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error)
```

- [ ] **Step 2 — Change public method signature on MongoStore**

Edit `room-service/store_mongo.go`. Replace the existing:

```go
func (s *MongoStore) ListRoomMembers(ctx context.Context, roomID string, limit, offset *int) ([]model.RoomMember, error) {
    err := s.roomMembers.FindOne(ctx, bson.M{"rid": roomID}).Err()
    switch {
    case err == nil:
        return s.getRoomMembers(ctx, roomID, limit, offset)
    case errors.Is(err, mongo.ErrNoDocuments):
        return s.getRoomSubscriptions(ctx, roomID, limit, offset)
    default:
        return nil, fmt.Errorf("probe room_members for %q: %w", roomID, err)
    }
}
```

with:

```go
func (s *MongoStore) ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    // Lightweight existence probe — project only _id to minimize payload.
    err := s.roomMembers.FindOne(ctx, bson.M{"rid": roomID},
        options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
    switch {
    case err == nil:
        return s.getRoomMembers(ctx, roomID, limit, offset, enrich)
    case errors.Is(err, mongo.ErrNoDocuments):
        return s.getRoomSubscriptions(ctx, roomID, limit, offset, enrich)
    default:
        return nil, fmt.Errorf("probe room_members for %q: %w", roomID, err)
    }
}
```

- [ ] **Step 3 — Thread enrich through the two helpers (accept + ignore)**

The helper bodies will gain real enrichment logic in Part 4. For now, accept the flag and discard it with `_ = enrich` so the build stays green and lint doesn't flag it.

In `room-service/store_mongo.go`, change the `getRoomMembers` helper signature:

```go
func (s *MongoStore) getRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    _ = enrich // TODO(part-4): append enrichment stages when true
    // ... existing body unchanged ...
}
```

Keep the entire existing body below that `_ = enrich` line untouched.

Do the same for `getRoomSubscriptions`:

```go
func (s *MongoStore) getRoomSubscriptions(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    _ = enrich // TODO(part-4): batch-load users + set IsOwner when true
    // ... existing body unchanged ...
}
```

- [ ] **Step 4 — Verify compilation fails on handler and tests**

```bash
go build ./room-service/... 2>&1 | head -20
```

Expected: errors like `h.store.ListRoomMembers(...) has too few arguments` in `handler.go`, and similar in `handler_test.go` / `integration_test.go`. That is the intended state — the next tasks in this Part + Part 3 fix them.

---

### Task 2.2 — Update handler to pass `enrich=false` literal (temporary)

The real pass-through of `req.Enrich` is Part 3. For this commit we keep the handler's behavior unchanged (still bare-only) by passing a literal `false`. This lets the test suite compile independently of the handler change landing.

**Files:**
- Modify: `room-service/handler.go`

- [ ] **Step 1 — Locate the call site**

In `room-service/handler.go`, find the existing line:

```go
    members, err := h.store.ListRoomMembers(ctx, roomID, req.Limit, req.Offset)
```

- [ ] **Step 2 — Update the call to pass `false`**

Replace with:

```go
    members, err := h.store.ListRoomMembers(ctx, roomID, req.Limit, req.Offset, false)
```

Note: this is intentionally a literal `false`, not `req.Enrich`. Part 3 will plumb the flag through properly once all the downstream types compile.

- [ ] **Step 3 — Verify compilation**

```bash
go build ./room-service/... 2>&1 | head -10
```

Expected: `handler.go` compiles. Test files still fail (fixed in later steps of this task).

---

### Task 2.3 — Update handler_test call sites to the new signature

**File:**
- Modify: `room-service/handler_test.go`

- [ ] **Step 1 — Find all `ListRoomMembers` mock expectations**

Run:

```bash
grep -n "ListRoomMembers(gomock\|EXPECT().ListRoomMembers" room-service/handler_test.go
```

Each hit is a mock expectation line like:

```go
s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil)).
    Return([]model.RoomMember{orgMember, existingMember}, nil)
```

- [ ] **Step 2 — Append the `enrich` argument to every expectation**

For every line matching `EXPECT().ListRoomMembers(...)`, add one trailing argument (before the closing paren of the `EXPECT`). Two shapes appear in the existing tests:

Shape A — explicit args with `(*int)(nil)`:

```go
// BEFORE
s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil)).
    Return([]model.RoomMember{orgMember, existingMember}, nil)

// AFTER (add `, false` as the last arg)
s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), false).
    Return([]model.RoomMember{orgMember, existingMember}, nil)
```

Shape B — `gomock.Any()` for limit/offset (pagination subtest):

```go
// BEFORE
s.EXPECT().ListRoomMembers(gomock.Any(), roomID, gomock.Any(), gomock.Any()).
    DoAndReturn(func(_ context.Context, _ string, limit, offset *int) ([]model.RoomMember, error) {
        ...

// AFTER
s.EXPECT().ListRoomMembers(gomock.Any(), roomID, gomock.Any(), gomock.Any(), false).
    DoAndReturn(func(_ context.Context, _ string, limit, offset *int, _ bool) ([]model.RoomMember, error) {
        ...
```

Note: the `DoAndReturn` callback signature must also match the new method shape — add one `_ bool` parameter at the end.

Leave all other test code as-is. No new subtests here — those come in Part 5.

- [ ] **Step 3 — Verify handler tests compile and pass**

```bash
go test -race ./room-service/... -run TestHandler_ListMembers -v
```

Expected: all 10 existing subtests PASS.

---

### Task 2.4 — Update integration_test call sites to the new signature

**File:**
- Modify: `room-service/integration_test.go`

- [ ] **Step 1 — Find all direct `store.ListRoomMembers` call sites**

```bash
grep -n "store.ListRoomMembers" room-service/integration_test.go
```

Each line looks like:

```go
got, err := store.ListRoomMembers(ctx, "r1", nil, nil)
// or
page, err := store.ListRoomMembers(ctx, "r1", ptr(1), ptr(offset))
```

- [ ] **Step 2 — Append `false` to every call**

For each hit, append `, false` as the last argument. Do NOT change behavior of existing tests — they all continue to exercise the bare path.

Example:

```go
// BEFORE
got, err := store.ListRoomMembers(ctx, "r1", nil, nil)
// AFTER
got, err := store.ListRoomMembers(ctx, "r1", nil, nil, false)
```

Repeat for every call in `TestMongoStore_ListRoomMembers_Integration` and any other function that invokes `ListRoomMembers` directly.

- [ ] **Step 3 — Verify integration tests still compile and pass**

```bash
go test -race -tags integration -count=1 ./room-service/... -run TestMongoStore_ListRoomMembers_Integration -v
```

Expected: all 8 existing subtests PASS (behavior unchanged; signature change is transparent because all calls pass `enrich=false`).

---

### Task 2.5 — Regenerate mock, run full suites, commit all Part 2 changes

- [ ] **Step 1 — Regenerate the mock**

```bash
make generate SERVICE=room-service
```

If `mockgen` is missing or fails due to a Go-version mismatch, rebuild it:

```bash
go install go.uber.org/mock/mockgen@v0.6.0
export PATH=$PATH:$(go env GOPATH)/bin
make generate SERVICE=room-service
```

- [ ] **Step 2 — Verify the mock reflects the new signature**

```bash
grep -n "ListRoomMembers" room-service/mock_store_test.go
```

Expected: the mock's `func (m *MockRoomStore) ListRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error)` now takes five non-context arguments.

- [ ] **Step 3 — Run full room-service unit suite**

```bash
make test SERVICE=room-service
```

Expected: PASS with `-race`.

- [ ] **Step 4 — Run lint**

```bash
make lint
```

Expected: 0 findings.

- [ ] **Step 5 — Commit all Part 2 changes together**

```bash
git add room-service/store.go room-service/store_mongo.go room-service/handler.go \
        room-service/handler_test.go room-service/integration_test.go \
        room-service/mock_store_test.go
git commit -m "refactor(room-service): thread enrich bool through ListRoomMembers signature"
```

---

## Part 3 — Handler passes `req.Enrich` to the store

Replace the literal `false` introduced in Part 2 with the real request flag. TDD: add a handler unit test that asserts the flag plumbs through, then flip the literal.

### Task 3.1 — Handler test that asserts `Enrich` plumbs through

**File:**
- Modify: `room-service/handler_test.go`

- [ ] **Step 1 — Add a failing subtest**

Append a new entry to the `tests := []struct{...}{...}` table inside `TestHandler_ListMembers` (alongside the existing 10 subtests):

```go
{
    name:    "enrich=true passed through to store",
    subject: subj,
    body:    []byte(`{"enrich":true}`),
    setupMock: func(s *MockRoomStore) {
        s.EXPECT().GetSubscription(gomock.Any(), requester, roomID).
            Return(&model.Subscription{User: model.SubscriptionUser{Account: requester}, RoomID: roomID}, nil)
        s.EXPECT().ListRoomMembers(gomock.Any(), roomID, (*int)(nil), (*int)(nil), true).
            Return([]model.RoomMember{
                {
                    ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
                    Member: model.RoomMemberEntry{
                        ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
                        EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
                    },
                },
            }, nil)
    },
    want: want{members: []model.RoomMember{
        {
            ID: "rm1", RoomID: roomID, Ts: time.Unix(1, 0).UTC(),
            Member: model.RoomMemberEntry{
                ID: "alice", Type: model.RoomMemberIndividual, Account: "alice",
                EngName: "Alice Wang", ChineseName: "愛麗絲", IsOwner: true,
            },
        },
    }},
},
```

The `ListRoomMembers` expectation pins `true` as the last argument — the test fails until the handler passes `req.Enrich` through.

- [ ] **Step 2 — Run the test; verify it fails**

```bash
go test -race ./room-service/... -run "TestHandler_ListMembers/enrich=true_passed_through" -v
```

Expected: FAIL with a mock expectation miss — the handler is still calling `ListRoomMembers(..., false)`, so the mock (which expects `true`) rejects the call.

The exact error looks like:

```text
Unexpected call to *main.MockRoomStore.ListRoomMembers(...) at ... because:
expected call at ... has the following arguments:
    arg #4: is equal to true (bool), but got false (bool)
```

---

### Task 3.2 — Plumb `req.Enrich` into the store call

**File:**
- Modify: `room-service/handler.go`

- [ ] **Step 1 — Replace the literal**

Find the line in `handleListMembers`:

```go
    members, err := h.store.ListRoomMembers(ctx, roomID, req.Limit, req.Offset, false)
```

Replace with:

```go
    members, err := h.store.ListRoomMembers(ctx, roomID, req.Limit, req.Offset, req.Enrich)
```

No other changes to the handler.

- [ ] **Step 2 — Run the new subtest; verify it passes**

```bash
go test -race ./room-service/... -run "TestHandler_ListMembers/enrich=true_passed_through" -v
```

Expected: PASS.

- [ ] **Step 3 — Run full handler test suite to confirm no regressions**

```bash
go test -race ./room-service/... -run TestHandler_ListMembers -v
```

Expected: all 11 subtests PASS (10 existing + 1 new).

- [ ] **Step 4 — Run full room-service unit suite + lint**

```bash
make test SERVICE=room-service && make lint
```

Expected: both PASS / 0 issues.

- [ ] **Step 5 — Commit**

```bash
git add room-service/handler.go room-service/handler_test.go
git commit -m "feat(room-service): plumb req.Enrich through to store.ListRoomMembers"
```

---

## Part 4 — Mongo enrichment implementation

Replace the `_ = enrich` guards with real logic in both helpers. Integration-test driven: the enrichment integration tests land in Part 5 and are the "red" state that drives this Part's "green". For Part 4 alone we ship the implementation with a passing-by-construction review path (unit + lint + build).

### Task 4.1 — Enrichment for the `subscriptions` fallback (`getRoomSubscriptions`)

Simpler path (no aggregation `$lookup`; just a batch user lookup in Go). Landing this first lets us validate the display-field wiring end-to-end before tackling the aggregation pipeline.

**File:**
- Modify: `room-service/store_mongo.go`

- [ ] **Step 1 — Replace the guard with real enrichment logic**

Find the current `getRoomSubscriptions` body. After Part 2 it looks like:

```go
func (s *MongoStore) getRoomSubscriptions(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    _ = enrich // TODO(part-4): batch-load users + set IsOwner when true
    opts := options.Find().SetSort(bson.D{
        {Key: "joinedAt", Value: 1},
        {Key: "_id", Value: 1},
    })
    if offset != nil {
        opts.SetSkip(int64(*offset))
    }
    if limit != nil {
        opts.SetLimit(int64(*limit))
    }
    cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID}, opts)
    if err != nil {
        return nil, fmt.Errorf("find subscriptions for %q: %w", roomID, err)
    }
    defer cursor.Close(ctx)

    var subs []model.Subscription
    if err := cursor.All(ctx, &subs); err != nil {
        return nil, fmt.Errorf("decode subscriptions for %q: %w", roomID, err)
    }

    members := make([]model.RoomMember, 0, len(subs))
    for i := range subs {
        sub := &subs[i]
        members = append(members, model.RoomMember{
            ID:     sub.ID,
            RoomID: roomID,
            Ts:     sub.JoinedAt,
            Member: model.RoomMemberEntry{
                ID:      sub.User.ID,
                Type:    model.RoomMemberIndividual,
                Account: sub.User.Account,
            },
        })
    }
    return members, nil
}
```

Replace the body entirely with:

```go
func (s *MongoStore) getRoomSubscriptions(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    opts := options.Find().SetSort(bson.D{
        {Key: "joinedAt", Value: 1},
        {Key: "_id", Value: 1},
    })
    if offset != nil && *offset > 0 {
        opts.SetSkip(int64(*offset))
    }
    // SetLimit(0) means "no limit" in the driver, which would silently return
    // unbounded results. Only set when >0 so it matches the aggregation path.
    if limit != nil && *limit > 0 {
        opts.SetLimit(int64(*limit))
    }
    cursor, err := s.subscriptions.Find(ctx, bson.M{"roomId": roomID}, opts)
    if err != nil {
        return nil, fmt.Errorf("find subscriptions for %q: %w", roomID, err)
    }
    defer cursor.Close(ctx)

    var subs []model.Subscription
    if err := cursor.All(ctx, &subs); err != nil {
        return nil, fmt.Errorf("decode subscriptions for %q: %w", roomID, err)
    }

    members := make([]model.RoomMember, 0, len(subs))
    for i := range subs {
        sub := &subs[i]
        entry := model.RoomMemberEntry{
            ID:      sub.User.ID,
            Type:    model.RoomMemberIndividual,
            Account: sub.User.Account,
        }
        if enrich {
            entry.IsOwner = hasRoomOwnerRole(sub.Roles)
        }
        members = append(members, model.RoomMember{
            ID:     sub.ID,
            RoomID: roomID,
            Ts:     sub.JoinedAt,
            Member: entry,
        })
    }

    if enrich && len(members) > 0 {
        if err := s.attachUserDisplayNames(ctx, roomID, members); err != nil {
            return nil, err
        }
    }
    return members, nil
}

// hasRoomOwnerRole returns true when roles contains model.RoleOwner.
func hasRoomOwnerRole(roles []model.Role) bool {
    for _, r := range roles {
        if r == model.RoleOwner {
            return true
        }
    }
    return false
}

// attachUserDisplayNames batch-loads users for all individual members in the
// slice and copies EngName / ChineseName onto each member entry in place.
// Used only on the subscriptions-fallback + enrichment path.
func (s *MongoStore) attachUserDisplayNames(ctx context.Context, roomID string, members []model.RoomMember) error {
    accounts := make([]string, 0, len(members))
    for i := range members {
        if members[i].Member.Type == model.RoomMemberIndividual && members[i].Member.Account != "" {
            accounts = append(accounts, members[i].Member.Account)
        }
    }
    if len(accounts) == 0 {
        return nil
    }
    cursor, err := s.users.Find(ctx,
        bson.M{"account": bson.M{"$in": accounts}},
        options.Find().SetProjection(bson.M{"account": 1, "engName": 1, "chineseName": 1}),
    )
    if err != nil {
        return fmt.Errorf("find users for %q: %w", roomID, err)
    }
    defer cursor.Close(ctx)

    var users []model.User
    if err := cursor.All(ctx, &users); err != nil {
        return fmt.Errorf("decode users for %q: %w", roomID, err)
    }
    byAccount := make(map[string]*model.User, len(users))
    for i := range users {
        byAccount[users[i].Account] = &users[i]
    }
    for i := range members {
        if u, ok := byAccount[members[i].Member.Account]; ok {
            members[i].Member.EngName = u.EngName
            members[i].Member.ChineseName = u.ChineseName
        }
    }
    return nil
}
```

Note: `MongoStore` already has a `users` collection field from main (added by the add-member feature). Confirm with `grep 'users.*\*mongo.Collection' room-service/store_mongo.go`. If missing, add the field to the `MongoStore` struct and wire it in `NewMongoStore`, mirroring how `room-worker`'s `MongoStore` already does it:

```go
type MongoStore struct {
    rooms         *mongo.Collection
    subscriptions *mongo.Collection
    roomMembers   *mongo.Collection
    users         *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
    return &MongoStore{
        rooms:         db.Collection("rooms"),
        subscriptions: db.Collection("subscriptions"),
        roomMembers:   db.Collection("room_members"),
        users:         db.Collection("users"),
    }
}
```

- [ ] **Step 2 — Build; confirm no breakage**

```bash
go build ./room-service/... 2>&1 | head -10
```

Expected: clean build.

- [ ] **Step 3 — Run unit tests**

```bash
go test -race ./room-service/...
```

Expected: PASS. Existing unit tests still pass because they don't exercise the fallback path (they use mocks). The enrichment behavior is verified by the integration tests added in Part 5.

- [ ] **Step 4 — Commit**

```bash
git add room-service/store_mongo.go
git commit -m "feat(room-service): enrich subscriptions fallback with user display names + isOwner"
```

---

### Task 4.2 — Enrichment for the `room_members` aggregation path (`getRoomMembers`)

Append enrichment `$lookup` + `$set` stages when `enrich=true`. The stages attach display values to a parallel `display` field on each document (not inside `member`, since `member.*` display keys would be stripped by `bson:"-"` on decode). Post-process in Go to copy values from `display.*` onto `Member.*`.

**File:**
- Modify: `room-service/store_mongo.go`

- [ ] **Step 1 — Replace the guard with real enrichment logic**

Find the current `getRoomMembers` body. After Part 2 it looks like:

```go
func (s *MongoStore) getRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    _ = enrich // TODO(part-4): append enrichment stages when true
    pipeline := mongo.Pipeline{
        bson.D{{Key: "$match", Value: bson.M{"rid": roomID}}},
        bson.D{{Key: "$addFields", Value: bson.M{
            "typeOrder": bson.M{"$cond": bson.A{
                bson.M{"$eq": bson.A{"$member.type", "org"}}, 0, 1,
            }},
        }}},
        bson.D{{Key: "$sort", Value: bson.D{
            {Key: "typeOrder", Value: 1},
            {Key: "ts", Value: 1},
            {Key: "_id", Value: 1},
        }}},
        bson.D{{Key: "$project", Value: bson.M{"typeOrder": 0}}},
    }
    if offset != nil {
        pipeline = append(pipeline, bson.D{{Key: "$skip", Value: int64(*offset)}})
    }
    if limit != nil {
        pipeline = append(pipeline, bson.D{{Key: "$limit", Value: int64(*limit)}})
    }

    cursor, err := s.roomMembers.Aggregate(ctx, pipeline)
    if err != nil {
        return nil, fmt.Errorf("aggregate room_members for %q: %w", roomID, err)
    }
    defer cursor.Close(ctx)

    members := []model.RoomMember{}
    if err := cursor.All(ctx, &members); err != nil {
        return nil, fmt.Errorf("decode room_members for %q: %w", roomID, err)
    }
    return members, nil
}
```

Replace the body entirely with:

```go
func (s *MongoStore) getRoomMembers(ctx context.Context, roomID string, limit, offset *int, enrich bool) ([]model.RoomMember, error) {
    pipeline := mongo.Pipeline{
        bson.D{{Key: "$match", Value: bson.M{"rid": roomID}}},
        bson.D{{Key: "$addFields", Value: bson.M{
            "typeOrder": bson.M{"$cond": bson.A{
                bson.M{"$eq": bson.A{"$member.type", "org"}}, 0, 1,
            }},
        }}},
        bson.D{{Key: "$sort", Value: bson.D{
            {Key: "typeOrder", Value: 1},
            {Key: "ts", Value: 1},
            {Key: "_id", Value: 1},
        }}},
    }
    if offset != nil && *offset > 0 {
        pipeline = append(pipeline, bson.D{{Key: "$skip", Value: int64(*offset)}})
    }
    // Mongo rejects {$limit: 0}; the handler guards against <=0 but we
    // defend here too so the store is robust to direct internal callers.
    if limit != nil && *limit > 0 {
        pipeline = append(pipeline, bson.D{{Key: "$limit", Value: int64(*limit)}})
    }

    if enrich {
        pipeline = append(pipeline, enrichRoomMembersStages(roomID)...)
    }

    // Drop the helper typeOrder field last so it never leaks into the result.
    pipeline = append(pipeline, bson.D{{Key: "$project", Value: bson.M{"typeOrder": 0}}})

    cursor, err := s.roomMembers.Aggregate(ctx, pipeline)
    if err != nil {
        return nil, fmt.Errorf("aggregate room_members for %q: %w", roomID, err)
    }
    defer cursor.Close(ctx)

    if !enrich {
        members := []model.RoomMember{}
        if err := cursor.All(ctx, &members); err != nil {
            return nil, fmt.Errorf("decode room_members for %q: %w", roomID, err)
        }
        return members, nil
    }

    // Enriched path: decode into a hybrid row type that carries a parallel
    // `display` sub-document (the aggregation writes values there to sidestep
    // the bson:"-" tags on RoomMemberEntry's display fields). Then copy the
    // display values onto Member.* in Go memory, where bson:"-" is irrelevant.
    var rows []roomMemberEnrichedRow
    if err := cursor.All(ctx, &rows); err != nil {
        return nil, fmt.Errorf("decode enriched room_members for %q: %w", roomID, err)
    }
    members := make([]model.RoomMember, 0, len(rows))
    for i := range rows {
        rm := rows[i].RoomMember
        d := rows[i].Display
        rm.Member.EngName = d.EngName
        rm.Member.ChineseName = d.ChineseName
        rm.Member.IsOwner = d.IsOwner
        rm.Member.SectName = d.SectName
        rm.Member.MemberCount = d.MemberCount
        members = append(members, rm)
    }
    return members, nil
}

// roomMemberEnrichedRow is the decode target for the enriched aggregation
// pipeline. It carries the standard RoomMember plus a parallel `display`
// sub-document populated by enrichment stages. This exists because
// RoomMemberEntry's display fields are tagged bson:"-" for persistence
// safety — the pipeline therefore writes enrichment values to a separate
// field that has normal bson tags, and Go-side post-processing copies
// them onto RoomMemberEntry.
type roomMemberEnrichedRow struct {
    model.RoomMember `bson:",inline"`
    Display          roomMemberEnrichedDisplay `bson:"display"`
}

type roomMemberEnrichedDisplay struct {
    EngName     string `bson:"engName,omitempty"`
    ChineseName string `bson:"chineseName,omitempty"`
    IsOwner     bool   `bson:"isOwner,omitempty"`
    SectName    string `bson:"sectName,omitempty"`
    MemberCount int    `bson:"memberCount,omitempty"`
}

// enrichRoomMembersStages returns the $lookup + $set stages appended to the
// room_members aggregation when enrich=true. Each stage matches on
// member.type via $expr so it only fires for rows of the appropriate kind.
// All enrichment output is written into a `display` sub-document so it
// survives the RoomMemberEntry bson:"-" tags on decode.
func enrichRoomMembersStages(roomID string) []bson.D {
    return []bson.D{
        // Individuals: join users on account → pull engName / chineseName.
        {{Key: "$lookup", Value: bson.M{
            "from": "users",
            "let": bson.M{
                "acct": "$member.account",
                "mtyp": "$member.type",
            },
            "pipeline": bson.A{
                bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
                    bson.M{"$eq": bson.A{"$$mtyp", "individual"}},
                    bson.M{"$eq": bson.A{"$account", "$$acct"}},
                }}}},
                bson.M{"$limit": 1},
                bson.M{"$project": bson.M{"engName": 1, "chineseName": 1, "_id": 0}},
            },
            "as": "_userMatch",
        }}},
        // Individuals: join subscriptions on (roomId, u.account) → pull roles.
        {{Key: "$lookup", Value: bson.M{
            "from": "subscriptions",
            "let": bson.M{
                "acct": "$member.account",
                "mtyp": "$member.type",
            },
            "pipeline": bson.A{
                bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
                    bson.M{"$eq": bson.A{"$$mtyp", "individual"}},
                    bson.M{"$eq": bson.A{"$roomId", roomID}},
                    bson.M{"$eq": bson.A{"$u.account", "$$acct"}},
                }}}},
                bson.M{"$limit": 1},
                bson.M{"$project": bson.M{"roles": 1, "_id": 0}},
            },
            "as": "_subMatch",
        }}},
        // Orgs: join users on sectId = member.id → sectName + count.
        {{Key: "$lookup", Value: bson.M{
            "from": "users",
            "let": bson.M{
                "orgId": "$member.id",
                "mtyp":  "$member.type",
            },
            "pipeline": bson.A{
                bson.M{"$match": bson.M{"$expr": bson.M{"$and": bson.A{
                    bson.M{"$eq": bson.A{"$$mtyp", "org"}},
                    bson.M{"$eq": bson.A{"$sectId", "$$orgId"}},
                }}}},
                // $first:$sectName relies on the invariant that all users
                // sharing a sectId carry the same sectName; if that ever drifts,
                // the chosen name is non-deterministic without an upstream $sort.
                bson.M{"$group": bson.M{
                    "_id":         nil,
                    "sectName":    bson.M{"$first": "$sectName"},
                    "memberCount": bson.M{"$sum": 1},
                }},
            },
            "as": "_orgMatch",
        }}},
        // Fold the three matches into a single `display` sub-document.
        {{Key: "$set", Value: bson.M{
            "display": bson.M{
                "engName":     bson.M{"$arrayElemAt": bson.A{"$_userMatch.engName", 0}},
                "chineseName": bson.M{"$arrayElemAt": bson.A{"$_userMatch.chineseName", 0}},
                "isOwner": bson.M{"$in": bson.A{
                    "owner",
                    bson.M{"$ifNull": bson.A{
                        bson.M{"$arrayElemAt": bson.A{"$_subMatch.roles", 0}},
                        bson.A{},
                    }},
                }},
                "sectName":    bson.M{"$arrayElemAt": bson.A{"$_orgMatch.sectName", 0}},
                "memberCount": bson.M{"$arrayElemAt": bson.A{"$_orgMatch.memberCount", 0}},
            },
        }}},
        // Drop the temporary join arrays.
        {{Key: "$project", Value: bson.M{"_userMatch": 0, "_subMatch": 0, "_orgMatch": 0}}},
    }
}
```

- [ ] **Step 2 — Build; confirm clean compile**

```bash
go build ./room-service/... 2>&1 | head -10
```

Expected: clean build.

- [ ] **Step 3 — Run unit tests + lint**

```bash
make test SERVICE=room-service && make lint
```

Expected: PASS / 0 issues. Unit tests still pass because they use mocks; aggregation correctness is covered by Part 5's integration tests.

- [ ] **Step 4 — Commit**

```bash
git add room-service/store_mongo.go
git commit -m "feat(room-service): enrich room_members aggregation with users and subscriptions lookups"
```

---

## Part 5 — Integration tests for enrichment + final verification

Exercises both enrichment paths against a real Mongo testcontainer. Tests verify display values end-to-end (pipeline → Go post-processing → caller sees populated `RoomMember.Member.*`).

### Task 5.1 — Integration tests for enrichment

**File:**
- Modify: `room-service/integration_test.go`

- [ ] **Step 1 — Add the new integration test function**

Append this function to `room-service/integration_test.go`:

```go
func TestMongoStore_ListRoomMembers_Enrich_Integration(t *testing.T) {
    ctx := context.Background()

    insertRM := func(t *testing.T, db *mongo.Database, rm model.RoomMember) {
        t.Helper()
        _, err := db.Collection("room_members").InsertOne(ctx, rm)
        require.NoError(t, err)
    }
    insertUser := func(t *testing.T, db *mongo.Database, u model.User) {
        t.Helper()
        _, err := db.Collection("users").InsertOne(ctx, u)
        require.NoError(t, err)
    }
    insertSub := func(t *testing.T, store *MongoStore, sub model.Subscription) {
        t.Helper()
        require.NoError(t, store.CreateSubscription(ctx, &sub))
    }
    ptr := func(i int) *int { return &i }

    t.Run("individual enrichment via room_members path", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

        insertUser(t, db, model.User{
            ID: "u-alice", Account: "alice", SiteID: "site-a",
            EngName: "Alice Wang", ChineseName: "愛麗絲",
        })
        insertSub(t, store, model.Subscription{
            ID: "sub-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
            RoomID: "r1", SiteID: "site-a",
            Roles: []model.Role{model.RoleOwner}, JoinedAt: base,
        })
        insertRM(t, db, model.RoomMember{
            ID: "rm-alice", RoomID: "r1", Ts: base,
            Member: model.RoomMemberEntry{ID: "u-alice", Type: model.RoomMemberIndividual, Account: "alice"},
        })

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
        require.NoError(t, err)
        require.Len(t, got, 1)
        m := got[0].Member
        assert.Equal(t, "Alice Wang", m.EngName)
        assert.Equal(t, "愛麗絲", m.ChineseName)
        assert.True(t, m.IsOwner)
        assert.Empty(t, m.SectName)
        assert.Zero(t, m.MemberCount)
    })

    t.Run("individual non-owner sets IsOwner false", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

        insertUser(t, db, model.User{ID: "u-bob", Account: "bob", EngName: "Bob", ChineseName: "鮑伯"})
        insertSub(t, store, model.Subscription{
            ID: "sub-bob", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"},
            RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: base,
        })
        insertRM(t, db, model.RoomMember{
            ID: "rm-bob", RoomID: "r1", Ts: base,
            Member: model.RoomMemberEntry{ID: "u-bob", Type: model.RoomMemberIndividual, Account: "bob"},
        })

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
        require.NoError(t, err)
        require.Len(t, got, 1)
        assert.False(t, got[0].Member.IsOwner)
    })

    t.Run("org enrichment via room_members path", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

        // 3 users share sectId=sect-eng with sectName="Engineering".
        for i, acct := range []string{"a", "b", "c"} {
            insertUser(t, db, model.User{
                ID: fmt.Sprintf("u-%d", i), Account: acct,
                SectID: "sect-eng", SectName: "Engineering",
            })
        }
        insertRM(t, db, model.RoomMember{
            ID: "rm-org", RoomID: "r1", Ts: base,
            Member: model.RoomMemberEntry{ID: "sect-eng", Type: model.RoomMemberOrg},
        })

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
        require.NoError(t, err)
        require.Len(t, got, 1)
        m := got[0].Member
        assert.Equal(t, model.RoomMemberOrg, m.Type)
        assert.Equal(t, "Engineering", m.SectName)
        assert.Equal(t, 3, m.MemberCount)
        assert.Empty(t, m.EngName)
        assert.False(t, m.IsOwner)
    })

    t.Run("enrichment via subscriptions fallback", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)

        insertUser(t, db, model.User{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"})
        insertUser(t, db, model.User{ID: "u-bob", Account: "bob", EngName: "Bob", ChineseName: "鮑伯"})
        insertSub(t, store, model.Subscription{
            ID: "sub-a", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
            RoomID: "r1", Roles: []model.Role{model.RoleOwner}, JoinedAt: base.Add(10 * time.Second),
        })
        insertSub(t, store, model.Subscription{
            ID: "sub-b", User: model.SubscriptionUser{ID: "u-bob", Account: "bob"},
            RoomID: "r1", Roles: []model.Role{model.RoleMember}, JoinedAt: base.Add(20 * time.Second),
        })
        // Note: NO room_members docs inserted — exercises the fallback path.

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
        require.NoError(t, err)
        require.Len(t, got, 2)

        alice, bob := got[0].Member, got[1].Member
        assert.Equal(t, "alice", alice.Account)
        assert.Equal(t, "Alice Wang", alice.EngName)
        assert.True(t, alice.IsOwner)
        assert.Equal(t, "bob", bob.Account)
        assert.Equal(t, "Bob", bob.EngName)
        assert.False(t, bob.IsOwner)
    })

    t.Run("enrich=false leaves display fields zero on same seed data", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)

        insertUser(t, db, model.User{ID: "u-alice", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"})
        insertSub(t, store, model.Subscription{
            ID: "sub-alice", User: model.SubscriptionUser{ID: "u-alice", Account: "alice"},
            RoomID: "r1", Roles: []model.Role{model.RoleOwner}, JoinedAt: base,
        })
        insertRM(t, db, model.RoomMember{
            ID: "rm-alice", RoomID: "r1", Ts: base,
            Member: model.RoomMemberEntry{ID: "u-alice", Type: model.RoomMemberIndividual, Account: "alice"},
        })

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, false)
        require.NoError(t, err)
        require.Len(t, got, 1)
        m := got[0].Member
        assert.Empty(t, m.EngName)
        assert.Empty(t, m.ChineseName)
        assert.False(t, m.IsOwner)
        assert.Empty(t, m.SectName)
        assert.Zero(t, m.MemberCount)
    })

    t.Run("enrich=true preserves sort and pagination", func(t *testing.T) {
        db := setupMongo(t)
        store := NewMongoStore(db)
        base := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)

        // Seed: 2 individuals + 1 org in room_members; users for each.
        insertUser(t, db, model.User{ID: "u-a", Account: "a", EngName: "A"})
        insertUser(t, db, model.User{ID: "u-b", Account: "b", EngName: "B"})
        insertUser(t, db, model.User{ID: "u-c", Account: "c", SectID: "sect-c", SectName: "C"})
        insertRM(t, db, model.RoomMember{ID: "rm-a", RoomID: "r1", Ts: base.Add(10 * time.Second),
            Member: model.RoomMemberEntry{ID: "u-a", Type: model.RoomMemberIndividual, Account: "a"}})
        insertRM(t, db, model.RoomMember{ID: "rm-b", RoomID: "r1", Ts: base.Add(20 * time.Second),
            Member: model.RoomMemberEntry{ID: "u-b", Type: model.RoomMemberIndividual, Account: "b"}})
        insertRM(t, db, model.RoomMember{ID: "rm-org", RoomID: "r1", Ts: base.Add(30 * time.Second),
            Member: model.RoomMemberEntry{ID: "sect-c", Type: model.RoomMemberOrg}})

        got, err := store.ListRoomMembers(ctx, "r1", nil, nil, true)
        require.NoError(t, err)
        require.Len(t, got, 3)
        // Orgs first, then individuals by ts ascending.
        assert.Equal(t, model.RoomMemberOrg, got[0].Member.Type)
        assert.Equal(t, "a", got[1].Member.Account)
        assert.Equal(t, "b", got[2].Member.Account)

        // Pagination is applied before enrichment — paging to the same slice
        // with enrich=true and enrich=false must yield the same members (by ID).
        bare, err := store.ListRoomMembers(ctx, "r1", ptr(1), ptr(1), false)
        require.NoError(t, err)
        enriched, err := store.ListRoomMembers(ctx, "r1", ptr(1), ptr(1), true)
        require.NoError(t, err)
        require.Len(t, bare, 1)
        require.Len(t, enriched, 1)
        assert.Equal(t, bare[0].ID, enriched[0].ID)
        assert.Equal(t, bare[0].Member.Type, enriched[0].Member.Type)
    })
}
```

- [ ] **Step 2 — Run the new integration tests**

```bash
go test -race -tags integration -count=1 ./room-service/... -run TestMongoStore_ListRoomMembers_Enrich_Integration -v
```

Expected: all 6 subtests PASS. If Docker is not running, start `dockerd --storage-driver=vfs` first.

- [ ] **Step 3 — Run the full room-service integration suite (non-regression)**

```bash
go test -race -tags integration -count=1 ./room-service/...
```

Expected: PASS (existing 8 `TestMongoStore_ListRoomMembers_Integration` subtests + 6 new enrichment subtests + all pre-existing room-service integration tests).

- [ ] **Step 4 — Commit**

```bash
git add room-service/integration_test.go
git commit -m "test(room-service): integration tests for ListRoomMembers enrichment paths"
```

---

### Task 5.2 — Final verification

- [ ] **Step 1 — Lint**

```bash
make lint
```

Expected: 0 issues.

- [ ] **Step 2 — Full unit suite**

```bash
make test
```

Expected: PASS across the whole repo.

- [ ] **Step 3 — Coverage check on enrichment code paths**

```bash
go test -race -coverprofile=/tmp/rs.out ./room-service/...
go tool cover -func=/tmp/rs.out | grep -E "getRoomMembers|getRoomSubscriptions|attachUserDisplayNames|hasRoomOwnerRole|enrichRoomMembersStages"
```

Expected: each function ≥ 80% (CLAUDE.md §4 threshold).

- [ ] **Step 4 — Integration coverage**

```bash
go test -race -tags integration -coverprofile=/tmp/rs-int.out ./room-service/...
go tool cover -func=/tmp/rs-int.out | grep -E "getRoomMembers|getRoomSubscriptions|attachUserDisplayNames|enrichRoomMembersStages"
```

Expected: ≥ 90% on each (integration coverage is where the enrichment code is exercised).

- [ ] **Step 5 — Push**

```bash
git push -u origin claude/add-room-member-feature-5FPbS
```

No force-push needed — the enrichment commits sit on top of the existing branch history.

---

## Appendix — Coding rules reminder (from CLAUDE.md)

- Error wrapping: `fmt.Errorf("short description: %w", err)` — never bare err, never "error: %w".
- `errors.Is` / `errors.As` only — never string comparison.
- `log/slog` JSON with structured fields — never `fmt.Println` or interpolated strings.
- Mocks live in `mock_store_test.go`, regenerated via `make generate SERVICE=room-service` whenever `store.go` changes.
- `//go:build integration` tag on integration tests; use `testcontainers-go`.
- Unit tests must not touch Mongo / NATS; use `MockRoomStore` with gomock.
- Always run with `-race` via `make test` / `make test-integration`.
- Minimum 80% coverage, target 90%+ on handlers and store.

## Appendix — Commit map

| Task | Commit subject |
|------|----------------|
| 1.1 | `feat(model): add Enrich flag to ListRoomMembersRequest` |
| 1.2 | `feat(model): add display fields to RoomMemberEntry (bson:"-", json omitempty)` |
| 2.1–2.5 | `refactor(room-service): thread enrich bool through ListRoomMembers signature` |
| 3.1–3.2 | `feat(room-service): plumb req.Enrich through to store.ListRoomMembers` |
| 4.1 | `feat(room-service): enrich subscriptions fallback with user display names + isOwner` |
| 4.2 | `feat(room-service): enrich room_members aggregation with users and subscriptions lookups` |
| 5.1 | `test(room-service): integration tests for ListRoomMembers enrichment paths` |
| 5.2 | (no commit unless coverage top-up tests are added) |
