# Thread Parent Delete — Set `type = 'message_removed'` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When soft-deleting a thread parent message (one whose `TCount > 0`), also set `type = 'message_removed'` in every Cassandra table the message lives in, so the frontend can distinguish a deleted thread parent from a deleted regular message.

**Architecture:** All changes are confined to `history-service/internal/cassrepo/write.go` and its integration test file. No handler, service, or event struct changes are needed — `SoftDeleteMessage`'s public signature is unchanged; the Cassandra writes it produces are what differ. The detection criterion is `msg.TCount != nil && *msg.TCount > 0`: a message has `TCount` set (to any value) only when it has ever been a thread parent, and `> 0` means at least one reply currently exists, meaning the frontend still has a thread to render as "parent removed". The CAS gate on `messages_by_id` uses the appropriate query constant (with or without `type = 'message_removed'`) based on this check, making the type update atomic with the delete. Mirror tables (`messages_by_room`, `thread_messages_by_room`, `pinned_messages_by_room`) each get a corresponding new query constant and helper.

**Tech Stack:** Go 1.25, `github.com/gocql/gocql`, `testcontainers-go` for integration tests.

---

## Background — which tables a message can live in

| Scenario | `messages_by_id` | `messages_by_room` | `thread_messages_by_room` | `pinned_messages_by_room` |
|---|---|---|---|---|
| Top-level regular message | ✓ | ✓ | — | if pinned |
| Top-level thread parent (`TCount > 0`) | ✓ | ✓ | — | if pinned |
| Reply (not itself a parent) | ✓ | — | ✓ | if pinned |
| Reply that is also a thread parent (`ThreadParentID != ""` + `TCount > 0`) | ✓ | — | ✓ | if pinned |

`type = 'message_removed'` must be set in **all** tables the message lives in when it is a thread parent.

---

## File Map

| File | Change |
|---|---|
| `history-service/internal/cassrepo/write.go` | Add `MessageTypeRemoved` constant; add 4 new CQL query constants; add 3 new per-table helper methods; update `SoftDeleteMessage` to branch on `isThreadParent` |
| `history-service/internal/cassrepo/write_integration_test.go` | Add 3 new integration tests covering thread-parent delete, non-thread-parent delete, and reply-thread-parent delete |

---

## Task 1: Write failing integration tests (Red)

**Files:**
- Modify: `history-service/internal/cassrepo/write_integration_test.go`

