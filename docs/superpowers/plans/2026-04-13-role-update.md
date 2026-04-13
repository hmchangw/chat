# Role Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a role-update operation that promotes or demotes room members between `owner` and `member` roles, with cross-site replication via the outbox/inbox pattern.

**Architecture:** Room-service validates the request (room type, owner auth, role validity, duplicate role, last-owner guard) and publishes to the ROOMS JetStream stream using a canonical subject. Room-worker consumes the message, updates the subscription in MongoDB, and publishes a SubscriptionUpdateEvent. For cross-site members, an OutboxEvent replicates the change via inbox-worker.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-04-13-role-update-design.md`

---

### Task 1: Model and Subject Changes

**Files:**
- Modify: `pkg/model/subscription.go` — change `Role` to `Roles []Role`
- Modify: `pkg/model/event.go` — add `UpdateRoleRequest`
- Modify: `pkg/model/model_test.go` — update `TestSubscriptionJSON`, add `TestUpdateRoleRequestJSON`
- Modify: `pkg/subject/subject.go` — add `MemberRoleUpdate`, `MemberRoleUpdateWildcard`, `RoomCanonical`, `RoomCanonicalWildcard`
- Modify: `pkg/subject/subject_test.go` — add tests for new builders
- Modify: `pkg/stream/stream.go` — update `Rooms` to use canonical subjects
- Modify: `pkg/stream/stream_test.go` — update `Rooms` test case

- [ ] **Step 1: Update Subscription model — change Role to Roles**

In `pkg/model/subscription.go`, change the `Role` field to `Roles`:

```go
type Subscription struct {
	ID                 string           `json:"id" bson:"_id"`
	User               SubscriptionUser `json:"u" bson:"u"`
	RoomID             string           `json:"roomId" bson:"roomId"`
	SiteID             string           `json:"siteId" bson:"siteId"`
	Roles              []Role           `json:"roles" bson:"roles"`
	HistorySharedSince *time.Time       `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
	JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
	HasMention         bool             `json:"hasMention" bson:"hasMention"`
}
```

- [ ] **Step 2: Add UpdateRoleRequest to event.go**

In `pkg/model/event.go`, add the new request type:

```go
type UpdateRoleRequest struct {
	RoomID  string `json:"roomId"  bson:"roomId"`
	Account string `json:"account" bson:"account"`
	NewRole Role   `json:"newRole" bson:"newRole"`
}
```

- [ ] **Step 3: Fix all compilation errors from Role -> Roles change**

The `Role` -> `Roles` change breaks existing code. Update every reference:

In `room-service/handler.go` `handleCreateRoom`, change:
```go
// old
Role: model.RoleOwner,
// new
Roles: []model.Role{model.RoleOwner},
```

In `room-service/handler_test.go`, update subscription expectations:
```go
// old
Role: model.RoleOwner
// new
Roles: []model.Role{model.RoleOwner}
```
and
```go
// old
Role: model.RoleMember
// new
Roles: []model.Role{model.RoleMember}
```

In `room-worker/handler.go` `processInvite`, change:
```go
// old
Role: model.RoleMember,
// new
Roles: []model.Role{model.RoleMember},
```

In `room-worker/handler_test.go`, update subscription expectations:
```go
// old
Role: model.RoleOwner
// new
Roles: []model.Role{model.RoleOwner}
```
and
```go
// old
Role: model.RoleMember
// new
Roles: []model.Role{model.RoleMember}
```

In `inbox-worker/handler.go` `handleMemberAdded`, change:
```go
// old
Role: model.RoleMember,
// new
Roles: []model.Role{model.RoleMember},
```

In `inbox-worker/handler_test.go`, add `"slices"` to the import block and update all assertions:
```go
// old
if sub.Role != model.RoleMember {
    t.Errorf("subscription Role = %q, want %q", sub.Role, model.RoleMember)
}
// new
if !slices.Contains(sub.Roles, model.RoleMember) {
    t.Errorf("subscription Roles = %v, want to contain %q", sub.Roles, model.RoleMember)
}
```

In `room-service/handler.go` `handleInvite`, change the owner check:
```go
// old
if sub.Role != model.RoleOwner {
    return nil, fmt.Errorf("only owners can invite members")
}
// new
if !hasRole(sub.Roles, model.RoleOwner) {
    return nil, fmt.Errorf("only owners can invite members")
}
```

Add a local `hasRole` helper in `room-service/handler.go` (will be moved to `helper.go` in Task 3):
```go
func hasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Update model tests**

In `pkg/model/model_test.go`, update `TestSubscriptionJSON`:

```go
func TestSubscriptionJSON(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:             "r1",
		SiteID:             "site-a",
		Roles:              []model.Role{model.RoleOwner},
		HistorySharedSince: &hss,
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}

	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.Subscription
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, s)
	}
}
```

Update `TestSubscriptionUpdateEventJSON` to use `Roles`:

```go
func TestSubscriptionUpdateEventJSON(t *testing.T) {
	src := model.SubscriptionUpdateEvent{
		UserID: "u1",
		Subscription: model.Subscription{
			ID:     "s1",
			User:   model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID: "r1",
			SiteID: "site-a",
			Roles:  []model.Role{model.RoleMember},
			JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Action:    "added",
		Timestamp: 1735689600000,
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.SubscriptionUpdateEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}
```

Add `TestUpdateRoleRequestJSON`:

```go
func TestUpdateRoleRequestJSON(t *testing.T) {
	src := model.UpdateRoleRequest{
		RoomID:  "r1",
		Account: "bob",
		NewRole: model.RoleOwner,
	}
	roundTrip(t, &src, &model.UpdateRoleRequest{})
}
```

- [ ] **Step 5: Run tests to verify model changes compile and pass**

Run: `make test SERVICE=pkg/model`

Expected: PASS

- [ ] **Step 6: Add subject builders**

In `pkg/subject/subject.go`, add the new builders:

```go
// MemberRoleUpdate returns the specific subject for a member.role-update request.
func MemberRoleUpdate(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.role-update", account, roomID, siteID)
}

// MemberRoleUpdateWildcard returns the wildcard subscription pattern for member.role-update requests.
func MemberRoleUpdateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.role-update", siteID)
}

// RoomCanonical returns a canonical subject for room operations published to the ROOMS stream.
func RoomCanonical(siteID, operation string) string {
	return fmt.Sprintf("chat.room.canonical.%s.%s", siteID, operation)
}

// RoomCanonicalWildcard returns the wildcard pattern for consuming canonical room operations.
func RoomCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.room.canonical.%s.>", siteID)
}
```

- [ ] **Step 7: Add subject builder tests**

In `pkg/subject/subject_test.go`, add entries to `TestSubjectBuilders`:

```go
{"MemberRoleUpdate", subject.MemberRoleUpdate("alice", "r1", "site-a"),
    "chat.user.alice.request.room.r1.site-a.member.role-update"},
{"RoomCanonical", subject.RoomCanonical("site-a", "member.role-update"),
    "chat.room.canonical.site-a.member.role-update"},
