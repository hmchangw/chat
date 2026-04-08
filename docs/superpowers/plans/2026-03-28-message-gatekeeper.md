# Message Gatekeeper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a message-gatekeeper microservice that centralizes message validation, replaces the FANOUT stream with MESSAGES_CANONICAL, and simplifies all downstream workers.

**Architecture:** A new `message-gatekeeper` service consumes from the existing `MESSAGES` stream, validates messages (UUID, content size, room membership), and publishes to a new `MESSAGES_CANONICAL` stream. All downstream workers (message-worker, broadcast-worker, notification-worker) consume from MESSAGES_CANONICAL. The FANOUT stream is eliminated entirely.

**Tech Stack:** Go 1.24, NATS JetStream, MongoDB, Cassandra, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md`

---

## File Structure

### New Files
| File | Purpose |
|------|---------|
| `message-gatekeeper/store.go` | Store interface with `GetSubscription` + mockgen directive |
| `message-gatekeeper/store_mongo.go` | MongoDB implementation of Store |
| `message-gatekeeper/handler.go` | Message validation, MESSAGES_CANONICAL publishing, reply routing |
| `message-gatekeeper/handler_test.go` | Table-driven tests for `processMessage` (9 cases) |
| `message-gatekeeper/main.go` | Config, NATS/Mongo, stream setup, high-throughput consumer, graceful shutdown |
| `message-gatekeeper/deploy/Dockerfile` | Multi-stage build (golang:1.24-alpine / alpine:3.21) |
| `message-gatekeeper/deploy/docker-compose.yml` | Local dev with NATS + MongoDB |
| `message-worker/store_cassandra.go` | Replaces `store_mongo.go` — Cassandra-only persistence |

### Modified Files
| File | Change |
|------|--------|
| `pkg/model/message.go` | Add `Username` + bson tags to `Message`; add `ID`, remove `RoomID` from `SendMessageRequest` |
| `pkg/model/event.go` | Remove `RoomID` from `MessageEvent` |
| `pkg/model/model_test.go` | Update and add roundtrip tests |
| `pkg/stream/stream.go` | Add `MessagesCanonical`, remove `Fanout` |
| `pkg/stream/stream_test.go` | Update test table |
| `pkg/subject/subject.go` | Add `ParseUserRoomSiteSubject`, `MsgCanonicalCreated`, `MsgCanonicalWildcard`; remove `Fanout`, `FanoutWildcard` |
| `pkg/subject/subject_test.go` | Add new test cases, remove Fanout cases |
| `message-worker/store.go` | Rename to `Store`, remove `GetSubscription`, `SaveMessage` takes value |
| `message-worker/handler.go` | Simplified: unmarshal `MessageEvent` + `SaveMessage` only |
| `message-worker/handler_test.go` | Rewrite for new handler |
| `message-worker/main.go` | Remove MongoDB, switch to MESSAGES_CANONICAL stream |
| `message-worker/integration_test.go` | Cassandra-only tests |
| `broadcast-worker/handler.go` | `evt.RoomID` → `evt.Message.RoomID` |
| `broadcast-worker/handler_test.go` | Remove top-level `RoomID` from `MessageEvent` |
| `broadcast-worker/main.go` | `stream.Fanout` → `stream.MessagesCanonical` |
| `notification-worker/handler.go` | `evt.RoomID` → `evt.Message.RoomID` |
| `notification-worker/handler_test.go` | Remove top-level `RoomID` from `MessageEvent` |
| `notification-worker/main.go` | `stream.Fanout` → `stream.MessagesCanonical` |
| `CLAUDE.md` | Update event flow, streams, subjects |

### Deleted Files
| File | Reason |
|------|--------|
| `message-worker/store_mongo.go` | MongoDB no longer used by message-worker |

---

### Task 1: Update Domain Models

**Files:**
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Update TestMessageJSON to expect Account field**

In `pkg/model/model_test.go`, replace:

```go
func TestMessageJSON(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &m, &model.Message{})
}
```

With:

```go
func TestMessageJSON(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", Account: "alice",
		Content:   "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &m, &model.Message{})
}
```

- [ ] **Step 2: Add TestSendMessageRequestJSON and TestMessageEventJSON**

Insert after `TestMessageJSON` in `pkg/model/model_test.go`:

```go
func TestSendMessageRequestJSON(t *testing.T) {
	r := model.SendMessageRequest{
		ID:        "msg-uuid-1",
		Content:   "hello world",
		RequestID: "req-1",
	}
	roundTrip(t, &r, &model.SendMessageRequest{})
}

