# Thread Reply Count via MongoDB (Remove LWT `tcount`) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the per-reply Cassandra LWT `tcount` increment/decrement with an idempotent MongoDB-sourced reply count mirrored onto the existing Cassandra `tcount` column with plain UPDATEs.

**Architecture:** `ThreadRoom` (MongoDB `thread_rooms`) becomes the source of truth for the reply count via a single-document guarded `$inc` (deduped against JetStream redelivery by a bounded `countedReplies` set). The authoritative count is mirrored to `messages_by_id` + `messages_by_room` with plain `UPDATE`s. The read path and Cassandra schema are unchanged.

**Tech Stack:** Go 1.25, MongoDB (`mongo-driver/v2`), Cassandra (`gocql`), `go.uber.org/mock`, `testify`, testcontainers via `pkg/testutil`.

**Spec:** `docs/superpowers/specs/2026-06-04-thread-reply-count-mongodb-design.md`

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `pkg/model/threadroom.go` | ThreadRoom domain type | Add `ReplyCount`, `CountedReplies` |
| `message-worker/store_mongo.go` | Mongo thread store | Seed count in `CreateThreadRoom`; guarded `$inc` in `UpdateThreadRoomLastMessage`; `countedRepliesCap` |
| `message-worker/store_cassandra.go` | Cassandra writes | Add `UpdateParentTcount`; delete `incrementParentTcount` + `casIncrement` |
| `message-worker/store.go` | Store/ThreadStore interfaces | New `UpdateParentTcount`; new `UpdateThreadRoomLastMessage` signature |
| `message-worker/handler.go` | Reply orchestration | Thread the count through; drive the mirror write |
| `history-service/internal/mongorepo/threadroom.go` | Mongo thread repo | Add `DecrementReplyCount` |
| `history-service/internal/cassrepo/write.go` | Cassandra delete | Drop `decrementParentTcount` + `casDecrement`; add `UpdateParentTcount` |
| `history-service/internal/service/service.go` | Service interfaces | `MessageWriter.UpdateParentTcount`; `ThreadRoomRepository.DecrementReplyCount` |
| `history-service/internal/service/messages.go` | DeleteMessage handler | Orchestrate Mongo decrement + Cassandra mirror |
| Generated mocks | — | `make generate` for both services |

**Cross-service invariant:** `UpdateParentTcount` exists independently in both `message-worker` (`Store`) and `history-service` (`cassrepo.Repository` / `MessageWriter`) with identical SQL — each service owns its own.

---

## Task 1: Add ReplyCount + CountedReplies to the ThreadRoom model

**Files:**
- Modify: `pkg/model/threadroom.go`
- Test: `pkg/model/model_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/model/model_test.go`:

```go
func TestThreadRoom_ReplyCountFieldsRoundTrip(t *testing.T) {
	tr := ThreadRoom{
		ID:              "tr-1",
		ParentMessageID: "m-parent",
		RoomID:          "r-1",
		SiteID:          "site-a",
		ReplyAccounts:   []string{"alice"},
		ReplyCount:      3,
		CountedReplies:  []string{"m-1", "m-2", "m-3"},
	}
	roundTrip(t, tr)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=model` (or `go test ./pkg/model/ -run TestThreadRoom_ReplyCountFields -v`)
Expected: FAIL — `tr.ReplyCount` / `tr.CountedReplies` undefined (compile error).

- [ ] **Step 3: Add the fields**

In `pkg/model/threadroom.go`, add inside the `ThreadRoom` struct after `ReplyAccounts`:

```go
	ReplyCount            int       `json:"replyCount"            bson:"replyCount"`
	CountedReplies        []string  `json:"countedReplies"        bson:"countedReplies"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=model`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/threadroom.go pkg/model/model_test.go
git commit -m "feat(model): add ReplyCount and CountedReplies to ThreadRoom"
```

---

## Task 2: Guarded idempotent increment in message-worker's Mongo store

**Files:**
- Modify: `message-worker/store_mongo.go`
- Test: `message-worker/integration_test.go`

This changes `UpdateThreadRoomLastMessage` to a guarded `findOneAndUpdate` returning `(newCount int, applied bool, err error)`, seeds the count + guard in `CreateThreadRoom`, and adds the `countedRepliesCap` constant. The interface and handler callers are updated in Task 4 — this task will not compile against the handler yet, so it is committed together with the interface change is deferred; here we change the impl and a focused integration test that calls the method directly.

- [ ] **Step 1: Write the failing integration test**

Add to `message-worker/integration_test.go` (mirror the existing Mongo setup helpers in that file — use `testutil.MongoDB(t, "message_worker")` and `newThreadStoreMongo(db)`):

