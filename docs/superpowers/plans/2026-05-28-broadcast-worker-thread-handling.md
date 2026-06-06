# Broadcast Worker Thread Message Handling — Implementation Plan

> **Status: IMPLEMENTED** — PR #245 (`claude/gallant-galileo-ice0C`). All tasks below are complete. See the design spec's "Implementation Notes" section for what diverged from the original plan. For notification-worker work that was intentionally left out, see `docs/thread-reply-notifications.md`.
>
> **Post-plan work:** After the initial implementation, three rounds of high-effort code review and a simplification pass produced additional commits that are not reflected in the four tasks below. See the "Post-Plan Fixes and Refactoring" section at the bottom of this file.

**Goal:** Add real-time fan-out of thread reply events (created, updated, deleted) to thread subscribers in broadcast-worker.

**Architecture:** Three new handler methods (`handleThreadCreated`, `handleThreadUpdated`, `handleThreadDeleted`) are added, each routing through a TShow gate added to the existing `handleCreated`/`handleUpdated`/`handleDeleted`. A new `ListThreadSubscriptions` store method queries the `thread_subscriptions` MongoDB collection. Fan-out publishes to `subject.UserRoomEvent(account)` per subscriber — the same per-user subject used for DMs. `Subscription.ThreadUnread` updates are out of scope; that gap lives in message-worker.

**Tech Stack:** Go 1.25, `go.mongodb.org/mongo-driver/v2`, `go.uber.org/mock`, `github.com/stretchr/testify`, `pkg/model`, `pkg/subject`, `pkg/mention`

---

## File Map

| File | Change |
|------|--------|
| `broadcast-worker/store.go` | Add `ListThreadSubscriptions` to `Store` interface |
| `broadcast-worker/store_mongo.go` | Add `threadSubCol` field, update `NewMongoStore`, implement `ListThreadSubscriptions` |
| `broadcast-worker/main.go` | Pass `db.Collection("thread_subscriptions")` to `NewMongoStore` |
| `broadcast-worker/mock_store_test.go` | Regenerated — never edit manually |
| `broadcast-worker/handler.go` | Add TShow routing gate to each event handler + three new thread handler methods |
| `broadcast-worker/handler_test.go` | New table-driven tests for all three thread handler methods |
| `broadcast-worker/integration_test.go` | Update all 6 existing `NewMongoStore` calls + add `ListThreadSubscriptions` integration test |

---

## Task 1: Store — interface, implementation, and integration test

**Files:**
- Modify: `broadcast-worker/store.go`
- Modify: `broadcast-worker/store_mongo.go`
- Modify: `broadcast-worker/main.go`
- Modify: `broadcast-worker/integration_test.go`
- Regenerate: `broadcast-worker/mock_store_test.go`

- [x] **Step 1: Add `ListThreadSubscriptions` to the `Store` interface**

In `broadcast-worker/store.go`, replace the current interface block with:

```go
//go:generate mockgen -destination=mock_store_test.go -package=main . Store
//go:generate mockgen -destination=mock_userstore_test.go -package=main github.com/hmchangw/chat/pkg/userstore UserStore
//go:generate mockgen -destination=mock_keystore_test.go -package=main . RoomKeyProvider

// Store defines data access operations for the broadcast worker.
type Store interface {
	GetRoom(ctx context.Context, roomID string) (*model.Room, error)
	GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error)
	ListSubscriptions(ctx context.Context, roomID string) ([]model.Subscription, error)
	ListThreadSubscriptions(ctx context.Context, parentMessageID, siteID string) ([]model.ThreadSubscription, error)
	UpdateRoomLastMessage(ctx context.Context, roomID, msgID string, msgAt time.Time, mentionAll bool) error
	SetSubscriptionMentions(ctx context.Context, roomID string, accounts []string) error
}
```

- [x] **Step 2: Update `mongoStore` to hold the thread subscriptions collection**

In `broadcast-worker/store_mongo.go`, replace the struct and constructor:

```go
type mongoStore struct {
	roomCol      *mongo.Collection
	subCol       *mongo.Collection
	threadSubCol *mongo.Collection
}

func NewMongoStore(roomCol, subCol, threadSubCol *mongo.Collection) *mongoStore {
	return &mongoStore{roomCol: roomCol, subCol: subCol, threadSubCol: threadSubCol}
}
```

- [x] **Step 3: Implement `ListThreadSubscriptions` in `store_mongo.go`**

Add this method to `mongoStore` (after `SetSubscriptionMentions`):