func TestMessageEventJSON(t *testing.T) {
	e := model.MessageEvent{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Account: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		SiteID: "site-a",
	}
	roundTrip(t, &e, &model.MessageEvent{})
}
```

- [ ] **Step 3: Update RoomEventJSON test Message construction**

In `pkg/model/model_test.go`, replace:

```go
	msg := model.Message{
		ID: "msg-1", RoomID: "room-1", UserID: "user-1",
		Content: "hello", CreatedAt: now,
	}
```

With:

```go
	msg := model.Message{
		ID: "msg-1", RoomID: "room-1", UserID: "user-1",
		Account: "alice", Content: "hello", CreatedAt: now,
	}
```

- [ ] **Step 4: Run tests to verify they fail (Red phase)**

```bash
make test SERVICE=pkg/model
```

Expected: FAIL — `model.Message` has no field `Username`, `model.SendMessageRequest` has no field `ID`.

- [ ] **Step 5: Update Message struct with Account and bson tags**

In `pkg/model/message.go`, replace:

```go
type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"roomId"`
	UserID    string    `json:"userId"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}
```

With:

```go
type Message struct {
	ID        string    `json:"id"        bson:"_id"`
	RoomID    string    `json:"roomId"    bson:"roomId"`
	UserID    string    `json:"userId"    bson:"userId"`
	Account  string    `json:"account"  bson:"account"`
	Content   string    `json:"content"   bson:"content"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
```

- [ ] **Step 6: Update SendMessageRequest — add ID, remove RoomID**

In `pkg/model/message.go`, replace:

```go
type SendMessageRequest struct {
	RoomID    string `json:"roomId"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
```

With:

```go
type SendMessageRequest struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
```

- [ ] **Step 7: Update MessageEvent — remove RoomID**

In `pkg/model/event.go`, replace:

```go
type MessageEvent struct {
	Message Message `json:"message"`
	RoomID  string  `json:"roomId"`
	SiteID  string  `json:"siteId"`
}
```

With:

```go
type MessageEvent struct {
	Message Message `json:"message"`
	SiteID  string  `json:"siteId"`
}
```

- [ ] **Step 8: Run tests to verify they pass (Green phase)**

```bash
make test SERVICE=pkg/model
```

Expected: PASS

- [ ] **Step 9: Lint**

```bash
make lint
```

- [ ] **Step 10: Commit**

```bash
git add pkg/model/message.go pkg/model/event.go pkg/model/model_test.go
git commit -m "feat: update domain models — add Account to Message, add ID to SendMessageRequest, remove redundant RoomID"
```

---

### Task 2: Update Stream Definitions

**Files:**
- Modify: `pkg/stream/stream.go`
- Modify: `pkg/stream/stream_test.go`

- [ ] **Step 1: Update test — add MessagesCanonical, remove Fanout (Red)**

In `pkg/stream/stream_test.go`, replace:

```go
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"Fanout", stream.Fanout(siteID), "FANOUT_site-a", "fanout.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.user.*.request.room.*.site-a.member.>"},
```

With:

```go
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"MessagesCanonical", stream.MessagesCanonical(siteID), "MESSAGES_CANONICAL_site-a", "chat.msg.canonical.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.user.*.request.room.*.site-a.member.>"},
```

Run: `make test SERVICE=pkg/stream` — Expected: FAIL (MessagesCanonical undefined)

- [ ] **Step 2: Add MessagesCanonical, remove Fanout (Green)**

In `pkg/stream/stream.go`, replace:

```go
func Fanout(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("FANOUT_%s", siteID),
		Subjects: []string{fmt.Sprintf("fanout.%s.>", siteID)},
	}
}
```

With:

```go
func MessagesCanonical(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("MESSAGES_CANONICAL_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.msg.canonical.%s.>", siteID)},
	}
}
```

- [ ] **Step 3: Run tests (Green)**

```bash
make test SERVICE=pkg/stream
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add pkg/stream/stream.go pkg/stream/stream_test.go
git commit -m "feat: add MESSAGES_CANONICAL stream, remove FANOUT stream"
```

---

### Task 3: Update Subject Builders

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Update tests — add new builders, remove Fanout (Red)**

In `pkg/subject/subject_test.go`, in `TestSubjectBuilders`, replace:

```go
		{"Fanout", subject.Fanout("site-a", "r1"),
			"fanout.site-a.r1"},
```

With:

```go
		{"MsgCanonicalCreated", subject.MsgCanonicalCreated("site-a"),
			"chat.msg.canonical.site-a.created"},
```

In `TestWildcardPatterns`, replace:

```go
		{"FanoutWild", subject.FanoutWildcard("site-a"),
			"fanout.site-a.>"},
```

With:

```go
		{"MsgCanonicalWild", subject.MsgCanonicalWildcard("site-a"),
			"chat.msg.canonical.site-a.>"},
