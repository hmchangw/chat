# Thread Parent Timestamp Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `ThreadParentMessageCreatedAt *time.Time` to `SendMessageRequest` and `Message`, validate the paired fields in message-gatekeeper, and propagate the timestamp through to `MESSAGES_CANONICAL`.

**Architecture:** The client supplies the parent message's timestamp alongside `ThreadParentMessageID` in `SendMessageRequest`. The gatekeeper validates that both fields are present together (or both absent), copies them into `Message`, and publishes the enriched `MessageEvent` to `MESSAGES_CANONICAL`. No store changes in this task.

**Tech Stack:** Go 1.25, `pkg/model` (shared models), `message-gatekeeper` (NATS JetStream consumer), `go.uber.org/mock` + `stretchr/testify` for tests.

---

### Task 1: Model field + model tests

**Files:**
- Modify: `pkg/model/message.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write failing model tests**

Open `pkg/model/model_test.go`. Add the following sub-tests.

Inside `TestMessageJSON`, add after the existing sub-tests:

```go
t.Run("with threadParentMessageCreatedAt", func(t *testing.T) {
    parentTS := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
    m := model.Message{
        ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
        Content:                      "reply",
        CreatedAt:                    time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
        ThreadParentMessageID:        "parent-msg-uuid",
        ThreadParentMessageCreatedAt: &parentTS,
    }
    data, err := json.Marshal(&m)
    require.NoError(t, err)
    var dst model.Message
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, m, dst)
})
```

Inside `TestSendMessageRequestJSON`, add after the existing sub-tests:

```go
t.Run("with threadParentMessageCreatedAt", func(t *testing.T) {
    parentTS := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
    r := model.SendMessageRequest{
        ID:                           "msg-uuid-1",
        Content:                      "reply",
        RequestID:                    "req-1",
        ThreadParentMessageID:        "parent-msg-uuid",
        ThreadParentMessageCreatedAt: &parentTS,
    }
    data, err := json.Marshal(&r)
    require.NoError(t, err)
    var dst model.SendMessageRequest
    require.NoError(t, json.Unmarshal(data, &dst))
    assert.Equal(t, r, dst)
})

t.Run("threadParentMessageCreatedAt omitted when nil", func(t *testing.T) {
    r := model.SendMessageRequest{
        ID:        "msg-uuid-1",
        Content:   "hello world",
        RequestID: "req-1",
    }
    data, err := json.Marshal(&r)
    require.NoError(t, err)
    var raw map[string]any
    require.NoError(t, json.Unmarshal(data, &raw))
    _, present := raw["threadParentMessageCreatedAt"]
    assert.False(t, present, "threadParentMessageCreatedAt should be omitted when nil")
})
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
make test
```

Expected: FAIL — `model.Message` and `model.SendMessageRequest` have no `ThreadParentMessageCreatedAt` field (compile error).

- [ ] **Step 3: Add the field to `pkg/model/message.go`**

The file currently looks like:

```go
type Message struct {
	ID                    string    `json:"id"                              bson:"_id"`
	RoomID                string    `json:"roomId"                          bson:"roomId"`
	UserID                string    `json:"userId"                          bson:"userId"`
	UserAccount           string    `json:"userAccount"                     bson:"userAccount"`
	Content               string    `json:"content"                         bson:"content"`
	CreatedAt             time.Time `json:"createdAt"                       bson:"createdAt"`
	ThreadParentMessageID string    `json:"threadParentMessageId,omitempty" bson:"threadParentMessageId,omitempty"`
}

type SendMessageRequest struct {
	ID                    string `json:"id"`
	Content               string `json:"content"`
	RequestID             string `json:"requestId"`
	ThreadParentMessageID string `json:"threadParentMessageId,omitempty"`
}
```

Replace the entire file with:

```go
package model

import "time"