```go
func (m *mongoStore) ListThreadSubscriptions(ctx context.Context, parentMessageID, siteID string) ([]model.ThreadSubscription, error) {
	filter := bson.M{"parentMessageId": parentMessageID, "siteId": siteID}
	cursor, err := m.threadSubCol.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query thread subscriptions for parent %s: %w", parentMessageID, err)
	}
	defer cursor.Close(ctx)
	var subs []model.ThreadSubscription
	if err := cursor.All(ctx, &subs); err != nil {
		return nil, fmt.Errorf("decode thread subscriptions: %w", err)
	}
	return subs, nil
}
```

- [x] **Step 4: Update `main.go` to pass the thread subscriptions collection**

In `broadcast-worker/main.go`, change line 74 from:

```go
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))
```

to:

```go
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
```

- [x] **Step 5: Update all `NewMongoStore` calls in `integration_test.go`**

There are **six** calls to `NewMongoStore` in `broadcast-worker/integration_test.go`, one per existing integration test function:

- `TestBroadcastWorker_ChannelRoom_Integration` (~line 80)
- `TestBroadcastWorker_ChannelRoom_MentionAll_Integration` (~line 126)
- `TestBroadcastWorker_ChannelRoom_IndividualMention_Integration` (~line 162)
- `TestBroadcastWorker_DMRoom_Integration` (~line 217)
- `TestBroadcastWorker_ChannelRoom_EncryptionDisabled_Integration` (~line 279)
- `TestBroadcastWorker_PersistsLastMessage_Integration` (~line 330)

Each currently passes two collections. Replace **all six** occurrences:

```go
// Before (all six occurrences):
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"))

// After (all six occurrences):
store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
```

- [x] **Step 6: Add `ListThreadSubscriptions` integration test to `integration_test.go`**

Append this test at the end of `broadcast-worker/integration_test.go`:

```go
func TestBroadcastWorker_ListThreadSubscriptions_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	parentMsgID := "parent-msg-1"
	siteID := "site-a"

	_, err := db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "ts1", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "alice", UserID: "u-alice", SiteID: siteID,
		},
		model.ThreadSubscription{
			ID: "ts2", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
		// different parent — must NOT be returned
		model.ThreadSubscription{
			ID: "ts3", ParentMessageID: "other-parent", RoomID: "r1",
			ThreadRoomID: "tr2", UserAccount: "charlie", UserID: "u-charlie", SiteID: siteID,
		},
		// different siteID — must NOT be returned
		model.ThreadSubscription{
			ID: "ts4", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "diana", UserID: "u-diana", SiteID: "site-b",
		},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	subs, err := store.ListThreadSubscriptions(ctx, parentMsgID, siteID)
	require.NoError(t, err)
	require.Len(t, subs, 2)
	accounts := []string{subs[0].UserAccount, subs[1].UserAccount}
	assert.ElementsMatch(t, []string{"alice", "bob"}, accounts)
}
```

- [x] **Step 7: Regenerate mocks**

```bash
make generate SERVICE=broadcast-worker
```

Expected: `broadcast-worker/mock_store_test.go` is updated with a `ListThreadSubscriptions` mock method. No other files change.

- [x] **Step 8: Verify compilation**

```bash
make build SERVICE=broadcast-worker
```

Expected: exits 0, binary produced.

- [x] **Step 9: Run unit tests**

```bash
make test SERVICE=broadcast-worker
```

Expected: all existing tests pass.

- [x] **Step 10: Run integration tests**

```bash
make test-integration SERVICE=broadcast-worker
```

Expected: all tests pass including `TestBroadcastWorker_ListThreadSubscriptions_Integration`.

- [x] **Step 11: Commit**

```bash
git add broadcast-worker/store.go broadcast-worker/store_mongo.go broadcast-worker/main.go \
        broadcast-worker/mock_store_test.go broadcast-worker/integration_test.go
git commit -m "feat(broadcast-worker): add ListThreadSubscriptions to store"
```

---

## Task 2: `handleThreadCreated` — TDD