```

- [ ] **Step 2: Add TestParseUserRoomSiteSubject**

Insert before `TestWildcardPatterns` in `pkg/subject/subject_test.go`:

```go
func TestParseUserRoomSiteSubject(t *testing.T) {
	tests := []struct {
		name         string
		subj         string
		wantAccount string
		wantRoomID   string
		wantSiteID   string
		wantOK       bool
	}{
		{"valid msg send", "chat.user.alice.room.r1.site-a.msg.send", "alice", "r1", "site-a", true},
		{"different values", "chat.user.bob.room.room-42.site-b.msg.send", "bob", "room-42", "site-b", true},
		{"too few parts", "chat.user.alice.room.r1.site-a", "", "", "", false},
		{"bad prefix", "foo.user.alice.room.r1.site-a.msg.send", "", "", "", false},
		{"not user", "chat.blah.alice.room.r1.site-a.msg.send", "", "", "", false},
		{"no room token", "chat.user.alice.notroom.r1.site-a.msg.send", "", "", "", false},
		{"empty", "", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(tt.subj)
			if ok != tt.wantOK || account != tt.wantAccount || roomID != tt.wantRoomID || siteID != tt.wantSiteID {
				t.Errorf("ParseUserRoomSiteSubject(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					tt.subj, account, roomID, siteID, ok, tt.wantUsername, tt.wantRoomID, tt.wantSiteID, tt.wantOK)
			}
		})
	}
}
```

Run: `make test SERVICE=pkg/subject` — Expected: FAIL

- [ ] **Step 3: Implement new builders and parser (Green)**

In `pkg/subject/subject.go`, add after `ParseUserRoomSubject`:

```go
// ParseUserRoomSiteSubject extracts account, roomID, and siteID from subjects
// matching "chat.user.{account}.room.{roomID}.{siteID}.…" (at least 7 parts).
func ParseUserRoomSiteSubject(subj string) (account, roomID, siteID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 7 || parts[0] != "chat" || parts[1] != "user" || parts[3] != "room" {
		return "", "", "", false
	}
	return parts[2], parts[4], parts[5], true
}
```

Replace `Fanout` function with `MsgCanonicalCreated`:

```go
// Replace:
func Fanout(siteID, roomID string) string {
	return fmt.Sprintf("fanout.%s.%s", siteID, roomID)
}

// With:
func MsgCanonicalCreated(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.created", siteID)
}
```

Replace `FanoutWildcard` function with `MsgCanonicalWildcard`:

```go
// Replace:
func FanoutWildcard(siteID string) string {
	return fmt.Sprintf("fanout.%s.>", siteID)
}