```

Add entries to `TestWildcardPatterns`:

```go
{"MemberRoleUpdateWild", subject.MemberRoleUpdateWildcard("site-a"),
    "chat.user.*.request.room.*.site-a.member.role-update"},
{"RoomCanonicalWild", subject.RoomCanonicalWildcard("site-a"),
    "chat.room.canonical.site-a.>"},
```

Add a test case to `TestParseUserRoomSubject`:

```go
{"role_update", "chat.user.alice.request.room.r1.site-a.member.role-update", "alice", "r1", true},
```

- [ ] **Step 8: Update ROOMS stream config**

In `pkg/stream/stream.go`, update the `Rooms` function:

```go
func Rooms(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("ROOMS_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.room.canonical.%s.>", siteID)},
	}
}
```

- [ ] **Step 9: Update stream test**

In `pkg/stream/stream_test.go`, update the `Rooms` test case:

```go
{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.room.canonical.site-a.>"},
```

- [ ] **Step 10: Run all tests to verify**

Run: `make test`

Expected: PASS — all existing tests updated for `Roles []Role`, new subject builders and stream config working.

- [ ] **Step 11: Commit**

```bash
git add pkg/model/subscription.go pkg/model/event.go pkg/model/model_test.go \
       pkg/subject/subject.go pkg/subject/subject_test.go \
       pkg/stream/stream.go pkg/stream/stream_test.go \
       room-service/handler.go room-service/handler_test.go \
       room-worker/handler.go room-worker/handler_test.go \
       inbox-worker/handler.go inbox-worker/handler_test.go
git commit -m "feat: add UpdateRoleRequest model, Roles []Role, canonical subjects and stream config"
```

---

### Task 2: Room-Service Helpers

**Files:**
- Create: `room-service/helper.go` — `HasRole`, `sanitizeError`
- Create: `room-service/helper_test.go` — tests for helpers
- Modify: `room-service/handler.go` — replace local `hasRole` with `HasRole` from helper.go

- [ ] **Step 1: Write failing tests for HasRole**

Create `room-service/helper_test.go`:

```go
package main

import (
	"fmt"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHasRole(t *testing.T) {
	tests := []struct {
		name   string
		roles  []model.Role
		target model.Role
		want   bool
	}{
		{"owner in [owner]", []model.Role{model.RoleOwner}, model.RoleOwner, true},
		{"member in [member]", []model.Role{model.RoleMember}, model.RoleMember, true},
		{"owner not in [member]", []model.Role{model.RoleMember}, model.RoleOwner, false},
		{"member not in [owner]", []model.Role{model.RoleOwner}, model.RoleMember, false},
		{"empty roles", []model.Role{}, model.RoleOwner, false},
		{"nil roles", nil, model.RoleOwner, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HasRole(tt.roles, tt.target)
			if got != tt.want {
				t.Errorf("HasRole(%v, %q) = %v, want %v", tt.roles, tt.target, got, tt.want)
			}
		})
	}
}

func TestSanitizeError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"invalid role: must be owner or member", "invalid role: must be owner or member"},
		{"only owners can update roles", "only owners can update roles"},
		{"cannot demote the last owner", "cannot demote the last owner"},
		{"some internal db error", "internal error"},
		{"mongo timeout", "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeError(fmt.Errorf("%s", tt.input))
			if got != tt.want {
				t.Errorf("sanitizeError(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=room-service`

Expected: FAIL — `HasRole` and `sanitizeError` not defined.

- [ ] **Step 3: Implement helpers**

Create `room-service/helper.go`:

```go
package main

import (
	"strings"

	"github.com/hmchangw/chat/pkg/model"
)

// HasRole returns true if the given role is present in the roles slice.
func HasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

// sanitizeError returns a user-safe error message.
// Errors with known user-facing prefixes pass through; all others return "internal error".
func sanitizeError(err error) string {
	msg := err.Error()
	userFacingPrefixes := []string{
		"invalid",
		"only owners",
		"cannot demote",
		"requester not found",
		"target user",
		"role update",
		"user already",
	}
	for _, pfx := range userFacingPrefixes {
		if strings.HasPrefix(msg, pfx) {
			return msg
		}
	}
	return "internal error"
}
```

- [ ] **Step 4: Remove local hasRole from handler.go, use HasRole**

In `room-service/handler.go`, remove the local `hasRole` function added in Task 1 Step 3, and update the call in `handleInvite`:

```go
// old
if !hasRole(sub.Roles, model.RoleOwner) {
// new
if !HasRole(sub.Roles, model.RoleOwner) {
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test SERVICE=room-service`

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-service/helper.go room-service/helper_test.go room-service/handler.go
git commit -m "feat(room-service): add HasRole and sanitizeError helpers"
```

---

### Task 3: Room-Service Role Update Handler

**Files:**
- Modify: `room-service/store.go` — add `CountOwners` method to `RoomStore`
- Modify: `room-service/store_mongo.go` — implement `CountOwners`
- Modify: `room-service/handler.go` — add `natsUpdateRole`, `handleUpdateRole`; update `publishToStream` signature; register subscription in `RegisterCRUD`
- Modify: `room-service/main.go` — update `publishToStream` wiring
- Modify: `room-service/handler_test.go` — add role-update tests
- Regenerate: `room-service/mock_store_test.go`

- [ ] **Step 1: Add CountOwners to store interface**

In `room-service/store.go`, add to `RoomStore`:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . RoomStore

type RoomStore interface {
	CreateRoom(ctx context.Context, room *model.Room) error
	GetRoom(ctx context.Context, id string) (*model.Room, error)
	ListRooms(ctx context.Context) ([]model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	CountOwners(ctx context.Context, roomID string) (int, error)
}
```

- [ ] **Step 2: Implement CountOwners in store_mongo.go**

In `room-service/store_mongo.go`, add:

```go
func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID, "roles": "owner"})
	if err != nil {
		return 0, fmt.Errorf("count owners for room %q: %w", roomID, err)
	}
	return int(count), nil
}
```

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=room-service`

- [ ] **Step 4: Update publishToStream signature in handler.go**

In `room-service/handler.go`, update the `Handler` struct and constructor:

```go
type Handler struct {
	store           RoomStore
	siteID          string
	maxRoomSize     int
	publishToStream func(ctx context.Context, subj string, data []byte) error
}

func NewHandler(
	store RoomStore,
	siteID string,
	maxRoomSize int,
	publishToStream func(context.Context, string, []byte) error,
) *Handler {
	return &Handler{
		store:           store,
		siteID:          siteID,
		maxRoomSize:     maxRoomSize,
		publishToStream: publishToStream,
	}
}
```

Update `handleInvite` to pass the subject:

```go
// old
if err := h.publishToStream(ctx, timestampedData); err != nil {
// new
if err := h.publishToStream(ctx, subject.MemberInvite(inviterAccount, roomID, h.siteID), timestampedData); err != nil {
```

- [ ] **Step 5: Register role-update subscription in RegisterCRUD**

In `room-service/handler.go`, add to `RegisterCRUD`:

```go
if _, err := nc.QueueSubscribe(subject.MemberRoleUpdateWildcard(h.siteID), queue, h.natsUpdateRole); err != nil {
    return err
}
```

- [ ] **Step 6: Add natsUpdateRole and handleUpdateRole**

In `room-service/handler.go`, add:

```go
func (h *Handler) natsUpdateRole(m otelnats.Msg) {
	resp, err := h.handleUpdateRole(m.Context(), m.Msg.Subject, m.Msg.Data)
	if err != nil {
		slog.Error("update role failed", "error", err)
		natsutil.ReplyError(m.Msg, sanitizeError(err))
		return
	}
	if err := m.Msg.Respond(resp); err != nil {
		slog.Error("failed to respond to update-role message", "error", err)
	}
}

func (h *Handler) handleUpdateRole(ctx context.Context, subj string, data []byte) ([]byte, error) {
	requester, roomID, ok := subject.ParseUserRoomSubject(subj)
	if !ok {
		return nil, fmt.Errorf("invalid subject: %s", subj)
	}

	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("invalid request: %w", err)
	}

	if req.RoomID != "" && req.RoomID != roomID {
		return nil, fmt.Errorf("invalid request: room ID mismatch")
	}
	req.RoomID = roomID

	// Validate role
	if req.NewRole != model.RoleOwner && req.NewRole != model.RoleMember {
		return nil, fmt.Errorf("invalid role: must be owner or member")
	}

	// Room must be a group
	room, err := h.store.GetRoom(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("role update is only allowed in group rooms: %w", err)
	}
	if room.Type != model.RoomTypeGroup {
		return nil, fmt.Errorf("role update is only allowed in group rooms")
	}

	// Authorization: only owners can change roles
	requesterSub, err := h.store.GetSubscription(ctx, requester, roomID)
	if err != nil {
		return nil, fmt.Errorf("requester not found: %w", err)
	}
	if !HasRole(requesterSub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can update roles")
	}

	// Target must be in the room
	targetSub, err := h.store.GetSubscription(ctx, req.Account, roomID)
	if err != nil {
		return nil, fmt.Errorf("target user is not a member of this room: %w", err)
	}

	// Duplicate role guard
	if HasRole(targetSub.Roles, req.NewRole) {
		return nil, fmt.Errorf("user already has the requested role")
	}

	// Last-owner guard: cannot demote self if last owner
	if req.NewRole == model.RoleMember && req.Account == requester {
		count, err := h.store.CountOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("cannot demote the last owner: %w", err)
		}
		if count <= 1 {
			return nil, fmt.Errorf("cannot demote the last owner")
		}
	}

	// Re-marshal with roomID set from subject
	data, err = json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal role update request: %w", err)
	}

	// Publish to ROOMS stream
	if err := h.publishToStream(ctx, subject.RoomCanonical(h.siteID, "member.role-update"), data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}
```

- [ ] **Step 7: Update main.go publishToStream wiring**

In `room-service/main.go`, update the handler construction:

```go
handler := NewHandler(store, cfg.SiteID, cfg.MaxRoomSize, func(ctx context.Context, subj string, data []byte) error {
    _, err := js.Publish(ctx, subj, data)
    return err
})
```

Remove the old `MemberInviteWildcard` subscription (invite is being removed), keep only `RegisterCRUD`:

```go
if err := handler.RegisterCRUD(nc); err != nil {
    slog.Error("register handlers failed", "error", err)
    os.Exit(1)
}
```

Delete these lines from main.go:
```go
inviteSubj := subject.MemberInviteWildcard(cfg.SiteID)
if _, err := nc.QueueSubscribe(inviteSubj, "room-service", handler.NatsHandleInvite); err != nil {
    slog.Error("subscribe invite failed", "error", err)
    os.Exit(1)
}
```

- [ ] **Step 8: Update existing handler tests for new publishToStream signature**

In `room-service/handler_test.go`, update the `publishToStream` capture in every test:

For `TestHandler_InviteOwner_Success`:
```go
// old
publishToStream: func(_ context.Context, data []byte) error { jsPublished = data; return nil },
// new
publishToStream: func(_ context.Context, _ string, data []byte) error { jsPublished = data; return nil },
```

For `TestHandler_InviteMember_Rejected`:
```go
// old
publishToStream: func(_ context.Context, data []byte) error { return nil },
// new
publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
```

For `TestHandler_InviteExceedsMaxSize`:
```go
// old
publishToStream: func(_ context.Context, data []byte) error { return nil },
// new
publishToStream: func(_ context.Context, _ string, data []byte) error { return nil },
```

- [ ] **Step 9: Write role-update handler tests**

Add to `room-service/handler_test.go`:

```go
func TestHandler_UpdateRole_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	var publishedSubj string
	var publishedData []byte
	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, subj string, data []byte) error {
			publishedSubj = subj
			publishedData = data
			return nil
		},
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	resp, err := h.handleUpdateRole(context.Background(), subj, data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	json.Unmarshal(resp, &result)
	if result["status"] != "accepted" {
		t.Errorf("expected status accepted, got %v", result)
	}

	if publishedSubj != "chat.room.canonical.site-a.member.role-update" {
		t.Errorf("published subject = %q, want canonical role-update subject", publishedSubj)
	}

	var publishedReq model.UpdateRoleRequest
	json.Unmarshal(publishedData, &publishedReq)
	if publishedReq.RoomID != "r1" || publishedReq.Account != "bob" || publishedReq.NewRole != model.RoleOwner {
		t.Errorf("published request = %+v", publishedReq)
	}
}

func TestHandler_UpdateRole_NonOwnerRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "charlie", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("bob", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for non-owner")
	}
}

func TestHandler_UpdateRole_DMRejected(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeDM}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for DM room")
	}
}