**Files:**
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/handler.go`

- [x] **Step 1: Add failing tests for `handleThreadCreated` to `handler_test.go`**

Append the following test function at the end of `broadcast-worker/handler_test.go`:

```go
func TestHandler_HandleThreadCreated(t *testing.T) {
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	const sender = "alice"

	tests := []struct {
		name            string
		content         string
		threadSubs      []model.ThreadSubscription
		metaErr         error
		listErr         error
		userLookupErr   error
		wantSubjects    []string
		wantErrContains string
	}{
		{
			name:    "fans out to thread subscribers excluding sender",
			content: "hello thread",
			threadSubs: []model.ThreadSubscription{
				{UserAccount: sender},
				{UserAccount: "bob"},
				{UserAccount: "carol"},
			},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("carol"),
			},
		},
		{
			name:    "mentioned non-subscriber included in fan-out",
			content: "hey @dave",
			threadSubs: []model.ThreadSubscription{
				{UserAccount: "bob"},
			},
			wantSubjects: []string{
				subject.UserRoomEvent("bob"),
				subject.UserRoomEvent("dave"),
			},
		},
		{
			name:    "mentioned user already a thread subscriber - deduped",
			content: "hey @bob",
			threadSubs: []model.ThreadSubscription{
				{UserAccount: "bob"},
			},
			wantSubjects: []string{subject.UserRoomEvent("bob")},
		},
		{
			name:         "only sender in subscriber list - no publish",
			content:      "hello",
			threadSubs:   []model.ThreadSubscription{{UserAccount: sender}},
			wantSubjects: nil,
		},
		{
			name:         "empty subscriber list and no mentions - no publish",
			content:      "hello",
			threadSubs:   []model.ThreadSubscription{},
			wantSubjects: nil,
		},
		{
			name:            "GetRoomMeta error - returns error",
			content:         "hello",
			metaErr:         errors.New("mongo down"),
			wantErrContains: "get room meta",
		},
		{
			name:            "ListThreadSubscriptions error - returns error",
			content:         "hello",
			listErr:         errors.New("db error"),
			wantErrContains: "list thread subscriptions",
		},
		{
			name:          "user lookup error - warns and continues, subscriber still notified",
			content:       "hello",
			threadSubs:    []model.ThreadSubscription{{UserAccount: "bob"}},
			userLookupErr: errors.New("db error"),
			wantSubjects:  []string{subject.UserRoomEvent("bob")},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			us := NewMockUserStore(ctrl)
			pub := &mockPublisher{}
			keyStore := NewMockRoomKeyProvider(ctrl)

			evt := model.MessageEvent{
				Event:  model.EventCreated,
				SiteID: siteID,
				Message: model.Message{
					ID:                    "reply-1",
					RoomID:                "room-1",
					UserID:                "u-alice",
					UserAccount:           sender,
					Content:               tc.content,
					CreatedAt:             msgTime,
					ThreadParentMessageID: parentMsgID,
					TShow:                 false,
				},
			}
			data, _ := json.Marshal(evt)

			// Parse content to know what FindUsersByAccounts will be called with.
			// mention.Parse("hello thread") → []
			// mention.Parse("hey @dave")    → ["dave"]
			// mention.Parse("hey @bob")     → ["bob"]
			// mention.Parse("hello")        → []
			var expectedLookup []string
			switch tc.content {
			case "hey @dave":
				expectedLookup = []string{sender, "dave"}
			case "hey @bob":
				expectedLookup = []string{sender, "bob"}
			default:
				expectedLookup = []string{sender}
			}
			if tc.userLookupErr != nil {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), expectedLookup).Return(nil, tc.userLookupErr)
			} else {
				us.EXPECT().FindUsersByAccounts(gomock.Any(), expectedLookup).Return(nil, nil)
			}

			if tc.metaErr != nil {
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(roommetacache.Meta{}, tc.metaErr)
			} else if tc.listErr != nil {
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
				store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return(nil, tc.listErr)
			} else {
				store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
				store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return(tc.threadSubs, nil)
			}

			h := NewHandler(store, us, pub, keyStore, false)
			err := h.HandleMessage(context.Background(), data)

			if tc.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErrContains)
				assert.Empty(t, pub.records)
				return
			}

			require.NoError(t, err)
			gotSubjects := make([]string, len(pub.records))
			for i, r := range pub.records {
				gotSubjects[i] = r.subject
			}
			assert.ElementsMatch(t, tc.wantSubjects, gotSubjects)

			// Verify event payload on each record.
			for _, r := range pub.records {
				var roomEvt model.RoomEvent
				require.NoError(t, json.Unmarshal(r.data, &roomEvt))
				assert.Equal(t, model.RoomEventNewMessage, roomEvt.Type)
				assert.Equal(t, "room-1", roomEvt.RoomID)
				assert.Equal(t, siteID, roomEvt.SiteID)
				require.NotNil(t, roomEvt.Message)
				assert.Equal(t, "reply-1", roomEvt.Message.ID)
				assert.Equal(t, parentMsgID, roomEvt.Message.ThreadParentMessageID)
			}
		})
	}
}

