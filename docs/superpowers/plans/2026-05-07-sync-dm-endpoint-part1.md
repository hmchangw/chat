# Sync Server-to-Server DM Endpoint — Part 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a synchronous server-to-server NATS request/reply endpoint hosted by `room-worker` that creates a DM or botDM (Room + 2 Subscriptions), emits the same events the async path emits, and replies with the requester's `Subscription`.

**Architecture:** New NATS subject `chat.server.request.room.{siteID}.create.dm` (queue group `room-worker`, server credentials). Handler is persistence-only — caller (user-service) is responsible for all data-integrity validation per the spec's caller-side validation contract. room-worker resolves User records via `GetUser` for data (User.ID, User.SiteID), persists Room + 2 subs, emits `subscription.update` events and cross-site `room_created` outbox event, and replies inline with the requester's Subscription. Idempotent against retries and concurrent races via duplicate-key fallback at insert time.

**Tech Stack:** Go 1.25, NATS (request/reply via core NATS — not JetStream), MongoDB driver v2, `go.uber.org/mock` (mockgen), `stretchr/testify`, `testcontainers-go`.

**Source spec:** `docs/superpowers/specs/2026-05-05-sync-dm-and-historyconfig-design.md` (Part 1).

**Out of scope (in this plan):** Part 2 (HistoryConfig.SharedSince removal) — separate plan.

---

## File Map

| Path | Change | Responsibility |
|------|--------|----------------|
| `pkg/model/member.go` | modify | New `SyncCreateDMRequest` and `SyncCreateDMReply` types |
| `pkg/model/model_test.go` | modify | JSON round-trip for new types |
| `pkg/subject/subject.go` | modify | New `RoomCreateDMSync(siteID)` builder |
| `pkg/subject/subject_test.go` | modify | Round-trip test for new subject |
| `room-worker/store.go` | modify | Add `FindDMSubscription` to `SubscriptionStore` interface |
| `room-worker/store_mongo.go` | modify | Mongo impl of `FindDMSubscription` |
| `room-worker/mock_store_test.go` | regenerate | Auto-generated; via `make generate SERVICE=room-worker` |
| `room-worker/handler.go` | modify | Extract `buildDMSubs` / `buildBotDMSubs` from `processCreateRoomDM` / `processCreateRoomBotDM` (pure code motion, no behavior change) |
| `room-worker/handler_test.go` | modify | Existing tests continue to pass; no test changes needed (verification only) |
| `room-worker/handler_sync_create_dm.go` | new | `natsServerCreateDM` handler + `handleSyncCreateDM` business logic + sentinels + `sanitizeSyncDMError` |
| `room-worker/handler_sync_create_dm_test.go` | new | Unit tests with mocked store |
| `room-worker/main.go` | modify | `NewHandler` plumbing for the new path; add `nc.QueueSubscribe` for the new subject; ensure `nc.Drain()` is in the existing shutdown chain (no new step needed since core NATS subscriptions are drained by the existing `nc.Drain()` callback) |
| `room-worker/integration_test.go` | modify | Add `TestSyncCreateDM_*` integration tests |

---

## Task 1: Add `SyncCreateDMRequest` and `SyncCreateDMReply` types

**Files:**
- Modify: `pkg/model/member.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/model/model_test.go` (anywhere in the file is fine — keep near other small JSON-roundtrip tests):

```go
func TestSyncCreateDMRequestJSON(t *testing.T) {
	src := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	b, err := json.Marshal(src)
	require.NoError(t, err)

	// Expect exact lower-camelCase keys.
	assert.JSONEq(t, `{"roomType":"dm","requesterAccount":"alice","otherAccount":"bob"}`, string(b))

	var dst model.SyncCreateDMRequest
	require.NoError(t, json.Unmarshal(b, &dst))
	assert.Equal(t, src, dst)
}

func TestSyncCreateDMReplyJSON(t *testing.T) {
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	src := model.SyncCreateDMReply{
		Success: true,
		Subscription: model.Subscription{
			ID:           "sub1",
			User:         model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID:       "room1",
			SiteID:       "site-a",
			Name:         "bob",
			RoomType:     model.RoomTypeDM,
			IsSubscribed: true,
			JoinedAt:     now,
		},
	}
	b, err := json.Marshal(src)
	require.NoError(t, err)

	var dst model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(b, &dst))
	assert.True(t, dst.Success)
	assert.Equal(t, src.Subscription.ID, dst.Subscription.ID)
	assert.Equal(t, src.Subscription.User, dst.Subscription.User)
	assert.Equal(t, src.Subscription.RoomID, dst.Subscription.RoomID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test -run 'TestSyncCreateDM(Request|Reply)JSON' ./pkg/model/...`

Expected: FAIL with `undefined: model.SyncCreateDMRequest` / `undefined: model.SyncCreateDMReply`.

- [ ] **Step 3: Add the types to `pkg/model/member.go`**

Append to `pkg/model/member.go` (after the existing types, before any closing trailing whitespace):

```go
// SyncCreateDMRequest is the request payload for chat.server.request.room.{siteID}.create.dm.
// Caller (user-service) MUST perform all data-integrity validation before issuing this request:
// existence checks, EngName/ChineseName checks, bot Assistant.Enabled, self-DM rejection, and
// dedup-existing via FindDMSubscription. room-worker performs only request-shape sanity checks
// and resolves User records via GetUser for data (User.ID, User.SiteID).
type SyncCreateDMRequest struct {
	RoomType         RoomType `json:"roomType"`         // RoomTypeDM or RoomTypeBotDM
	RequesterAccount string   `json:"requesterAccount"` // requester's account
	OtherAccount     string   `json:"otherAccount"`     // counterpart account (user or bot)
}

// SyncCreateDMReply is the success reply payload. Errors are returned via the standard
// natsutil.ReplyError -> model.ErrorResponse path (NOT this envelope). On the wire,
// callers should TryParseError first, then unmarshal SyncCreateDMReply on success.
//
// Success is always true on this path; it is retained as an explicit-positive signal
// that's cheap to compare on the caller side. Subscription is the requester-scoped
// subscription (User.Account == request.RequesterAccount).
type SyncCreateDMReply struct {
	Success      bool         `json:"success"`
	Subscription Subscription `json:"subscription"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test -run 'TestSyncCreateDM(Request|Reply)JSON' ./pkg/model/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add pkg/model/member.go pkg/model/model_test.go
