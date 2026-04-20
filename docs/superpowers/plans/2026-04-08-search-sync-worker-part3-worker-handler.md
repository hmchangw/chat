# Search Sync Worker — Part 3: Handler with Batch Buffer

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the search-sync-worker handler: ES document mapping, index name derivation, batch buffer with Add/Flush, per-item ack/nak.

**Architecture:** Handler buffers JetStream messages, maps MessageEvent → ES BulkAction, flushes via SearchEngine.Bulk(), acks/naks individual messages based on per-item results. Version conflicts (409) are acked (stale write correctly rejected).

**Tech Stack:** Go, pkg/searchengine, pkg/model, nats.go/jetstream, go.uber.org/mock

**Spec:** `docs/superpowers/specs/2026-04-07-search-sync-worker-design.md` — "Consumer Design" + "CUD → ES Bulk Action Mapping" sections

**Depends on:** Part 1 (EventType on MessageEvent), Part 2 (pkg/searchengine)

**Push after each commit:** `git push -u origin claude/implement-search-sync-worker-yFtCC`

---

## File Structure

| Action | File | Purpose |
|--------|------|---------|
| Create | `search-sync-worker/store.go` | Store interface + mockgen directive |
| Create | `search-sync-worker/handler.go` | Handler struct, Add, Flush, buildAction, buildDocument, indexName |
| Create | `search-sync-worker/handler_test.go` | Unit tests with mocked Store and stubbed jetstream.Msg |

---

### Task 6: Store interface and mock generation

**Files:**
- Create: `search-sync-worker/store.go`

- [ ] **Step 1: Create `search-sync-worker/store.go`**

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/searchengine"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store

// Store defines the search engine operations needed by the handler.
type Store interface {
	Bulk(ctx context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error)
}
```

- [ ] **Step 2: Generate mocks**

Run: `make generate SERVICE=search-sync-worker`

- [ ] **Step 3: Verify mock was generated**

Run: `ls search-sync-worker/mock_store_test.go`
Expected: File exists

- [ ] **Step 4: Commit**

```bash
git add search-sync-worker/store.go search-sync-worker/mock_store_test.go
git commit -m "feat(search-sync-worker): add Store interface and generate mock"
```

---

### Task 7: Document mapping and index name (pure functions + tests)

**Files:**
- Create: `search-sync-worker/handler.go` (partial — pure functions only)
- Create: `search-sync-worker/handler_test.go` (partial — pure function tests)

- [ ] **Step 1: Write failing tests in `search-sync-worker/handler_test.go`**

```go
package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