func TestHandler_ThreadCreated_TShow_FallsThroughToRoomBroadcast(t *testing.T) {
	// TShow=true thread replies must NOT go through handleThreadCreated.
	// They fall through to the existing channel broadcast path.
	msgTime := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	us := NewMockUserStore(ctrl)
	pub := &mockPublisher{}
	keyStore := NewMockRoomKeyProvider(ctrl)

	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID:                    "reply-tshow",
			RoomID:                "room-1",
			UserID:                "u-alice",
			UserAccount:           "alice",
			Content:               "also in channel",
			CreatedAt:             msgTime,
			ThreadParentMessageID: "parent-msg-1",
			TShow:                 true, // falls through to room broadcast
		},
	}
	data, _ := json.Marshal(evt)

	// Existing room broadcast path is called (UpdateRoomLastMessage + GetRoomMeta).
	// ListThreadSubscriptions must NOT be called.
	key := testRoomKey(t)
	keyStore.EXPECT().Get(gomock.Any(), "room-1").Return(key, nil)
	us.EXPECT().FindUsersByAccounts(gomock.Any(), []string{"alice"}).Return(nil, nil)
	store.EXPECT().UpdateRoomLastMessage(gomock.Any(), "room-1", "reply-tshow", msgTime, false).Return(nil)
	store.EXPECT().GetRoomMeta(gomock.Any(), "room-1").Return(metaOf(testChannelRoom), nil)
	// NO store.EXPECT().ListThreadSubscriptions(...)

	h := NewHandler(store, us, pub, keyStore, true)
	require.NoError(t, h.HandleMessage(context.Background(), data))

	// Published to the room channel subject, not per-user.
	require.Len(t, pub.records, 1)
	assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)
}
```

- [x] **Step 2: Run tests to confirm failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadCreated` FAILS — the mock for `UpdateRoomLastMessage` is called unexpectedly (the routing gate does not exist yet), or `ListThreadSubscriptions` is never called.

- [x] **Step 3: Add the TShow routing gate in `handleCreated` and a stub `handleThreadCreated`**

In `broadcast-worker/handler.go`, at the top of `handleCreated` (after `msg := evt.Message`), add:

```go
func (h *Handler) handleCreated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message

	if msg.ThreadParentMessageID != "" && !msg.TShow {
		return h.handleThreadCreated(ctx, evt)
	}

	// ... rest of existing handleCreated code unchanged ...
```

Then add the stub method after `handleCreated`:

```go
func (h *Handler) handleThreadCreated(ctx context.Context, evt *model.MessageEvent) error {
	return nil
}
```

- [x] **Step 4: Run tests — confirm different failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadCreated` FAILS — `GetRoomMeta` and `ListThreadSubscriptions` are expected by mocks but not called (stub returns nil immediately). `TestHandler_ThreadCreated_TShow_FallsThroughToRoomBroadcast` PASSES (existing path unchanged). All previously passing tests still pass.

- [x] **Step 5: Implement `handleThreadCreated`**

Replace the stub in `broadcast-worker/handler.go` with the full implementation:

```go
func (h *Handler) handleThreadCreated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message

	// Parse mentions first so we know which accounts to look up for sender enrichment.
	// Use parsed.Accounts (not ResolveFromParsed) for the fan-out set — raw account names
	// are sufficient and work even when the user store can't resolve a mentioned account.
	parsed := mention.Parse(msg.Content)
	lookupAccounts := dedupedAccounts(msg.UserAccount, parsed.Accounts)
	users, lookupErr := h.userStore.FindUsersByAccounts(ctx, lookupAccounts)
	if lookupErr != nil {
		slog.Warn("user lookup failed for thread reply, falling back to account",
			"error", lookupErr, "parentMessageID", msg.ThreadParentMessageID)
	}
	userByAccount := make(map[string]model.User, len(users))
	for i := range users {
		userByAccount[users[i].Account] = users[i]
	}

	meta, err := h.store.GetRoomMeta(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("get room meta %s: %w", msg.RoomID, err)
	}

	threadSubs, err := h.store.ListThreadSubscriptions(ctx, msg.ThreadParentMessageID, evt.SiteID)
	if err != nil {
		return fmt.Errorf("list thread subscriptions for parent %s: %w", msg.ThreadParentMessageID, err)
	}

	// Union of thread subscribers + mentioned accounts, dedup, exclude sender.
	seen := map[string]struct{}{msg.UserAccount: {}}
	var fanOut []string
	for i := range threadSubs {
		acc := threadSubs[i].UserAccount
		if _, ok := seen[acc]; ok {
			continue
		}
		seen[acc] = struct{}{}
		fanOut = append(fanOut, acc)
	}
	for _, acc := range parsed.Accounts {
		if _, ok := seen[acc]; ok {
			continue
		}
		seen[acc] = struct{}{}
		fanOut = append(fanOut, acc)
	}

	if len(fanOut) == 0 {
		slog.Debug("no thread subscribers to notify for thread reply",
			"parentMessageID", msg.ThreadParentMessageID)
		return nil
	}

	clientMsg := buildClientMessage(&msg, userByAccount)

	// Encrypt once for channel rooms when encryption is enabled.
	var encJSON json.RawMessage
	if meta.Type == model.RoomTypeChannel && h.encrypt {
		msgJSON, err := json.Marshal(clientMsg)
		if err != nil {
			return fmt.Errorf("marshal thread client message: %w", err)
		}
		key, err := h.currentRoomKey(ctx, meta.ID)
		if err != nil {
			return err
		}
		encrypted, err := h.encoder.Encode(meta.ID, string(msgJSON), key.KeyPair.PrivateKey, key.Version)
		if err != nil {
			return fmt.Errorf("encrypt thread message for room %s: %w", meta.ID, err)
		}
		encJSON, err = json.Marshal(encrypted)
		if err != nil {
			return fmt.Errorf("marshal encrypted thread message: %w", err)
		}
	}

	for _, account := range fanOut {
		roomEvt := buildRoomEvent(meta, clientMsg)
		if encJSON != nil {
			roomEvt.EncryptedMessage = encJSON
			roomEvt.Message = nil
		}
		payload, err := json.Marshal(roomEvt)
		if err != nil {
			return fmt.Errorf("marshal thread event for user %s: %w", account, err)
		}
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(account), payload); err != nil {
			slog.Error("publish thread event failed",
				"error", err, "account", account, "parentMessageID", msg.ThreadParentMessageID)
		}
	}
	return nil
}
```

- [x] **Step 6: Run tests — confirm pass**

```bash
make test SERVICE=broadcast-worker
```

Expected: all tests pass including `TestHandler_HandleThreadCreated` and `TestHandler_ThreadCreated_TShow_FallsThroughToRoomBroadcast`.

- [x] **Step 7: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat(broadcast-worker): fan-out thread reply created events to thread subscribers"
```

