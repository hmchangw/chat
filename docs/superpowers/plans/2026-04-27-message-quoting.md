# Message Quoting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add reply-with-quote support to the chat pipeline. Users send `quotedParentMessageId` on `SendMessageRequest`; gatekeeper resolves the parent via history-service's existing `GetMessageByID` RPC, projects it into `cassandra.QuotedParentMessage`, and embeds the snapshot on the canonical `MessageEvent`. message-worker persists it to all relevant Cassandra tables.

**Architecture:** Gatekeeper does a synchronous NATS request/reply against `history-service` to fetch the parent (no Cassandra dependency added to gatekeeper). On any failure (not found, timeout, forbidden, etc.) the quote is dropped silently (soft-fail) and the message ships without it. The snapshot rides on the canonical event so broadcast-worker and notification-worker see it without re-fetching.

**Tech Stack:** Go 1.25 · NATS / JetStream · Cassandra (gocql) · MongoDB · `caarlos0/env` · `go.uber.org/mock` · `stretchr/testify`

**Reference spec:** `docs/superpowers/specs/2026-04-27-message-quoting-design.md`

---

## Task 1: Add quote fields to wire model

**Goal:** Extend `model.SendMessageRequest` with the new client-supplied field, and `model.Message` with the snapshot field. Verify JSON round-trip.

**Files:**
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing tests for the new fields**

Append the following two test functions to the bottom of `pkg/model/model_test.go` (just before the `roundTrip` helper at line 1248):

```go
func TestSendMessageRequest_QuotedParentMessageID_JSON(t *testing.T) {
	t.Run("with quotedParentMessageId", func(t *testing.T) {
		r := model.SendMessageRequest{
			ID:                    "msg-uuid-1",
			Content:               "great point!",
			RequestID:             "req-1",
			QuotedParentMessageID: "parent-msg-uuid",
		}
		data, err := json.Marshal(&r)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		assert.Equal(t, "parent-msg-uuid", raw["quotedParentMessageId"])

		var dst model.SendMessageRequest
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, "parent-msg-uuid", dst.QuotedParentMessageID)
	})

	t.Run("quotedParentMessageId omitted when empty", func(t *testing.T) {
		r := model.SendMessageRequest{
			ID:        "msg-uuid-1",
			Content:   "hello",
			RequestID: "req-1",
		}
		data, err := json.Marshal(&r)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["quotedParentMessageId"]
		assert.False(t, present, "quotedParentMessageId should be omitted when empty")
	})
}

func TestMessage_QuotedParentMessage_JSON(t *testing.T) {
	parentTS := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("populated snapshot round-trips", func(t *testing.T) {
		m := model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "great point!",
			CreatedAt: now,
			QuotedParentMessage: &cassandra.QuotedParentMessage{
				MessageID: "parent-msg-uuid",
				RoomID:    "r1",
				Sender: cassandra.Participant{
					ID:      "u-bob",
					EngName: "Bob Chen",
					Account: "bob",
				},
				CreatedAt:   parentTS,
				Msg:         "the original message",
				Mentions:    []cassandra.Participant{{ID: "u-carol", Account: "carol", EngName: "Carol Lee"}},
				MessageLink: "http://localhost:3000/r1/parent-msg-uuid",
			},
		}
		data, err := json.Marshal(&m)
		require.NoError(t, err)

		var dst model.Message
		require.NoError(t, json.Unmarshal(data, &dst))
		require.NotNil(t, dst.QuotedParentMessage)
		assert.Equal(t, "parent-msg-uuid", dst.QuotedParentMessage.MessageID)
		assert.Equal(t, "r1", dst.QuotedParentMessage.RoomID)
		assert.Equal(t, "the original message", dst.QuotedParentMessage.Msg)
		assert.Equal(t, "bob", dst.QuotedParentMessage.Sender.Account)
		assert.Equal(t, "Bob Chen", dst.QuotedParentMessage.Sender.EngName)
		assert.Equal(t, parentTS, dst.QuotedParentMessage.CreatedAt.UTC())
		assert.Equal(t, "http://localhost:3000/r1/parent-msg-uuid", dst.QuotedParentMessage.MessageLink)
		require.Len(t, dst.QuotedParentMessage.Mentions, 1)
		assert.Equal(t, "carol", dst.QuotedParentMessage.Mentions[0].Account)
	})

	t.Run("quotedParentMessage omitted when nil", func(t *testing.T) {
		m := model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "hello",
			CreatedAt: now,
		}
		data, err := json.Marshal(&m)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["quotedParentMessage"]
		assert.False(t, present, "quotedParentMessage should be omitted when nil")
	})
}
```