```go
//go:build integration

func TestThreadStoreMongo_UpdateThreadRoomLastMessage_IdempotentIncrement(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "message_worker")
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

	now := time.Now().UTC()
	room := &model.ThreadRoom{
		ID:              "tr-inc",
		ParentMessageID: "m-parent-inc",
		RoomID:          "r-1",
		SiteID:          "site-a",
		LastMsgID:       "m-1",
		LastMsgAt:       now,
		ReplyAccounts:   []string{"alice"},
		ReplyCount:      1,
		CountedReplies:  []string{"m-1"},
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, store.CreateThreadRoom(ctx, room))

	// First time we see m-2: increments to 2, applied=true.
	count, applied, err := store.UpdateThreadRoomLastMessage(ctx, "tr-inc", "m-2", []string{"bob"}, now, "m-2")
	require.NoError(t, err)
	assert.True(t, applied)
	assert.Equal(t, 2, count)

	// Redelivery of m-2: no-op, applied=false, count unchanged.
	count, applied, err = store.UpdateThreadRoomLastMessage(ctx, "tr-inc", "m-2", []string{"bob"}, now, "m-2")
	require.NoError(t, err)
	assert.False(t, applied)
	assert.Equal(t, 0, count)

	// Confirm persisted count is still 2.
	got, err := store.GetThreadRoomByParentMessageID(ctx, "m-parent-inc")
	require.NoError(t, err)
	assert.Equal(t, 2, got.ReplyCount)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=message-worker`
Expected: FAIL — `UpdateThreadRoomLastMessage` returns 1 value, not 3 (compile error).

- [ ] **Step 3: Implement the guarded increment**

In `message-worker/store_mongo.go`, add the cap constant near the top (after the `var (...)` block):

```go
// countedRepliesCap bounds ThreadRoom.countedReplies. It only needs to exceed
// the number of replies that can arrive within a JetStream redelivery window,
// so the guarded $inc can dedup redeliveries without the array growing
// unbounded on busy threads.
const countedRepliesCap = 500
```

Replace `UpdateThreadRoomLastMessage` with:

```go
// UpdateThreadRoomLastMessage atomically (single document) increments replyCount
// only if replyID has not already been counted, records replyID in the bounded
// countedReplies guard, bumps the last-message pointer, and merges replyAccounts.
// Returns the new count and applied=true on the first delivery; on redelivery
// (replyID already in countedReplies) it is a no-op returning applied=false.
func (s *threadStoreMongo) UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID, lastMsgID string, replyAccounts []string, lastMsgAt time.Time, replyID string) (int, bool, error) {
	update := bson.M{
		"$inc": bson.M{"replyCount": 1},
		"$set": bson.M{
			"lastMsgAt": lastMsgAt,
			"lastMsgId": lastMsgID,
			"updatedAt": lastMsgAt,
		},
		"$push": bson.M{
			"countedReplies": bson.M{"$each": bson.A{replyID}, "$slice": -countedRepliesCap},
		},
	}
	if len(replyAccounts) > 0 {
		update["$addToSet"] = bson.M{"replyAccounts": bson.M{"$each": replyAccounts}}
	}

	var updated model.ThreadRoom
	err := s.threadRooms.FindOneAndUpdate(
		ctx,
		bson.M{"_id": threadRoomID, "countedReplies": bson.M{"$ne": replyID}},
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("update thread room last message: %w", err)
	}
	return updated.ReplyCount, true, nil
}
```

In `CreateThreadRoom`, seed the guard so the count is consistent with the increment path. Replace the nil-guard block:

```go
func (s *threadStoreMongo) CreateThreadRoom(ctx context.Context, room *model.ThreadRoom) error {
	toInsert := *room
	if toInsert.ReplyAccounts == nil {
		toInsert.ReplyAccounts = []string{}
	}
	if toInsert.CountedReplies == nil {
		toInsert.CountedReplies = []string{}
	}
	_, err := s.threadRooms.InsertOne(ctx, &toInsert)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("insert thread room: %w", errThreadRoomExists)
		}
		return fmt.Errorf("insert thread room: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS for `TestThreadStoreMongo_UpdateThreadRoomLastMessage_IdempotentIncrement`. (Handler/build errors elsewhere are resolved in Task 4 — if the package fails to compile because the handler still calls the old signature, proceed to Task 4 before committing. Otherwise commit now.)

- [ ] **Step 5: Commit (after Task 4 makes the package compile)**

Defer the commit to Task 4 Step 6, which lands the matching interface + handler changes in the same compiling commit.

---

## Task 3: message-worker Cassandra mirror write + remove LWT increment

**Files:**
- Modify: `message-worker/store_cassandra.go`
- Test: `message-worker/integration_test.go`

- [ ] **Step 1: Write the failing integration test**

Add to `message-worker/integration_test.go` (use the existing Cassandra setup helper pattern — `testutil.CassandraKeyspace(t, "message_worker")` returning `(keyspace, session, host)`, and `NewCassandraStore(session, bucket, nil)`; insert a parent row first, then mirror):

```go
//go:build integration

func TestCassandraStore_UpdateParentTcount(t *testing.T) {
	ctx := context.Background()
	_, session, _ := testutil.CassandraKeyspace(t, "message_worker")
	bucket := msgbucket.New(72 * time.Hour)
	store := NewCassandraStore(session, bucket, nil)

	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	parentBucket := bucket.Of(parentCreatedAt)
	// Seed a parent row in both tables (tcount null initially).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, bucket, created_at, message_id, site_id) VALUES (?, ?, ?, ?, ?)`,
		"r-1", parentBucket, parentCreatedAt, "m-parent", "site-a").WithContext(ctx).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, created_at, room_id, site_id) VALUES (?, ?, ?, ?)`,
		"m-parent", parentCreatedAt, "r-1", "site-a").WithContext(ctx).Exec())

	require.NoError(t, store.UpdateParentTcount(ctx, "r-1", "m-parent", parentCreatedAt, 5))

	var byRoom, byID *int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		"r-1", parentBucket, parentCreatedAt, "m-parent").WithContext(ctx).Scan(&byRoom))
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		"m-parent", parentCreatedAt).WithContext(ctx).Scan(&byID))
	require.NotNil(t, byRoom)
	require.NotNil(t, byID)
	assert.Equal(t, 5, *byRoom)
	assert.Equal(t, 5, *byID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=message-worker`