---

## Task 3: `handleThreadUpdated` — TDD

**Files:**
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/handler.go`

- [x] **Step 1: Add failing tests for `handleThreadUpdated` to `handler_test.go`**

Append at the end of `broadcast-worker/handler_test.go`:

```go
func TestHandler_HandleThreadUpdated(t *testing.T) {
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	edited := time.Date(2026, 5, 28, 10, 5, 0, 0, time.UTC)

	makeThreadEditEvt := func(tshow bool) []byte {
		evt := model.MessageEvent{
			Event:  model.EventUpdated,
			SiteID: siteID,
			Message: model.Message{
				ID:                    "reply-1",
				RoomID:                "room-1",
				UserID:                "u-alice",
				UserAccount:           "alice",
				Content:               "edited content",
				ThreadParentMessageID: parentMsgID,
				TShow:                 tshow,
				EditedAt:              &edited,
				UpdatedAt:             &edited,
			},
		}
		data, _ := json.Marshal(evt)
		return data
	}

	t.Run("fans out edit to thread subscribers excluding sender", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return([]model.ThreadSubscription{
			{UserAccount: "alice"}, // sender — excluded
			{UserAccount: "bob"},
			{UserAccount: "carol"},
		}, nil)

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadEditEvt(false)))

		require.Len(t, pub.records, 2)
		gotSubjects := []string{pub.records[0].subject, pub.records[1].subject}
		assert.ElementsMatch(t, []string{
			subject.UserRoomEvent("bob"),
			subject.UserRoomEvent("carol"),
		}, gotSubjects)

		var editEvt model.EditRoomEvent
		require.NoError(t, json.Unmarshal(pub.records[0].data, &editEvt))
		assert.Equal(t, model.RoomEventMessageEdited, editEvt.Type)
		assert.Equal(t, "room-1", editEvt.RoomID)
		assert.Equal(t, siteID, editEvt.SiteID)
		assert.Equal(t, "reply-1", editEvt.MessageID)
		assert.Equal(t, "edited content", editEvt.NewContent)
		assert.Equal(t, "alice", editEvt.EditedBy)
		assert.True(t, editEvt.EditedAt.Equal(edited))
	})

	t.Run("empty subscriber list - no publish", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return([]model.ThreadSubscription{}, nil)

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadEditEvt(false)))
		assert.Empty(t, pub.records)
	})

	t.Run("GetRoom error - returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("mongo down"))

		h := NewHandler(store, us, pub, keyStore, false)
		err := h.HandleMessage(context.Background(), makeThreadEditEvt(false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch room")
		assert.Empty(t, pub.records)
	})

	t.Run("ListThreadSubscriptions error - returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return(nil, errors.New("db error"))

		h := NewHandler(store, us, pub, keyStore, false)
		err := h.HandleMessage(context.Background(), makeThreadEditEvt(false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list thread subscriptions")
		assert.Empty(t, pub.records)
	})

	t.Run("TShow=true falls through to room broadcast not thread handler", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		// ListThreadSubscriptions must NOT be called for TShow=true

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadEditEvt(true)))

		require.Len(t, pub.records, 1)
		assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)
	})
}
```

- [x] **Step 2: Run tests — confirm failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadUpdated` FAILS — `ListThreadSubscriptions` is expected but not called (no routing gate yet for updated path).