func TestHandler_UpdateRole_InvalidRole(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: "admin"}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for invalid role")
	}
}

func TestHandler_UpdateRole_AlreadyHasRole(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for duplicate role")
	}
}

func TestHandler_UpdateRole_LastOwnerCannotDemote(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockRoomStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Type: model.RoomTypeGroup}, nil)
	store.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}}, nil).
		Times(2) // once for requester auth, once for target lookup (requester == target)
	store.EXPECT().CountOwners(gomock.Any(), "r1").Return(1, nil)

	h := &Handler{store: store, siteID: "site-a", maxRoomSize: 1000,
		publishToStream: func(_ context.Context, _ string, _ []byte) error { return nil },
	}

	req := model.UpdateRoleRequest{Account: "alice", NewRole: model.RoleMember}
	data, _ := json.Marshal(req)
	subj := subject.MemberRoleUpdate("alice", "r1", "site-a")

	_, err := h.handleUpdateRole(context.Background(), subj, data)
	if err == nil {
		t.Fatal("expected error for last owner demotion")
	}
}
```

- [ ] **Step 10: Run tests**

Run: `make test SERVICE=room-service`

Expected: PASS

- [ ] **Step 11: Run full test suite**

Run: `make test`

Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add room-service/
git commit -m "feat(room-service): add role-update validation handler with CountOwners"
```