func TestIndexName(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		createdAt time.Time
		want      string
	}{
		{"jan 2026", "messages-site1-v1", time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "messages-site1-v1-2026-01"},
		{"dec 2025", "msgs-v2", time.Date(2025, 12, 31, 23, 59, 0, 0, time.UTC), "msgs-v2-2025-12"},
		{"non-UTC normalized", "msgs", time.Date(2026, 1, 1, 5, 0, 0, 0, time.FixedZone("EST", -5*3600)), "msgs-2026-01"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexName(tt.prefix, tt.createdAt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildAction(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

	t.Run("created event produces index action", func(t *testing.T) {
		evt := model.MessageEvent{
			Event: model.EventCreated,
			Message: model.Message{
				ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
				Content: "hello", CreatedAt: ts,
			},
			SiteID:    "site-a",
			Timestamp: 1737964678390,
		}
		action := buildAction(evt, "msgs-v1")
		assert.Equal(t, searchengine.ActionIndex, action.Action)
		assert.Equal(t, "msgs-v1-2026-01", action.Index)
		assert.Equal(t, "msg-1", action.DocID)
		assert.Equal(t, int64(1737964678390), action.Version)
		require.NotNil(t, action.Doc)

		var doc map[string]any
		require.NoError(t, json.Unmarshal(action.Doc, &doc))
		assert.Equal(t, "msg-1", doc["messageId"])
		assert.Equal(t, "r1", doc["roomId"])
		assert.Equal(t, "site-a", doc["siteId"])
		assert.Equal(t, "hello", doc["msg"])
		assert.Equal(t, "alice", doc["senderUsername"])
	})

	t.Run("updated event produces index action (full replace)", func(t *testing.T) {
		evt := model.MessageEvent{
			Event: model.EventUpdated,
			Message: model.Message{
				ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
				Content: "updated", CreatedAt: ts,
			},
			SiteID:    "site-a",
			Timestamp: 1737964699000,
		}
		action := buildAction(evt, "msgs-v1")
		assert.Equal(t, searchengine.ActionIndex, action.Action)
		assert.Equal(t, "msgs-v1-2026-01", action.Index)
		assert.Equal(t, int64(1737964699000), action.Version)
	})

	t.Run("deleted event produces delete action", func(t *testing.T) {
		evt := model.MessageEvent{
			Event: model.EventDeleted,
			Message: model.Message{
				ID: "msg-1", RoomID: "r1", CreatedAt: ts,
			},
			SiteID:    "site-a",
			Timestamp: 1737964710000,
		}
		action := buildAction(evt, "msgs-v1")
		assert.Equal(t, searchengine.ActionDelete, action.Action)
		assert.Equal(t, "msgs-v1-2026-01", action.Index)
		assert.Equal(t, "msg-1", action.DocID)
		assert.Equal(t, int64(1737964710000), action.Version)
		assert.Nil(t, action.Doc)
	})

	t.Run("empty event defaults to created (backward compat)", func(t *testing.T) {
		evt := model.MessageEvent{
			Message: model.Message{
				ID: "msg-1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
				Content: "hello", CreatedAt: ts,
			},
			SiteID:    "site-a",
			Timestamp: 1735689600000,
		}
		action := buildAction(evt, "msgs-v1")
		assert.Equal(t, searchengine.ActionIndex, action.Action)
	})
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./search-sync-worker/ -run 'TestIndexName|TestBuildAction' -v`
Expected: FAIL — `indexName` and `buildAction` undefined

- [ ] **Step 3: Implement pure functions in `search-sync-worker/handler.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// searchDocument is the ES document structure for indexed messages.
type searchDocument struct {
	MessageID             string     `json:"messageId"`
	RoomID                string     `json:"roomId"`
	SiteID                string     `json:"siteId"`
	SenderUsername        string     `json:"senderUsername,omitempty"`
	Msg                   string     `json:"msg,omitempty"`
	CreatedAt             time.Time  `json:"createdAt"`
	ThreadParentID        string     `json:"threadParentId,omitempty"`
	ThreadParentCreatedAt *time.Time `json:"threadParentCreatedAt,omitempty"`
}

// indexName derives the monthly ES index from prefix and message creation time.
func indexName(prefix string, createdAt time.Time) string {
	return fmt.Sprintf("%s-%s", prefix, createdAt.UTC().Format("2006-01"))
}

// buildAction converts a MessageEvent into an ES BulkAction.
func buildAction(evt model.MessageEvent, indexPrefix string) searchengine.BulkAction {
	index := indexName(indexPrefix, evt.Message.CreatedAt)
	eventType := evt.Event
	if eventType == "" {
		eventType = model.EventCreated
	}

	if eventType == model.EventDeleted {
		return searchengine.BulkAction{
			Action:  searchengine.ActionDelete,
			Index:   index,
			DocID:   evt.Message.ID,
			Version: evt.Timestamp,
		}
	}

	doc := buildDocument(evt)
	return searchengine.BulkAction{
		Action:  searchengine.ActionIndex,
		Index:   index,
		DocID:   evt.Message.ID,
		Version: evt.Timestamp,
		Doc:     doc,
	}
}

// buildDocument maps a MessageEvent to an ES search document.
func buildDocument(evt model.MessageEvent) json.RawMessage {
	doc := searchDocument{
		MessageID:             evt.Message.ID,
		RoomID:                evt.Message.RoomID,
		SiteID:                evt.SiteID,
		SenderUsername:        evt.Message.UserAccount,
		Msg:                   evt.Message.Content,
		CreatedAt:             evt.Message.CreatedAt,
		ThreadParentID:        evt.Message.ThreadParentMessageID,
		ThreadParentCreatedAt: evt.Message.ThreadParentMessageCreatedAt,
	}
	data, _ := json.Marshal(doc)
	return data
}
```

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./search-sync-worker/ -run 'TestIndexName|TestBuildAction' -v -race`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add search-sync-worker/handler.go search-sync-worker/handler_test.go
git commit -m "feat(search-sync-worker): add document mapping, index name, and buildAction"
```

---

### Task 8: Handler Add/Flush with batch buffer and tests

**Files:**
- Modify: `search-sync-worker/handler.go` — add Handler struct, Add, Flush, BufferLen
- Modify: `search-sync-worker/handler_test.go` — add tests for Add/Flush with stubbed jetstream.Msg

- [ ] **Step 1: Write failing tests — append to `search-sync-worker/handler_test.go`**

First add the stub for jetstream.Msg at the top of the test file (after imports):

```go
import (
	"context"
	// ... existing imports ...

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/mock/gomock"
)

// stubMsg implements jetstream.Msg for testing.
type stubMsg struct {
	data   []byte
	acked  bool
	nacked bool
}

func (m *stubMsg) Data() []byte                                  { return m.data }
func (m *stubMsg) Ack() error                                    { m.acked = true; return nil }
func (m *stubMsg) Nak() error                                    { m.nacked = true; return nil }
func (m *stubMsg) NakWithDelay(time.Duration) error              { return nil }
func (m *stubMsg) InProgress() error                             { return nil }
func (m *stubMsg) Term() error                                   { return nil }
func (m *stubMsg) TermWithReason(string) error                   { return nil }
func (m *stubMsg) Metadata() (*jetstream.MsgMetadata, error)     { return nil, nil }
func (m *stubMsg) Subject() string                               { return "" }
func (m *stubMsg) Reply() string                                 { return "" }
func (m *stubMsg) Headers() nats.Header                          { return nil }
func (m *stubMsg) DoubleAck(context.Context) error               { return nil }

func makeStubMsg(t *testing.T, evt model.MessageEvent) *stubMsg {
	t.Helper()
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return &stubMsg{data: data}
}
```

Then add the tests:

```go
func TestHandler_Add(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	h := NewHandler(store, "msgs-v1", 500)

	evt := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		},
		SiteID: "site-a", Timestamp: 100,
	}
	msg := makeStubMsg(t, evt)

	h.Add(msg)
	assert.Equal(t, 1, h.BufferLen())
}

func TestHandler_Add_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	h := NewHandler(store, "msgs-v1", 500)

	msg := &stubMsg{data: []byte("{invalid")}
	h.Add(msg)
	// Malformed messages are acked (not retried) and not buffered
	assert.Equal(t, 0, h.BufferLen())
	assert.True(t, msg.acked)
}

func TestHandler_Flush(t *testing.T) {
	ts := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	baseEvt := model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: ts,
		},
		SiteID: "site-a", Timestamp: 100,
	}

	t.Run("all items succeed — all acked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 201}}, nil)

		h := NewHandler(store, "msgs-v1", 500)
		msg := makeStubMsg(t, baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.True(t, msg.acked)
		assert.False(t, msg.nacked)
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("version conflict (409) — acked not nacked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 409, Error: "version conflict"}}, nil)

		h := NewHandler(store, "msgs-v1", 500)
		msg := makeStubMsg(t, baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.True(t, msg.acked)
		assert.False(t, msg.nacked)
	})

	t.Run("item failure — nacked for redelivery", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(1)).
			Return([]searchengine.BulkResult{{Status: 500, Error: "internal"}}, nil)

		h := NewHandler(store, "msgs-v1", 500)
		msg := makeStubMsg(t, baseEvt)
		h.Add(msg)
		h.Flush(context.Background())

		assert.False(t, msg.acked)
		assert.True(t, msg.nacked)
	})

	t.Run("total bulk failure — all nacked", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(2)).
			Return(nil, fmt.Errorf("connection refused"))

		h := NewHandler(store, "msgs-v1", 500)
		msg1 := makeStubMsg(t, baseEvt)
		evt2 := baseEvt
		evt2.Message.ID = "m2"
		msg2 := makeStubMsg(t, evt2)

		h.Add(msg1)
		h.Add(msg2)
		h.Flush(context.Background())

		assert.True(t, msg1.nacked)
		assert.True(t, msg2.nacked)
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("empty flush is no-op", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		// No Bulk call expected
		h := NewHandler(store, "msgs-v1", 500)
		h.Flush(context.Background())
		assert.Equal(t, 0, h.BufferLen())
	})

	t.Run("mixed results — per-item ack/nak", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		store := NewMockStore(ctrl)
		store.EXPECT().
			Bulk(gomock.Any(), gomock.Len(3)).
			Return([]searchengine.BulkResult{
				{Status: 201},
				{Status: 409, Error: "version conflict"},
				{Status: 500, Error: "shard failure"},
			}, nil)

		h := NewHandler(store, "msgs-v1", 500)
		msgs := make([]*stubMsg, 3)
		for i := range msgs {
			evt := baseEvt
			evt.Message.ID = fmt.Sprintf("m%d", i)
			msgs[i] = makeStubMsg(t, evt)
			h.Add(msgs[i])
		}
		h.Flush(context.Background())

		assert.True(t, msgs[0].acked, "201 should be acked")
		assert.True(t, msgs[1].acked, "409 should be acked")
		assert.True(t, msgs[2].nacked, "500 should be nacked")
	})
}
```

- [ ] **Step 2: Run tests — verify they fail**

Run: `go test ./search-sync-worker/ -run 'TestHandler' -v`
Expected: FAIL — `NewHandler` undefined

- [ ] **Step 3: Add Handler struct and methods to `search-sync-worker/handler.go`**

Append to the existing handler.go file:

```go
import (
	"context"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go/jetstream"
)