git commit -m "feat(pkg/model): add SyncCreateDMRequest and SyncCreateDMReply types"
```

---

## Task 2: Add `RoomCreateDMSync` subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Test: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write the failing test**

Append to `pkg/subject/subject_test.go`:

```go
func TestRoomCreateDMSync(t *testing.T) {
	got := subject.RoomCreateDMSync("site-a")
	assert.Equal(t, "chat.server.request.room.site-a.create.dm", got)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test -run TestRoomCreateDMSync ./pkg/subject/...`

Expected: FAIL with `undefined: subject.RoomCreateDMSync`.

- [ ] **Step 3: Add the builder to `pkg/subject/subject.go`**

Append to `pkg/subject/subject.go` (next to `RoomsInfoBatch` for grouping):

```go
// RoomCreateDMSync is the server-to-server request subject for synchronous DM/botDM creation.
// Subscribed by room-worker with queue group "room-worker"; requires server credentials per the
// chat.server.> NATS callout policy.
func RoomCreateDMSync(siteID string) string {
	return fmt.Sprintf("chat.server.request.room.%s.create.dm", siteID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./pkg/subject/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(pkg/subject): add RoomCreateDMSync builder"
```

---

## Task 3: Add `FindDMSubscription` to room-worker store interface and Mongo impl

**Background:** room-worker's store doesn't currently expose dedup-existing lookups. The sync handler needs `FindDMSubscription` for the duplicate-key redelivery branch (after a `BulkCreateSubscriptions` race fails). Implementation mirrors `room-service/store_mongo.go:627`.

**Files:**
- Modify: `room-worker/store.go`
- Modify: `room-worker/store_mongo.go`
- Regenerate: `room-worker/mock_store_test.go` (auto-generated)

- [ ] **Step 1: Add the interface method**

Edit `room-worker/store.go`. Inside the `SubscriptionStore` interface, near the other Subscription methods (`BulkCreateSubscriptions`, etc.), add:

```go
	// FindDMSubscription returns the requester's existing dm/botDM sub with Name == targetName.
	// Returns model.ErrSubscriptionNotFound when no match exists. Used only on the duplicate-key
	// redelivery branch of the sync DM handler — not on the happy path.
	FindDMSubscription(ctx context.Context, account, targetName string) (*model.Subscription, error)
```

- [ ] **Step 2: Add the Mongo implementation**

Append to `room-worker/store_mongo.go`. Confirm imports include `"go.mongodb.org/mongo-driver/v2/bson"` and `"go.mongodb.org/mongo-driver/v2/mongo"` (most are already present; add `"errors"` if not):

```go
// FindDMSubscription returns the requester's existing dm/botDM sub with Name == targetName.
// Returns model.ErrSubscriptionNotFound on no match.
func (s *MongoStore) FindDMSubscription(ctx context.Context, account, targetName string) (*model.Subscription, error) {
	var sub model.Subscription
	err := s.subscriptions.FindOne(ctx, bson.M{
		"u.account": account,
		"name":      targetName,
		"roomType":  bson.M{"$in": []model.RoomType{model.RoomTypeDM, model.RoomTypeBotDM}},
	}).Decode(&sub)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, model.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find dm subscription: %w", err)
	}
	return &sub, nil
}
```

> **Note:** Verify the MongoStore struct already has a `subscriptions` field. If it's named differently in `store_mongo.go` (look near the top of the file for `type MongoStore struct`), match the existing field name.

- [ ] **Step 3: Regenerate the mock**

Run: `cd /home/user/chat && make generate SERVICE=room-worker`

This regenerates `room-worker/mock_store_test.go` to include `FindDMSubscription`.

- [ ] **Step 4: Verify build still compiles**

Run: `cd /home/user/chat && go build ./room-worker/...`

Expected: no errors.

- [ ] **Step 5: Verify existing tests still pass**

Run: `cd /home/user/chat && make test SERVICE=room-worker`

Expected: PASS (no behavior change yet — new method is unused).

- [ ] **Step 6: Commit**

```bash
cd /home/user/chat
git add room-worker/store.go room-worker/store_mongo.go room-worker/mock_store_test.go
git commit -m "feat(room-worker): add FindDMSubscription to SubscriptionStore"
```

---

## Task 4: Refactor — extract `buildDMSubs` and `buildBotDMSubs` helpers

**Background:** The async path's `processCreateRoomDM` (`room-worker/handler.go:934`) and `processCreateRoomBotDM` (`room-worker/handler.go:953`) build the subscription pair inline. Extract those two `[]*model.Subscription{...}` literals into named helpers so the new sync handler can call them. **Pure code motion — no behavior change.** Existing tests must continue to pass without modification.

**Files:**
- Modify: `room-worker/handler.go`
- Verify: `room-worker/handler_test.go` (no changes — existing `TestProcessCreateRoomDM*` / `TestProcessCreateRoomBotDM*` should still pass)

- [ ] **Step 1: Extract `buildDMSubs`**

Edit `room-worker/handler.go`. Add this helper near `newSub` (around line 824), above `processCreateRoom`:

```go
// buildDMSubs returns the two subscriptions for a DM: requester's sub names the other,
// other's sub names the requester. Both have IsSubscribed=false (matches existing behavior).
func buildDMSubs(requester, other *model.User, room *model.Room, acceptedAt time.Time) []*model.Subscription {
	return []*model.Subscription{
		newSub(idgen.GenerateUUIDv7(), requester, room, nil, other.Account, false, acceptedAt),
		newSub(idgen.GenerateUUIDv7(), other, room, nil, requester.Account, false, acceptedAt),
	}
}

// buildBotDMSubs returns the two subscriptions for a botDM. Human's sub: Name=bot.Account,
// IsSubscribed=true. Bot's sub: Name=requester.Account, IsSubscribed=false.
func buildBotDMSubs(requester, bot *model.User, room *model.Room, acceptedAt time.Time) []*model.Subscription {
	return []*model.Subscription{
		newSub(idgen.GenerateUUIDv7(), requester, room, nil, bot.Account, true, acceptedAt),
		newSub(idgen.GenerateUUIDv7(), bot, room, nil, requester.Account, false, acceptedAt),
	}
}
```

- [ ] **Step 2: Update `processCreateRoomDM` to call `buildDMSubs`**

In `room-worker/handler.go:934`, replace the inline literal:

```go
	subs := []*model.Subscription{
		newSub(idgen.GenerateUUIDv7(), requester, room, nil, other.Account, false, acceptedAt),
		newSub(idgen.GenerateUUIDv7(), other, room, nil, requester.Account, false, acceptedAt),
	}
```

with:

```go
	subs := buildDMSubs(requester, other, room, acceptedAt)
```

- [ ] **Step 3: Update `processCreateRoomBotDM` to call `buildBotDMSubs`**

In `room-worker/handler.go:953`, replace the inline literal:

```go
	subs := []*model.Subscription{
		// Human's sub: Name = bot account; IsSubscribed = true
		newSub(idgen.GenerateUUIDv7(), requester, room, nil, bot.Account, true, acceptedAt),
		// Bot's sub: Name = requester's account; IsSubscribed = false
		newSub(idgen.GenerateUUIDv7(), bot, room, nil, requester.Account, false, acceptedAt),
	}
```

with:

```go
	subs := buildBotDMSubs(requester, bot, room, acceptedAt)
```

- [ ] **Step 4: Verify all existing room-worker tests still pass**

Run: `cd /home/user/chat && make test SERVICE=room-worker`

Expected: PASS — every existing test (notably `TestProcessCreateRoomDM*`, `TestProcessCreateRoomBotDM*`, `TestFinishCreateRoom*`) continues to pass without modification.

If any test fails, the extraction was not byte-equivalent. Re-read steps 1–3 and check that argument order matches `newSub`'s signature exactly.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler.go
git commit -m "refactor(room-worker): extract buildDMSubs/buildBotDMSubs helpers"
```

---

## Task 5: Add sentinels and `sanitizeSyncDMError` helper

**Background:** room-worker's existing handlers don't reply to clients (they emit AsyncJobResult). The sync handler is the first to call `natsutil.ReplyError`, so it needs its own sentinel set + sanitization helper.

**Files:**
- Create: `room-worker/handler_sync_create_dm.go`

- [ ] **Step 1: Write a unit test stub** (real tests come in later tasks; this just exercises the sanitizer)

Create `room-worker/handler_sync_create_dm_test.go` with the package declaration and one test:

```go
package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeSyncDMError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want string
	}{
		{"nil returns empty", nil, ""},
		{"missing request ID surfaced", errMissingRequestID, "missing X-Request-ID header"},
		{"invalid request ID surfaced", errInvalidRequestID, "invalid X-Request-ID header"},
		{"invalid sync DM request surfaced", errInvalidSyncDMRequest, "invalid sync DM request"},
		{"user lookup failed surfaced", errUserLookupFailed, "user lookup failed"},
		{"cross-site requester surfaced", errCrossSiteRequester, "requester is not on this site"},
		{"room ID collision surfaced", errRoomIDCollision, "room ID collision (existing room metadata mismatch)"},
		{"unknown error masked as internal", errors.New("mongo: connection refused"), "internal error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, sanitizeSyncDMError(tc.in))
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test -run TestSanitizeSyncDMError ./room-worker/...`

Expected: FAIL with `undefined: errMissingRequestID` (and the others).

- [ ] **Step 3: Create `room-worker/handler_sync_create_dm.go` with sentinels + sanitizer**

Create the new file with this initial content (more handler code lands in later tasks):

```go
package main

import (
	"errors"
)

var (
	errMissingRequestID     = errors.New("missing X-Request-ID header")
	errInvalidRequestID     = errors.New("invalid X-Request-ID header")
	errInvalidSyncDMRequest = errors.New("invalid sync DM request")
	errUserLookupFailed     = errors.New("user lookup failed")
	errCrossSiteRequester   = errors.New("requester is not on this site")
	errRoomIDCollision      = errors.New("room ID collision (existing room metadata mismatch)")
)

// sanitizeSyncDMError maps a handler error to a user-displayable string.
// Known sentinels surface their literal message; anything else becomes "internal error"
// to avoid leaking raw error text (e.g. mongo or NATS internals).
func sanitizeSyncDMError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, errMissingRequestID),
		errors.Is(err, errInvalidRequestID),
		errors.Is(err, errInvalidSyncDMRequest),
		errors.Is(err, errUserLookupFailed),
		errors.Is(err, errCrossSiteRequester),
		errors.Is(err, errRoomIDCollision):
		return err.Error()
	default:
		return "internal error"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test -run TestSanitizeSyncDMError ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): sync DM handler sentinels and sanitizer"
```

---

## Task 6: Implement `handleSyncCreateDM` — request shape validation

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests for shape validation**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
import (
	"context"
	"encoding/json"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// newRequestCtx returns a context carrying a syntactically-valid X-Request-ID.
func newRequestCtx() context.Context {
	return natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f")
}

func TestHandleSyncCreateDM_MissingRequestID(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(context.Background(), data)
	assert.ErrorIs(t, err, errMissingRequestID)
}

func TestHandleSyncCreateDM_InvalidJSON(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	_, err := h.handleSyncCreateDM(newRequestCtx(), []byte("{not json"))
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_InvalidRoomType(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeChannel, // invalid for sync DM
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}

func TestHandleSyncCreateDM_EmptyAccounts(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	cases := []model.SyncCreateDMRequest{
		{RoomType: model.RoomTypeDM, RequesterAccount: "", OtherAccount: "bob"},
		{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: ""},
	}
	for _, req := range cases {
		data, _ := json.Marshal(req)
		_, err := h.handleSyncCreateDM(newRequestCtx(), data)
		assert.ErrorIs(t, err, errInvalidSyncDMRequest)
	}
}

func TestHandleSyncCreateDM_SelfDM(t *testing.T) {
	h := &Handler{siteID: "site-a"}
	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "alice",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errInvalidSyncDMRequest)
}
```

> **Note on `natsutil.WithRequestID`:** This is already exported by `pkg/natsutil/request_id.go:17`. Use it directly — no new helper needed.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: FAIL — `handleSyncCreateDM` is undefined.

- [ ] **Step 3: Add the `handleSyncCreateDM` method with shape validation only**

Append to `room-worker/handler_sync_create_dm.go`:

```go
import (
	"context"
	"encoding/json"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// handleSyncCreateDM is the business logic for the sync DM endpoint. It takes the inbound
// request bytes and returns either the marshalled SyncCreateDMReply payload or an error.
// The natsServerCreateDM wrapper (Task 12) handles unwrapping the NATS msg and replying.
func (h *Handler) handleSyncCreateDM(ctx context.Context, data []byte) ([]byte, error) {
	requestID := natsutil.RequestIDFromContext(ctx)
	if requestID == "" {
		return nil, errMissingRequestID
	}
	if !idgen.IsValidUUID(requestID) {
		return nil, errInvalidRequestID
	}

	var req model.SyncCreateDMRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, errInvalidSyncDMRequest
	}
	if err := validateSyncCreateDMShape(&req); err != nil {
		return nil, err
	}

	// Subsequent steps (User resolution, persistence, events, reply) land in later tasks.
	// For now this is a stub that reaches no further than shape validation.
	return nil, errInvalidSyncDMRequest // placeholder — replaced in Task 7
}

func validateSyncCreateDMShape(req *model.SyncCreateDMRequest) error {
	switch req.RoomType {
	case model.RoomTypeDM, model.RoomTypeBotDM:
		// ok
	default:
		return errInvalidSyncDMRequest
	}
	if req.RequesterAccount == "" || req.OtherAccount == "" {
		return errInvalidSyncDMRequest
	}
	if req.RequesterAccount == req.OtherAccount {
		return errInvalidSyncDMRequest
	}
	return nil
}
```

> **Note on `idgen.IsValidUUID`:** confirm this function exists with `grep -n "func IsValidUUID\b" /home/user/chat/pkg/idgen/idgen.go`. The existing room-service code uses it (`room-service/handler.go:157`). If not available, fall back to `idgen.IsValidUUIDv7` per the project CLAUDE.md (which states 36-char hyphenated UUIDv7 is the request-ID format) and adjust accordingly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS for the shape-validation tests.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go pkg/natsutil/request_id.go
git commit -m "feat(room-worker): handleSyncCreateDM request-shape validation"
```

(Drop `pkg/natsutil/request_id.go` from the `git add` if you didn't need to touch it.)

---

## Task 7: Resolve User records and validate cross-site requester

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
import (
	"go.uber.org/mock/gomock"
)

func TestHandleSyncCreateDM_RequesterNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_OtherNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(nil, ErrUserNotFound)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errUserLookupFailed)
}

func TestHandleSyncCreateDM_CrossSiteRequester(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	// Requester's home is site-b, but this room-worker is site-a → routing bug.
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(&model.User{ID: "u-alice", Account: "alice", SiteID: "site-b"}, nil)

	req := model.SyncCreateDMRequest{
		RoomType:         model.RoomTypeDM,
		RequesterAccount: "alice",
		OtherAccount:     "bob",
	}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errCrossSiteRequester)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_(RequesterNotFound|OtherNotFound|CrossSiteRequester)' ./room-worker/...`

Expected: FAIL.

- [ ] **Step 3: Add User resolution to `handleSyncCreateDM`**

Edit `room-worker/handler_sync_create_dm.go`. Replace the placeholder return-line in `handleSyncCreateDM` with the User resolution block, and add an `errors` import:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)
```

Replace the `return nil, errInvalidSyncDMRequest // placeholder` line with:

```go
	requester, err := h.store.GetUser(ctx, req.RequesterAccount)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, errUserLookupFailed
		}
		return nil, fmt.Errorf("get requester: %w", errUserLookupFailed)
	}
	if requester.SiteID != h.siteID {
		return nil, errCrossSiteRequester
	}

	other, err := h.store.GetUser(ctx, req.OtherAccount)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, errUserLookupFailed
		}
		return nil, fmt.Errorf("get counterpart: %w", errUserLookupFailed)
	}

	// Suppress unused warnings until subsequent tasks consume these.
	_, _ = requester, other
	return nil, errInvalidSyncDMRequest // placeholder — replaced in Task 8
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): resolve User records in sync DM handler"
```

---

## Task 8: Persist Room with duplicate-key fallback

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
import (
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestHandleSyncCreateDM_RoomCollisionMismatch(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)

	dupErr := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(dupErr)
	// Existing room with mismatched Type → collision.
	store.EXPECT().GetRoom(gomock.Any(), gomock.Any()).Return(&model.Room{
		ID: idgen.BuildDMRoomID("u-alice", "u-bob"), Type: model.RoomTypeChannel,
		SiteID: "site-a", Name: "", CreatedBy: "u-alice",
	}, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	assert.ErrorIs(t, err, errRoomIDCollision)
}
```

> **Note:** Confirm `mongo.IsDuplicateKeyError` is used as the duplicate-key check (it's used in `processCreateRoom`). The `WriteException{Code:11000}` form satisfies it.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test -run TestHandleSyncCreateDM_RoomCollisionMismatch ./room-worker/...`

Expected: FAIL — `CreateRoom` is not called yet.

- [ ] **Step 3: Add Room persistence**

Replace the placeholder lines (`_, _ = requester, other` and the `return nil, errInvalidSyncDMRequest // placeholder` line) in `handleSyncCreateDM` with:

```go
	acceptedAt := time.Now().UTC()
	roomID := idgen.BuildDMRoomID(requester.ID, other.ID)

	room := &model.Room{
		ID:        roomID,
		Name:      "",
		Type:      req.RoomType,
		CreatedBy: requester.ID,
		SiteID:    h.siteID,
		CreatedAt: acceptedAt,
		UpdatedAt: acceptedAt,
	}
	if err := h.store.CreateRoom(ctx, room); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("create room: %w", err)
		}
		existing, fetchErr := h.store.GetRoom(ctx, room.ID)
		if fetchErr != nil {
			return nil, fmt.Errorf("fetch room on duplicate-key: %w", fetchErr)
		}
		if existing.Type != room.Type ||
			existing.SiteID != room.SiteID ||
			existing.Name != room.Name ||
			existing.CreatedBy != room.CreatedBy {
			return nil, errRoomIDCollision
		}
		room = existing
		acceptedAt = existing.CreatedAt
	}

	_, _ = room, acceptedAt
	return nil, errInvalidSyncDMRequest // placeholder — replaced in Task 9
```

Add the new imports `"time"` and `"go.mongodb.org/mongo-driver/v2/mongo"` to the file's import block.

- [ ] **Step 4: Run tests**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): persist Room with duplicate-key fallback"
```

---

## Task 9: Persist subscriptions and handle bulk-insert duplicate-key

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests for the happy path and the bulk-duplicate-key fallback**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
func TestHandleSyncCreateDM_DM_PersistsSubsAndReturnsRequester(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store, publish: noopPublish}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)

	var captured []*model.Subscription
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, subs []*model.Subscription) error {
			captured = subs
			return nil
		})
	store.EXPECT().ReconcileMemberCounts(gomock.Any(), gomock.Any()).Return(nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.True(t, reply.Success)
	assert.Equal(t, "alice", reply.Subscription.User.Account)
	assert.Equal(t, "u-alice", reply.Subscription.User.ID)
	assert.Equal(t, idgen.BuildDMRoomID("u-alice", "u-bob"), reply.Subscription.RoomID)
	assert.Equal(t, "bob", reply.Subscription.Name)
	assert.Equal(t, model.RoomTypeDM, reply.Subscription.RoomType)

	require.Len(t, captured, 2)
}

func TestHandleSyncCreateDM_BotDM_RequesterSubIsSubscribedTrue(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store, publish: noopPublish}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	bot := &model.User{ID: "u-bot", Account: "helper.bot", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "helper.bot").Return(bot, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ReconcileMemberCounts(gomock.Any(), gomock.Any()).Return(nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeBotDM, RequesterAccount: "alice", OtherAccount: "helper.bot"}
	data, _ := json.Marshal(req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.True(t, reply.Subscription.IsSubscribed)
	assert.Equal(t, "alice", reply.Subscription.User.Account)
}

func TestHandleSyncCreateDM_BulkInsertDuplicateKey_FallsBackToFind(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	h := &Handler{siteID: "site-a", store: store, publish: noopPublish}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)

	dupErr := mongo.WriteException{WriteErrors: []mongo.WriteError{{Code: 11000}}}
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(dupErr)
	existingSub := &model.Subscription{
		ID:           "existing-sub",
		User:         model.SubscriptionUser{ID: "u-alice", Account: "alice"},
		RoomID:       idgen.BuildDMRoomID("u-alice", "u-bob"),
		Name:         "bob",
		RoomType:     model.RoomTypeDM,
		IsSubscribed: true,
	}
	store.EXPECT().FindDMSubscription(gomock.Any(), "alice", "bob").Return(existingSub, nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	rawReply, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var reply model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(rawReply, &reply))
	assert.Equal(t, "existing-sub", reply.Subscription.ID)
}

// noopPublish is a PublishFunc that drops all events; used by tests that don't
// assert on publish behavior.
func noopPublish(_ context.Context, _ string, _ []byte, _ string) error { return nil }
```

> **Note:** the `publish` field on `Handler` is the third arg to `NewHandler` (see `room-worker/main.go:77`). The exported tests can either use the existing constructor or, if simpler, set the unexported `publish` field directly via a test-only constructor in `room-worker/handler.go`. Confirm the `Handler` struct field names with `grep -n "type Handler struct" /home/user/chat/room-worker/handler.go`. If `publish` isn't exported in the struct definition or accessible from `_test.go` in the same package, no change is needed — same-package `_test.go` files can read/write unexported fields directly.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_(DM_PersistsSubs|BotDM_|BulkInsertDuplicateKey)' ./room-worker/...`

Expected: FAIL — subscription persistence and reply marshalling not yet wired.

- [ ] **Step 3: Add subscription persistence + reply**

Replace the existing placeholder block (`_, _ = room, acceptedAt` and the `return nil, errInvalidSyncDMRequest`) with:

```go
	var subs []*model.Subscription
	switch req.RoomType {
	case model.RoomTypeDM:
		subs = buildDMSubs(requester, other, room, acceptedAt)
	case model.RoomTypeBotDM:
		subs = buildBotDMSubs(requester, other, room, acceptedAt)
	}

	requesterSub, err := h.persistSyncDMSubs(ctx, requester, other, subs)
	if err != nil {
		return nil, err
	}

	if rcErr := h.store.ReconcileMemberCounts(ctx, room.ID); rcErr != nil {
		slog.Error("sync DM: reconcile member counts failed",
			"error", rcErr, "roomID", room.ID, "requestID", requestID)
	}

	// subscription.update fan-out + outbox emission land in Tasks 10 and 11.

	reply, err := json.Marshal(model.SyncCreateDMReply{
		Success:      true,
		Subscription: *requesterSub,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal reply: %w", err)
	}
	return reply, nil
}

// persistSyncDMSubs inserts both subs. On a duplicate-key race (concurrent caller or retry),
// it falls back to fetching the requester's existing sub via FindDMSubscription and returns
// that — preserving idempotent behavior.
func (h *Handler) persistSyncDMSubs(ctx context.Context, requester, other *model.User,
	subs []*model.Subscription,
) (*model.Subscription, error) {
	err := h.store.BulkCreateSubscriptions(ctx, subs)
	if err == nil {
		return pickRequesterSub(subs, requester.Account), nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return nil, fmt.Errorf("bulk create subs: %w", err)
	}
	existing, fetchErr := h.store.FindDMSubscription(ctx, requester.Account, other.Account)
	if fetchErr != nil {
		return nil, fmt.Errorf("find existing sub on duplicate-key: %w", fetchErr)
	}
	return existing, nil
}

func pickRequesterSub(subs []*model.Subscription, requesterAccount string) *model.Subscription {
	for _, s := range subs {
		if s.User.Account == requesterAccount {
			return s
		}
	}
	return nil
}
```

Add `"log/slog"` to the import block.

- [ ] **Step 4: Run tests**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): persist subs with duplicate-key fallback and reply"
```

---

## Task 10: Publish `subscription.update` events

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
import (
	"sync"

	"github.com/hmchangw/chat/pkg/subject"
)

type capturedPublish struct {
	subject string
	data    []byte
	msgID   string
}

type publishCapturer struct {
	mu       sync.Mutex
	captured []capturedPublish
}

func (c *publishCapturer) fn(_ context.Context, subj string, data []byte, msgID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.captured = append(c.captured, capturedPublish{subject: subj, data: append([]byte(nil), data...), msgID: msgID})
	return nil
}

func TestHandleSyncCreateDM_PublishesSubscriptionUpdateForBothUsers(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	cap := &publishCapturer{}
	h := &Handler{siteID: "site-a", store: store, publish: cap.fn}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"}
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ReconcileMemberCounts(gomock.Any(), gomock.Any()).Return(nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	// Two subscription.update publishes — one per user.
	subjects := map[string]int{}
	for _, p := range cap.captured {
		subjects[p.subject]++
	}
	assert.Equal(t, 1, subjects[subject.SubscriptionUpdate("alice")])
	assert.Equal(t, 1, subjects[subject.SubscriptionUpdate("bob")])
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test -run TestHandleSyncCreateDM_PublishesSubscriptionUpdateForBothUsers ./room-worker/...`

Expected: FAIL — events not published.

- [ ] **Step 3: Add subscription.update fan-out**

Edit `room-worker/handler_sync_create_dm.go`. Replace the comment line `// subscription.update fan-out + outbox emission land in Tasks 10 and 11.` with the fan-out call:

```go
	h.publishSubscriptionUpdates(ctx, subs, acceptedAt, requestID)
	// outbox emission lands in Task 11.
```

Then add the new method (anywhere in the same file, below `handleSyncCreateDM`):

```go
func (h *Handler) publishSubscriptionUpdates(ctx context.Context, subs []*model.Subscription, acceptedAt time.Time, requestID string) {
	for _, sub := range subs {
		evt := model.SubscriptionUpdateEvent{
			UserID:       sub.User.ID,
			Subscription: *sub,
			Action:       "added",
			Timestamp:    acceptedAt.UnixMilli(),
		}
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("sync DM: marshal subscription.update failed",
				"error", err, "account", sub.User.Account, "requestID", requestID)
			continue
		}
		if err := h.publish(ctx, subject.SubscriptionUpdate(sub.User.Account), data, ""); err != nil {
			slog.Error("sync DM: publish subscription.update failed",
				"error", err, "account", sub.User.Account, "requestID", requestID)
		}
	}
}
```

Add `"github.com/hmchangw/chat/pkg/subject"` to the import block.

- [ ] **Step 4: Run tests**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): publish subscription.update events from sync DM handler"
```

---

## Task 11: Publish cross-site `room_created` outbox event

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/handler_sync_create_dm_test.go`

- [ ] **Step 1: Write failing tests**

Append to `room-worker/handler_sync_create_dm_test.go`:

```go
func TestHandleSyncCreateDM_CrossSite_EmitsOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	cap := &publishCapturer{}
	h := &Handler{siteID: "site-a", store: store, publish: cap.fn}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-b"} // remote
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ReconcileMemberCounts(gomock.Any(), gomock.Any()).Return(nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	var outbox *capturedPublish
	for i := range cap.captured {
		if cap.captured[i].subject == subject.Outbox("site-a", "site-b", model.OutboxTypeRoomCreated) {
			outbox = &cap.captured[i]
			break
		}
	}
	require.NotNil(t, outbox, "expected an outbox publish to site-b")

	var env model.OutboxEvent
	require.NoError(t, json.Unmarshal(outbox.data, &env))
	assert.Equal(t, model.OutboxTypeRoomCreated, env.Type)
	assert.Equal(t, "site-a", env.SiteID)
	assert.Equal(t, "site-b", env.DestSiteID)

	var payload model.RoomCreatedOutbox
	require.NoError(t, json.Unmarshal(env.Payload, &payload))
	assert.Equal(t, model.RoomTypeDM, payload.RoomType)
	assert.Equal(t, "", payload.RoomName)
	assert.Equal(t, "site-a", payload.HomeSiteID)
	assert.Equal(t, []string{"bob"}, payload.Accounts)
	assert.Equal(t, "alice", payload.RequesterAccount)
	// Nats-Msg-Id encoded into the publish msgID for JetStream dedup.
	assert.Equal(t, "01970a4f-8c2d-7c9a-abcd-e0123456789f:site-b", outbox.msgID)
}

func TestHandleSyncCreateDM_SameSite_NoOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	store := NewMockSubscriptionStore(ctrl)
	cap := &publishCapturer{}
	h := &Handler{siteID: "site-a", store: store, publish: cap.fn}

	requester := &model.User{ID: "u-alice", Account: "alice", SiteID: "site-a"}
	other := &model.User{ID: "u-bob", Account: "bob", SiteID: "site-a"} // same site
	store.EXPECT().GetUser(gomock.Any(), "alice").Return(requester, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(other, nil)
	store.EXPECT().CreateRoom(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().ReconcileMemberCounts(gomock.Any(), gomock.Any()).Return(nil)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	_, err := h.handleSyncCreateDM(newRequestCtx(), data)
	require.NoError(t, err)

	for _, p := range cap.captured {
		assert.NotContains(t, p.subject, "outbox.", "no outbox publish expected for same-site DM")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_(CrossSite|SameSite)' ./room-worker/...`

Expected: FAIL — outbox publish not yet wired.

- [ ] **Step 3: Add cross-site outbox publish**

Edit `room-worker/handler_sync_create_dm.go`. Replace the comment line `// outbox emission lands in Task 11.` with:

```go
	if err := h.publishSyncDMOutbox(ctx, room, requester, other, acceptedAt, requestID); err != nil {
		slog.Error("sync DM: publish outbox failed",
			"error", err, "roomID", room.ID, "requestID", requestID)
	}
```

Then append a new method below `publishSubscriptionUpdates`:

```go
func (h *Handler) publishSyncDMOutbox(ctx context.Context, room *model.Room, requester, other *model.User, acceptedAt time.Time, requestID string) error {
	if other.SiteID == "" || other.SiteID == h.siteID {
		return nil
	}

	payload := model.RoomCreatedOutbox{
		RoomID:           room.ID,
		RoomType:         room.Type,
		RoomName:         "",
		HomeSiteID:       room.SiteID,
		Accounts:         []string{other.Account},
		RequesterAccount: requester.Account,
		Timestamp:        acceptedAt.UnixMilli(),
	}
	pData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal room_created outbox payload: %w", err)
	}
	envelope := model.OutboxEvent{
		Type:       model.OutboxTypeRoomCreated,
		SiteID:     room.SiteID,
		DestSiteID: other.SiteID,
		Payload:    pData,
		Timestamp:  acceptedAt.UnixMilli(),
	}
	eData, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal outbox envelope: %w", err)
	}
	return h.publish(ctx,
		subject.Outbox(room.SiteID, other.SiteID, model.OutboxTypeRoomCreated),
		eData,
		requestID+":"+other.SiteID,
	)
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/user/chat && go test -run 'TestHandleSyncCreateDM_' ./room-worker/...`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/handler_sync_create_dm_test.go
git commit -m "feat(room-worker): emit cross-site room_created outbox from sync DM handler"
```

---

## Task 12: Add `natsServerCreateDM` wrapper and wire it up in `main.go`

**Files:**
- Modify: `room-worker/handler_sync_create_dm.go`
- Modify: `room-worker/main.go`

- [ ] **Step 1: Add the NATS handler wrapper**

Append to `room-worker/handler_sync_create_dm.go`. This wraps `handleSyncCreateDM` with NATS msg unmarshalling and reply marshalling:

```go
import (
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
)

// natsServerCreateDM is the NATS-side entry point for chat.server.request.room.{siteID}.create.dm.
// It builds the handler context, invokes handleSyncCreateDM, and replies via natsutil.
func (h *Handler) natsServerCreateDM(m otelnats.Msg) {
	ctx := natsutil.ContextWithRequestIDFromHeaders(m.Context(), m.Msg.Header)
	reply, err := h.handleSyncCreateDM(ctx, m.Msg.Data)
	if err != nil {
		natsutil.ReplyError(m.Msg, sanitizeSyncDMError(err))
		return
	}
	if _, werr := m.Msg.RespondMsg(natsutil.NewMsg(ctx, m.Msg.Reply, reply)); werr != nil {
		slog.Error("sync DM: reply failed", "error", werr)
	}
}
```

> **Note:** Confirm the otelnats package path with `grep -n "otelnats\." /home/user/chat/room-service/handler.go | head -3`. The exact import path used by room-service is the same one to use here.

- [ ] **Step 2: Wire the subscription in `room-worker/main.go`**

Edit `room-worker/main.go`. After the existing `handler := NewHandler(...)` block (line 91, before the `cons, err := js.CreateOrUpdateConsumer(...)` block), add:

```go
	if _, err := nc.QueueSubscribe(subject.RoomCreateDMSync(cfg.SiteID), "room-worker", handler.natsServerCreateDM); err != nil {
		slog.Error("subscribe sync DM endpoint failed", "error", err)
		os.Exit(1)
	}
```

Add `"github.com/hmchangw/chat/pkg/subject"` to the import block (it isn't there today).

> **Note on `nc` type:** confirm `nc.QueueSubscribe(...)` is available with the appropriate signature on the `*otelnats.Conn` returned by `natsutil.Connect`. If room-service uses the same call shape (it does — `room-service/handler.go:60`), this pattern is correct.

- [ ] **Step 3: Verify the build**

Run: `cd /home/user/chat && go build ./room-worker/...`

Expected: clean build.

- [ ] **Step 4: Run all room-worker unit tests**

Run: `cd /home/user/chat && make test SERVICE=room-worker`

Expected: PASS.

- [ ] **Step 5: Verify lint**

Run: `cd /home/user/chat && make lint`

Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd /home/user/chat
git add room-worker/handler_sync_create_dm.go room-worker/main.go
git commit -m "feat(room-worker): wire sync DM NATS endpoint in main"
```

---

## Task 13: Add integration tests with testcontainers

**Files:**
- Modify: `room-worker/integration_test.go`

- [ ] **Step 1: Survey the existing integration test setup**

Run: `cd /home/user/chat && grep -n "TestProcessCreateRoomDM\|setupMongo\|setupNATS\|//go:build integration" room-worker/integration_test.go | head -20`

This shows the existing helpers (`setupMongo`, etc.) you'll reuse. Use them directly — don't reinvent.

- [ ] **Step 2: Add the integration tests**

Append to `room-worker/integration_test.go` (inside the `//go:build integration` file):

```go
func TestSyncCreateDM_DM_PersistsRoomAndSubs(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t) // existing helper
	siteID := "site-a"

	// Seed both User docs the handler will GetUser.
	seedUser(t, store, &model.User{ID: "u-alice", Account: "alice", SiteID: siteID, EngName: "Alice", ChineseName: "愛麗絲"})
	seedUser(t, store, &model.User{ID: "u-bob", Account: "bob", SiteID: siteID, EngName: "Bob", ChineseName: "鮑勃"})

	captured := newPublishCapturer()
	handler := NewHandler(store, siteID, captured.fn)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)
	reply, err := handler.handleSyncCreateDM(newIntegRequestCtx(), data)
	require.NoError(t, err)

	var got model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(reply, &got))
	assert.True(t, got.Success)
	assert.Equal(t, "alice", got.Subscription.User.Account)

	// Verify Room + 2 subs in Mongo.
	roomID := idgen.BuildDMRoomID("u-alice", "u-bob")
	room, err := store.GetRoom(ctx, roomID)
	require.NoError(t, err)
	assert.Equal(t, model.RoomTypeDM, room.Type)
	assert.Equal(t, siteID, room.SiteID)

	// 2 subscription.update publishes captured.
	assert.Equal(t, 2, captured.countSubject(subject.SubscriptionUpdate("alice"))+captured.countSubject(subject.SubscriptionUpdate("bob")))
}

func TestSyncCreateDM_BotDM_CrossSiteOutbox(t *testing.T) {
	store := setupMongo(t)
	siteID := "site-a"

	seedUser(t, store, &model.User{ID: "u-alice", Account: "alice", SiteID: siteID, EngName: "Alice", ChineseName: "愛麗絲"})
	seedUser(t, store, &model.User{ID: "u-bot", Account: "helper.bot", SiteID: "site-b"})

	captured := newPublishCapturer()
	handler := NewHandler(store, siteID, captured.fn)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeBotDM, RequesterAccount: "alice", OtherAccount: "helper.bot"}
	data, _ := json.Marshal(req)
	_, err := handler.handleSyncCreateDM(newIntegRequestCtx(), data)
	require.NoError(t, err)

	assert.Equal(t, 1, captured.countSubject(subject.Outbox("site-a", "site-b", model.OutboxTypeRoomCreated)))
}

func TestSyncCreateDM_RetryIdempotent(t *testing.T) {
	ctx := context.Background()
	store := setupMongo(t)
	siteID := "site-a"

	seedUser(t, store, &model.User{ID: "u-alice", Account: "alice", SiteID: siteID, EngName: "Alice", ChineseName: "愛麗絲"})
	seedUser(t, store, &model.User{ID: "u-bob", Account: "bob", SiteID: siteID, EngName: "Bob", ChineseName: "鮑勃"})

	captured := newPublishCapturer()
	handler := NewHandler(store, siteID, captured.fn)

	req := model.SyncCreateDMRequest{RoomType: model.RoomTypeDM, RequesterAccount: "alice", OtherAccount: "bob"}
	data, _ := json.Marshal(req)

	reqCtx := newIntegRequestCtx() // same X-Request-ID for both calls
	r1, err := handler.handleSyncCreateDM(reqCtx, data)
	require.NoError(t, err)
	r2, err := handler.handleSyncCreateDM(reqCtx, data)
	require.NoError(t, err)

	var rep1, rep2 model.SyncCreateDMReply
	require.NoError(t, json.Unmarshal(r1, &rep1))
	require.NoError(t, json.Unmarshal(r2, &rep2))
	assert.Equal(t, rep1.Subscription.RoomID, rep2.Subscription.RoomID)

	// Exactly one Room and two subs in Mongo.
	roomID := idgen.BuildDMRoomID("u-alice", "u-bob")
	room, err := store.GetRoom(ctx, roomID)
	require.NoError(t, err)
	assert.Equal(t, roomID, room.ID)
	// (Optionally: count subs by querying the subscriptions collection directly via the test helper.)
}
```

> **Note:** the helpers `setupMongo`, `seedUser`, `newPublishCapturer`, and `newIntegRequestCtx` are placeholders — wire them to whatever the existing `room-worker/integration_test.go` already uses. If `seedUser` doesn't exist, the existing fixture-loading pattern in the file (look for the existing `TestProcess*` integration tests) is the template. If `newIntegRequestCtx` doesn't exist, define it once at the top of the file as `func newIntegRequestCtx() context.Context { return natsutil.WithRequestID(context.Background(), "01970a4f-8c2d-7c9a-abcd-e0123456789f") }`. Use a hard-coded valid UUID rather than generating one — `idgen.GenerateUUIDv7` returns the 32-char no-hyphen form which is reserved for entity Mongo `_id`s, not request IDs (see project CLAUDE.md §"Request Logging & Tracing").

- [ ] **Step 3: Run integration tests**

Run: `cd /home/user/chat && make test-integration SERVICE=room-worker`

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd /home/user/chat
git add room-worker/integration_test.go
git commit -m "test(room-worker): integration tests for sync DM endpoint"
```

---

## Task 14: Final verification

- [ ] **Step 1: Run lint**

Run: `cd /home/user/chat && make lint`

Expected: clean. Fix any issues before proceeding.

- [ ] **Step 2: Run all unit tests with race detector**

Run: `cd /home/user/chat && make test`

Expected: all green.

- [ ] **Step 3: Run room-worker integration tests**

Run: `cd /home/user/chat && make test-integration SERVICE=room-worker`

Expected: all green.

- [ ] **Step 4: Manual smoke test (optional)**

Spin up the local docker-compose for room-worker:

```bash
cd /home/user/chat/room-worker/deploy
docker compose up -d
```

Issue a test request via `nats` CLI:

```bash
nats request --creds <server-creds-path> chat.server.request.room.site-local.create.dm \
  '{"roomType":"dm","requesterAccount":"alice","otherAccount":"bob"}' \
  -H "X-Request-ID: 01970a4f-8c2d-7c9a-abcd-e0123456789f"
```

Expected: success reply with `success:true` and the requester's `Subscription`. (Requires Mongo to be seeded with both Users beforehand.)

- [ ] **Step 5: No final commit**

Verification doesn't produce code changes. If any of steps 1–3 surfaced a bug, fix it as a new commit on the branch.

---

## Out-of-scope (handled separately)

- **Part 2 — `HistoryConfig.SharedSince` removal.** Will be a separate plan in `docs/superpowers/plans/2026-05-07-historyconfig-sharedsince-removal.md`.
- **NATS callout policy update.** Adding `chat.server.request.room.>` permission for `room-worker` is an ops/IaC change, owned outside this repo.
- **user-service caller.** The actual caller (which performs the validation contract per the spec) is a separate PR.
