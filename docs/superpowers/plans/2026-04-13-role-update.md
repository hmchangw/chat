# Role Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a role-update operation that promotes or demotes room members between `owner` and `member` roles, with cross-site replication via the outbox/inbox pattern.

**Architecture:** Room-service validates the request (room type, owner auth, role validity, duplicate role, last-owner guard) and publishes to the ROOMS JetStream stream using a canonical subject. Room-worker consumes the message, updates the subscription in MongoDB using additive role operations (`$addToSet` for promote, `$pull` for demote), and publishes a SubscriptionUpdateEvent. For cross-site members, an OutboxEvent replicates the change via inbox-worker.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-04-13-role-update-design.md`

---

### Task 1: Model and Subject Changes

**Files:**
- Modify: `pkg/model/subscription.go` — change `Role` to `Roles []Role`
- Modify: `pkg/model/event.go` — add `UpdateRoleRequest`
- Modify: `pkg/model/model_test.go` — update `TestSubscriptionJSON`, `TestSubscriptionUpdateEventJSON`, add `TestUpdateRoleRequestJSON`
- Modify: `pkg/subject/subject.go` — add `MemberRoleUpdate`, `MemberRoleUpdateWildcard`, `RoomCanonical`, `RoomCanonicalWildcard`
- Modify: `pkg/subject/subject_test.go` — add tests for new builders
- Modify: `pkg/stream/stream.go` — update `Rooms` to use canonical subjects
- Modify: `pkg/stream/stream_test.go` — update `Rooms` test case
- Fix compilation in: `room-service/`, `room-worker/`, `inbox-worker/`, `message-gatekeeper/`, `history-service/`

- [ ] **Step 1: Update Subscription model**

In `pkg/model/subscription.go`, change `Role Role` to `Roles []Role`:

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

```go
type UpdateRoleRequest struct {
	RoomID  string `json:"roomId"  bson:"roomId"`
	Account string `json:"account" bson:"account"`
	NewRole Role   `json:"newRole" bson:"newRole"`
}
```

- [ ] **Step 3: Fix all compilation errors from Role -> Roles**

Update every `Role:` field to `Roles:` slice across all services. Add a temporary `hasRole` helper in `room-service/handler.go` for the invite owner check. Add `"slices"` import to `inbox-worker/handler_test.go` and use `slices.Contains` for assertions.

- [ ] **Step 4: Update model tests**

Update `TestSubscriptionJSON` and `TestSubscriptionUpdateEventJSON` to use `Roles`. Add `TestUpdateRoleRequestJSON`.

- [ ] **Step 5: Add subject builders**

In `pkg/subject/subject.go`:
```go
func MemberRoleUpdate(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.role-update", account, roomID, siteID)
}
func MemberRoleUpdateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.role-update", siteID)
}
func RoomCanonical(siteID, operation string) string {
	return fmt.Sprintf("chat.room.canonical.%s.%s", siteID, operation)
}
func RoomCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.room.canonical.%s.>", siteID)
}
```

- [ ] **Step 6: Add subject and stream tests, update ROOMS stream**

Update `pkg/stream/stream.go` `Rooms` to `chat.room.canonical.{siteID}.>`. Add test entries for all new builders and wildcards.

- [ ] **Step 7: Run `make test`, commit**

---

### Task 2: Room-Service Helpers

**Files:**
- Create: `room-service/helper.go` — `HasRole`, `sanitizeError`
- Create: `room-service/helper_test.go` — tests for both
- Modify: `room-service/handler.go` — replace local `hasRole` with `HasRole`

- [ ] **Step 1: Create helper_test.go with tests for HasRole and sanitizeError**

- [ ] **Step 2: Create helper.go**

```go
func HasRole(roles []model.Role, target model.Role) bool {
	for _, r := range roles {
		if r == target { return true }
	}
	return false
}