- [x] **Step 3: Add routing gate in `handleUpdated` and a stub `handleThreadUpdated`**

In `broadcast-worker/handler.go`, add the gate at the top of `handleUpdated`, after the `EditedAt`/`UpdatedAt` guard:

```go
func (h *Handler) handleUpdated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	if msg.EditedAt == nil || msg.UpdatedAt == nil {
		return fmt.Errorf("updated event missing EditedAt or UpdatedAt: %s", msg.ID)
	}

	if msg.ThreadParentMessageID != "" && !msg.TShow {
		return h.handleThreadUpdated(ctx, evt)
	}

	// ... rest of existing handleUpdated code unchanged ...
```

Add the stub:

```go
func (h *Handler) handleThreadUpdated(ctx context.Context, evt *model.MessageEvent) error {
	return nil
}
```

- [x] **Step 4: Run tests — confirm different failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadUpdated` FAILS — `ListThreadSubscriptions` expected but not called (stub returns nil). All pre-existing tests still pass.

- [x] **Step 5: Implement `handleThreadUpdated`**

Replace the stub in `broadcast-worker/handler.go`:

```go
func (h *Handler) handleThreadUpdated(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message

	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("fetch room %s: %w", msg.RoomID, err)
	}

	threadSubs, err := h.store.ListThreadSubscriptions(ctx, msg.ThreadParentMessageID, evt.SiteID)
	if err != nil {
		return fmt.Errorf("list thread subscriptions for parent %s: %w", msg.ThreadParentMessageID, err)
	}

	edit := model.EditRoomEvent{
		Type:       model.RoomEventMessageEdited,
		RoomID:     room.ID,
		SiteID:     room.SiteID,
		Timestamp:  time.Now().UTC().UnixMilli(),
		MessageID:  msg.ID,
		NewContent: msg.Content,
		EditedBy:   msg.UserAccount,
		EditedAt:   *msg.EditedAt,
		UpdatedAt:  *msg.UpdatedAt,
	}
	if room.Type == model.RoomTypeChannel && h.encrypt {
		if err := h.encryptEditedContent(ctx, room.ID, &edit); err != nil {
			return err
		}
	}

	payload, err := json.Marshal(edit)
	if err != nil {
		return fmt.Errorf("marshal thread edit event: %w", err)
	}

	for i := range threadSubs {
		if threadSubs[i].UserAccount == msg.UserAccount {
			continue
		}
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(threadSubs[i].UserAccount), payload); err != nil {
			slog.Error("publish thread edit event failed",
				"error", err,
				"account", threadSubs[i].UserAccount,
				"parentMessageID", msg.ThreadParentMessageID,
			)
		}
	}
	return nil
}
```

- [x] **Step 6: Run tests — confirm pass**

```bash
make test SERVICE=broadcast-worker
```

Expected: all tests pass including `TestHandler_HandleThreadUpdated`.

- [x] **Step 7: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat(broadcast-worker): fan-out thread reply edit events to thread subscribers"
```

---

## Task 4: `handleThreadDeleted` — TDD