// With:
func MsgCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.>", siteID)
}
```

- [ ] **Step 4: Run tests (Green)**

```bash
make test SERVICE=pkg/subject
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat: add MESSAGES_CANONICAL subject builders and ParseUserRoomSiteSubject, remove Fanout"
```

---

### Task 4: Create message-gatekeeper Service

**Files:**
- Create: `message-gatekeeper/store.go`
- Create: `message-gatekeeper/store_mongo.go`
- Create: `message-gatekeeper/handler.go`
- Create: `message-gatekeeper/handler_test.go`
- Create: `message-gatekeeper/main.go`
- Create: `message-gatekeeper/deploy/Dockerfile`
- Create: `message-gatekeeper/deploy/docker-compose.yml`
- Generate: `message-gatekeeper/mock_store_test.go`

- [ ] **Step 1: Create store interface**

Create `message-gatekeeper/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines persistence operations for the message gatekeeper.
type Store interface {
	GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error)
}
```

- [ ] **Step 2: Generate mocks**

```bash
make generate SERVICE=message-gatekeeper
```

- [ ] **Step 3: Write handler tests (Red phase)**

Create `message-gatekeeper/handler_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_processMessage(t *testing.T) {
	const (
		validUUID = "550e8400-e29b-41d4-a716-446655440000"
		siteID    = "site-a"
	)

	happySub := &model.Subscription{
		User:   model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID: "r1",
		Role:   model.RoleMember,
	}

	tests := []struct {
		name          string
		account      string
		roomID        string
		siteID        string
		payload       any
		rawPayload    []byte
		setupStore    func(*MockStore)
		publishErr    error
		wantErr       bool
		wantInfraErr  bool
		wantErrSubstr string
		wantPublished bool
	}{
		{
			name:     "happy path",
			username: "alice",
			roomID:   "r1",
			siteID:   siteID,
			payload: model.SendMessageRequest{
				ID: validUUID, Content: "hello world", RequestID: "req-1",
			},
			setupStore: func(m *MockStore) {
				m.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(happySub, nil)
			},
			wantPublished: true,
		},
		{
			name: "invalid UUID format", account: "alice", roomID: "r1", siteID: siteID,
			payload:       model.SendMessageRequest{ID: "not-a-uuid", Content: "hello", RequestID: "req-1"},
			setupStore:    func(m *MockStore) {},
			wantErr:       true,
			wantErrSubstr: "invalid message ID",
		},
		{
			name: "empty content", account: "alice", roomID: "r1", siteID: siteID,
			payload:       model.SendMessageRequest{ID: validUUID, Content: "", RequestID: "req-1"},
			setupStore:    func(m *MockStore) {},
			wantErr:       true,
			wantErrSubstr: "content is empty",
		},
		{
			name: "content exceeding 20KB", account: "alice", roomID: "r1", siteID: siteID,
			payload:       model.SendMessageRequest{ID: validUUID, Content: strings.Repeat("a", 20*1024+1), RequestID: "req-1"},
			setupStore:    func(m *MockStore) {},
			wantErr:       true,
			wantErrSubstr: "content exceeds",
		},
		{
			name: "user not in room", account: "alice", roomID: "r1", siteID: siteID,
			payload: model.SendMessageRequest{ID: validUUID, Content: "hello", RequestID: "req-1"},
			setupStore: func(m *MockStore) {
				m.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, errors.New("subscription not found"))
			},
			wantErr: true, wantInfraErr: true, wantErrSubstr: "not subscribed",
		},
		{
			name: "store infra error", account: "alice", roomID: "r1", siteID: siteID,
			payload: model.SendMessageRequest{ID: validUUID, Content: "hello", RequestID: "req-1"},
			setupStore: func(m *MockStore) {
				m.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, errors.New("connection refused"))
			},
			wantErr: true, wantInfraErr: true,
		},
		{
			name: "publish to MESSAGES_CANONICAL fails", account: "alice", roomID: "r1", siteID: siteID,
			payload: model.SendMessageRequest{ID: validUUID, Content: "hello", RequestID: "req-1"},
			setupStore: func(m *MockStore) {
				m.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(happySub, nil)
			},
			publishErr: errors.New("nats publish failed"), wantErr: true, wantInfraErr: true,
		},
		{
			name: "malformed JSON", account: "alice", roomID: "r1", siteID: siteID,
			rawPayload: []byte("{invalid json"), setupStore: func(m *MockStore) {},
			wantErr: true, wantErrSubstr: "unmarshal",
		},
		{
			name: "siteID mismatch", account: "alice", roomID: "r1", siteID: "site-other",
			payload:       model.SendMessageRequest{ID: validUUID, Content: "hello", RequestID: "req-1"},
			setupStore:    func(m *MockStore) {},
			wantErr:       true,
			wantErrSubstr: "site ID mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			tc.setupStore(mockStore)

			var publishedSubject string
			var publishedData []byte
			publishCalled := false

			publish := func(subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
				publishCalled = true
				publishedSubject = subj
				publishedData = data
				if tc.publishErr != nil {
					return nil, tc.publishErr
				}
				return &jetstream.PubAck{}, nil
			}

			reply := func(subj string, data []byte) error { return nil }

			h := &Handler{store: mockStore, publish: publish, reply: reply, siteID: siteID}

			var data []byte
			if tc.rawPayload != nil {
				data = tc.rawPayload
			} else {
				data, _ = json.Marshal(tc.payload)
			}

			replyData, err := h.processMessage(context.Background(), tc.account, tc.roomID, tc.siteID, data)

			if tc.wantErr {
				require.Error(t, err)
				if tc.wantErrSubstr != "" {
					assert.Contains(t, err.Error(), tc.wantErrSubstr)
				}
				if tc.wantInfraErr {
					var ie *infraError
					assert.True(t, errors.As(err, &ie), "expected infraError, got: %v", err)
				} else {
					var ie *infraError
					assert.False(t, errors.As(err, &ie), "expected validation error, got: %v", err)
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, replyData)

			var msg model.Message
			require.NoError(t, json.Unmarshal(replyData, &msg))
			assert.Equal(t, validUUID, msg.ID)
			assert.Equal(t, tc.roomID, msg.RoomID)
			assert.Equal(t, "u1", msg.UserID)
			assert.Equal(t, "alice", msg.UserAccount)
			assert.Equal(t, "hello world", msg.Content)
			assert.False(t, msg.CreatedAt.IsZero())

			if tc.wantPublished {
				require.True(t, publishCalled)
				assert.Equal(t, "chat.msg.canonical.site-a.created", publishedSubject)
				var evt model.MessageEvent
				require.NoError(t, json.Unmarshal(publishedData, &evt))
				assert.Equal(t, msg.ID, evt.Message.ID)
				assert.Equal(t, siteID, evt.SiteID)
			}
		})
	}
}
```

Run: `make test SERVICE=message-gatekeeper` — Expected: FAIL (handler.go doesn't exist)

- [ ] **Step 4: Implement handler (Green phase)**

Create `message-gatekeeper/handler.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/hmchangw/chat/pkg/subject"
)