func sanitizeError(err error) string {
	msg := err.Error()
	userFacingPrefixes := []string{
		"invalid", "only owners", "cannot demote",
		"requester not found", "target user", "role update", "user already", "user is",
	}
	for _, pfx := range userFacingPrefixes {
		if strings.HasPrefix(msg, pfx) { return msg }
	}
	return "internal error"
}
```

- [ ] **Step 3: Replace local `hasRole` with `HasRole` in handler.go, run tests, commit**

---

### Task 3: Room-Service Role Update Handler

**Files:**
- Modify: `room-service/store.go` — add `CountOwners`
- Modify: `room-service/store_mongo.go` — implement `CountOwners`
- Modify: `room-service/handler.go` — update `publishToStream` signature, add `natsUpdateRole`/`handleUpdateRole`, register in `RegisterCRUD`
- Modify: `room-service/main.go` — update wiring
- Modify: `room-service/handler_test.go` — update existing tests, add 7 role-update tests
- Regenerate: `room-service/mock_store_test.go`

- [ ] **Step 1: Add CountOwners to store interface and implement**

```go
func (s *MongoStore) CountOwners(ctx context.Context, roomID string) (int, error) {
	count, err := s.subscriptions.CountDocuments(ctx, bson.M{"roomId": roomID, "roles": "owner"})
	if err != nil { return 0, fmt.Errorf("count owners for room %q: %w", roomID, err) }
	return int(count), nil
}
```

- [ ] **Step 2: Regenerate mocks — `make generate SERVICE=room-service`**

- [ ] **Step 3: Update publishToStream signature to `func(ctx, subj string, data []byte) error`**

Update handler struct, constructor, `handleInvite` call site, and `main.go` wiring.

- [ ] **Step 4: Add handleUpdateRole with validation**

Validation chain: parse subject → unmarshal → validate role → room must be group → requester must be owner → target must be in room → promote check (`HasRole(target, owner)` → "user is already an owner") → demote check (`!HasRole(target, owner)` → "user is not an owner") → last-owner guard → publish to `RoomCanonical(siteID, "member.role-update")`.

- [ ] **Step 5: Register subscription in RegisterCRUD**

```go
nc.QueueSubscribe(subject.MemberRoleUpdateWildcard(h.siteID), queue, h.natsUpdateRole)
```

- [ ] **Step 6: Update existing tests for new publishToStream signature**

- [ ] **Step 7: Add 7 role-update handler tests**

`TestHandler_UpdateRole_Success`, `_NonOwnerRejected`, `_DMRejected`, `_InvalidRole`, `_AlreadyHasRole` (promote owner who is already owner), `_DemoteNonOwner` (demote member who is not owner), `_LastOwnerCannotDemote`.

- [ ] **Step 8: Run `make test`, commit**

---

### Task 4: Room-Worker Role Update Processing

**Files:**
- Modify: `room-worker/store.go` — add `GetSubscription`, `AddRole`, `RemoveRole`
- Modify: `room-worker/store_mongo.go` — implement new methods
- Modify: `room-worker/handler.go` — subject-based dispatch, `processRoleUpdate`
- Modify: `room-worker/handler_test.go` — 3 role-update tests
- Regenerate: `room-worker/mock_store_test.go`

- [ ] **Step 1: Add new methods to SubscriptionStore**

```go
type SubscriptionStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	ListByRoom(ctx context.Context, roomID string) ([]model.Subscription, error)
	IncrementUserCount(ctx context.Context, roomID string) error
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
	AddRole(ctx context.Context, account, roomID string, role model.Role) error
	RemoveRole(ctx context.Context, account, roomID string, role model.Role) error
}
```

- [ ] **Step 2: Implement store methods**

```go
func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.account": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("subscription not found for %q in room %q: %w", account, roomID, err)
	}
	return &sub, nil
}

func (s *MongoStore) AddRole(ctx context.Context, account, roomID string, role model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$addToSet": bson.M{"roles": role}}
	_, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("add role %q for %q in room %q: %w", role, account, roomID, err)
	}
	return nil
}

func (s *MongoStore) RemoveRole(ctx context.Context, account, roomID string, role model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$pull": bson.M{"roles": role}}
	_, err := s.subscriptions.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("remove role %q for %q in room %q: %w", role, account, roomID, err)
	}
	return nil
}
```

- [ ] **Step 3: Regenerate mocks — `make generate SERVICE=room-worker`**

- [ ] **Step 4: Add subject-based dispatch to HandleJetStreamMsg**

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

- [ ] **Step 5: Implement processRoleUpdate**

Promote adds `"owner"` via `AddRole`; demote removes `"owner"` via `RemoveRole`. Re-reads the subscription after the update to get actual roles for the event.

```go
func (h *Handler) processRoleUpdate(ctx context.Context, data []byte) error {
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal role update request: %w", err)
	}
	switch req.NewRole {
	case model.RoleOwner:
		if err := h.store.AddRole(ctx, req.Account, req.RoomID, model.RoleOwner); err != nil {
			return fmt.Errorf("add owner role: %w", err)
		}
	case model.RoleMember:
		if err := h.store.RemoveRole(ctx, req.Account, req.RoomID, model.RoleOwner); err != nil {
			return fmt.Errorf("remove owner role: %w", err)
		}
	}
	updatedSub, err := h.store.GetSubscription(ctx, req.Account, req.RoomID)
	if err != nil {
		return fmt.Errorf("get updated subscription: %w", err)
	}
	now := time.Now().UTC()
	subEvt := model.SubscriptionUpdateEvent{
		UserID:       updatedSub.User.ID,
		Subscription: *updatedSub,
		Action:       "role_updated",
		Timestamp:    now.UnixMilli(),
	}
	subEvtData, err := json.Marshal(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}
	if err := h.publish(ctx, subject.SubscriptionUpdate(updatedSub.User.Account), subEvtData); err != nil {
		return fmt.Errorf("publish subscription update: %w", err)
	}
	// Look up user's siteID to determine if cross-site
	user, err := h.store.GetUser(ctx, req.Account)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	if user.SiteID != h.siteID {
		outbox := model.OutboxEvent{
			Type: "role_updated", SiteID: h.siteID, DestSiteID: user.SiteID,
			Payload: subEvtData, Timestamp: now.UnixMilli(),
		}
		outboxData, _ := json.Marshal(outbox)
		outboxSubj := subject.Outbox(h.siteID, user.SiteID, "role_updated")
		if err := h.publish(ctx, outboxSubj, outboxData); err != nil {
			return fmt.Errorf("publish outbox: %w", err)
		}
	}
	return nil
}
```

- [ ] **Step 6: Write 3 room-worker tests**

`TestHandler_ProcessRoleUpdate_Promote` — mock `AddRole`, `GetSubscription` returns `["member", "owner"]`, verify event contains both roles.

`TestHandler_ProcessRoleUpdate_Demote` — mock `RemoveRole`, `GetSubscription` returns `["member"]`, verify event does not contain owner.

`TestHandler_ProcessRoleUpdate_CrossSite` — mock `AddRole`, `GetSubscription` returns sub with different siteID, verify 2 publishes (subscription update + outbox).

- [ ] **Step 7: Run `make test SERVICE=room-worker`, commit**

---

### Task 5: Inbox-Worker Role Updated Handling

**Files:**
- Modify: `inbox-worker/handler.go` — add `UpdateSubscriptionRoles` to `InboxStore`, add `handleRoleUpdated`, add `"role_updated"` case
- Modify: `inbox-worker/main.go` — implement `UpdateSubscriptionRoles` on `mongoInboxStore`
- Modify: `inbox-worker/handler_test.go` — add stub method, 2 tests

- [ ] **Step 1: Add UpdateSubscriptionRoles to InboxStore**

```go
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	UpsertRoom(ctx context.Context, room *model.Room) error
	UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error
}
```

Inbox-worker uses `$set` with the full roles slice from the event payload — it receives the authoritative state from the room-worker.

- [ ] **Step 2: Add role_updated case and handleRoleUpdated**

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
	// No SubscriptionUpdateEvent publish — room-worker already handles that via NATS supercluster
	return nil
}
```