---

### Task 4: Room-Worker Role Update Processing

**Files:**
- Modify: `room-worker/store.go` — add `GetSubscription`, `UpdateSubscriptionRoles`
- Modify: `room-worker/store_mongo.go` — implement new store methods
- Modify: `room-worker/handler.go` — add subject-based dispatch, `processRoleUpdate`
- Modify: `room-worker/handler_test.go` — add role-update tests
- Regenerate: `room-worker/mock_store_test.go`

- [ ] **Step 1: Add new methods to SubscriptionStore**

In `room-worker/store.go`, update the interface:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . SubscriptionStore

type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
}
```

- [ ] **Step 2: Implement new store methods**

In `room-worker/store_mongo.go`, add:

```go
func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found for %q in room %q: %w", account, roomID, err)
	}
	return &sub, nil
}

func (s *MongoStore) UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$set": bson.M{"roles": roles}}
	_, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("update roles for %q in room %q: %w", account, roomID, err)
	}
	return nil
}
```

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=room-worker`

- [ ] **Step 4: Add subject-based dispatch to HandleJetStreamMsg**

In `room-worker/handler.go`, update `HandleJetStreamMsg` to dispatch by subject:

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()

	switch {
	case strings.HasSuffix(subj, "member.role-update"):
		err = h.processRoleUpdate(ctx, msg.Data())
	default:
		err = h.processInvite(ctx, msg.Data())
	}

	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "error", nakErr)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "error", err)
	}
}
```

Add the `"strings"` import to the import block.

- [ ] **Step 5: Implement processRoleUpdate**

In `room-worker/handler.go`, add:

```go
func (h *Handler) processRoleUpdate(ctx context.Context, data []byte) error {
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal role update request: %w", err)
	}

	// Look up the subscription to get user info for the event
	sub, err := h.store.GetSubscription(ctx, req.Account, req.RoomID)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}

	// Update the roles
	newRoles := []model.Role{req.NewRole}
	if err := h.store.UpdateSubscriptionRoles(ctx, req.Account, req.RoomID, newRoles); err != nil {
		return fmt.Errorf("update subscription roles: %w", err)
	}

	now := time.Now().UTC()

	// Build the updated subscription for the event
	updatedSub := *sub
	updatedSub.Roles = []model.Role{req.NewRole}

	// Publish SubscriptionUpdateEvent to the affected user
	subEvt := model.SubscriptionUpdateEvent{
		UserID:       sub.User.ID,
		Subscription: updatedSub,
		Action:       "role_updated",
		Timestamp:    now.UnixMilli(),
	}
	subEvtData, err := json.Marshal(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}
	if err := h.publish(ctx, subject.SubscriptionUpdate(sub.User.Account), subEvtData); err != nil {
		return fmt.Errorf("publish subscription update: %w", err)
	}

	// If cross-site, publish outbox event
	if sub.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type:       "role_updated",
			SiteID:     h.siteID,
			DestSiteID: sub.SiteID,
			Payload:    subEvtData,
			Timestamp:  now.UnixMilli(),
		}
		outboxData, err := json.Marshal(outbox)
		if err != nil {
			return fmt.Errorf("marshal outbox event: %w", err)
		}
		outboxSubj := subject.Outbox(h.siteID, sub.SiteID, "role_updated")
		if err := h.publish(ctx, outboxSubj, outboxData); err != nil {
			return fmt.Errorf("publish outbox: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 6: Write room-worker role-update tests**

Add to `room-worker/handler_test.go`:

```go
func TestHandler_ProcessRoleUpdate_LocalUser(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{
			ID:     "s1",
			User:   model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "r1",
			SiteID: "site-a",
			Roles:  []model.Role{model.RoleMember},
		}, nil)
	store.EXPECT().UpdateSubscriptionRoles(gomock.Any(), "bob", "r1", []model.Role{model.RoleOwner}).Return(nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)

	if err := h.processRoleUpdate(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only SubscriptionUpdateEvent should be published (no outbox for local user)
	if len(published) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(published))
	}

	if published[0].subj != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want subscription update for bob", published[0].subj)
	}

	var evt model.SubscriptionUpdateEvent
	json.Unmarshal(published[0].data, &evt)
	if evt.Action != "role_updated" {
		t.Errorf("action = %q, want role_updated", evt.Action)
	}
	if evt.UserID != "u2" {
		t.Errorf("userID = %q, want u2", evt.UserID)
	}
	if !slices.Contains(evt.Subscription.Roles, model.RoleOwner) {
		t.Errorf("subscription roles = %v, want to contain owner", evt.Subscription.Roles)
	}
	if evt.Timestamp <= 0 {
		t.Error("expected Timestamp > 0")
	}
}

func TestHandler_ProcessRoleUpdate_CrossSite(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetSubscription(gomock.Any(), "bob", "r1").
		Return(&model.Subscription{
			ID:     "s1",
			User:   model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "r1",
			SiteID: "site-b", // different from handler's siteID
			Roles:  []model.Role{model.RoleMember},
		}, nil)
	store.EXPECT().UpdateSubscriptionRoles(gomock.Any(), "bob", "r1", []model.Role{model.RoleOwner}).Return(nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(_ context.Context, subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)

	if err := h.processRoleUpdate(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// SubscriptionUpdateEvent + OutboxEvent
	if len(published) != 2 {
		t.Fatalf("expected 2 publishes, got %d", len(published))
	}

	// First: subscription update
	if published[0].subj != "chat.user.bob.event.subscription.update" {
		t.Errorf("first subject = %q, want subscription update", published[0].subj)
	}

	// Second: outbox
	wantOutboxSubj := "outbox.site-a.to.site-b.role_updated"
	if published[1].subj != wantOutboxSubj {
		t.Errorf("second subject = %q, want %q", published[1].subj, wantOutboxSubj)
	}

	var outbox model.OutboxEvent
	json.Unmarshal(published[1].data, &outbox)
	if outbox.Type != "role_updated" {
		t.Errorf("outbox type = %q, want role_updated", outbox.Type)
	}
	if outbox.SiteID != "site-a" || outbox.DestSiteID != "site-b" {
		t.Errorf("outbox sites = %q -> %q, want site-a -> site-b", outbox.SiteID, outbox.DestSiteID)
	}

	// Verify outbox payload is a SubscriptionUpdateEvent
	var innerEvt model.SubscriptionUpdateEvent
	json.Unmarshal(outbox.Payload, &innerEvt)
	if innerEvt.Action != "role_updated" {
		t.Errorf("inner event action = %q, want role_updated", innerEvt.Action)
	}
	if !slices.Contains(innerEvt.Subscription.Roles, model.RoleOwner) {
		t.Errorf("inner subscription roles = %v, want to contain owner", innerEvt.Subscription.Roles)
	}
}
```

- [ ] **Step 7: Run tests**

Run: `make test SERVICE=room-worker`

Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add room-worker/
git commit -m "feat(room-worker): add processRoleUpdate with subject-based dispatch and outbox"
```

---

### Task 5: Inbox-Worker Role Updated Handling

**Files:**
- Modify: `inbox-worker/handler.go` — add `UpdateSubscriptionRoles` to `InboxStore`, add `handleRoleUpdated`, add `"role_updated"` case to switch
- Modify: `inbox-worker/main.go` — implement `UpdateSubscriptionRoles` on `mongoInboxStore`
- Modify: `inbox-worker/handler_test.go` — add role_updated tests, update stub store

- [ ] **Step 1: Add UpdateSubscriptionRoles to InboxStore**

In `inbox-worker/handler.go`, update the interface:

```go
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	UpsertRoom(ctx context.Context, room *model.Room) error
	UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
}
```

- [ ] **Step 2: Add role_updated case to HandleEvent**

In `inbox-worker/handler.go`, update the switch in `HandleEvent`:

```go
switch evt.Type {
case "member_added":
    return h.handleMemberAdded(ctx, &evt)
case "room_sync":
    return h.handleRoomSync(ctx, &evt)
case "role_updated":
    return h.handleRoleUpdated(ctx, &evt)
default:
    slog.Warn("unknown event type, skipping", "type", evt.Type)
    return nil
}
```

- [ ] **Step 3: Implement handleRoleUpdated**

In `inbox-worker/handler.go`, add:

```go
func (h *Handler) handleRoleUpdated(ctx context.Context, evt *model.OutboxEvent) error {
	var subEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(evt.Payload, &subEvt); err != nil {
		return fmt.Errorf("unmarshal role_updated payload: %w", err)
	}

	account := subEvt.Subscription.User.Account
	roomID := subEvt.Subscription.RoomID
	roles := subEvt.Subscription.Roles

	if len(roles) == 0 {
		return fmt.Errorf("role_updated event has empty roles")
	}

	if err := h.store.UpdateSubscriptionRoles(ctx, account, roomID, roles); err != nil {
		return fmt.Errorf("update subscription roles: %w", err)
	}

	// Re-publish the SubscriptionUpdateEvent to the local user
	updateData, err := natsutil.MarshalResponse(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}

	subj := subject.SubscriptionUpdate(account)
	if err := h.pub.Publish(ctx, subj, updateData); err != nil {
		slog.Error("publish subscription update failed", "error", err, "account", account)
	}

	return nil
}
```

- [ ] **Step 4: Implement UpdateSubscriptionRoles on mongoInboxStore**

In `inbox-worker/main.go`, add to `mongoInboxStore`:

```go
func (s *mongoInboxStore) UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$set": bson.M{"roles": roles}}
	_, err := s.subCol.UpdateOne(ctx, filter, update)
	return err
}
```

Add `"github.com/hmchangw/chat/pkg/model"` to the imports in `main.go` if not already present.

- [ ] **Step 5: Update stubInboxStore in tests**

In `inbox-worker/handler_test.go`, add `UpdateSubscriptionRoles` to the stub and a helper to read updated roles:

```go
type roleUpdate struct {
	account string
	roomID  string
	roles   []model.Role
}