Expected: FAIL — `store.UpdateParentTcount` undefined.

- [ ] **Step 3: Add `UpdateParentTcount`, delete the LWT increment**

In `message-worker/store_cassandra.go`:

Add the method:

```go
// UpdateParentTcount mirrors the authoritative reply count (sourced from
// MongoDB) onto the parent message row in both Cassandra tables with plain
// (non-LWT) UPDATEs. The parent's bucket is derived from parentCreatedAt.
func (s *CassandraStore) UpdateParentTcount(ctx context.Context, roomID, parentID string, parentCreatedAt time.Time, count int) error {
	if err := s.cassSession.Query(
		`UPDATE messages_by_id SET tcount = ? WHERE message_id = ? AND created_at = ?`,
		count, parentID, parentCreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update parent tcount in messages_by_id for %s: %w", parentID, err)
	}
	parentBucket := s.bucket.Of(parentCreatedAt)
	if err := s.cassSession.Query(
		`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		count, roomID, parentBucket, parentCreatedAt, parentID,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update parent tcount in messages_by_room for %s in room %s: %w", parentID, roomID, err)
	}
	return nil
}
```

Delete the entire `casIncrement` function, the `casMaxRetries` const, and the entire `incrementParentTcount` method.

In `SaveThreadMessage` and `saveThreadMessageEncrypted`, remove the trailing call:

```go
	if err := s.incrementParentTcount(ctx, msg); err != nil {
		return err
	}