- [ ] **Step 3: Implement UpdateSubscriptionRoles on mongoInboxStore**

```go
func (s *mongoInboxStore) UpdateSubscriptionRoles(ctx context.Context, account, roomID string, roles []model.Role) error {
	filter := bson.M{"u.account": account, "roomId": roomID}
	update := bson.M{"$set": bson.M{"roles": roles}}
	_, err := s.subCol.UpdateOne(ctx, filter, update)
	return err
}
```

- [ ] **Step 4: Update stub store in tests, add 2 tests**

`TestHandleEvent_RoleUpdated` — happy path: verify store receives correct roles, verify notification published.

`TestHandleEvent_RoleUpdated_InvalidPayload` — error path: invalid JSON payload.

- [ ] **Step 5: Run `make test SERVICE=inbox-worker`, commit**

---

### Task 6: Integration Tests — room-service

**File:** `room-service/integration_test.go`

- [ ] **Step 1: Add TestMongoStore_CountOwners_Integration**

Seed 4 subscriptions: 2 owners in r1, 1 member in r1, 1 owner in r2. Verify `CountOwners(r1) == 2`, `CountOwners(r2) == 1`, `CountOwners("nonexistent") == 0`.

- [ ] **Step 2: Run `make test-integration SERVICE=room-service`, commit**

---

### Task 7: Integration Tests — room-worker

**File:** `room-worker/integration_test.go`

- [ ] **Step 1: Add TestMongoStore_GetSubscription_Integration**

Seed a subscription, verify `GetSubscription` returns correct data. Verify not-found case returns error.

- [ ] **Step 2: Add TestMongoStore_AddRole_RemoveRole_Integration**

Seed subscription with `["member"]`. `AddRole("owner")` → verify roles become `["member", "owner"]`. `AddRole("owner")` again → verify idempotent (still 2 roles). `RemoveRole("owner")` → verify roles become `["member"]`.

- [ ] **Step 3: Run `make test-integration SERVICE=room-worker`, commit**

---

### Task 8: Integration Tests — inbox-worker

**File:** `inbox-worker/integration_test.go`

- [ ] **Step 1: Fix recordingPublisher signature**

Update `Publish(subj string, data []byte) error` to `Publish(_ context.Context, subj string, data []byte) error` to match the `Publisher` interface.

- [ ] **Step 2: Add TestInboxWorker_RoleUpdated_Integration**

Seed a member subscription. Build and process a `role_updated` outbox event promoting to owner. Verify the subscription in MongoDB has `["member", "owner"]` in roles. Verify notification published to correct subject.

- [ ] **Step 3: Run `make test-integration SERVICE=inbox-worker`, commit**

---

### Task 9: Final Verification

- [ ] **Step 1: Run `make test`** — all unit tests pass
- [ ] **Step 2: Run `make lint`** — 0 issues
- [ ] **Step 3: Run `make build SERVICE=room-service && make build SERVICE=room-worker && make build SERVICE=inbox-worker`** — all compile
- [ ] **Step 4: Run integration tests for all 3 services** — all pass
- [ ] **Step 5: Push to remote** — `git push -u origin feat/role-update`