**Files:**
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/handler.go`

- [x] **Step 1: Add failing tests for `handleThreadDeleted` to `handler_test.go`**

Append at the end of `broadcast-worker/handler_test.go`:

```go
func TestHandler_HandleThreadDeleted(t *testing.T) {
	const parentMsgID = "parent-msg-1"
	const siteID = "site-a"
	deletedAt := time.Date(2026, 5, 28, 10, 10, 0, 0, time.UTC)

	makeThreadDelEvt := func(tshow bool) []byte {
		evt := model.MessageEvent{
			Event:  model.EventDeleted,
			SiteID: siteID,
			Message: model.Message{
				ID:                    "reply-1",
				RoomID:                "room-1",
				UserID:                "u-alice",
				UserAccount:           "alice",
				ThreadParentMessageID: parentMsgID,
				TShow:                 tshow,
				UpdatedAt:             &deletedAt,
			},
		}
		data, _ := json.Marshal(evt)
		return data
	}

	t.Run("fans out delete to thread subscribers excluding sender", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return([]model.ThreadSubscription{
			{UserAccount: "alice"}, // sender — excluded
			{UserAccount: "bob"},
		}, nil)

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadDelEvt(false)))

		require.Len(t, pub.records, 1)
		assert.Equal(t, subject.UserRoomEvent("bob"), pub.records[0].subject)

		var delEvt model.DeleteRoomEvent
		require.NoError(t, json.Unmarshal(pub.records[0].data, &delEvt))
		assert.Equal(t, model.RoomEventMessageDeleted, delEvt.Type)
		assert.Equal(t, "room-1", delEvt.RoomID)
		assert.Equal(t, siteID, delEvt.SiteID)
		assert.Equal(t, "reply-1", delEvt.MessageID)
		assert.Equal(t, "alice", delEvt.DeletedBy)
		assert.True(t, delEvt.DeletedAt.Equal(deletedAt))
	})

	t.Run("empty subscriber list - no publish", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return([]model.ThreadSubscription{}, nil)

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadDelEvt(false)))
		assert.Empty(t, pub.records)
	})

	t.Run("GetRoom error - returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(nil, errors.New("mongo down"))

		h := NewHandler(store, us, pub, keyStore, false)
		err := h.HandleMessage(context.Background(), makeThreadDelEvt(false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch room")
		assert.Empty(t, pub.records)
	})

	t.Run("ListThreadSubscriptions error - returns error", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		store.EXPECT().ListThreadSubscriptions(gomock.Any(), parentMsgID, siteID).Return(nil, errors.New("db error"))

		h := NewHandler(store, us, pub, keyStore, false)
		err := h.HandleMessage(context.Background(), makeThreadDelEvt(false))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "list thread subscriptions")
		assert.Empty(t, pub.records)
	})

	t.Run("TShow=true falls through to room broadcast not thread handler", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		us := NewMockUserStore(ctrl)
		pub := &mockPublisher{}
		keyStore := NewMockRoomKeyProvider(ctrl)

		room := &model.Room{ID: "room-1", Type: model.RoomTypeChannel, SiteID: siteID}
		store.EXPECT().GetRoom(gomock.Any(), "room-1").Return(room, nil)
		// ListThreadSubscriptions must NOT be called for TShow=true

		h := NewHandler(store, us, pub, keyStore, false)
		require.NoError(t, h.HandleMessage(context.Background(), makeThreadDelEvt(true)))

		require.Len(t, pub.records, 1)
		assert.Equal(t, subject.RoomEvent("room-1"), pub.records[0].subject)
	})
}
```

- [x] **Step 2: Run tests — confirm failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadDeleted` FAILS — `ListThreadSubscriptions` expected but not called.

- [x] **Step 3: Add routing gate in `handleDeleted` and a stub `handleThreadDeleted`**

In `broadcast-worker/handler.go`, add the gate at the top of `handleDeleted`, after the `UpdatedAt` guard:

```go
func (h *Handler) handleDeleted(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message
	if msg.UpdatedAt == nil {
		return fmt.Errorf("deleted event missing UpdatedAt: %s", msg.ID)
	}

	if msg.ThreadParentMessageID != "" && !msg.TShow {
		return h.handleThreadDeleted(ctx, evt)
	}

	// ... rest of existing handleDeleted code unchanged ...
```

Add the stub:

```go
func (h *Handler) handleThreadDeleted(ctx context.Context, evt *model.MessageEvent) error {
	return nil
}
```

- [x] **Step 4: Run tests — confirm different failure**

```bash
make test SERVICE=broadcast-worker
```

Expected: `TestHandler_HandleThreadDeleted` FAILS — stub returns nil, no publishes happen. All pre-existing tests still pass.

- [x] **Step 5: Implement `handleThreadDeleted`**

Replace the stub in `broadcast-worker/handler.go`:

```go
func (h *Handler) handleThreadDeleted(ctx context.Context, evt *model.MessageEvent) error {
	msg := evt.Message

	room, err := h.store.GetRoom(ctx, msg.RoomID)
	if err != nil {
		return fmt.Errorf("fetch room %s: %w", msg.RoomID, err)
	}

	threadSubs, err := h.store.ListThreadSubscriptions(ctx, msg.ThreadParentMessageID, evt.SiteID)
	if err != nil {
		return fmt.Errorf("list thread subscriptions for parent %s: %w", msg.ThreadParentMessageID, err)
	}

	del := model.DeleteRoomEvent{
		Type:      model.RoomEventMessageDeleted,
		RoomID:    room.ID,
		SiteID:    room.SiteID,
		Timestamp: time.Now().UTC().UnixMilli(),
		MessageID: msg.ID,
		DeletedBy: msg.UserAccount,
		DeletedAt: *msg.UpdatedAt,
		UpdatedAt: *msg.UpdatedAt,
	}

	payload, err := json.Marshal(del)
	if err != nil {
		return fmt.Errorf("marshal thread delete event: %w", err)
	}

	for i := range threadSubs {
		if threadSubs[i].UserAccount == msg.UserAccount {
			continue
		}
		if err := h.pub.Publish(ctx, subject.UserRoomEvent(threadSubs[i].UserAccount), payload); err != nil {
			slog.Error("publish thread delete event failed",
				"error", err,
				"account", threadSubs[i].UserAccount,
				"parentMessageID", msg.ThreadParentMessageID,
			)
		}
	}
	return nil
}
```