```

(Leave the rest of both functions intact; they now end after the `ExecuteBatch` success.)

- [ ] **Step 4: Run test to verify it passes**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS for `TestCassandraStore_UpdateParentTcount`. Existing `incrementParentTcount` unit/integration tests will now fail to compile — delete those test cases (search `incrementParentTcount` / `casIncrement` in `*_test.go` and remove the dead tests).

- [ ] **Step 5: Commit (deferred)**

Commit together with Task 4 (the interface + handler changes make the package compile).

---

## Task 4: message-worker interfaces, handler wiring, mocks

**Files:**
- Modify: `message-worker/store.go`
- Modify: `message-worker/handler.go`
- Regenerate: `message-worker/mock_store_test.go`
- Test: `message-worker/handler_test.go`

- [ ] **Step 1: Update the interfaces**

In `message-worker/store.go`, change the `Store` interface to add the mirror and the `ThreadStore` interface to change the increment signature:

```go
type Store interface {
	SaveMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string) error
	SaveThreadMessage(ctx context.Context, msg *model.Message, sender *cassParticipant, siteID string, threadRoomID string) error
	GetMessageSender(ctx context.Context, messageID string) (*cassParticipant, error)
	UpdateParentMessageThreadRoomID(ctx context.Context, parentMessageID, roomID string, parentCreatedAt time.Time, threadRoomID string) error
	UpdateParentTcount(ctx context.Context, roomID, parentID string, parentCreatedAt time.Time, count int) error
}
```

In the `ThreadStore` interface, change:

```go
	UpdateThreadRoomLastMessage(ctx context.Context, threadRoomID, lastMsgID string, replyAccounts []string, lastMsgAt time.Time, replyID string) (int, bool, error)
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=message-worker`
Expected: `mock_store_test.go` updated with the new signatures.

- [ ] **Step 3: Write the failing handler test**

Add to `message-worker/handler_test.go` (follow the existing handler-test construction: build `Handler` with mocked `Store`, `ThreadStore`, `UserStore`, and a capturing publish func). This covers the subsequent-reply mirror:

```go
func TestHandler_SubsequentThreadReply_MirrorsCountToCassandra(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	threadStore := NewMockThreadStore(ctrl)
	userStore := NewMockUserStore(ctrl)

	parentCreatedAt := time.Now().UTC()
	user := &model.User{ID: "u-bob", SiteID: "site-a"}
	userStore.EXPECT().FindUserByID(gomock.Any(), "u-bob").Return(user, nil).AnyTimes()
	userStore.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	// Thread room already exists -> subsequent path.
	threadStore.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(errThreadRoomExists)
	threadStore.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "m-parent").
		Return(&model.ThreadRoom{ID: "tr-1", ParentMessageID: "m-parent", RoomID: "r-1"}, nil)
	store.EXPECT().GetMessageSender(gomock.Any(), "m-parent").
		Return(&cassParticipant{ID: "u-alice", Account: "alice"}, nil)
	threadStore.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	threadStore.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-1", "m-reply", gomock.Any(), gomock.Any(), "m-reply").
		Return(7, true, nil)
	store.EXPECT().UpdateParentMessageThreadRoomID(gomock.Any(), "m-parent", "r-1", parentCreatedAt, "tr-1").Return(nil)
	store.EXPECT().SaveThreadMessage(gomock.Any(), gomock.Any(), gomock.Any(), "site-a", "tr-1").Return(nil)

	// The authoritative count (7) is mirrored to Cassandra.
	store.EXPECT().UpdateParentTcount(gomock.Any(), "r-1", "m-parent", parentCreatedAt, 7).Return(nil)

	h := NewHandler(store, userStore, threadStore, "site-a", func(context.Context, string, []byte, string) error { return nil })

	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID: "m-reply", RoomID: "r-1", UserID: "u-bob", UserAccount: "bob",
			Content: "hi", CreatedAt: time.Now().UTC(),
			ThreadParentMessageID:        "m-parent",
			ThreadParentMessageCreatedAt: &parentCreatedAt,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, h.processMessage(context.Background(), data))
}
```

Add a second test asserting **no mirror on redelivery** (`UpdateThreadRoomLastMessage` returns `(0, false, nil)` and `UpdateParentTcount` is NOT expected):

```go
func TestHandler_SubsequentThreadReply_RedeliveryDoesNotMirror(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	threadStore := NewMockThreadStore(ctrl)
	userStore := NewMockUserStore(ctrl)

	parentCreatedAt := time.Now().UTC()
	userStore.EXPECT().FindUserByID(gomock.Any(), "u-bob").Return(&model.User{ID: "u-bob", SiteID: "site-a"}, nil).AnyTimes()
	userStore.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	threadStore.EXPECT().CreateThreadRoom(gomock.Any(), gomock.Any()).Return(errThreadRoomExists)
	threadStore.EXPECT().GetThreadRoomByParentMessageID(gomock.Any(), "m-parent").
		Return(&model.ThreadRoom{ID: "tr-1", ParentMessageID: "m-parent", RoomID: "r-1"}, nil)
	store.EXPECT().GetMessageSender(gomock.Any(), "m-parent").Return(&cassParticipant{ID: "u-alice", Account: "alice"}, nil)
	threadStore.EXPECT().UpsertThreadSubscription(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	threadStore.EXPECT().UpdateThreadRoomLastMessage(gomock.Any(), "tr-1", "m-reply", gomock.Any(), gomock.Any(), "m-reply").
		Return(0, false, nil)
	store.EXPECT().UpdateParentMessageThreadRoomID(gomock.Any(), "m-parent", "r-1", parentCreatedAt, "tr-1").Return(nil)
	store.EXPECT().SaveThreadMessage(gomock.Any(), gomock.Any(), gomock.Any(), "site-a", "tr-1").Return(nil)
	// No UpdateParentTcount expectation — must not be called.

	h := NewHandler(store, userStore, threadStore, "site-a", func(context.Context, string, []byte, string) error { return nil })
	evt := model.MessageEvent{SiteID: "site-a", Message: model.Message{
		ID: "m-reply", RoomID: "r-1", UserID: "u-bob", UserAccount: "bob", Content: "hi",
		CreatedAt: time.Now().UTC(), ThreadParentMessageID: "m-parent", ThreadParentMessageCreatedAt: &parentCreatedAt,
	}}
	data, _ := json.Marshal(evt)
	require.NoError(t, h.processMessage(context.Background(), data))
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `make test SERVICE=message-worker`
Expected: FAIL — handler does not yet call `UpdateParentTcount`; `handleSubsequentThreadReply` signature mismatch.

- [ ] **Step 5: Wire the handler**

In `message-worker/handler.go`:

Change `handleThreadRoomAndSubscriptions` to return the count and an `applied` flag:

```go
func (h *Handler) handleThreadRoomAndSubscriptions(ctx context.Context, msg *model.Message, eventSiteID string, replier *model.User) (string, int, bool, error) {
	now := msg.CreatedAt

	var parentCreatedAt time.Time
	if msg.ThreadParentMessageCreatedAt != nil {
		parentCreatedAt = *msg.ThreadParentMessageCreatedAt
	}
	threadRoom := model.ThreadRoom{
		ID:                    idgen.GenerateUUIDv7(),
		ParentMessageID:       msg.ThreadParentMessageID,
		ThreadParentCreatedAt: parentCreatedAt,
		RoomID:                msg.RoomID,
		SiteID:                eventSiteID,
		LastMsgAt:             now,
		LastMsgID:             msg.ID,
		ReplyAccounts:         []string{msg.UserAccount},
		ReplyCount:            1,
		CountedReplies:        []string{msg.ID},
		CreatedAt:             now,
		UpdatedAt:             now,
	}

	err := h.threadStore.CreateThreadRoom(ctx, &threadRoom)
	switch {
	case err == nil:
		if ferr := h.handleFirstThreadReply(ctx, msg, eventSiteID, threadRoom.ID, replier, now); ferr != nil {
			return "", 0, false, ferr
		}
		return threadRoom.ID, 1, true, nil
	case errors.Is(err, errThreadRoomExists):
		return h.handleSubsequentThreadReply(ctx, msg, eventSiteID, replier, now)
	default:
		return "", 0, false, fmt.Errorf("create thread room: %w", err)
	}
}
```

Change `handleSubsequentThreadReply`'s signature to `(string, int, bool, error)` and its `UpdateThreadRoomLastMessage` call + returns. Replace the `UpdateThreadRoomLastMessage` block and all `return` statements:

```go
func (h *Handler) handleSubsequentThreadReply(ctx context.Context, msg *model.Message, eventSiteID string, replier *model.User, now time.Time) (string, int, bool, error) {
	existingRoom, err := h.threadStore.GetThreadRoomByParentMessageID(ctx, msg.ThreadParentMessageID)
	if err != nil {
		return "", 0, false, fmt.Errorf("get existing thread room: %w", err)
	}
	// ... existing parentFound / subscription block unchanged, but every
	//     `return "", fmt.Errorf(...)` becomes `return "", 0, false, fmt.Errorf(...)` ...

	replyAccounts := []string{msg.UserAccount}
	if parentFound {
		replyAccounts = append(replyAccounts, parentSender.Account)
	}
	count, applied, err := h.threadStore.UpdateThreadRoomLastMessage(ctx, existingRoom.ID, msg.ID, replyAccounts, now, msg.ID)
	if err != nil {
		return "", 0, false, fmt.Errorf("update thread room last message: %w", err)
	}

	// ... existing thread_room_id re-stamp switch unchanged, but its
	//     `return "", fmt.Errorf(...)` becomes `return "", 0, false, fmt.Errorf(...)` ...

	return existingRoom.ID, count, applied, nil
}
```

In `processMessage`, capture and use the count + applied flag, and drive the mirror after `SaveThreadMessage`:

```go
	if evt.Message.ThreadParentMessageID != "" {
		threadRoomID, count, applied, err := h.handleThreadRoomAndSubscriptions(ctx, &evt.Message, evt.SiteID, user)
		if err != nil {
			return fmt.Errorf("handle thread room and subscriptions: %w", err)
		}
		if err := h.markThreadMentions(ctx, &evt.Message, threadRoomID, evt.SiteID); err != nil {
			return fmt.Errorf("mark thread mentions: %w", err)
		}
		if err := h.store.SaveThreadMessage(ctx, &evt.Message, sender, evt.SiteID, threadRoomID); err != nil {
			return fmt.Errorf("save thread message: %w", err)
		}
		// Mirror the authoritative count to Cassandra only on first delivery
		// (applied) and only when the parent's full key is known.
		if applied && evt.Message.ThreadParentMessageCreatedAt != nil {
			if err := h.store.UpdateParentTcount(ctx, evt.Message.RoomID, evt.Message.ThreadParentMessageID, *evt.Message.ThreadParentMessageCreatedAt, count); err != nil {
				return fmt.Errorf("mirror parent tcount: %w", err)
			}
		}
	} else {
		if err := h.store.SaveMessage(ctx, &evt.Message, sender, evt.SiteID); err != nil {
			return fmt.Errorf("save message: %w", err)
		}
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `make test SERVICE=message-worker && make test-integration SERVICE=message-worker`
Expected: PASS (includes Task 2 + Task 3 tests now that the package compiles).

- [ ] **Step 7: Commit (Tasks 2–4 together)**

```bash
git add message-worker/ pkg/model/
git commit -m "feat(message-worker): source thread reply count from MongoDB, mirror to Cassandra

Replace the per-reply tcount LWT (incrementParentTcount) with an idempotent
guarded \$inc on the ThreadRoom document and a plain UPDATE mirror onto the
Cassandra parent row. Dedups JetStream redelivery via a bounded countedReplies
guard."
```

---

## Task 5: DecrementReplyCount in history-service's Mongo thread repo

**Files:**
- Modify: `history-service/internal/mongorepo/threadroom.go`
- Test: `history-service/internal/mongorepo/threadroom_test.go`

- [ ] **Step 1: Write the failing integration test**

Add to `history-service/internal/mongorepo/threadroom_test.go` (follow the existing setup in that file — `testutil.MongoDB`, `NewThreadRoomRepo`; insert a ThreadRoom via the raw collection):

```go
//go:build integration

func TestThreadRoomRepo_DecrementReplyCount(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "history_threadroom")
	repo := NewThreadRoomRepo(db)

	_, err := db.Collection("thread_rooms").InsertOne(ctx, model.ThreadRoom{
		ID: "tr-dec", ParentMessageID: "m-p", RoomID: "r-1", SiteID: "site-a",
		ReplyCount: 2, CountedReplies: []string{"m-1", "m-2"},
	})
	require.NoError(t, err)

	n, err := repo.DecrementReplyCount(ctx, "tr-dec")
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	n, err = repo.DecrementReplyCount(ctx, "tr-dec")
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// At zero: guarded, stays 0, returns 0 (no negative).
	n, err = repo.DecrementReplyCount(ctx, "tr-dec")
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// Missing thread room: returns 0, no error.
	n, err = repo.DecrementReplyCount(ctx, "tr-missing")
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=history-service`
Expected: FAIL — `repo.DecrementReplyCount` undefined.

- [ ] **Step 3: Implement `DecrementReplyCount`**

In `history-service/internal/mongorepo/threadroom.go`, add the import for `errors` and `options`/`bson` (already imported), then:

```go
// DecrementReplyCount atomically decrements replyCount by 1, guarded by
// replyCount > 0 so it never goes negative, and returns the new count. A
// missing thread room or an already-zero count is a no-op returning 0.
// Idempotency of the *caller* (one decrement per delete) is guaranteed upstream
// by the SoftDeleteMessage `IF deleted != true` gate.
func (r *ThreadRoomRepo) DecrementReplyCount(ctx context.Context, threadRoomID string) (int, error) {
	var updated model.ThreadRoom
	err := r.threadRooms.Raw().FindOneAndUpdate(
		ctx,
		bson.M{"_id": threadRoomID, "replyCount": bson.M{"$gt": 0}},
		bson.M{"$inc": bson.M{"replyCount": -1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("decrement reply count for thread room %s: %w", threadRoomID, err)
	}
	return updated.ReplyCount, nil
}
```

Add `"errors"` to the import block if not present.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test-integration SERVICE=history-service`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add history-service/internal/mongorepo/
git commit -m "feat(history-service): add ThreadRoomRepo.DecrementReplyCount"
```

---

## Task 6: history-service Cassandra — mirror write + remove LWT decrement

**Files:**
- Modify: `history-service/internal/cassrepo/write.go`
- Test: `history-service/internal/cassrepo/write_integration_test.go`

- [ ] **Step 1: Write the failing integration test**

Add to `history-service/internal/cassrepo/write_integration_test.go` (follow the existing setup — `testutil.CassandraKeyspace`, `NewRepository(session, bucket, maxBuckets, nil)`):

```go
//go:build integration

func TestRepository_UpdateParentTcount(t *testing.T) {
	ctx := context.Background()
	_, session, _ := testutil.CassandraKeyspace(t, "history_cassrepo")
	bucket := msgbucket.New(72 * time.Hour)
	repo := NewRepository(session, bucket, 365, nil)

	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	parentBucket := bucket.Of(parentCreatedAt)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, bucket, created_at, message_id, site_id, tcount) VALUES (?, ?, ?, ?, ?, ?)`,
		"r-1", parentBucket, parentCreatedAt, "m-parent", "site-a", 3).WithContext(ctx).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, created_at, room_id, site_id, tcount) VALUES (?, ?, ?, ?, ?)`,
		"m-parent", parentCreatedAt, "r-1", "site-a", 3).WithContext(ctx).Exec())

	require.NoError(t, repo.UpdateParentTcount(ctx, "r-1", "m-parent", parentCreatedAt, 2))

	var byRoom, byID *int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		"r-1", parentBucket, parentCreatedAt, "m-parent").WithContext(ctx).Scan(&byRoom))
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		"m-parent", parentCreatedAt).WithContext(ctx).Scan(&byID))
	assert.Equal(t, 2, *byRoom)
	assert.Equal(t, 2, *byID)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test-integration SERVICE=history-service`
Expected: FAIL — `repo.UpdateParentTcount` undefined.

- [ ] **Step 3: Add `UpdateParentTcount`, delete the LWT decrement**

In `history-service/internal/cassrepo/write.go`:

Add the method:

```go
// UpdateParentTcount mirrors the authoritative reply count (sourced from
// MongoDB) onto the parent message row in both Cassandra tables with plain
// (non-LWT) UPDATEs. Called by the service layer after a Mongo decrement.
func (r *Repository) UpdateParentTcount(ctx context.Context, roomID, parentID string, parentCreatedAt time.Time, count int) error {
	if err := r.session.Query(
		`UPDATE messages_by_id SET tcount = ? WHERE message_id = ? AND created_at = ?`,
		count, parentID, parentCreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update parent tcount in messages_by_id for %s: %w", parentID, err)
	}
	parentBucket := r.bucket.Of(parentCreatedAt)
	if err := r.session.Query(
		`UPDATE messages_by_room SET tcount = ? WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		count, roomID, parentBucket, parentCreatedAt, parentID,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("update parent tcount in messages_by_room for %s in room %s: %w", parentID, roomID, err)
	}
	return nil
}
```

Delete the `casDecrement` function and the `decrementParentTcount` method. In `SoftDeleteMessage`, remove the trailing decrement block:

```go
	if msg.ThreadParentID != "" {
		if err := r.decrementParentTcount(ctx, msg); err != nil {
			return time.Time{}, false, fmt.Errorf("decrement parent tcount for message %s: %w", msg.MessageID, err)
		}
	}
```

The `casMaxRetries` const stays only if still used elsewhere in the file; after removing `casDecrement`, search the file for `casMaxRetries` — if there are no remaining references, delete the const and its doc comment too.

- [ ] **Step 4: Run test to verify it passes**

Run: `make test-integration SERVICE=history-service`
Expected: PASS for `TestRepository_UpdateParentTcount`. Remove any now-dead `decrementParentTcount`/`casDecrement` unit tests (search `*_test.go`).

- [ ] **Step 5: Commit (deferred)**

Commit with Task 7 so the service-layer caller lands in the same compiling commit.

---

## Task 7: history-service service-layer orchestration + mocks

**Files:**
- Modify: `history-service/internal/service/service.go`
- Modify: `history-service/internal/service/messages.go`
- Regenerate: `history-service/internal/service/mocks/mock_repository.go`
- Test: `history-service/internal/service/messages_test.go`

- [ ] **Step 1: Update the interfaces**

In `history-service/internal/service/service.go`:

Add to `MessageWriter`:

```go
	UpdateParentTcount(ctx context.Context, roomID, parentID string, parentCreatedAt time.Time, count int) error
```

Add to `ThreadRoomRepository`:

```go
	DecrementReplyCount(ctx context.Context, threadRoomID string) (int, error)
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=history-service`
Expected: `mocks/mock_repository.go` gains `UpdateParentTcount` and `DecrementReplyCount`.

- [ ] **Step 3: Write the failing handler test**

Add to `history-service/internal/service/messages_test.go` (follow the existing `DeleteMessage` test construction; the deleted message is a thread reply). Assert the Mongo decrement and Cassandra mirror run, in order, on an applied delete:

```go
func TestHistoryService_DeleteMessage_ReplyDecrementsThreadCount(t *testing.T) {
	// ... standard setup: mocked msgs (MessageRepository), subs, rooms, pub,
	//     threadRooms (ThreadRoomRepository), svc := New(...) ...
	parentCreatedAt := time.Now().UTC()
	reply := &models.Message{
		MessageID: "m-reply", RoomID: "r-1", CreatedAt: time.Now().UTC(),
		Sender:                models.Participant{ID: "u-bob", Account: "bob"},
		ThreadParentID:        "m-parent",
		ThreadRoomID:          "tr-1",
		ThreadParentCreatedAt: &parentCreatedAt,
	}
	subs.EXPECT().GetHistorySharedSince(gomock.Any(), "bob", "r-1").Return(nil, true, nil)
	msgs.EXPECT().GetMessageByID(gomock.Any(), "m-reply").Return(reply, nil)
	msgs.EXPECT().SoftDeleteMessage(gomock.Any(), reply, gomock.Any()).
		Return(time.Now().UTC(), true, nil)

	threadRooms.EXPECT().DecrementReplyCount(gomock.Any(), "tr-1").Return(4, nil)
	msgs.EXPECT().UpdateParentTcount(gomock.Any(), "r-1", "m-parent", parentCreatedAt, 4).Return(nil)
	pub.EXPECT().Publish(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

	c := newTestContext(t, map[string]string{"account": "bob", "roomID": "r-1"})
	_, err := svc.DeleteMessage(c, "site-a", models.DeleteMessageRequest{MessageID: "m-reply"})
	require.NoError(t, err)
}
```

Add a second test: deleting a **non-reply** message (`ThreadParentID == ""`) must NOT call `DecrementReplyCount` or `UpdateParentTcount`.

- [ ] **Step 4: Run tests to verify they fail**

Run: `make test SERVICE=history-service`
Expected: FAIL — `DeleteMessage` does not yet decrement/mirror.

- [ ] **Step 5: Wire the orchestration in `DeleteMessage`**

In `history-service/internal/service/messages.go`, in `DeleteMessage`, after the `if !applied { ... }` early return and before building the canonical event, insert:

```go
	// Reply delete: decrement the Mongo-sourced count and mirror it to the
	// Cassandra parent row. Runs only on an applied delete (the SoftDeleteMessage
	// `IF deleted != true` gate guarantees exactly-once), so no extra dedup is
	// needed here.
	if msg.ThreadParentID != "" && msg.ThreadRoomID != "" && msg.ThreadParentCreatedAt != nil {
		newCount, err := s.threadRooms.DecrementReplyCount(c, msg.ThreadRoomID)
		if err != nil {
			return nil, fmt.Errorf("decrement thread reply count for %s: %w", req.MessageID, err)
		}
		if err := s.msgWriter.UpdateParentTcount(c, msg.RoomID, msg.ThreadParentID, *msg.ThreadParentCreatedAt, newCount); err != nil {
			return nil, fmt.Errorf("mirror parent tcount for %s: %w", req.MessageID, err)
		}
	}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `make test SERVICE=history-service && make test-integration SERVICE=history-service`
Expected: PASS (includes Task 6 tests now that the package compiles).

- [ ] **Step 7: Commit (Tasks 6–7 together)**

```bash
git add history-service/
git commit -m "feat(history-service): decrement thread count in MongoDB on reply delete

Replace the tcount decrement LWT (decrementParentTcount) with a guarded Mongo
\$inc:-1 and a plain UPDATE mirror to the Cassandra parent row, orchestrated in
the service layer (cassrepo stays Mongo-free). The existing IF deleted != true
delete gate guarantees the decrement runs exactly once."
```

---

## Task 8: End-to-end idempotency + parity integration test

**Files:**
- Test: `message-worker/integration_test.go`

A focused integration test that drives the handler through Mongo + Cassandra and asserts the redelivery invariant and the Mongo↔Cassandra parity end to end.

- [ ] **Step 1: Write the failing test**

Add (use both `testutil.MongoDB` and `testutil.CassandraKeyspace`, wire a real `CassandraStore` + `threadStoreMongo` into a `Handler`, mock only `UserStore` and publish):

```go
//go:build integration

func TestHandler_ThreadReply_RedeliveryDoesNotDoubleCount(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "message_worker")
	_, session, _ := testutil.CassandraKeyspace(t, "message_worker")
	bucket := msgbucket.New(72 * time.Hour)

	threadStore := newThreadStoreMongo(db)
	require.NoError(t, threadStore.EnsureIndexes(ctx))
	cassStore := NewCassandraStore(session, bucket, nil)

	ctrl := gomock.NewController(t)
	userStore := NewMockUserStore(ctrl)
	userStore.EXPECT().FindUserByID(gomock.Any(), gomock.Any()).Return(&model.User{ID: "u-bob", SiteID: "site-a"}, nil).AnyTimes()
	userStore.EXPECT().FindUsersByAccounts(gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

	h := NewHandler(cassStore, userStore, threadStore, "site-a", func(context.Context, string, []byte, string) error { return nil })

	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	parentBucket := bucket.Of(parentCreatedAt)
	// Seed the parent message row so the mirror UPDATE has a row to hit.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, created_at, room_id, site_id) VALUES (?, ?, ?, ?)`,
		"m-parent", parentCreatedAt, "r-1", "site-a").WithContext(ctx).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, bucket, created_at, message_id, site_id) VALUES (?, ?, ?, ?, ?)`,
		"r-1", parentBucket, parentCreatedAt, "m-parent", "site-a").WithContext(ctx).Exec())

	reply := func(id string) []byte {
		evt := model.MessageEvent{SiteID: "site-a", Message: model.Message{
			ID: id, RoomID: "r-1", UserID: "u-bob", UserAccount: "bob", Content: "hi",
			CreatedAt: time.Now().UTC(), ThreadParentMessageID: "m-parent", ThreadParentMessageCreatedAt: &parentCreatedAt,
		}}
		data, _ := json.Marshal(evt)
		return data
	}

	require.NoError(t, h.processMessage(ctx, reply("m-r1"))) // first reply -> count 1
	require.NoError(t, h.processMessage(ctx, reply("m-r2"))) // second reply -> count 2
	require.NoError(t, h.processMessage(ctx, reply("m-r2"))) // REDELIVERY of m-r2 -> still 2

	room, err := threadStore.GetThreadRoomByParentMessageID(ctx, "m-parent")
	require.NoError(t, err)
	assert.Equal(t, 2, room.ReplyCount)

	var tcount *int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND bucket = ? AND created_at = ? AND message_id = ?`,
		"r-1", parentBucket, parentCreatedAt, "m-parent").WithContext(ctx).Scan(&tcount))
	require.NotNil(t, tcount)
	assert.Equal(t, 2, *tcount, "Cassandra mirror must equal Mongo ReplyCount")
}
```

- [ ] **Step 2: Run test to verify it fails, then passes**

Run: `make test-integration SERVICE=message-worker`
Expected: PASS (implementation already complete from Tasks 2–4; this is the end-to-end guard). If it fails, the redelivery dedup or mirror wiring is wrong — fix before committing.

- [ ] **Step 3: Commit**

```bash
git add message-worker/integration_test.go
git commit -m "test(message-worker): end-to-end thread reply redelivery idempotency + Cassandra parity"
```

---

## Task 9: Full verification, lint, SAST, docs flip

**Files:**
- Modify: `docs/superpowers/specs/2026-06-04-thread-reply-count-mongodb-design.md` (status flip)

- [ ] **Step 1: Regenerate all mocks (in case any were missed)**

Run: `make generate`
Expected: no unexpected diff; if there is one, commit it.

- [ ] **Step 2: Full unit test suite with race detector**

Run: `make test`
Expected: PASS, no races.

- [ ] **Step 3: Full integration suite**

Run: `make test-integration SERVICE=message-worker && make test-integration SERVICE=history-service`
Expected: PASS.

- [ ] **Step 4: Lint + format**

Run: `make fmt && make lint`
Expected: clean.

- [ ] **Step 5: SAST**

Run: `make sast`
Expected: no medium+ findings. (No `InsecureSkipVerify`/unsafe conversions introduced.)

- [x] **Step 6: Confirm the spec status is `implemented`**

The spec `docs/superpowers/specs/2026-06-04-thread-reply-count-mongodb-design.md` already carries `**Status:** implemented` — no status flip remains.

- [ ] **Step 7: Push**

```bash
git push -u origin claude/cassandra-loading-impact-hCZuH
```

---

## Self-Review Notes (for the implementer)

- **`tcount` column untouched** — no change to `pkg/model/cassandra/message.go`, the Cassandra DDL, or any read query. Reads keep working unchanged.
- **`docs/client-api.md`** — no edit required (no wire change). Do not add one.
- **No backfill** — pre-existing threads start `$inc` from absent; older counts may read low until activity. This is intended.
- **Crash window** — if the process dies between the Mongo `$inc` and the Cassandra mirror, the redelivery is deduped (`applied=false`) and skips the mirror, leaving Cassandra one behind until the next reply re-mirrors. This is within the accepted transient-staleness tolerance (spec §6) — do NOT add a transaction or a compensating read.
- **Type consistency:** `UpdateParentTcount(ctx, roomID, parentID string, parentCreatedAt time.Time, count int)` is identical in `message-worker/Store` and `history-service/MessageWriter`. `UpdateThreadRoomLastMessage(..., replyID string) (int, bool, error)` and `DecrementReplyCount(ctx, threadRoomID string) (int, error)` match across interface and impl.