const maxContentBytes = 20 * 1024 // 20KB

type publishFunc func(subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)

// infraError signals a transient infrastructure failure that should trigger a nack/retry.
type infraError struct {
	err error
}

func (e *infraError) Error() string { return e.err.Error() }
func (e *infraError) Unwrap() error { return e.err }

type Handler struct {
	store   Store
	publish publishFunc
	reply   func(subject string, data []byte) error
	siteID  string
}

func NewHandler(store Store, siteID string, publish publishFunc, reply func(string, []byte) error) *Handler {
	return &Handler{store: store, publish: publish, reply: reply, siteID: siteID}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	account, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(msg.Subject())
	if !ok {
		slog.Warn("invalid subject", "subject", msg.Subject())
		if err := msg.Ack(); err != nil {
			slog.Error("failed to ack message", "error", err)
		}
		return
	}

	ctx := context.Background()
	replyData, err := h.processMessage(ctx, account, roomID, siteID, msg.Data())
	if err != nil {
		slog.Error("process message failed", "error", err, "account", account, "roomID", roomID)

		var ie *infraError
		if errors.As(err, &ie) {
			if nackErr := msg.Nak(); nackErr != nil {
				slog.Error("failed to nak message", "error", nackErr)
			}
			return
		}

		if reqID := extractRequestID(msg.Data()); reqID != "" {
			respSubj := subject.UserResponse(account, reqID)
			errData := natsutil.MarshalError(err.Error())
			if pubErr := h.reply(respSubj, errData); pubErr != nil {
				slog.Error("reply error publish failed", "error", pubErr)
			}
		}
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("failed to ack message", "error", ackErr)
		}
		return
	}

	if reqID := extractRequestID(msg.Data()); reqID != "" {
		respSubj := subject.UserResponse(account, reqID)
		if pubErr := h.reply(respSubj, replyData); pubErr != nil {
			slog.Error("reply publish failed", "error", pubErr)
		}
	}
	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("failed to ack message", "error", ackErr)
	}
}

func (h *Handler) processMessage(ctx context.Context, account, roomID, siteID string, data []byte) ([]byte, error) {
	if siteID != h.siteID {
		return nil, fmt.Errorf("site ID mismatch: got %s, want %s", siteID, h.siteID)
	}

	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}

	if _, err := uuid.Parse(req.ID); err != nil {
		return nil, fmt.Errorf("invalid message ID: %w", err)
	}

	if len(req.Content) == 0 {
		return nil, fmt.Errorf("content is empty")
	}
	if len(req.Content) > maxContentBytes {
		return nil, fmt.Errorf("content exceeds %d bytes", maxContentBytes)
	}

	sub, err := h.store.GetSubscription(ctx, account, roomID)
	if err != nil {
		return nil, &infraError{err: fmt.Errorf("not subscribed: %w", err)}
	}

	msg := model.Message{
		ID:        req.ID,
		RoomID:    roomID,
		UserID:    sub.User.ID,
		Account:  sub.User.Account,
		Content:   req.Content,
		CreatedAt: time.Now().UTC(),
	}

	evt := model.MessageEvent{Message: msg, SiteID: siteID}
	evtData, err := json.Marshal(evt)
	if err != nil {
		return nil, &infraError{err: fmt.Errorf("marshal event: %w", err)}
	}

	canonicalSubj := subject.MsgCanonicalCreated(siteID)
	if _, err := h.publish(canonicalSubj, evtData, jetstream.WithMsgID(msg.ID)); err != nil {
		return nil, &infraError{err: fmt.Errorf("publish to MESSAGES_CANONICAL: %w", err)}
	}

	return json.Marshal(msg)
}

func extractRequestID(data []byte) string {
	var req model.SendMessageRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return ""
	}
	return req.RequestID
}
```

Run: `make test SERVICE=message-gatekeeper` — Expected: PASS

- [ ] **Step 5: Create MongoDB store**

Create `message-gatekeeper/store_mongo.go`:

```go
package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

type MongoStore struct {
	subscriptions *mongo.Collection
}

func NewMongoStore(db *mongo.Database) *MongoStore {
	return &MongoStore{subscriptions: db.Collection("subscriptions")}
}

