# Room Member Management v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework room member management to support cross-site routing via NATS gateways, all-writes-before-ack processing, org resolution via users collection, cross-site owner promotion, system messages, and flipped outbox direction.

**Architecture:** Room-service validates add/remove/role-update requests and publishes to the ROOMS JetStream stream. Room-worker processes all DB writes and event publishing synchronously before ack (NAK on any failure for full retry). Outbox events flow from the room's site to each remote user's site for subscription replication. Inbox-worker handles member_added, member_removed, and role_updated events.

**Tech Stack:** Go 1.25, NATS JetStream, MongoDB, Cassandra, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-04-10-room-member-management-v2-design.md`

---

## File Structure

| File | Change | Responsibility |
|------|--------|---------------|
| `pkg/model/user.go` | Modify | Add `SectID` field |
| `pkg/model/member.go` | Modify | Rename `Username` → `Account`, make `RemoveMemberRequest.Account` omitempty, remove invite-related types, add `MembersAdded`/`MembersRemoved` |
| `pkg/model/event.go` | Modify | Remove `InviteMemberRequest` |
| `pkg/model/message.go` | Modify | Add `Type` and `SysMsgData` fields |
| `pkg/model/model_test.go` | Modify | Update round-trip tests for changed models |
| `room-service/handler.go` | Modify | Remove invite handler, update `handleRemoveMember`/`handleUpdateRole` for `Account` field, remove federation guard, update `GetOrgAccounts` usage |
| `room-service/store.go` | Modify | Update `GetOrgAccounts` contract (users collection instead of hr_data) |
| `room-service/store_mongo.go` | Modify | Rewrite `GetOrgAccounts` to query users collection, remove hr_data, update indexes for `account` instead of `username`, update `CountSubscriptions` filter |
| `room-service/main.go` | Modify | Remove invite subscription |
| `room-worker/handler.go` | Modify | Remove `processInvite`, rewrite `processAddMembers`/`processRemoveMember`/`processRoleUpdate` for all-before-ack + system messages + flipped outbox |
| `room-worker/store.go` | Modify | Remove `GetOrgAccounts` (no longer needed — room-service resolves orgs) |
| `room-worker/store_mongo.go` | Modify | Remove hr_data, update `DeleteRoomMember` to use `account` instead of `username`, remove `GetOrgAccounts`, update indexes |
| `inbox-worker/handler.go` | Modify | Replace `InviteMemberRequest` handling with `MemberChangeEvent`, add `role_updated` handler |
| `inbox-worker/handler_test.go` | Modify | Update tests for new payload types |
| `inbox-worker/main.go` | Modify | Add `UpdateSubscriptionRole` to store |
| `message-worker/store_cassandra.go` | Modify | Add `type` and `sys_msg_data` columns to insert queries |
| All `mock_store_test.go` files | Regenerate | Run `make generate` |

---

### Task 1: Model changes — User.SectID, Message.Type/SysMsgData, member.go renames

**Files:**
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/member.go`
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Update User struct**

In `pkg/model/user.go`, add `SectID` field:

```go
package model

type User struct {
	ID          string `json:"id"          bson:"_id"`
	Account     string `json:"account"     bson:"account"`
	SiteID      string `json:"siteId"      bson:"siteId"`
	SectID      string `json:"sectId"      bson:"sectId"`
	EngName     string `json:"engName"     bson:"engName"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EmployeeID  string `json:"employeeId"  bson:"employeeId"`
}
```

- [ ] **Step 2: Update member.go — rename Username→Account, make RemoveMemberRequest.Account omitempty, add system message structs**

Replace `pkg/model/member.go`:

```go
package model

import "time"

type RoomMemberType string

const (
	RoomMemberTypeIndividual RoomMemberType = "individual"
	RoomMemberTypeOrg        RoomMemberType = "org"
)

type HistoryMode string

const (
	HistoryModeNone HistoryMode = "none"
	HistoryModeAll  HistoryMode = "all"
)

type HistoryConfig struct {
	Mode HistoryMode `json:"mode" bson:"mode"`
}

type AddMembersRequest struct {
	RoomID   string        `json:"roomId"   bson:"roomId"`
	Users    []string      `json:"users"    bson:"users"`
	Orgs     []string      `json:"orgs"     bson:"orgs"`
	Channels []string      `json:"channels" bson:"channels"`
	History  HistoryConfig `json:"history"  bson:"history"`
}

type RoomMemberEntry struct {
	ID      string         `json:"id"                bson:"id"`
	Type    RoomMemberType `json:"type"              bson:"type"`
	Account string         `json:"account,omitempty" bson:"account,omitempty"`
}

type RemoveMemberRequest struct {
	RoomID  string `json:"roomId"            bson:"roomId"`
	Account string `json:"account,omitempty" bson:"account,omitempty"`
	OrgID   string `json:"orgId,omitempty"   bson:"orgId,omitempty"`
}

type UpdateRoleRequest struct {
	RoomID  string `json:"roomId"  bson:"roomId"`
	Account string `json:"account" bson:"account"`
	NewRole Role   `json:"newRole" bson:"newRole"`
}