- [ ] **Step 1: Add `TestRepository_SoftDeleteMessage_ThreadParent_SetsTypeRemoved`**

  Append at the end of `write_integration_test.go`:

  ```go
  // TestRepository_SoftDeleteMessage_ThreadParent_SetsTypeRemoved verifies that
  // deleting a top-level thread parent (TCount > 0) sets type = 'message_removed'
  // atomically in messages_by_id and messages_by_room.
  func TestRepository_SoftDeleteMessage_ThreadParent_SetsTypeRemoved(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	roomID := "room-tp-del"
  	msgID := "m-tp-del"
  	tcount := 2
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	// Seed messages_by_id with tcount = 2 (has active replies — thread parent).
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
  		msgID, roomID, createdAt, sender, "parent msg", tcount, false,
  	).Exec())

  	// Seed messages_by_room (top-level message: no thread_parent_id).
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
  		roomID, createdAt, msgID, sender, "parent msg", tcount, false,
  	).Exec())

  	msg := &models.Message{
  		MessageID:      msgID,
  		RoomID:         roomID,
  		CreatedAt:      createdAt,
  		TCount:         &tcount,
  		ThreadParentID: "", // top-level
  	}

  	deletedAt := createdAt.Add(time.Minute)
  	_, applied, err := repo.SoftDeleteMessage(ctx, msg, deletedAt)
  	require.NoError(t, err)
  	require.True(t, applied)

  	// Verify messages_by_id: deleted = true AND type = 'message_removed'.
  	var gotDeleted bool
  	var gotType string
  	require.NoError(t, session.Query(
  		`SELECT deleted, type FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
  		msgID, createdAt,
  	).Scan(&gotDeleted, &gotType))
  	assert.True(t, gotDeleted)
  	assert.Equal(t, "message_removed", gotType, "messages_by_id must have type='message_removed' for thread parent")

  	// Verify messages_by_room: deleted = true AND type = 'message_removed'.
  	require.NoError(t, session.Query(
  		`SELECT deleted, type FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
  		roomID, createdAt, msgID,
  	).Scan(&gotDeleted, &gotType))
  	assert.True(t, gotDeleted)
  	assert.Equal(t, "message_removed", gotType, "messages_by_room must have type='message_removed' for thread parent")
  }
  ```

- [ ] **Step 2: Add `TestRepository_SoftDeleteMessage_NonThreadParent_NoTypeChange`**

  ```go
  // TestRepository_SoftDeleteMessage_NonThreadParent_NoTypeChange verifies that
  // deleting a regular message (TCount nil) does NOT set type = 'message_removed'.
  func TestRepository_SoftDeleteMessage_NonThreadParent_NoTypeChange(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	roomID := "room-non-tp"
  	msgID := "m-non-tp"
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	// Seed with no tcount (regular message, never had a thread).
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, deleted) VALUES (?, ?, ?, ?, ?, ?)`,
  		msgID, roomID, createdAt, sender, "regular msg", false,
  	).Exec())
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, deleted) VALUES (?, ?, ?, ?, ?, ?)`,
  		roomID, createdAt, msgID, sender, "regular msg", false,
  	).Exec())

  	msg := &models.Message{
  		MessageID:      msgID,
  		RoomID:         roomID,
  		CreatedAt:      createdAt,
  		TCount:         nil, // no replies — not a thread parent
  		ThreadParentID: "",
  	}

  	deletedAt := createdAt.Add(time.Minute)
  	_, applied, err := repo.SoftDeleteMessage(ctx, msg, deletedAt)
  	require.NoError(t, err)
  	require.True(t, applied)

  	// type column should be empty (not set).
  	var gotType string
  	require.NoError(t, session.Query(
  		`SELECT type FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
  		msgID, createdAt,
  	).Scan(&gotType))
  	assert.Empty(t, gotType, "regular message delete must NOT set type")
  }
  ```

- [ ] **Step 3: Add `TestRepository_SoftDeleteMessage_ReplyThreadParent_SetsTypeRemoved`**

  ```go
  // TestRepository_SoftDeleteMessage_ReplyThreadParent_SetsTypeRemoved verifies that
  // deleting a reply that is itself a thread parent (ThreadParentID != "" AND TCount > 0)
  // sets type = 'message_removed' in messages_by_id and thread_messages_by_room.
  func TestRepository_SoftDeleteMessage_ReplyThreadParent_SetsTypeRemoved(t *testing.T) {
  	session := setupCassandra(t)
  	repo := NewRepository(session)
  	ctx := context.Background()

  	sender := models.Participant{ID: "u1", Account: "alice"}
  	roomID := "room-rtp"
  	threadRoomID := "tr-rtp"
  	parentMsgID := "m-rtp-parent"
  	msgID := "m-rtp"
  	tcount := 1
  	parentCreatedAt := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Millisecond)
  	createdAt := time.Now().UTC().Truncate(time.Millisecond)

  	// Seed messages_by_id: this message is a reply (ThreadParentID set) AND a parent (TCount = 1).
  	require.NoError(t, session.Query(
  		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_room_id, thread_parent_created_at, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
  		msgID, roomID, createdAt, sender, "nested thread parent", parentMsgID, threadRoomID, parentCreatedAt, tcount, false,
  	).Exec())

  	// Seed thread_messages_by_room (message is a reply in the parent's thread).
  	require.NoError(t, session.Query(
  		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, tcount, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
  		roomID, threadRoomID, createdAt, msgID, sender, "nested thread parent", tcount, false,
  	).Exec())

  	msg := &models.Message{
  		MessageID:             msgID,
  		RoomID:                roomID,
  		ThreadRoomID:          threadRoomID,
  		CreatedAt:             createdAt,
  		ThreadParentID:        parentMsgID,
  		ThreadParentCreatedAt: &parentCreatedAt,
  		TCount:                &tcount,
  	}

  	deletedAt := createdAt.Add(time.Minute)
  	_, applied, err := repo.SoftDeleteMessage(ctx, msg, deletedAt)
  	require.NoError(t, err)
  	require.True(t, applied)

  	// Verify messages_by_id: type = 'message_removed'.
  	var gotType string
  	require.NoError(t, session.Query(
  		`SELECT type FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
  		msgID, createdAt,
  	).Scan(&gotType))
  	assert.Equal(t, "message_removed", gotType, "messages_by_id must have type='message_removed'")

  	// Verify thread_messages_by_room: type = 'message_removed'.
  	require.NoError(t, session.Query(
  		`SELECT type FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
  		roomID, threadRoomID, createdAt, msgID,
  	).Scan(&gotType))
  	assert.Equal(t, "message_removed", gotType, "thread_messages_by_room must have type='message_removed'")
  }
  ```

- [ ] **Step 4: Run the new tests to confirm they FAIL (Red)**

  ```bash
  make test-integration SERVICE=history-service 2>&1 | grep -E "FAIL|PASS|ThreadParent|NonThread|ReplyThread"
  ```

  Expected: the three new tests FAIL (`type` column is empty after delete because the current code does not set it). All pre-existing integration tests still PASS.

---

## Task 2: Add constants, helpers, update `SoftDeleteMessage` (Green)

**Files:**
- Modify: `history-service/internal/cassrepo/write.go`

- [ ] **Step 1: Read `write.go` in full**

  Open `history-service/internal/cassrepo/write.go`. You will add new constants and helpers without changing any existing ones.

- [ ] **Step 2: Add `MessageTypeRemoved` constant and thread-parent delete query constants**

  After the existing `casMaxRetries` constant and the existing `const (...)` block for the regular edit/delete queries, add a new `const` block:

  ```go
  // MessageTypeRemoved is the Cassandra type value written to thread parent messages
  // when they are soft-deleted, signalling to the frontend that the thread's
  // parent message has been removed.
  const MessageTypeRemoved = "message_removed"

  // Thread-parent delete queries — identical to the regular delete queries but also
  // set type = MessageTypeRemoved. Used when msg.TCount != nil && *msg.TCount > 0.
  const (
  	deleteThreadParentMsgByIDCAS = `UPDATE messages_by_id SET deleted = true, type = 'message_removed', updated_at = ? WHERE message_id = ? AND created_at = ? IF deleted != true`
  	deleteThreadParentMsgByRoom  = `UPDATE messages_by_room SET deleted = true, type = 'message_removed', updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
  	deleteThreadParentThreadMsg  = `UPDATE thread_messages_by_room SET deleted = true, type = 'message_removed', updated_at = ? WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`
  	deleteThreadParentPinnedMsg  = `UPDATE pinned_messages_by_room SET deleted = true, type = 'message_removed', updated_at = ? WHERE room_id = ? AND created_at = ? AND message_id = ?`
  )
  ```

- [ ] **Step 3: Add three per-table helper methods for thread-parent deletion**

  Add these after the existing `deleteInPinnedMessagesByRoom` helper:

  ```go
  func (r *Repository) deleteThreadParentInMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deleteThreadParentMsgByRoom, deletedAt, msg.RoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) deleteThreadParentInThreadMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deleteThreadParentThreadMsg, deletedAt, msg.RoomID, msg.ThreadRoomID, msg.CreatedAt, msg.MessageID).WithContext(ctx).Exec()
  }

  func (r *Repository) deleteThreadParentInPinnedMessagesByRoom(ctx context.Context, msg *models.Message, deletedAt time.Time) error {
  	return r.session.Query(deleteThreadParentPinnedMsg, deletedAt, msg.RoomID, *msg.PinnedAt, msg.MessageID).WithContext(ctx).Exec()
  }
  ```

- [ ] **Step 4: Update `SoftDeleteMessage` to branch on `isThreadParent`**

  Replace the body of `SoftDeleteMessage` with the version below. The guard clause, CAS scan, and tcount decrement are unchanged; only the query selection and mirror-table helper calls differ:

  ```go
  func (r *Repository) SoftDeleteMessage(ctx context.Context, msg *models.Message, deletedAt time.Time) (time.Time, bool, error) {
  	if msg.ThreadParentID != "" && msg.ThreadRoomID == "" {
  		return time.Time{}, false, fmt.Errorf("delete thread message %s: ThreadParentID %q is set but ThreadRoomID is empty", msg.MessageID, msg.ThreadParentID)
  	}

  	isThreadParent := msg.TCount != nil && *msg.TCount > 0

  	casQuery := deleteMsgByIDCAS
  	if isThreadParent {
  		casQuery = deleteThreadParentMsgByIDCAS
  	}

  	var current bool
  	applied, err := r.session.Query(
  		casQuery,
  		deletedAt, msg.MessageID, msg.CreatedAt,
  	).WithContext(ctx).ScanCAS(&current)
  	if err != nil {
  		return time.Time{}, false, fmt.Errorf("cas update messages_by_id for message %s: %w", msg.MessageID, err)
  	}
  	if !applied {
  		var existing time.Time
  		if err := r.session.Query(
  			`SELECT updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
  			msg.MessageID, msg.CreatedAt,
  		).WithContext(ctx).Scan(&existing); err != nil {
  			if errors.Is(err, gocql.ErrNotFound) {
  				return time.Time{}, false, fmt.Errorf("message %s vanished after cas miss: %w", msg.MessageID, gocql.ErrNotFound)
  			}
  			return time.Time{}, false, fmt.Errorf("read updated_at after cas miss for message %s: %w", msg.MessageID, err)
  		}
  		return existing, false, nil
  	}

  	if msg.ThreadParentID == "" {
  		if isThreadParent {
  			if err := r.deleteThreadParentInMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  			}
  		} else {
  			if err := r.deleteInMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  			}
  		}
  	} else {
  		if isThreadParent {
  			if err := r.deleteThreadParentInThreadMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
  			}
  		} else {
  			if err := r.deleteInThreadMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update thread_messages_by_room for message %s room %s thread %s: %w", msg.MessageID, msg.RoomID, msg.ThreadRoomID, err)
  			}
  		}
  	}

  	if msg.PinnedAt != nil {
  		if isThreadParent {
  			if err := r.deleteThreadParentInPinnedMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  			}
  		} else {
  			if err := r.deleteInPinnedMessagesByRoom(ctx, msg, deletedAt); err != nil {
  				return time.Time{}, false, fmt.Errorf("update pinned_messages_by_room for message %s in room %s: %w", msg.MessageID, msg.RoomID, err)
  			}
  		}
  	}

  	if msg.ThreadParentID != "" {
  		if err := r.decrementParentTcount(ctx, msg); err != nil {
  			return time.Time{}, false, fmt.Errorf("decrement parent tcount for message %s: %w", msg.MessageID, err)
  		}
  	}

  	return deletedAt, true, nil
  }
  ```

- [ ] **Step 5: Run integration tests (Green)**

  ```bash
  make test-integration SERVICE=history-service
  ```

  Expected: all integration tests pass, including the three new thread-parent tests. All pre-existing tests (`SoftDeleteMessage_TopLevel`, `SoftDeleteMessage_ThreadReply`, `SoftDeleteMessage_Pinned`, `LWTGatesDoubleDecrement`, `RoundTrip`, etc.) still pass — they use `TCount: nil` (or unset) so they take the existing code path unchanged.

- [ ] **Step 6: Run unit tests**

  ```bash
  make test SERVICE=history-service
  ```

  Expected: all unit tests pass. The service-layer mocks `SoftDeleteMessage` directly; none of the handler tests exercise cassrepo, so no unit test changes are needed.

- [ ] **Step 7: Run lint**

  ```bash
  make lint
  ```

  Expected: no errors.

- [ ] **Step 8: Commit**

  ```bash
  git add history-service/internal/cassrepo/write.go \
          history-service/internal/cassrepo/write_integration_test.go
  git commit -m "feat(cassrepo): set type='message_removed' when deleting a thread parent message"
  ```

- [ ] **Step 9: Push**

  ```bash
  git push -u origin claude/review-spec-plan-2b31C
  ```

---

## Self-Review

**Spec coverage:**

| Requirement | Task |
|---|---|
| `type = 'message_removed'` set when deleting a thread parent | Task 2 |
| Detection criterion: `TCount != nil && *TCount > 0` | Task 2 — `isThreadParent` variable |
| Atomic with the delete gate (CAS on `messages_by_id`) | Task 2 — `deleteThreadParentMsgByIDCAS` used in ScanCAS call |
| Mirrors updated in all applicable tables | Task 2 — branches cover `messages_by_room`, `thread_messages_by_room`, `pinned_messages_by_room` |
| Reply-that-is-also-a-parent case covered | Task 2 — `else` branch uses `deleteThreadParentInThreadMessagesByRoom` when `isThreadParent` |
| Non-thread-parent deletes unchanged | Task 2 — `isThreadParent = false` takes existing helper path |
| Integration tests for thread-parent top-level | Task 1 step 1 |
| Integration tests for non-thread-parent (no type change) | Task 1 step 2 |
| Integration tests for reply-thread-parent | Task 1 step 3 |
| No handler or service changes | Confirmed — public signature of `SoftDeleteMessage` is identical |

**Placeholder scan:** None found.

**Type consistency:**
- `deleteThreadParentMsgByIDCAS` constant defined in Task 2 step 2 → used in `SoftDeleteMessage` step 4 ✓
- `deleteThreadParentInMessagesByRoom` defined in Task 2 step 3 → called in Task 2 step 4 ✓
- `deleteThreadParentInThreadMessagesByRoom` defined in step 3 → called in step 4 ✓
- `deleteThreadParentInPinnedMessagesByRoom` defined in step 3 → called in step 4 ✓
- `msg.TCount *int` — dereference guarded by `msg.TCount != nil` before `*msg.TCount > 0` ✓
