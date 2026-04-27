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