type RoomMember struct {
	ID     string          `json:"id"     bson:"_id"`
	RoomID string          `json:"rid"    bson:"rid"`
	Ts     time.Time       `json:"ts"     bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}

type MembersAdded struct {
	Individuals     []string `json:"individuals"`
	Orgs            []string `json:"orgs"`
	Channels        []string `json:"channels"`
	AddedUsersCount int      `json:"addedUsersCount"`
}

type MembersRemoved struct {
	Account           string `json:"account,omitempty"`
	OrgID             string `json:"orgId,omitempty"`
	RemovedUsersCount int    `json:"removedUsersCount"`
}
```

- [ ] **Step 3: Remove InviteMemberRequest from event.go**

In `pkg/model/event.go`, delete the `InviteMemberRequest` struct (lines 27-34).

- [ ] **Step 4: Add Type and SysMsgData to Message**

In `pkg/model/message.go`, add two fields after `ThreadParentMessageCreatedAt`:

```go
type Message struct {
	ID                           string     `json:"id"                                     bson:"_id"`
	RoomID                       string     `json:"roomId"                                 bson:"roomId"`
	UserID                       string     `json:"userId"                                 bson:"userId"`
	UserAccount                  string     `json:"userAccount"                            bson:"userAccount"`
	Content                      string     `json:"content"                                bson:"content"`
	CreatedAt                    time.Time  `json:"createdAt"                              bson:"createdAt"`
	ThreadParentMessageID        string     `json:"threadParentMessageId,omitempty"        bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
	Type                         string     `json:"type,omitempty"                         bson:"type,omitempty"`
	SysMsgData                   []byte     `json:"sysMsgData,omitempty"                   bson:"sysMsgData,omitempty"`
}
```

- [ ] **Step 5: Update model_test.go**

Update `TestUserJSON` to include `SectID`:

```go
func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Account: "alice", SiteID: "site-a", SectID: "eng-team", EngName: "Alice", ChineseName: "愛麗絲", EmployeeID: "E001"}
	roundTrip(t, &u, &model.User{})
}
```

Remove `TestInviteMemberRequestJSON` and `TestSubscriptionUpdateEventJSON` references to `InviteMemberRequest`.

Add new tests:

```go
func TestMembersAddedJSON(t *testing.T) {
	src := model.MembersAdded{
		Individuals:     []string{"alice", "bob"},
		Orgs:            []string{"eng-team"},
		Channels:        []string{"room-1"},
		AddedUsersCount: 5,
	}
	roundTrip(t, &src, &model.MembersAdded{})
}

func TestMembersRemovedJSON(t *testing.T) {
	src := model.MembersRemoved{
		Account:           "alice",
		RemovedUsersCount: 1,
	}
	roundTrip(t, &src, &model.MembersRemoved{})
}