func (s *MongoStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	var sub model.Subscription
	filter := bson.M{"u.username": account, "roomId": roomID}
	if err := s.subscriptions.FindOne(ctx, filter).Decode(&sub); err != nil {
		return nil, fmt.Errorf("find subscription for user %s in room %s: %w", account, roomID, err)
	}
	return &sub, nil
}
```

- [ ] **Step 6: Create main.go**

Create `message-gatekeeper/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL    string `env:"NATS_URL"    envDefault:"nats://localhost:4222"`
	SiteID     string `env:"SITE_ID"     envDefault:"site-local"`
	MongoURI   string `env:"MONGO_URI"   envDefault:"mongodb://localhost:27017"`
	MongoDB    string `env:"MONGO_DB"    envDefault:"chat"`
	MaxWorkers int    `env:"MAX_WORKERS" envDefault:"100"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := jetstream.New(nc)
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
	store := NewMongoStore(db)

	publish := func(subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
		return js.Publish(ctx, subj, data, opts...)
	}
	reply := func(subj string, data []byte) error {
		return nc.Publish(subj, data)
	}
	handler := NewHandler(store, cfg.SiteID, publish, reply)

	msgCfg := stream.Messages(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: msgCfg.Name, Subjects: msgCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES stream failed", "error", err)
		os.Exit(1)
	}

	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: canonicalCfg.Name, Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, msgCfg.Name, jetstream.ConsumerConfig{
		Durable: "message-gatekeeper", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(2 * cfg.MaxWorkers))
	if err != nil {
		slog.Error("messages failed", "error", err)
		os.Exit(1)
	}

	sem := make(chan struct{}, cfg.MaxWorkers)
	var wg sync.WaitGroup

	go func() {
		for {
			msg, err := iter.Next()
			if err != nil {
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				handler.HandleJetStreamMsg(msg)
			}()
		}
	}()

	slog.Info("message-gatekeeper running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { iter.Stop(); return nil },
		func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
	)
}
```

- [ ] **Step 7: Create Dockerfile**

Create `message-gatekeeper/deploy/Dockerfile`:

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY message-gatekeeper/ message-gatekeeper/
RUN CGO_ENABLED=0 go build -o /message-gatekeeper ./message-gatekeeper/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /message-gatekeeper /message-gatekeeper
ENTRYPOINT ["/message-gatekeeper"]
```

- [ ] **Step 8: Create docker-compose.yml**

Create `message-gatekeeper/deploy/docker-compose.yml`:

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  message-gatekeeper:
    build:
      context: ../..
      dockerfile: message-gatekeeper/deploy/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 9: Verify**

```bash
make generate SERVICE=message-gatekeeper
make test SERVICE=message-gatekeeper
make lint
```

- [ ] **Step 10: Commit**

```bash
git add message-gatekeeper/
git commit -m "feat: add message-gatekeeper service — validates messages and publishes to MESSAGES_CANONICAL"
```

---

### Task 5: Refactor message-worker

**Files:**
- Modify: `message-worker/store.go`
- Delete: `message-worker/store_mongo.go`
- Create: `message-worker/store_cassandra.go`
- Rewrite: `message-worker/handler.go`
- Rewrite: `message-worker/handler_test.go`
- Rewrite: `message-worker/main.go`
- Regenerate: `message-worker/mock_store_test.go`

- [ ] **Step 1: Rewrite store.go**

Replace entire `message-worker/store.go`:

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg model.Message) error
}
```

- [ ] **Step 2: Regenerate mocks**

```bash
make generate SERVICE=message-worker
```

- [ ] **Step 3: Rewrite handler_test.go (Red phase)**

Replace entire `message-worker/handler_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_processMessage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	validEvt := model.MessageEvent{
		Message: model.Message{
			ID: "msg-1", RoomID: "r1", UserID: "u1", Account: "alice",
			Content: "hello", CreatedAt: now,
		},
		SiteID: "site-a",
	}
	validData, _ := json.Marshal(validEvt)

	tests := []struct {
		name      string
		data      []byte
		setupMock func(s *MockStore)
		wantErr   bool
		errMsg    string
	}{
		{
			name: "happy path",
			data: validData,
			setupMock: func(s *MockStore) {
				s.EXPECT().SaveMessage(gomock.Any(), validEvt.Message).Return(nil)
			},
		},
		{
			name:      "invalid JSON",
			data:      []byte(`{not json}`),
			setupMock: func(s *MockStore) {},
			wantErr:   true,
			errMsg:    "unmarshal message event",
		},
		{
			name: "store error",
			data: validData,
			setupMock: func(s *MockStore) {
				s.EXPECT().SaveMessage(gomock.Any(), validEvt.Message).Return(fmt.Errorf("cassandra timeout"))
			},
			wantErr: true,
			errMsg:  "save message",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			store := NewMockStore(ctrl)
			tc.setupMock(store)

			h := NewHandler(store)
			err := h.processMessage(context.Background(), tc.data)

			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
```

Run: `make test SERVICE=message-worker` — Expected: FAIL

- [ ] **Step 4: Rewrite handler.go (Green phase)**