- [x] **Step 6: Run tests — confirm pass**

```bash
make test SERVICE=broadcast-worker
```

Expected: all tests pass including `TestHandler_HandleThreadDeleted`.

- [x] **Step 7: Commit**

```bash
git add broadcast-worker/handler.go broadcast-worker/handler_test.go
git commit -m "feat(broadcast-worker): fan-out thread reply delete events to thread subscribers"
```

---

## Task 5: Final verification

- [x] **Step 1: Run lint**

```bash
make lint
```

Expected: exits 0, no errors.

- [x] **Step 2: Run all unit tests with race detector**

```bash
make test SERVICE=broadcast-worker
```

Expected: all tests pass with `-race`.

- [x] **Step 3: Run integration tests**

```bash
make test-integration SERVICE=broadcast-worker
```

Expected: all tests pass.

- [x] **Step 4: Push branch**

```bash
git push -u origin claude/gallant-galileo-ice0C
```

---

## Out-of-scope reminder

`Subscription.ThreadUnread` — the array on a user's room subscription that tracks unread thread parent message IDs — is NOT updated by broadcast-worker. This is a known gap in message-worker that must be addressed in a separate task. Without it, the unread thread badge on the client will not reflect new thread replies until the user reads the thread explicitly.

---

## Post-Plan Fixes and Refactoring

After the four tasks above were complete, three rounds of high-effort code review (`/code-review --effort high`) and a simplification pass (`/simplify`) identified and fixed additional issues. All changes are in PR #245 on branch `claude/gallant-galileo-ice0C`.

### Correctness fixes (broadcast-worker)

- **`evt.Timestamp` propagation** (`fix(broadcast-worker): propagate evt.Timestamp`): `EditRoomEvent` and `DeleteRoomEvent` were stamping `Timestamp` with `time.Now()` at broadcast time instead of forwarding `evt.Timestamp` from the canonical event. This caused the timestamp to differ across JetStream redeliveries and drift from the canonical timeline. Fixed in all four edit/delete handlers; unit and integration tests updated with exact-equality assertions.

- **TShow=true badge on delete** (`fix(broadcast-worker): publish tcount badge for TShow=true deleted thread replies`): When a `TShow=true` thread reply is deleted, `handleDeleted` takes the normal room broadcast path and `handleThreadDeleted` is never called. The tcount badge update was therefore never published. Fixed by detecting `ThreadParentMessageID != ""` in `handleDeleted` and calling `publishThreadBadge` there.

- **history-service tcount errors best-effort** (`fix(history-service): treat messages_by_room tcount errors as best-effort in decrementParentTcount`): Cassandra errors on the secondary `messages_by_room` tcount mirror were propagating and causing JetStream redelivery, re-running a CAS-decrement that was already committed on `messages_by_id`. Fixed by logging the mirror error and returning the already-decremented value.

### Simplification and defensive fixes (broadcast-worker)

- **`shouldUseThreadFanOut` rename** (was `isThreadReply`): the predicate encodes a routing decision, not structural identity — renamed for clarity at all 3 call sites.

- **`buildEditRoomEvent` / `buildDeleteRoomEvent` helpers**: eliminated duplicated 9-field struct literals that appeared independently in the non-thread and thread handler variants.

- **`publishThreadBadge` helper**: consolidated the `publishThreadMetadata` + error-log pattern duplicated in `handleThreadDeleted` and `handleDeleted`.

- **`handleThreadDeleted` default-branch `return nil` removed**: the early return silently skipped the tcount badge block for unknown room types. Badge block now runs unconditionally after the switch.

- **Nil guard in `handleThreadUpdated`**: defensive `EditedAt`/`UpdatedAt` nil check added before dereferencing, mirroring the outer guard in `handleUpdated`.

- **Integration test timestamp assertion** (`TestBroadcastWorker_ThreadDeleted_Integration`): updated from a wall-clock range check (stale `#13` comment) to an exact `evt.Timestamp` equality assertion, consistent with the corrected implementation.