Add the new import to the top of the file (in the existing `import` block):

```go
"github.com/hmchangw/chat/pkg/model/cassandra"
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `make test SERVICE=pkg/model`

Expected: FAIL — `model.Message` has no field `QuotedParentMessage`; `model.SendMessageRequest` has no field `QuotedParentMessageID`. Compilation error.

- [ ] **Step 3: Add the new fields to `pkg/model/message.go`**

Replace the entire file with:

```go
package model

import (
	"time"

	"github.com/hmchangw/chat/pkg/model/cassandra"
)

type Message struct {
	ID                           string                         `json:"id"                                     bson:"_id"`
	RoomID                       string                         `json:"roomId"                                 bson:"roomId"`
	UserID                       string                         `json:"userId"                                 bson:"userId"`
	UserAccount                  string                         `json:"userAccount"                            bson:"userAccount"`
	Content                      string                         `json:"content"                                bson:"content"`
	Mentions                     []Participant                  `json:"mentions,omitempty"                     bson:"mentions,omitempty"`
	CreatedAt                    time.Time                      `json:"createdAt"                              bson:"createdAt"`
	ThreadParentMessageID        string                         `json:"threadParentMessageId,omitempty"        bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time                     `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
	TShow                        bool                           `json:"tshow,omitempty"                        bson:"tshow,omitempty"`
	Type                         string                         `json:"type,omitempty"                         bson:"type,omitempty"`
	SysMsgData                   []byte                         `json:"sysMsgData,omitempty"                   bson:"sysMsgData,omitempty"`
	QuotedParentMessage          *cassandra.QuotedParentMessage `json:"quotedParentMessage,omitempty"          bson:"quotedParentMessage,omitempty"`
}