Replace entire `message-worker/handler.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/model"
)

// Handler consumes validated messages from MESSAGES_CANONICAL and persists them to Cassandra.
type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES_CANONICAL stream.
func (h *Handler) HandleJetStreamMsg(msg jetstream.Msg) {
	ctx := context.Background()
	if err := h.processMessage(ctx, msg.Data()); err != nil {
		slog.Error("process message failed", "error", err)
		if err := msg.Nak(); err != nil {
			slog.Error("failed to nak message", "error", err)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "error", err)
	}
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}
	if err := h.store.SaveMessage(ctx, evt.Message); err != nil {
		return fmt.Errorf("save message: %w", err)
	}
	return nil
}
```

Run: `make test SERVICE=message-worker` — Expected: PASS

- [ ] **Step 5: Delete store_mongo.go, create store_cassandra.go**

```bash
rm message-worker/store_mongo.go
```

Create `message-worker/store_cassandra.go`:

```go
package main

import (
	"context"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

type CassandraStore struct {
	session *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{session: session}
}

func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message) error {
	return s.session.Query(
		`INSERT INTO messages (room_id, created_at, id, user_id, account, content) VALUES (?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.UserAccount, msg.Content,
	).WithContext(ctx).Exec()
}
```

- [ ] **Step 6: Rewrite main.go**

Replace entire `message-worker/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
)

type config struct {
	NatsURL           string `env:"NATS_URL"           envDefault:"nats://localhost:4222"`
	SiteID            string `env:"SITE_ID"            envDefault:"site-local"`
	CassandraHosts    string `env:"CASSANDRA_HOSTS"    envDefault:"localhost"`
	CassandraKeyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
	MaxWorkers        int    `env:"MAX_WORKERS"        envDefault:"100"`
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := env.ParseAs[config]()
	if err != nil {
		slog.Error("parse config", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	cassSession, err := cassutil.Connect(strings.Split(cfg.CassandraHosts, ","), cfg.CassandraKeyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	store := NewCassandraStore(cassSession)
	handler := NewHandler(store)

	streamCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: streamCfg.Name, Subjects: streamCfg.Subjects,
	}); err != nil {
		slog.Error("create stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, streamCfg.Name, jetstream.ConsumerConfig{
		Durable: "message-worker", AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		slog.Error("create consumer failed", "error", err)
		os.Exit(1)
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(2 * cfg.MaxWorkers))
	if err != nil {
		slog.Error("messages failed", "error", err)
		os.Exit(1)
	}

	sem := make(chan struct{}, cfg.MaxWorkers)
	var wg sync.WaitGroup

	go func() {
		for {
			msg, err := iter.Next()
			if err != nil {
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				handler.HandleJetStreamMsg(msg)
			}()
		}
	}()

	slog.Info("message-worker running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error { iter.Stop(); return nil },
		func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("worker drain timed out: %w", ctx.Err())
			}
		},
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
```

- [ ] **Step 7: Verify**

```bash
make generate SERVICE=message-worker
make test SERVICE=message-worker
make lint
```

- [ ] **Step 8: Commit**

```bash
git add message-worker/
git commit -m "refactor: simplify message-worker — consume from MESSAGES_CANONICAL, remove MongoDB and FANOUT"
```

---

### Task 6: Refactor broadcast-worker

**Files:**
- Modify: `broadcast-worker/handler.go`
- Modify: `broadcast-worker/handler_test.go`
- Modify: `broadcast-worker/main.go`

- [ ] **Step 1: Update tests — remove RoomID from MessageEvent (Red)**

In `broadcast-worker/handler_test.go`, update `makeMessageEvent`:

```go
// Replace:
func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		RoomID: roomID,
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}

// With:
func makeMessageEvent(roomID, content string, msgTime time.Time) []byte {
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
			ID: "msg-1", RoomID: roomID, UserID: "user-1",
			Content: content, CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)
	return data
}
```

In the DM test, update:

```go
// Replace:
			evt := model.MessageEvent{
				RoomID: "dm-1", SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "dm-1", UserID: "alice-id",
					Content: tc.content, CreatedAt: msgTime,
				},
			}

// With:
			evt := model.MessageEvent{
				SiteID: "site-a",
				Message: model.Message{
					ID: "msg-1", RoomID: "dm-1", UserID: "alice-id",
					Content: tc.content, CreatedAt: msgTime,
				},
			}
```

Run: `make test SERVICE=broadcast-worker` — Expected: FAIL

- [ ] **Step 2: Update handler — evt.RoomID to evt.Message.RoomID (Green)**

In `broadcast-worker/handler.go`, replace:

```go
	room, err := h.store.GetRoom(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.RoomID, err)
	}
```

With:

```go
	room, err := h.store.GetRoom(ctx, evt.Message.RoomID)
	if err != nil {
		return fmt.Errorf("get room %s: %w", evt.Message.RoomID, err)
	}