// Add field to stubInboxStore:
// roleUpdates []roleUpdate

func (s *stubInboxStore) UpdateSubscriptionRoles(_ context.Context, account, roomID string, roles []model.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roleUpdates = append(s.roleUpdates, roleUpdate{account: account, roomID: roomID, roles: roles})
	return nil
}

func (s *stubInboxStore) getRoleUpdates() []roleUpdate {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]roleUpdate, len(s.roleUpdates))
	copy(cp, s.roleUpdates)
	return cp
}
```

Add the `roleUpdates` field to the `stubInboxStore` struct:

```go
type stubInboxStore struct {
	mu            sync.Mutex
	subscriptions []model.Subscription
	rooms         []model.Room
	roleUpdates   []roleUpdate
}
```

- [ ] **Step 6: Write role_updated tests**

Add to `inbox-worker/handler_test.go`:

```go
func TestHandleEvent_RoleUpdated(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	subEvt := model.SubscriptionUpdateEvent{
		UserID: "u2",
		Subscription: model.Subscription{
			ID:     "s1",
			User:   model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "room-1",
			SiteID: "site-a",
			Roles:  []model.Role{model.RoleOwner},
		},
		Action:    "role_updated",
		Timestamp: 1735689600000,
	}
	subEvtData, _ := json.Marshal(subEvt)

	evt := model.OutboxEvent{
		Type:       "role_updated",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    subEvtData,
		Timestamp:  1735689600000,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify role was updated in store
	updates := store.getRoleUpdates()
	if len(updates) != 1 {
		t.Fatalf("expected 1 role update, got %d", len(updates))
	}
	if updates[0].account != "bob" || updates[0].roomID != "room-1" {
		t.Errorf("role update = %+v, want bob/room-1", updates[0])
	}
	if len(updates[0].roles) != 1 || updates[0].roles[0] != model.RoleOwner {
		t.Errorf("role update roles = %v, want [owner]", updates[0].roles)
	}

	// Verify SubscriptionUpdateEvent was published to local user
	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}
	if records[0].subject != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want bob subscription update", records[0].subject)
	}

	var publishedEvt model.SubscriptionUpdateEvent
	json.Unmarshal(records[0].data, &publishedEvt)
	if publishedEvt.Action != "role_updated" {
		t.Errorf("action = %q, want role_updated", publishedEvt.Action)
	}
}