type SendMessageRequest struct {
	ID                           string `json:"id"`
	Content                      string `json:"content"`
	RequestID                    string `json:"requestId"`
	ThreadParentMessageID        string `json:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *int64 `json:"threadParentMessageCreatedAt,omitempty"`
	QuotedParentMessageID        string `json:"quotedParentMessageId,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `make test SERVICE=pkg/model`

Expected: PASS — all model tests including the two new ones.

- [ ] **Step 5: Run full test suite to confirm no regressions**

Run: `make test`

Expected: PASS — no other package broke. (Adding an `omitempty` field is a non-breaking change.)

- [ ] **Step 6: Commit**

```bash
git add pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(pkg/model): add quoted-parent fields to message + send-request

Adds QuotedParentMessageID to SendMessageRequest (client input) and
QuotedParentMessage *cassandra.QuotedParentMessage to Message (snapshot
that rides on the canonical event). Both fields are omitempty so existing
clients and downstream services see no behavior change."
```

---

## Task 2: Add `subject.MsgGet` concrete-subject helper

**Goal:** Provide a concrete-subject builder so gatekeeper can publish on history-service's `GetMessageByID` subject. Sits next to the existing `MsgGetPattern` (which is the natsrouter pattern variant the receiver uses).

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write the failing test**

Open `pkg/subject/subject_test.go` and add a new test case to the `tests` table inside `TestSubjectBuilders` (the slice starts at line 11). Insert this entry just before the closing `}` of the slice (before line 81):

```go
{"MsgGet", subject.MsgGet("alice", "r1", "site-a"),
	"chat.user.alice.request.room.r1.site-a.msg.get"},
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `make test SERVICE=pkg/subject`

Expected: FAIL — `subject.MsgGet undefined`. Compilation error.

- [ ] **Step 3: Add the helper to `pkg/subject/subject.go`**

Insert the following function immediately after `MsgGetPattern` (currently at line 276):

```go
// MsgGet returns the concrete subject for issuing a GetMessageByID request to
// history-service on behalf of a given user/room. Pair with MsgGetPattern,
// which is the natsrouter pattern used by history-service to register the
// handler.
func MsgGet(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.get", account, roomID, siteID)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `make test SERVICE=pkg/subject`

Expected: PASS — including the new `MsgGet` row.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(pkg/subject): add MsgGet concrete-subject helper

Pairs with the existing MsgGetPattern (used by history-service's
natsrouter Register). Callers like message-gatekeeper need the concrete
form to publish a request on behalf of a specific user+room+site."
```

---

## Task 3: Add `ParentMessageFetcher` interface, regen mocks, wire handler branch + tests

**Goal:** Define the consumer-side interface message-gatekeeper will use to fetch the quoted parent. Add a new branch to `processMessage` that, when `req.QuotedParentMessageID` is non-empty, calls the fetcher and either embeds the snapshot on the message or soft-fails (warn + drop). Cover the new path with table-driven tests using a generated mock.

**Files:**
- Modify: `message-gatekeeper/store.go`
- Modify: `message-gatekeeper/handler.go`
- Modify: `message-gatekeeper/handler_test.go`
- Generated (do not edit by hand): `message-gatekeeper/mock_store_test.go`

- [ ] **Step 1: Add the interface to `message-gatekeeper/store.go`**

Replace the entire file with:

```go
package main

import (
	"context"
	"errors"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,ParentMessageFetcher

// errNotSubscribed is returned when the user is not subscribed to the room.
var errNotSubscribed = errors.New("not subscribed")

type Store interface {
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
}

// ParentMessageFetcher resolves a quoted parent message into a snapshot
// suitable for embedding on the new message's canonical event. Implementations
// should treat any failure (not found, RPC timeout, forbidden, etc.) as a
// reason to return an error — the handler soft-fails on every error and ships
// the message without the quote.
type ParentMessageFetcher interface {
	FetchQuotedParent(ctx context.Context, account, roomID, siteID, messageID string) (*cassandra.QuotedParentMessage, error)
}
```

- [ ] **Step 2: Regenerate mocks**

Run: `make generate SERVICE=message-gatekeeper`

Expected: `mock_store_test.go` is regenerated and now contains a `MockParentMessageFetcher` type alongside the existing `MockStore`.

Verify with: `grep -c "MockParentMessageFetcher" message-gatekeeper/mock_store_test.go`

Expected: a number ≥ 1.

- [ ] **Step 3: Write the failing handler tests**

Append the following test function to the bottom of `message-gatekeeper/handler_test.go`:

```go
func TestHandler_ProcessMessage_WithQuote(t *testing.T) {
	validID := uuid.New().String()
	validContent := "great point!"
	validSiteID := "site-a"
	validRoomID := "room-1"
	validAccount := "alice"
	parentMessageID := uuid.New().String()

	sub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u1", Account: validAccount},
		RoomID: validRoomID,
		Roles:  []model.Role{model.RoleMember},
	}

	parentSnapshot := &cassandra.QuotedParentMessage{
		MessageID:   parentMessageID,
		RoomID:      validRoomID,
		Sender:      cassandra.Participant{ID: "u-bob", Account: "bob", EngName: "Bob Chen"},
		CreatedAt:   time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
		Msg:         "the original message",
		MessageLink: "http://localhost:3000/" + validRoomID + "/" + parentMessageID,
	}

	tests := []struct {
		name          string
		buildData     func() []byte
		setupStore    func(s *MockStore)
		setupFetcher  func(f *MockParentMessageFetcher)
		setupPub      func() (publishFunc, *[]publishedMsg)
		wantErr       bool
		assertMessage func(t *testing.T, msg model.Message)
	}{
		{
			name: "happy path — fetcher returns snapshot, snapshot embedded on event",
			buildData: func() []byte {
				req := model.SendMessageRequest{
					ID:                    validID,
					Content:               validContent,
					QuotedParentMessageID: parentMessageID,
				}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().GetSubscription(gomock.Any(), validAccount, validRoomID).Return(sub, nil)
			},
			setupFetcher: func(f *MockParentMessageFetcher) {
				f.EXPECT().
					FetchQuotedParent(gomock.Any(), validAccount, validRoomID, validSiteID, parentMessageID).
					Return(parentSnapshot, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			assertMessage: func(t *testing.T, msg model.Message) {
				require.NotNil(t, msg.QuotedParentMessage)
				assert.Equal(t, parentMessageID, msg.QuotedParentMessage.MessageID)
				assert.Equal(t, "the original message", msg.QuotedParentMessage.Msg)
				assert.Equal(t, "bob", msg.QuotedParentMessage.Sender.Account)
				assert.Equal(t, parentSnapshot.MessageLink, msg.QuotedParentMessage.MessageLink)
			},
		},
		{
			name: "fetcher error — quote dropped, message still published",
			buildData: func() []byte {
				req := model.SendMessageRequest{
					ID:                    validID,
					Content:               validContent,
					QuotedParentMessageID: parentMessageID,
				}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().GetSubscription(gomock.Any(), validAccount, validRoomID).Return(sub, nil)
			},
			setupFetcher: func(f *MockParentMessageFetcher) {
				f.EXPECT().
					FetchQuotedParent(gomock.Any(), validAccount, validRoomID, validSiteID, parentMessageID).
					Return(nil, fmt.Errorf("history response error: not found"))
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			assertMessage: func(t *testing.T, msg model.Message) {
				assert.Nil(t, msg.QuotedParentMessage, "quote must be dropped on fetcher error")
				assert.Equal(t, validContent, msg.Content, "message body must still ship")
			},
		},
		{
			name: "thread + quote both set — both fields propagate",
			buildData: func() []byte {
				parentMillis := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).UnixMilli()
				return []byte(fmt.Sprintf(
					`{"id":%q,"content":%q,"requestId":"req-1","threadParentMessageId":"thread-parent-uuid","threadParentMessageCreatedAt":%d,"quotedParentMessageId":%q}`,
					validID, validContent, parentMillis, parentMessageID,
				))
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().GetSubscription(gomock.Any(), validAccount, validRoomID).Return(sub, nil)
			},
			setupFetcher: func(f *MockParentMessageFetcher) {
				f.EXPECT().
					FetchQuotedParent(gomock.Any(), validAccount, validRoomID, validSiteID, parentMessageID).
					Return(parentSnapshot, nil)
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			assertMessage: func(t *testing.T, msg model.Message) {
				assert.Equal(t, "thread-parent-uuid", msg.ThreadParentMessageID)
				require.NotNil(t, msg.ThreadParentMessageCreatedAt)
				require.NotNil(t, msg.QuotedParentMessage)
				assert.Equal(t, parentMessageID, msg.QuotedParentMessage.MessageID)
			},
		},
		{
			name: "no quote field — fetcher not called",
			buildData: func() []byte {
				req := model.SendMessageRequest{ID: validID, Content: validContent}
				data, _ := json.Marshal(req)
				return data
			},
			setupStore: func(s *MockStore) {
				s.EXPECT().GetSubscription(gomock.Any(), validAccount, validRoomID).Return(sub, nil)
			},
			setupFetcher: func(f *MockParentMessageFetcher) {
				// no EXPECT — gomock will fail the test if FetchQuotedParent is called
			},
			setupPub: func() (publishFunc, *[]publishedMsg) {
				var published []publishedMsg
				return makePublishFunc(&published, nil), &published
			},
			assertMessage: func(t *testing.T, msg model.Message) {
				assert.Nil(t, msg.QuotedParentMessage)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			fetcher := NewMockParentMessageFetcher(ctrl)
			tc.setupStore(store)
			tc.setupFetcher(fetcher)

			pub, publishedPtr := tc.setupPub()

			h := &Handler{
				store:         store,
				publish:       pub,
				siteID:        validSiteID,
				parentFetcher: fetcher,
			}

			data, err := h.processMessage(context.Background(), validAccount, validRoomID, validSiteID, tc.buildData())

			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, data)

			var msg model.Message
			require.NoError(t, json.Unmarshal(data, &msg))
			tc.assertMessage(t, msg)

			// Also verify the snapshot reaches the canonical event.
			require.NotNil(t, publishedPtr)
			require.Len(t, *publishedPtr, 1)
			var evt model.MessageEvent
			require.NoError(t, json.Unmarshal((*publishedPtr)[0].data, &evt))
			if msg.QuotedParentMessage != nil {
				require.NotNil(t, evt.Message.QuotedParentMessage)
				assert.Equal(t, msg.QuotedParentMessage.MessageID, evt.Message.QuotedParentMessage.MessageID)
			} else {
				assert.Nil(t, evt.Message.QuotedParentMessage)
			}
		})
	}
}
```

Add the `cassandra` import to the existing `import` block at the top of `handler_test.go`:

```go
"github.com/hmchangw/chat/pkg/model/cassandra"
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `make test SERVICE=message-gatekeeper`

Expected: FAIL — `Handler` has no field `parentFetcher`; `processMessage` does nothing with the new field. Compilation error.

- [ ] **Step 5: Wire the handler — add field, constructor arg, and the new branch**

In `message-gatekeeper/handler.go`:

(a) Add the `cassandra` import to the existing `import` block:

```go
"github.com/hmchangw/chat/pkg/model/cassandra"
```

(b) Add the field to the `Handler` struct (replace lines 42-47):

```go
type Handler struct {
	store         Store
	publish       publishFunc
	reply         replyFunc
	siteID        string
	parentFetcher ParentMessageFetcher
}
```

(c) Update `NewHandler` (replace lines 49-52):

```go
// NewHandler constructs a new Handler with the given dependencies.
func NewHandler(store Store, publish publishFunc, reply replyFunc, siteID string, parentFetcher ParentMessageFetcher) *Handler {
	return &Handler{store: store, publish: publish, reply: reply, siteID: siteID, parentFetcher: parentFetcher}
}
```

(d) Add the quote-resolution branch inside `processMessage`. Find this block (around line 160):

```go
	msg := model.Message{
		ID:                           req.ID,
		RoomID:                       roomID,
		UserID:                       sub.User.ID,
		UserAccount:                  sub.User.Account,
		Content:                      req.Content,
		CreatedAt:                    now,
		ThreadParentMessageID:        req.ThreadParentMessageID,
		ThreadParentMessageCreatedAt: threadParentCreatedAt,
	}
```

Replace it with:

```go
	var quotedSnapshot *cassandra.QuotedParentMessage
	if req.QuotedParentMessageID != "" {
		snap, err := h.parentFetcher.FetchQuotedParent(ctx, account, roomID, siteID, req.QuotedParentMessageID)
		if err != nil {
			slog.Warn("quoted parent unavailable, dropping quote",
				"quotedParentMessageId", req.QuotedParentMessageID,
				"roomId", roomID,
				"error", err)
		} else {
			quotedSnapshot = snap
		}
	}

	msg := model.Message{
		ID:                           req.ID,
		RoomID:                       roomID,
		UserID:                       sub.User.ID,
		UserAccount:                  sub.User.Account,
		Content:                      req.Content,
		CreatedAt:                    now,
		ThreadParentMessageID:        req.ThreadParentMessageID,
		ThreadParentMessageCreatedAt: threadParentCreatedAt,
		QuotedParentMessage:          quotedSnapshot,
	}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `make test SERVICE=message-gatekeeper`

Expected: PASS — both the existing `TestHandler_ProcessMessage` table and the new `TestHandler_ProcessMessage_WithQuote` table all pass.

- [ ] **Step 7: Run the full test suite to confirm nothing else broke**

Run: `make test`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add message-gatekeeper/store.go message-gatekeeper/handler.go message-gatekeeper/handler_test.go message-gatekeeper/mock_store_test.go
git commit -m "feat(gatekeeper): add ParentMessageFetcher and quote branch in handler

Adds the consumer-side ParentMessageFetcher interface and a branch in
processMessage that calls it when SendMessageRequest carries
quotedParentMessageId. On success the snapshot is embedded on
model.Message.QuotedParentMessage; on any error the quote is dropped
silently (warn-logged) and the message ships as today.

The fetcher implementation lands in a follow-up commit."
```

---