```

Run: `make test SERVICE=broadcast-worker` — Expected: PASS

- [ ] **Step 3: Update main.go — switch to MESSAGES_CANONICAL**

In `broadcast-worker/main.go`, replace:

```go
	fanoutCfg := stream.Fanout(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	}); err != nil {
		slog.Error("create fanout stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
```

With:

```go
	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
```

- [ ] **Step 4: Verify and commit**

```bash
make test SERVICE=broadcast-worker
make lint
git add broadcast-worker/
git commit -m "refactor: broadcast-worker consumes from MESSAGES_CANONICAL instead of FANOUT"
```

---

### Task 7: Refactor notification-worker

**Files:**
- Modify: `notification-worker/handler.go`
- Modify: `notification-worker/handler_test.go`
- Modify: `notification-worker/main.go`

- [ ] **Step 1: Update tests — remove RoomID from MessageEvent (Red)**

In `notification-worker/handler_test.go`, update all 3 `MessageEvent` constructions.

Replace in `TestHandleMessage_FanOutSkipsSender`:

```go
	evt := model.MessageEvent{
		RoomID: "room-1",
		SiteID: "site-a",
		Message: model.Message{
```

With:

```go
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
```

Replace in `TestHandleMessage_NoMembers`:

```go
	evt := model.MessageEvent{
		RoomID: "empty-room",
		SiteID: "site-a",
		Message: model.Message{
```

With:

```go
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
```

Replace in `TestHandleMessage_SoleMember`:

```go
	evt := model.MessageEvent{
		RoomID: "room-solo",
		SiteID: "site-a",
		Message: model.Message{
```

With:

```go
	evt := model.MessageEvent{
		SiteID: "site-a",
		Message: model.Message{
```

Run: `make test SERVICE=notification-worker` — Expected: FAIL

- [ ] **Step 2: Update handler — evt.RoomID to evt.Message.RoomID (Green)**

In `notification-worker/handler.go`, replace:

```go
	subs, err := h.members.ListSubscriptions(ctx, evt.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions for room %s: %w", evt.RoomID, err)
	}

	notif := model.NotificationEvent{
		Type:    "new_message",
		RoomID:  evt.RoomID,
		Message: evt.Message,
	}
```

With:

```go
	subs, err := h.members.ListSubscriptions(ctx, evt.Message.RoomID)
	if err != nil {
		return fmt.Errorf("list subscriptions for room %s: %w", evt.Message.RoomID, err)
	}

	notif := model.NotificationEvent{
		Type:    "new_message",
		RoomID:  evt.Message.RoomID,
		Message: evt.Message,
	}
```

Run: `make test SERVICE=notification-worker` — Expected: PASS

- [ ] **Step 3: Update main.go — switch to MESSAGES_CANONICAL**

In `notification-worker/main.go`, replace:

```go
	fanoutCfg := stream.Fanout(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     fanoutCfg.Name,
		Subjects: fanoutCfg.Subjects,
	}); err != nil {
		slog.Error("create fanout stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, fanoutCfg.Name, jetstream.ConsumerConfig{
```

With:

```go
	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
```

- [ ] **Step 4: Verify and commit**

```bash
make test SERVICE=notification-worker
make lint
git add notification-worker/
git commit -m "refactor: notification-worker consumes from MESSAGES_CANONICAL instead of FANOUT"
```

---

### Task 8: Update CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update event flow**

Replace:

```
**Event flow:** User publishes message to MESSAGES stream → `message-worker` stores and publishes to FANOUT → `broadcast-worker` delivers to room members → `notification-worker` sends notifications → cross-site events flow via OUTBOX/INBOX streams.
```

With:

```
**Event flow:** User publishes message to MESSAGES stream → `message-gatekeeper` validates and publishes to MESSAGES_CANONICAL → `message-worker` persists to Cassandra, `broadcast-worker` delivers to room members, `notification-worker` sends notifications → cross-site events flow via OUTBOX/INBOX streams.
```

- [ ] **Step 2: Update JetStream Streams list**

Replace:

```
- `FANOUT_{siteID}` — Broadcast messages for fan-out
```

With:

```
- `MESSAGES_CANONICAL_{siteID}` — Validated messages (single source of truth for downstream workers)
```

- [ ] **Step 3: Update subject naming**

Replace:

```
- Fanout: `fanout.{siteID}.{roomID}`
```

With:

```
- MESSAGES_CANONICAL: `chat.msg.canonical.{siteID}.created` (`.edited`, `.deleted` for future)
```

- [ ] **Step 4: Add message-gatekeeper to per-service note**

After the line `- `mock_store_test.go` — Generated mocks (never edit manually)`, add:

```

All services follow this layout, including `message-gatekeeper` (validates messages and publishes to MESSAGES_CANONICAL).
```

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md for message-gatekeeper architecture"
```