func TestHandleEvent_RoleUpdated_InvalidPayload(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	evt := model.OutboxEvent{
		Type:       "role_updated",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    []byte("not valid json"),
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err == nil {
		t.Error("expected error for invalid role_updated payload")
	}

	if len(store.getRoleUpdates()) != 0 {
		t.Error("no role update should have been applied")
	}
}
```

- [ ] **Step 7: Run tests**

Run: `make test SERVICE=inbox-worker`

Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add inbox-worker/
git commit -m "feat(inbox-worker): handle role_updated outbox events"
```

---

### Task 6: Integration Tests — room-service

**Files:**
- Modify: `room-service/integration_test.go` — add `CountOwners` store test

- [ ] **Step 1: Write CountOwners integration test**

Add to `room-service/integration_test.go`:

```go
func TestMongoStore_CountOwners_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	// Seed subscriptions: 2 owners and 1 member
	_, err := db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "charlie"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
		model.Subscription{ID: "s4", User: model.SubscriptionUser{ID: "u4", Account: "dave"}, RoomID: "r2", Roles: []model.Role{model.RoleOwner}},
	})
	if err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}

	count, err := store.CountOwners(ctx, "r1")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 2 {
		t.Errorf("CountOwners(r1) = %d, want 2", count)
	}

	count, err = store.CountOwners(ctx, "r2")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 1 {
		t.Errorf("CountOwners(r2) = %d, want 1", count)
	}

	count, err = store.CountOwners(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("CountOwners: %v", err)
	}
	if count != 0 {
		t.Errorf("CountOwners(nonexistent) = %d, want 0", count)
	}
}
```

- [ ] **Step 2: Run integration test**

Run: `make test-integration SERVICE=room-service`

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-service/integration_test.go
git commit -m "test(room-service): add CountOwners integration test"
```

---

### Task 7: Integration Tests — room-worker

**Files:**
- Modify: `room-worker/integration_test.go` — add `GetSubscription`, `UpdateSubscriptionRoles` store tests

- [ ] **Step 1: Write store integration tests**

Add to `room-worker/integration_test.go`:

```go
func TestMongoStore_GetSubscription_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1",
		SiteID: "site-a",
		Roles:  []model.Role{model.RoleOwner},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	sub, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if sub.User.Account != "alice" || sub.RoomID != "r1" {
		t.Errorf("got %+v", sub)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles = %v, want to contain owner", sub.Roles)
	}

	// Not found case
	_, err = store.GetSubscription(ctx, "nonexistent", "r1")
	if err == nil {
		t.Error("expected error for nonexistent subscription")
	}
}