type bufferedMsg struct {
	action searchengine.BulkAction
	jsMsg  jetstream.Msg
}

// Handler buffers JetStream messages and flushes them as ES bulk requests.
type Handler struct {
	store       Store
	indexPrefix string
	batchSize   int
	mu          sync.Mutex
	buffer      []bufferedMsg
}

// NewHandler creates a Handler with the given store, index prefix, and batch size.
func NewHandler(store Store, indexPrefix string, batchSize int) *Handler {
	return &Handler{
		store:       store,
		indexPrefix: indexPrefix,
		batchSize:   batchSize,
		buffer:      make([]bufferedMsg, 0, batchSize),
	}
}

// Add parses a JetStream message and adds it to the buffer.
func (h *Handler) Add(msg jetstream.Msg) {
	var evt model.MessageEvent
	if err := json.Unmarshal(msg.Data(), &evt); err != nil {
		slog.Error("unmarshal message event", "error", err)
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("ack malformed message", "error", ackErr)
		}
		return
	}

	action := buildAction(evt, h.indexPrefix)

	h.mu.Lock()
	h.buffer = append(h.buffer, bufferedMsg{action: action, jsMsg: msg})
	h.mu.Unlock()
}

// Flush sends all buffered actions to ES and acks/naks per item.
func (h *Handler) Flush(ctx context.Context) {
	h.mu.Lock()
	if len(h.buffer) == 0 {
		h.mu.Unlock()
		return
	}
	items := h.buffer
	h.buffer = make([]bufferedMsg, 0, h.batchSize)
	h.mu.Unlock()

	actions := make([]searchengine.BulkAction, len(items))
	for i, item := range items {
		actions[i] = item.action
	}

	results, err := h.store.Bulk(ctx, actions)
	if err != nil {
		slog.Error("bulk request failed", "error", err, "count", len(items))
		for _, item := range items {
			if nakErr := item.jsMsg.Nak(); nakErr != nil {
				slog.Error("nak failed", "error", nakErr)
			}
		}
		return
	}

	for i, result := range results {
		if result.Status == 409 || (result.Status >= 200 && result.Status < 300) {
			if ackErr := items[i].jsMsg.Ack(); ackErr != nil {
				slog.Error("ack failed", "error", ackErr)
			}
		} else {
			slog.Error("bulk item failed", "status", result.Status, "error", result.Error, "docID", items[i].action.DocID)
			if nakErr := items[i].jsMsg.Nak(); nakErr != nil {
				slog.Error("nak failed", "error", nakErr)
			}
		}
	}
}

// BufferLen returns the current buffer size.
func (h *Handler) BufferLen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.buffer)
}

// BufferFull returns true if the buffer has reached batch size.
func (h *Handler) BufferFull() bool {
	return h.BufferLen() >= h.batchSize
}
```

Note: The final handler.go should have a single import block combining all imports. When implementing, merge the import blocks from the pure functions and the Handler methods.

- [ ] **Step 4: Run tests — verify they pass**

Run: `go test ./search-sync-worker/ -v -race`
Expected: PASS

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit and push**

```bash
git add search-sync-worker/
git commit -m "feat(search-sync-worker): implement Handler with batch buffer, Add, and Flush"
git push -u origin claude/implement-search-sync-worker-yFtCC
```