func TestMessageJSON_SystemMessage(t *testing.T) {
	sysData, _ := json.Marshal(model.MembersAdded{Individuals: []string{"bob"}, AddedUsersCount: 1})
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
		Content:   "Alice added members to the channel",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Type:      "members_added",
		SysMsgData: sysData,
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	var dst model.Message
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, "members_added", dst.Type)
	assert.Equal(t, sysData, dst.SysMsgData)
}
```

Update existing `TestSubscriptionJSON` and `TestSubscriptionUpdateEventJSON` to remove any references to `Username` in `SubscriptionUser` (it already only has `ID` and `Account`, so this should be fine — just verify compilation).

- [ ] **Step 6: Run tests**

Run: `make test SERVICE=pkg/model`
Expected: PASS (may need to fix compilation errors from removed `InviteMemberRequest` references in other tests)

- [ ] **Step 7: Fix compilation across codebase**

Run: `make lint`

Any file referencing `InviteMemberRequest`, `Username` on `RemoveMemberRequest`/`UpdateRoleRequest`/`RoomMemberEntry`, or `hr_data` will fail. Fix these in subsequent tasks. For now, ensure `pkg/model` tests pass.

- [ ] **Step 8: Commit**

```bash
git add pkg/model/
git commit -m "feat: update models — User.SectID, Message.Type/SysMsgData, rename Username to Account, remove InviteMemberRequest, add MembersAdded/MembersRemoved"
```

---

### Task 2: Room-service store and handler updates

**Files:**
- Modify: `room-service/store.go`
- Modify: `room-service/store_mongo.go`
- Modify: `room-service/handler.go`
- Modify: `room-service/main.go`
- Modify: `room-service/handler_test.go`

- [ ] **Step 1: Update store_mongo.go — replace hr_data with users collection for GetOrgAccounts**

In `room-service/store_mongo.go`, change `GetOrgAccounts` to query the `users` collection:

```go
func (s *MongoStore) GetOrgAccounts(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.users.Find(ctx, bson.M{"sectId": orgID})
	if err != nil {
		return nil, fmt.Errorf("get org accounts for %q: %w", orgID, err)
	}
	var docs []struct {
		Account string `bson:"account"`
	}
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode org accounts for %q: %w", orgID, err)
	}
	accounts := make([]string, len(docs))
	for i, d := range docs {
		accounts[i] = d.Account
	}
	return accounts, nil
}
```

Remove `hrData` from `MongoStore` struct and `NewMongoStore`:

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

Update `CountSubscriptions` to filter by `u.account` instead of `u.username`:

```go
func (s *MongoStore) CountSubscriptions(ctx context.Context, roomID string) (int, error) {
	filter := bson.M{
		"roomId": roomID,
		"u.account": bson.M{
			"$not": bson.Regex{Pattern: `(\.bot$|^p_)`, Options: ""},
		},
	}
	count, err := s.subscriptions.CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count subscriptions for room %q: %w", roomID, err)
	}
	return int(count), nil
}
```

Update `EnsureIndexes` — remove the `u.username` index, update `room_members` index to use `member.account`:

```go
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create subscriptions account index: %w", err)
	}
	_, err = s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "rid", Value: 1}, {Key: "member.type", Value: 1}, {Key: "member.id", Value: 1}, {Key: "member.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create room_members index: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Update handler.go — remove invite, update field names, remove federation guard**

In `room-service/handler.go`:

1. Remove `NatsHandleInvite`, `handleInvite`, and any invite-related methods.

2. In `handleRemoveMember`, change `req.Username` to `req.Account` everywhere:

```go
isSelfLeave := !isOrgRemoval && req.Account == requester
```

3. In `handleUpdateRole`, change `req.Username` to `req.Account` and **remove the federation guard** that checks `targetSub.SiteID != h.siteID`:

```go
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
		return nil, fmt.Errorf("room ID mismatch")
	}
	req.RoomID = roomID

	sub, err := h.store.GetSubscription(ctx, requester, roomID)
	if err != nil {
		return nil, fmt.Errorf("requester not found: %w", err)
	}
	if !HasRole(sub.Roles, model.RoleOwner) {
		return nil, fmt.Errorf("only owners can change roles")
	}

	if req.NewRole != model.RoleOwner && req.NewRole != model.RoleMember {
		return nil, fmt.Errorf("invalid role: %q", req.NewRole)
	}

	// Last owner cannot demote themselves
	if req.NewRole == model.RoleMember && req.Account == requester {
		count, err := h.store.CountOwners(ctx, roomID)
		if err != nil {
			return nil, fmt.Errorf("count owners: %w", err)
		}
		if count <= 1 {
			return nil, fmt.Errorf("cannot demote the last owner")
		}
	}

	data, marshalErr := json.Marshal(req)
	if marshalErr != nil {
		return nil, fmt.Errorf("marshal role update request: %w", marshalErr)
	}

	if err := h.publishToStream(ctx, subj, data); err != nil {
		return nil, fmt.Errorf("publish to stream: %w", err)
	}

	return json.Marshal(map[string]string{"status": "accepted"})
}
```

4. In `RegisterCRUD`, remove the invite subscription. In `room-service/main.go`, remove the `MemberInviteWildcard` queue subscribe call.

- [ ] **Step 3: Regenerate mocks**

Run: `make generate SERVICE=room-service`

- [ ] **Step 4: Update handler_test.go**

Remove all invite-related tests (`TestHandler_InviteOwner_Success`, etc.). Update remaining tests to use `Account` instead of `Username` in `RemoveMemberRequest` and `UpdateRoleRequest`. Remove the federation guard test for role promotion (cross-site owners are now allowed).

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-service`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-service/
git commit -m "feat: update room-service — remove invite, hr_data→users for org resolution, Username→Account, remove federation guard on promotion"
```

---

### Task 3: Room-worker — all-writes-before-ack, system messages, flipped outbox

**Files:**
- Modify: `room-worker/handler.go`
- Modify: `room-worker/store.go`
- Modify: `room-worker/store_mongo.go`

- [ ] **Step 1: Update store — remove hr_data, update field names**

In `room-worker/store.go`, remove `GetOrgAccounts` (room-service now resolves orgs). Add `GetOrgAccountsBySectID` if room-worker still needs it for remove-by-org, or keep the existing `GetOrgAccounts` but change its implementation:

Actually, `processRemoveMember` still needs to resolve org members. Update `GetOrgAccounts` to query users collection:

In `room-worker/store_mongo.go`:

```go
func (s *MongoStore) GetOrgAccounts(ctx context.Context, orgID string) ([]string, error) {
	cursor, err := s.users.Find(ctx, bson.M{"sectId": orgID})
	if err != nil {
		return nil, fmt.Errorf("find org accounts: %w", err)
	}
	var results []struct {
		Account string `bson:"account"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode org accounts: %w", err)
	}
	accounts := make([]string, len(results))
	for i, r := range results {
		accounts[i] = r.Account
	}
	return accounts, nil
}
```

Remove `hrData` from `MongoStore` struct and constructor.

Update `DeleteRoomMember` to use `member.account` instead of `member.username`:

```go
func (s *MongoStore) DeleteRoomMember(ctx context.Context, account, roomID string) error {
	_, err := s.roomMembers.DeleteOne(ctx, bson.M{"member.account": account, "rid": roomID})
	if err != nil {
		return fmt.Errorf("delete room member: %w", err)
	}
	return nil
}
```

Update `EnsureIndexes` — remove `u.username` index, update `room_members` index to use `member.account`:

```go
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	_, err := s.subscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "roomId", Value: 1}, {Key: "u.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create subscriptions account index: %w", err)
	}
	_, err = s.roomMembers.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "rid", Value: 1}, {Key: "member.type", Value: 1}, {Key: "member.id", Value: 1}, {Key: "member.account", Value: 1}},
		Options: options.Index().SetUnique(true),
	})
	if err != nil {
		return fmt.Errorf("create room_members index: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Rewrite processAddMembers — all synchronous before ack, system message, flipped outbox**

Replace `processAddMembers` in `room-worker/handler.go`:

```go
func (h *Handler) processAddMembers(ctx context.Context, data []byte) error {
	var req model.AddMembersRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal add members request: %w", err)
	}
	now := time.Now().UTC()
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room: %w", err)
	}

	// Look up users and build subscriptions
	var subs []*model.Subscription
	var userSiteIDs map[string]string = make(map[string]string) // account → siteID
	for _, account := range req.Users {
		user, err := h.store.GetUser(ctx, account)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				slog.Warn("user not found, skipping", "account", account)
				continue
			}
			return fmt.Errorf("get user %q: %w", account, err)
		}
		sub := &model.Subscription{
			ID: uuid.New().String(), User: model.SubscriptionUser{ID: user.ID, Account: user.Account},
			RoomID: req.RoomID, SiteID: user.SiteID, Roles: []model.Role{model.RoleMember}, JoinedAt: now,
		}
		if req.History.Mode != model.HistoryModeAll {
			sub.HistorySharedSince = &now
		}
		subs = append(subs, sub)
		userSiteIDs[user.Account] = user.SiteID
	}

	// 1. Bulk create subscriptions
	if err := h.store.BulkCreateSubscriptions(ctx, subs); err != nil {
		return fmt.Errorf("bulk create subscriptions: %w", err)
	}

	// 2. Write room_members (only if orgs involved)
	hasOrgs := len(req.Orgs) > 0
	if hasOrgs {
		for _, account := range req.Users {
			m := &model.RoomMember{ID: uuid.New().String(), RoomID: req.RoomID, Ts: now,
				Member: model.RoomMemberEntry{ID: account, Type: model.RoomMemberTypeIndividual, Account: account}}
			if err := h.store.CreateRoomMember(ctx, m); err != nil {
				slog.Error("create room member failed", "error", err, "account", account)
			}
		}
		for _, orgID := range req.Orgs {
			m := &model.RoomMember{ID: uuid.New().String(), RoomID: req.RoomID, Ts: now,
				Member: model.RoomMemberEntry{ID: orgID, Type: model.RoomMemberTypeOrg}}
			if err := h.store.CreateRoomMember(ctx, m); err != nil {
				slog.Error("create org room member failed", "error", err, "orgID", orgID)
			}
		}
	}

	// 3. Increment userCount
	if len(subs) > 0 {
		if err := h.store.IncrementUserCount(ctx, req.RoomID); err != nil {
			return fmt.Errorf("increment user count: %w", err)
		}
	}

	// 4. Publish SubscriptionUpdateEvent per member
	for _, sub := range subs {
		evt := model.SubscriptionUpdateEvent{UserID: sub.User.ID, Subscription: *sub, Action: "added", Timestamp: now.UnixMilli()}
		evtData, _ := json.Marshal(evt)
		if err := h.publishLocal(ctx, subject.SubscriptionUpdate(sub.User.Account), evtData); err != nil {
			return fmt.Errorf("publish subscription update for %q: %w", sub.User.Account, err)
		}
	}

	// 5. Publish MemberChangeEvent
	accounts := make([]string, len(subs))
	for i, sub := range subs {
		accounts[i] = sub.User.Account
	}
	memberEvt := model.MemberChangeEvent{Type: "member-added", RoomID: req.RoomID, Accounts: accounts, SiteID: h.siteID}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publishLocal(ctx, subject.RoomMemberEvent(req.RoomID), memberEvtData); err != nil {
		return fmt.Errorf("publish member change event: %w", err)
	}

	// 6. Publish system message (members_added)
	sysData, _ := json.Marshal(model.MembersAdded{
		Individuals:     req.Users,
		Orgs:            req.Orgs,
		Channels:        req.Channels,
		AddedUsersCount: len(subs),
	})
	// Look up requester name for system message
	requesterName := ""
	if len(subs) > 0 {
		// Requester info is not in AddMembersRequest — get from room.CreatedBy or first existing member
		// For now use the room creator as a fallback; the requester account is in the NATS subject
	}
	sysMsgEvt := model.MessageEvent{
		Message: model.Message{
			ID: uuid.New().String(), RoomID: req.RoomID,
			Content:    requesterName + " added members to the channel",
			CreatedAt:  now,
			Type:       "members_added",
			SysMsgData: sysData,
		},
		SiteID:    h.siteID,
		Timestamp: now.UnixMilli(),
	}
	sysMsgData, _ := json.Marshal(sysMsgEvt)
	if err := h.publishLocal(ctx, subject.MsgCanonicalCreated(h.siteID), sysMsgData); err != nil {
		return fmt.Errorf("publish members_added system message: %w", err)
	}

	// 7. Outbox for cross-site members (room site → user site)
	for _, sub := range subs {
		userSiteID := userSiteIDs[sub.User.Account]
		if userSiteID != "" && userSiteID != room.SiteID {
			outbox := model.OutboxEvent{Type: "member_added", SiteID: room.SiteID, DestSiteID: userSiteID, Payload: memberEvtData, Timestamp: now.UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, userSiteID, "member_added"), outboxData); err != nil {
				return fmt.Errorf("publish outbox for %q: %w", sub.User.Account, err)
			}
		}
	}

	return nil
}
```

- [ ] **Step 3: Rewrite processRemoveMember — all synchronous, system message, flipped outbox**

Replace `processRemoveMember`:

```go
func (h *Handler) processRemoveMember(ctx context.Context, data []byte) error {
	var req model.RemoveMemberRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal remove member request: %w", err)
	}

	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room: %w", err)
	}

	var accounts []string
	var userSiteIDs map[string]string = make(map[string]string)

	if req.OrgID != "" {
		orgAccounts, err := h.store.GetOrgAccounts(ctx, req.OrgID)
		if err != nil {
			return fmt.Errorf("get org accounts %q: %w", req.OrgID, err)
		}
		for _, account := range orgAccounts {
			if err := h.store.DeleteSubscription(ctx, account, req.RoomID); err != nil {
				slog.Error("delete subscription failed", "error", err, "account", account)
			}
			user, err := h.store.GetUser(ctx, account)
			if err == nil {
				userSiteIDs[account] = user.SiteID
			}
		}
		accounts = orgAccounts
	} else {
		if err := h.store.DeleteSubscription(ctx, req.Account, req.RoomID); err != nil {
			return fmt.Errorf("delete subscription: %w", err)
		}
		user, err := h.store.GetUser(ctx, req.Account)
		if err == nil {
			userSiteIDs[req.Account] = user.SiteID
		}
		accounts = []string{req.Account}
	}

	// Delete room_members
	if req.OrgID != "" {
		if err := h.store.DeleteOrgRoomMember(ctx, req.OrgID, req.RoomID); err != nil {
			slog.Error("delete org room member failed", "error", err, "orgID", req.OrgID)
		}
		for _, account := range accounts {
			if err := h.store.DeleteRoomMember(ctx, account, req.RoomID); err != nil {
				slog.Error("delete room member failed", "error", err, "account", account)
			}
		}
	} else {
		if err := h.store.DeleteRoomMember(ctx, req.Account, req.RoomID); err != nil {
			slog.Error("delete room member failed", "error", err, "account", req.Account)
		}
	}

	// Decrement userCount
	if len(accounts) > 0 {
		if err := h.store.DecrementUserCount(ctx, req.RoomID); err != nil {
			return fmt.Errorf("decrement user count: %w", err)
		}
	}

	// Publish SubscriptionUpdateEvent per removed account
	now := time.Now().UTC()
	for _, account := range accounts {
		evt := model.SubscriptionUpdateEvent{
			Subscription: model.Subscription{RoomID: req.RoomID, User: model.SubscriptionUser{Account: account}},
			Action: "removed", Timestamp: now.UnixMilli(),
		}
		evtData, _ := json.Marshal(evt)
		if err := h.publishLocal(ctx, subject.SubscriptionUpdate(account), evtData); err != nil {
			return fmt.Errorf("publish subscription update for %q: %w", account, err)
		}
	}

	// Publish MemberChangeEvent
	memberEvt := model.MemberChangeEvent{Type: "member-removed", RoomID: req.RoomID, Accounts: accounts, SiteID: h.siteID}
	memberEvtData, _ := json.Marshal(memberEvt)
	if err := h.publishLocal(ctx, subject.RoomMemberEvent(req.RoomID), memberEvtData); err != nil {
		return fmt.Errorf("publish member change event: %w", err)
	}

	// Publish system message (member_removed)
	removedTarget := req.Account
	if req.OrgID != "" {
		removedTarget = req.OrgID
	}
	sysData, _ := json.Marshal(model.MembersRemoved{
		Account:           req.Account,
		OrgID:             req.OrgID,
		RemovedUsersCount: len(accounts),
	})
	sysMsgEvt := model.MessageEvent{
		Message: model.Message{
			ID: uuid.New().String(), RoomID: req.RoomID,
			Content:    "removed " + removedTarget + " from the room",
			CreatedAt:  now,
			Type:       "member_removed",
			SysMsgData: sysData,
		},
		SiteID:    h.siteID,
		Timestamp: now.UnixMilli(),
	}
	sysMsgData, _ := json.Marshal(sysMsgEvt)
	if err := h.publishLocal(ctx, subject.MsgCanonicalCreated(h.siteID), sysMsgData); err != nil {
		return fmt.Errorf("publish member_removed system message: %w", err)
	}

	// Outbox for cross-site members
	for _, account := range accounts {
		userSiteID := userSiteIDs[account]
		if userSiteID != "" && userSiteID != room.SiteID {
			outbox := model.OutboxEvent{Type: "member_removed", SiteID: room.SiteID, DestSiteID: userSiteID, Payload: memberEvtData, Timestamp: now.UnixMilli()}
			outboxData, _ := json.Marshal(outbox)
			if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, userSiteID, "member_removed"), outboxData); err != nil {
				return fmt.Errorf("publish outbox for %q: %w", account, err)
			}
		}
	}

	return nil
}
```

- [ ] **Step 4: Rewrite processRoleUpdate — add outbox for cross-site**

```go
func (h *Handler) processRoleUpdate(ctx context.Context, data []byte) error {
	var req model.UpdateRoleRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal role update request: %w", err)
	}

	if err := h.store.UpdateSubscriptionRole(ctx, req.Account, req.RoomID, req.NewRole); err != nil {
		return fmt.Errorf("update subscription role: %w", err)
	}

	now := time.Now().UTC()
	evt := model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{RoomID: req.RoomID, User: model.SubscriptionUser{Account: req.Account}, Roles: []model.Role{req.NewRole}},
		Action: "role_updated", Timestamp: now.UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)
	if err := h.publishLocal(ctx, subject.SubscriptionUpdate(req.Account), evtData); err != nil {
		return fmt.Errorf("publish subscription update: %w", err)
	}

	// Outbox for cross-site role change
	room, err := h.store.GetRoom(ctx, req.RoomID)
	if err != nil {
		return fmt.Errorf("get room for outbox: %w", err)
	}
	user, err := h.store.GetUser(ctx, req.Account)
	if err == nil && user.SiteID != room.SiteID {
		outbox := model.OutboxEvent{Type: "role_updated", SiteID: room.SiteID, DestSiteID: user.SiteID, Payload: evtData, Timestamp: now.UnixMilli()}
		outboxData, _ := json.Marshal(outbox)
		if err := h.publishOutbox(ctx, subject.Outbox(room.SiteID, user.SiteID, "role_updated"), outboxData); err != nil {
			return fmt.Errorf("publish role update outbox: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 5: Remove processInvite and update HandleJetStreamMsg routing**

Remove `processInvite` entirely. Update `HandleJetStreamMsg`:

```go
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	var err error
	subj := msg.Subject()
	parts := strings.Split(subj, ".")
	if len(parts) >= 2 {
		op := parts[len(parts)-2] + "." + parts[len(parts)-1]
		switch op {
		case "member.add":
			err = h.processAddMembers(ctx, msg.Data())
		case "member.remove":
			err = h.processRemoveMember(ctx, msg.Data())
		case "member.role-update":
			err = h.processRoleUpdate(ctx, msg.Data())
		default:
			slog.Warn("unknown member operation", "op", op, "subject", subj)
			err = fmt.Errorf("unknown member operation: %s", op)
		}
	}
	if err != nil {
		slog.Error("process message failed", "error", err, "subject", subj)
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("failed to nak message", "err", nakErr)
		}
		return
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("failed to ack message", "err", ackErr)
	}
}
```

- [ ] **Step 6: Regenerate mocks**

Run: `make generate SERVICE=room-worker`

- [ ] **Step 7: Run tests**

Run: `make test SERVICE=room-worker`
Expected: FAIL — existing tests reference old patterns. Fix in Task 4.

- [ ] **Step 8: Commit**

```bash
git add room-worker/
git commit -m "feat: rewrite room-worker — all-before-ack, system messages, flipped outbox, remove processInvite, Username→Account"
```

---

### Task 4: Room-worker handler tests

**Files:**
- Modify: `room-worker/handler_test.go`

- [ ] **Step 1: Update existing tests for new patterns**

Update all tests to:
- Use `Account` instead of `Username` in `RemoveMemberRequest` and `UpdateRoleRequest`
- Remove tests for `processInvite`
- Verify no async goroutines — all assertions can be checked immediately after the call (remove `time.Sleep` calls if any)
- Add assertions for system message publishing to MESSAGES_CANONICAL subject
- Add assertions for outbox direction: `outbox.{room.SiteID}.to.{user.SiteID}.{eventType}`

- [ ] **Step 2: Add test for processAddMembers with system message**

```go
func TestHandler_ProcessAddMembers_SystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Name: "general", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-a", SectID: "eng"}, nil).AnyTimes()
	store.EXPECT().BulkCreateSubscriptions(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().IncrementUserCount(gomock.Any(), "r1").Return(nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a",
		publishLocal:  func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
		publishOutbox: func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
	}

	req := model.AddMembersRequest{RoomID: "r1", Users: []string{"bob"}}
	data, _ := json.Marshal(req)

	err := h.processAddMembers(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the system message publish
	found := false
	for _, p := range published {
		if strings.Contains(p.subj, "canonical") {
			found = true
			var evt model.MessageEvent
			json.Unmarshal(p.data, &evt)
			if evt.Message.Type != "members_added" {
				t.Errorf("system message type = %q, want members_added", evt.Message.Type)
			}
			if evt.Message.SysMsgData == nil {
				t.Error("system message SysMsgData should not be nil")
			}
		}
	}
	if !found {
		t.Error("expected system message published to MESSAGES_CANONICAL")
	}
}
```

- [ ] **Step 3: Add test for processRemoveMember with system message**

```go
func TestHandler_ProcessRemoveMember_SystemMessage(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", Name: "general", SiteID: "site-a"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-a"}, nil).AnyTimes()
	store.EXPECT().DeleteSubscription(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().DeleteRoomMember(gomock.Any(), "bob", "r1").Return(nil)
	store.EXPECT().DecrementUserCount(gomock.Any(), "r1").Return(nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a",
		publishLocal:  func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
		publishOutbox: func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
	}

	req := model.RemoveMemberRequest{RoomID: "r1", Account: "bob"}
	data, _ := json.Marshal(req)

	err := h.processRemoveMember(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, p := range published {
		if strings.Contains(p.subj, "canonical") {
			found = true
			var evt model.MessageEvent
			json.Unmarshal(p.data, &evt)
			if evt.Message.Type != "member_removed" {
				t.Errorf("system message type = %q, want member_removed", evt.Message.Type)
			}
		}
	}
	if !found {
		t.Error("expected system message published to MESSAGES_CANONICAL")
	}
}
```

- [ ] **Step 4: Add test for processRoleUpdate with cross-site outbox**

```go
func TestHandler_ProcessRoleUpdate_CrossSiteOutbox(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	store.EXPECT().UpdateSubscriptionRole(gomock.Any(), "bob", "r1", model.RoleOwner).Return(nil)
	store.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1", SiteID: "site-eu"}, nil)
	store.EXPECT().GetUser(gomock.Any(), "bob").Return(&model.User{ID: "u2", Account: "bob", SiteID: "site-us"}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-eu",
		publishLocal:  func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
		publishOutbox: func(_ context.Context, subj string, data []byte) error { published = append(published, publishedMsg{subj: subj, data: data}); return nil },
	}

	req := model.UpdateRoleRequest{RoomID: "r1", Account: "bob", NewRole: model.RoleOwner}
	data, _ := json.Marshal(req)

	err := h.processRoleUpdate(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have outbox publish to site-us
	outboxFound := false
	for _, p := range published {
		if strings.Contains(p.subj, "outbox.site-eu.to.site-us.role_updated") {
			outboxFound = true
		}
	}
	if !outboxFound {
		t.Error("expected outbox event published to site-us for cross-site role update")
	}
}
```

- [ ] **Step 5: Run tests**

Run: `make test SERVICE=room-worker`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add room-worker/handler_test.go
git commit -m "test: update room-worker tests for all-before-ack, system messages, flipped outbox, Username→Account"
```

---

### Task 5: Inbox-worker — replace InviteMemberRequest, add role_updated handler

**Files:**
- Modify: `inbox-worker/handler.go`
- Modify: `inbox-worker/handler_test.go`
- Modify: `inbox-worker/main.go`

- [ ] **Step 1: Update InboxStore interface**

Add `UpdateSubscriptionRole` to the interface in `inbox-worker/handler.go`:

```go
type InboxStore interface {
	CreateSubscription(ctx context.Context, sub *model.Subscription) error
	DeleteSubscription(ctx context.Context, account string, roomID string) error
	UpdateSubscriptionRole(ctx context.Context, account string, roomID string, role model.Role) error
	UpsertRoom(ctx context.Context, room *model.Room) error
}
```

- [ ] **Step 2: Rewrite handleMemberAdded to use MemberChangeEvent payload**

Replace `handleMemberAdded`:

```go
func (h *Handler) handleMemberAdded(ctx context.Context, evt *model.OutboxEvent) error {
	var change model.MemberChangeEvent
	if err := json.Unmarshal(evt.Payload, &change); err != nil {
		return fmt.Errorf("unmarshal member_added payload: %w", err)
	}

	now := time.Now().UTC()
	for _, account := range change.Accounts {
		sub := model.Subscription{
			ID:       uuid.New().String(),
			User:     model.SubscriptionUser{Account: account},
			RoomID:   change.RoomID,
			SiteID:   change.SiteID,
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: now,
		}
		if err := h.store.CreateSubscription(ctx, &sub); err != nil {
			return fmt.Errorf("create subscription for %q: %w", account, err)
		}

		updateEvt := model.SubscriptionUpdateEvent{
			Subscription: sub,
			Action:       "added",
			Timestamp:    now.UnixMilli(),
		}
		updateData, err := natsutil.MarshalResponse(updateEvt)
		if err != nil {
			return fmt.Errorf("marshal subscription update event: %w", err)
		}
		if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(account), updateData); err != nil {
			slog.Error("publish subscription update failed", "error", err, "account", account)
		}
	}
	return nil
}
```

- [ ] **Step 3: Add handleRoleUpdated**

Add new handler and update the switch:

```go
func (h *Handler) handleRoleUpdated(ctx context.Context, evt *model.OutboxEvent) error {
	var subEvt model.SubscriptionUpdateEvent
	if err := json.Unmarshal(evt.Payload, &subEvt); err != nil {
		return fmt.Errorf("unmarshal role_updated payload: %w", err)
	}

	account := subEvt.Subscription.User.Account
	roomID := subEvt.Subscription.RoomID
	if len(subEvt.Subscription.Roles) == 0 {
		return fmt.Errorf("no role in subscription update event")
	}
	newRole := subEvt.Subscription.Roles[0]

	if err := h.store.UpdateSubscriptionRole(ctx, account, roomID, newRole); err != nil {
		return fmt.Errorf("update subscription role for %q: %w", account, err)
	}

	// Republish locally so the user's client is notified
	updateData, err := natsutil.MarshalResponse(subEvt)
	if err != nil {
		return fmt.Errorf("marshal subscription update event: %w", err)
	}
	if err := h.pub.Publish(ctx, subject.SubscriptionUpdate(account), updateData); err != nil {
		slog.Error("publish subscription update failed", "error", err, "account", account)
	}

	return nil
}
```

Update the switch in `HandleEvent`:

```go
switch evt.Type {
case "member_added":
	return h.handleMemberAdded(ctx, &evt)
case "member_removed":
	return h.handleMemberRemoved(ctx, &evt)
case "role_updated":
	return h.handleRoleUpdated(ctx, &evt)
case "room_sync":
	return h.handleRoomSync(ctx, &evt)
default:
	slog.Warn("unknown event type, skipping", "type", evt.Type)
	return nil
}
```

- [ ] **Step 4: Implement UpdateSubscriptionRole in mongoInboxStore**

In `inbox-worker/main.go`, add:

```go
func (s *mongoInboxStore) UpdateSubscriptionRole(ctx context.Context, account string, roomID string, role model.Role) error {
	_, err := s.subCol.UpdateOne(ctx,
		bson.M{"u.account": account, "roomId": roomID},
		bson.M{"$set": bson.M{"roles": []model.Role{role}}},
	)
	return err
}
```

- [ ] **Step 5: Update handler_test.go**

Update `TestHandleEvent_MemberAdded` — the payload is now `MemberChangeEvent`, not `InviteMemberRequest`:

```go
func TestHandleEvent_MemberAdded(t *testing.T) {
	store := &stubInboxStore{}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	change := model.MemberChangeEvent{
		Type:     "member-added",
		RoomID:   "room-1",
		Accounts: []string{"bob"},
		SiteID:   "site-b",
	}
	changeData, _ := json.Marshal(change)

	evt := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-b",
		DestSiteID: "site-a",
		Payload:    changeData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].User.Account != "bob" {
		t.Errorf("subscription account = %q, want bob", subs[0].User.Account)
	}
}
```

Add `UpdateSubscriptionRole` to `stubInboxStore` and add test for `role_updated`:

```go
func (s *stubInboxStore) UpdateSubscriptionRole(ctx context.Context, account string, roomID string, role model.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.subscriptions {
		if s.subscriptions[i].User.Account == account && s.subscriptions[i].RoomID == roomID {
			s.subscriptions[i].Roles = []model.Role{role}
			return nil
		}
	}
	return nil
}

func TestHandleEvent_RoleUpdated(t *testing.T) {
	store := &stubInboxStore{}
	store.subscriptions = []model.Subscription{
		{ID: "s1", User: model.SubscriptionUser{Account: "bob"}, RoomID: "room-1", Roles: []model.Role{model.RoleMember}},
	}
	pub := &mockPublisher{}
	h := NewHandler(store, pub)

	subEvt := model.SubscriptionUpdateEvent{
		Subscription: model.Subscription{
			RoomID: "room-1",
			User:   model.SubscriptionUser{Account: "bob"},
			Roles:  []model.Role{model.RoleOwner},
		},
		Action:    "role_updated",
		Timestamp: 1735689600000,
	}
	subEvtData, _ := json.Marshal(subEvt)

	evt := model.OutboxEvent{
		Type:       "role_updated",
		SiteID:     "site-eu",
		DestSiteID: "site-us",
		Payload:    subEvtData,
	}
	evtData, _ := json.Marshal(evt)

	err := h.HandleEvent(context.Background(), evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	subs := store.getSubscriptions()
	if len(subs) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(subs))
	}
	if subs[0].Roles[0] != model.RoleOwner {
		t.Errorf("role = %v, want owner", subs[0].Roles)
	}

	records := pub.getRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 publish, got %d", len(records))
	}
	if records[0].subject != "chat.user.bob.event.subscription.update" {
		t.Errorf("subject = %q, want chat.user.bob.event.subscription.update", records[0].subject)
	}
}
```

- [ ] **Step 6: Run tests**

Run: `make test SERVICE=inbox-worker`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add inbox-worker/
git commit -m "feat: update inbox-worker — MemberChangeEvent payload for member_added, add role_updated handler, UpdateSubscriptionRole"
```

---

### Task 6: Message-worker — persist type and sys_msg_data

**Files:**
- Modify: `message-worker/store_cassandra.go`

- [ ] **Step 1: Update SaveMessage to include type and sys_msg_data columns**

In `message-worker/store_cassandra.go`, update both insert queries:

```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, type, sys_msg_data, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, sender, msg.Content, msg.Type, msg.SysMsgData, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, type, sys_msg_data, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, sender, msg.Content, msg.Type, msg.SysMsgData, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}
```

For regular messages, `msg.Type` is `""` and `msg.SysMsgData` is `nil` — Cassandra stores these as null. No impact on normal flow.

- [ ] **Step 2: Run tests**

Run: `make test SERVICE=message-worker`
Expected: PASS (existing tests may need mock updates if they verify the query)

- [ ] **Step 3: Commit**

```bash
git add message-worker/
git commit -m "feat: persist type and sys_msg_data columns in message-worker SaveMessage"
```

---

### Task 7: Final verification

**Files:** None (verification only)

- [ ] **Step 1: Regenerate all mocks**

Run: `make generate`

- [ ] **Step 2: Run full linter**

Run: `make fmt && make lint`
Expected: 0 issues

- [ ] **Step 3: Run full test suite**

Run: `make test`
Expected: All packages PASS

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "chore: fix lint and test issues from room member management v2"
```

- [ ] **Step 5: Push**

```bash
git push origin feat/room-member
```