func TestMongoStore_UpdateSubscriptionRoles_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db)
	ctx := context.Background()

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1",
		Roles:  []model.Role{model.RoleMember},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// Promote to owner
	err = store.UpdateSubscriptionRoles(ctx, "alice", "r1", []model.Role{model.RoleOwner})
	if err != nil {
		t.Fatalf("UpdateSubscriptionRoles: %v", err)
	}

	// Verify the update persisted
	sub, err := store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after update: %v", err)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles after update = %v, want to contain owner", sub.Roles)
	}

	// Demote back to member
	err = store.UpdateSubscriptionRoles(ctx, "alice", "r1", []model.Role{model.RoleMember})
	if err != nil {
		t.Fatalf("UpdateSubscriptionRoles demote: %v", err)
	}

	sub, err = store.GetSubscription(ctx, "alice", "r1")
	if err != nil {
		t.Fatalf("GetSubscription after demote: %v", err)
	}
	if !slices.Contains(sub.Roles, model.RoleMember) {
		t.Errorf("roles after demote = %v, want to contain member", sub.Roles)
	}
}
```

- [ ] **Step 2: Run integration test**

Run: `make test-integration SERVICE=room-worker`

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add room-worker/integration_test.go
git commit -m "test(room-worker): add GetSubscription and UpdateSubscriptionRoles integration tests"
```