type Message struct {
	ID                           string     `json:"id"                                   bson:"_id"`
	RoomID                       string     `json:"roomId"                               bson:"roomId"`
	UserID                       string     `json:"userId"                               bson:"userId"`
	UserAccount                  string     `json:"userAccount"                          bson:"userAccount"`
	Content                      string     `json:"content"                              bson:"content"`
	CreatedAt                    time.Time  `json:"createdAt"                            bson:"createdAt"`
	ThreadParentMessageID        string     `json:"threadParentMessageId,omitempty"      bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
}

type SendMessageRequest struct {
	ID                           string     `json:"id"`
	Content                      string     `json:"content"`
	RequestID                    string     `json:"requestId"`
	ThreadParentMessageID        string     `json:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty"`
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
make test
```

Expected: PASS — all existing and new model tests green.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/message.go pkg/model/model_test.go
git commit -m "feat(model): add ThreadParentMessageCreatedAt to Message and SendMessageRequest"
```

---

### Task 2: Gatekeeper validation + handler tests

**Files:**
- Modify: `message-gatekeeper/handler.go`
- Modify: `message-gatekeeper/handler_test.go`

- [ ] **Step 1: Write failing handler test cases**

Open `message-gatekeeper/handler_test.go`.

Add `"time"` to the import block:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)
```

In the `tests` slice inside `TestHandler_ProcessMessage`, add these two cases after the existing `"happy path with thread parent"` case:

```go
{
    name:    "thread reply with ID and timestamp",
    account: validAccount,
    roomID:  validRoomID,
    siteID:  validSiteID,
    buildData: func() []byte {
        parentTS := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
        req := model.SendMessageRequest{
            ID:                           validID,
            Content:                      validContent,
            ThreadParentMessageID:        "parent-msg-uuid",
            ThreadParentMessageCreatedAt: &parentTS,
        }
        data, _ := json.Marshal(req)
        return data
    },
    setupStore: func(s *MockStore) {
        s.EXPECT().
            GetSubscription(gomock.Any(), validAccount, validRoomID).
            Return(sub, nil)
    },
    setupPub: func() (publishFunc, *[]publishedMsg) {
        var published []publishedMsg
        return makePublishFunc(&published, nil), &published
    },
    wantErr: false,
    checkResult: func(t *testing.T, data []byte, published []publishedMsg) {
        parentTS := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
        require.NotNil(t, data)
        var msg model.Message
        require.NoError(t, json.Unmarshal(data, &msg))
        assert.Equal(t, "parent-msg-uuid", msg.ThreadParentMessageID)
        require.NotNil(t, msg.ThreadParentMessageCreatedAt)
        assert.Equal(t, parentTS, msg.ThreadParentMessageCreatedAt.UTC())

        require.Len(t, published, 1)
        var evt model.MessageEvent
        require.NoError(t, json.Unmarshal(published[0].data, &evt))
        assert.Equal(t, "parent-msg-uuid", evt.Message.ThreadParentMessageID)
        require.NotNil(t, evt.Message.ThreadParentMessageCreatedAt)
        assert.Equal(t, parentTS, evt.Message.ThreadParentMessageCreatedAt.UTC())
    },
},
{
    name:    "thread parent ID without timestamp",
    account: validAccount,
    roomID:  validRoomID,
    siteID:  validSiteID,
    buildData: func() []byte {
        req := model.SendMessageRequest{
            ID:                    validID,
            Content:               validContent,
            ThreadParentMessageID: "parent-msg-uuid",
        }
        data, _ := json.Marshal(req)
        return data
    },
    setupStore: func(s *MockStore) {},
    setupPub: func() (publishFunc, *[]publishedMsg) {
        return makePublishFunc(nil, nil), nil
    },
    wantErr:   true,
    wantInfra: false,
},
```

- [ ] **Step 2: Run tests to confirm new cases fail**

```bash
make test SERVICE=message-gatekeeper
```

Expected: FAIL — `"thread parent ID without timestamp"` passes (no validation yet), `"thread reply with ID and timestamp"` may pass or fail depending on field copy. Both cases should fail their assertions.

- [ ] **Step 3: Add validation and field copy to `message-gatekeeper/handler.go`**

In `processMessage`, after the content-size check (line 134) and before the subscription lookup (line 138), add:

```go
// Validate thread parent fields are paired
if req.ThreadParentMessageID != "" && req.ThreadParentMessageCreatedAt == nil {
    return nil, fmt.Errorf("threadParentMessageCreatedAt is required when threadParentMessageId is set")
}
```

In the `Message` literal (starting at line 149), add the new field:

```go
msg := model.Message{
    ID:                           req.ID,
    RoomID:                       roomID,
    UserID:                       sub.User.ID,
    UserAccount:                  sub.User.Account,
    Content:                      req.Content,
    CreatedAt:                    now,
    ThreadParentMessageID:        req.ThreadParentMessageID,
    ThreadParentMessageCreatedAt: req.ThreadParentMessageCreatedAt,
}
```

- [ ] **Step 4: Run tests to confirm all pass**

```bash
make test SERVICE=message-gatekeeper
```

Expected: PASS — all existing cases plus the two new cases green.

- [ ] **Step 5: Run full test suite to confirm no regressions**

```bash
make test
```

Expected: PASS — all packages green.

- [ ] **Step 6: Run linter**

```bash
make lint
```

Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add message-gatekeeper/handler.go message-gatekeeper/handler_test.go
git commit -m "feat(message-gatekeeper): validate and propagate ThreadParentMessageCreatedAt"
```

- [ ] **Step 8: Push**

```bash
git push -u origin claude/add-thread-parent-timestamp-CybWE
```