---

### Task 8: Integration Tests — inbox-worker

**Files:**
- Modify: `inbox-worker/integration_test.go` — add end-to-end `role_updated` event flow test

- [ ] **Step 1: Write end-to-end role_updated integration test**

Add to `inbox-worker/integration_test.go`:

```go
func TestInboxWorker_RoleUpdated_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	// Seed an existing subscription (member being promoted)
	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "room-1",
		SiteID: "site-a",
		Roles:  []model.Role{model.RoleMember},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	// Build the role_updated outbox event
	subEvt := model.SubscriptionUpdateEvent{
		UserID: "u2",
		Subscription: model.Subscription{
			ID:     "s1",
			User:   model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "room-1",
			SiteID: "site-a",
			Roles:  []model.Role{model.RoleOwner},
		},
		Action:    "role_updated",
		Timestamp: time.Now().UTC().UnixMilli(),
	}
	subEvtData, _ := json.Marshal(subEvt)

	evt := model.OutboxEvent{
		Type:       "role_updated",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    subEvtData,
		Timestamp:  time.Now().UTC().UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)

	// Process the event
	err = handler.HandleEvent(ctx, evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify the subscription role was updated in MongoDB
	var sub model.Subscription
	err = db.Collection("subscriptions").FindOne(ctx, bson.M{"u.account": "bob", "roomId": "room-1"}).Decode(&sub)
	if err != nil {
		t.Fatalf("subscription not found: %v", err)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles = %v, want to contain owner", sub.Roles)
	}

	// Verify SubscriptionUpdateEvent was published
	if len(pub.subjects) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(pub.subjects))
	}
	if pub.subjects[0] != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want bob subscription update", pub.subjects[0])
	}
}
```

- [ ] **Step 2: Run integration test**

Run: `make test-integration SERVICE=inbox-worker`

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add inbox-worker/integration_test.go
git commit -m "test(inbox-worker): add role_updated end-to-end integration test"
```

---

### Task 9: Final Verification

- [ ] **Step 1: Run full test suite with race detector**

Run: `make test`

Expected: PASS — all unit tests pass across all services.

- [ ] **Step 2: Run lint**

Run: `make lint`

Expected: PASS — no lint errors.

- [ ] **Step 3: Verify builds compile**

Run: `make build SERVICE=room-service && make build SERVICE=room-worker && make build SERVICE=inbox-worker`

Expected: All three binaries build successfully.

- [ ] **Step 4: Push to remote**

```bash
git push -u origin feat/role-update
```
