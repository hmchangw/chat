# Message-Worker Positioning — Implementation Plan

## Overview

Update message-worker to match its position in the pipeline: persist full-schema messages to Cassandra (`messages_by_room`) and publish to a new `SEARCH_INDEX` JetStream stream for downstream search indexing (ES/OpenSearch).

---

## Worklist

| # | Task | Files |
|---|------|-------|
| 1 | Move UDT types (`Participant`, `File`, `Card`, `CardAction`) from `history-service/internal/models/` to `pkg/model/` | `pkg/model/types.go`, `pkg/model/utils.go` |
| 2 | Expand `model.Message` to full 22-field schema | `pkg/model/message.go` |
| 3 | Update `model.MessageEvent` if needed | `pkg/model/event.go` |
| 4 | Define `SearchIndex` stream config | `pkg/stream/stream.go` |
| 5 | Add `SearchIndexCreated` subject builder | `pkg/subject/subject.go` |
| 6 | Define `SearchIndexEvent` model | `pkg/model/event.go` |
| 7 | Update `Store` interface — `SaveMessage` accepts expanded `model.Message` | `message-worker/store.go` |
| 8 | Rewrite `CassandraStore.SaveMessage` — insert all 22 columns into `messages_by_room` | `message-worker/store_cassandra.go` |
| 9 | Add `publishFunc` to `Handler` — publish `SearchIndexEvent` after Cassandra persist | `message-worker/handler.go` |
| 10 | Update `main.go` — create `SEARCH_INDEX` stream, wire `publishFunc` into handler | `message-worker/main.go` |
| 11 | Write handler unit tests (TDD red-green) | `message-worker/handler_test.go` |
| 12 | Regenerate mocks | `message-worker/mock_store_test.go` |
| 13 | Update integration test — new schema with UDTs, `messages_by_room` table | `message-worker/integration_test.go` |
| 14 | Update `history-service` to import UDTs from `pkg/model/` instead of local `models` | `history-service/internal/models/types.go`, `history-service/internal/cassrepo/` |
| 15 | Lint, test, verify | `make lint && make test SERVICE=message-worker` |

---

## Before (Current State)

### `pkg/model/message.go` — 6 fields
```go
type Message struct {
    ID        string    `json:"id"        bson:"_id"`
    RoomID    string    `json:"roomId"    bson:"roomId"`
    UserID    string    `json:"userId"    bson:"userId"`
    Username  string    `json:"username"  bson:"username"`
    Content   string    `json:"content"   bson:"content"`
    CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}
```

### `message-worker/store.go` — single method, lean model
```go
type Store interface {
    SaveMessage(ctx context.Context, msg model.Message) error
}
```

### `message-worker/store_cassandra.go` — 5 columns, old table
```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message) error {
    return s.cassSession.Query(
        `INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
        msg.RoomID, msg.CreatedAt, msg.ID, msg.UserID, msg.Content,
    ).WithContext(ctx).Exec()
}
```

### `message-worker/handler.go` — persist only, no publish
```go
type Handler struct {
    store Store
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
    var evt model.MessageEvent
    json.Unmarshal(data, &evt)
    return h.store.SaveMessage(ctx, evt.Message)
}
```

### `pkg/stream/stream.go` — no search index stream
```
Messages, MessagesCanonical, Rooms, Outbox, Inbox
```

### `pkg/subject/subject.go` — no search index subject
```
MsgCanonicalCreated, RoomEvent, Notification, Outbox, ...
```

### UDT types — locked inside history-service
```
history-service/internal/models/types.go  (Participant, File, Card, CardAction)
history-service/internal/models/utils.go  (marshal/unmarshal helpers)
```

---

## After (Expected State)

### `pkg/model/types.go` — UDTs promoted to shared package
```go
// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
    ID          string `json:"id"                    cql:"id"`
    UserName    string `json:"userName"              cql:"user_name"`
    EngName     string `json:"engName,omitempty"     cql:"eng_name"`
    CompanyName string `json:"companyName,omitempty" cql:"company_name"`
    AppID       string `json:"appId,omitempty"       cql:"app_id"`
    AppName     string `json:"appName,omitempty"     cql:"app_name"`
    IsBot       bool   `json:"isBot,omitempty"       cql:"is_bot"`
}

// File, Card, CardAction — same pattern with MarshalUDT/UnmarshalUDT
```

### `pkg/model/message.go` — full 22-field schema
```go
type Message struct {
    ID                    string                   `json:"id"                              bson:"_id"`
    RoomID                string                   `json:"roomId"                          bson:"roomId"`
    CreatedAt             time.Time                `json:"createdAt"                       bson:"createdAt"`
    Sender                Participant              `json:"sender"                          bson:"sender"`
    TargetUser            *Participant             `json:"targetUser,omitempty"            bson:"targetUser,omitempty"`
    Content               string                   `json:"content"                         bson:"content"`
    Mentions              []Participant            `json:"mentions,omitempty"              bson:"mentions,omitempty"`
    Attachments           [][]byte                 `json:"attachments,omitempty"           bson:"attachments,omitempty"`
    File                  *File                    `json:"file,omitempty"                  bson:"file,omitempty"`
    Card                  *Card                    `json:"card,omitempty"                  bson:"card,omitempty"`
    CardAction            *CardAction              `json:"cardAction,omitempty"            bson:"cardAction,omitempty"`
    TShow                 bool                     `json:"tshow,omitempty"                 bson:"tshow,omitempty"`
    ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty" bson:"threadParentCreatedAt,omitempty"`
    VisibleTo             string                   `json:"visibleTo,omitempty"             bson:"visibleTo,omitempty"`
    Unread                bool                     `json:"unread,omitempty"                bson:"unread,omitempty"`
    Reactions             map[string][]Participant `json:"reactions,omitempty"             bson:"reactions,omitempty"`
    Deleted               bool                     `json:"deleted,omitempty"               bson:"deleted,omitempty"`
    SysMsgType            string                   `json:"sysMsgType,omitempty"            bson:"sysMsgType,omitempty"`
    SysMsgData            []byte                   `json:"sysMsgData,omitempty"            bson:"sysMsgData,omitempty"`
    FederateFrom          string                   `json:"federateFrom,omitempty"          bson:"federateFrom,omitempty"`
    EditedAt              *time.Time               `json:"editedAt,omitempty"              bson:"editedAt,omitempty"`
    UpdatedAt             *time.Time               `json:"updatedAt,omitempty"             bson:"updatedAt,omitempty"`
}
```

> **Note:** `UserID` and `Username` are removed — replaced by `Sender` (`Participant` UDT).
> `Content` is kept as the JSON field name; Cassandra column maps to `msg`.

### `message-worker/store_cassandra.go` — 22 columns, `messages_by_room` table
```go
func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message) error {
    return s.cassSession.Query(
        `INSERT INTO messages_by_room (
            room_id, created_at, message_id, sender, target_user,
            msg, mentions, attachments, file, card, card_action, tshow,
            thread_parent_created_at, visible_to, unread, reactions, deleted,
            sys_msg_type, sys_msg_data, federate_from, edited_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
        msg.RoomID, msg.CreatedAt, msg.ID, msg.Sender, msg.TargetUser,
        msg.Content, msg.Mentions, msg.Attachments, msg.File, msg.Card,
        msg.CardAction, msg.TShow, msg.ThreadParentCreatedAt, msg.VisibleTo,
        msg.Unread, msg.Reactions, msg.Deleted, msg.SysMsgType, msg.SysMsgData,
        msg.FederateFrom, msg.EditedAt, msg.UpdatedAt,
    ).WithContext(ctx).Exec()
}
```

### `message-worker/handler.go` — persist + publish to search index
```go
// publishFunc is the function signature for publishing to JetStream.
type publishFunc func(subject string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)

type Handler struct {
    store   Store
    publish publishFunc
    siteID  string
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
    var evt model.MessageEvent
    json.Unmarshal(data, &evt)

    // 1. Persist to Cassandra
    if err := h.store.SaveMessage(ctx, evt.Message); err != nil {
        return fmt.Errorf("save message: %w", err)
    }

    // 2. Publish to SEARCH_INDEX stream
    idxEvt := model.SearchIndexEvent{Message: evt.Message, SiteID: evt.SiteID}
    payload, _ := json.Marshal(idxEvt)
    if _, err := h.publish(
        subject.SearchIndexCreated(evt.SiteID),
        payload,
        jetstream.WithMsgID(evt.Message.ID),
    ); err != nil {
        return fmt.Errorf("publish search index event: %w", err)
    }

    return nil
}
```

### `pkg/model/event.go` — new SearchIndexEvent
```go
// SearchIndexEvent carries indexable message fields for the search pipeline.
type SearchIndexEvent struct {
    Message Message `json:"message"`
    SiteID  string  `json:"siteId"`
}
```

### `pkg/stream/stream.go` — new stream
```go
func SearchIndex(siteID string) Config {
    return Config{
        Name:     fmt.Sprintf("SEARCH_INDEX_%s", siteID),
        Subjects: []string{fmt.Sprintf("chat.search.index.%s.>", siteID)},
    }
}
```

### `pkg/subject/subject.go` — new subject builder
```go
func SearchIndexCreated(siteID string) string {
    return fmt.Sprintf("chat.search.index.%s.created", siteID)
}
```

### `message-worker/main.go` — wires search index stream + publishFunc
```go
// Create SEARCH_INDEX stream
searchCfg := stream.SearchIndex(cfg.SiteID)
js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
    Name:     searchCfg.Name,
    Subjects: searchCfg.Subjects,
})

// Wire publishFunc
pub := func(subj string, data []byte, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
    return js.Publish(ctx, subj, data, opts...)
}
handler := NewHandler(store, pub, cfg.SiteID)
```

### `history-service/internal/models/` — imports from `pkg/model/`
```go
// types.go becomes thin re-exports or is removed
// UDT types imported from pkg/model
import "github.com/hmchangw/chat/pkg/model"
type Participant = model.Participant
```

---

## Dependency Graph

```
MESSAGES_CANONICAL_{siteID}
        │
        ▼
  message-worker
        │
        ├──► Cassandra (messages_by_room, 22 cols)
        │         │
        │         ▼
        │    history-service (reads)
        │
        └──► SEARCH_INDEX_{siteID} (JetStream)
                  │
                  ▼
             future search-indexer (ES / OpenSearch)
```

---

## Risk & Notes

- **Breaking change to `model.Message`**: `UserID`/`Username` replaced by `Sender` (`Participant`). All consumers of `model.Message` (message-gatekeeper, broadcast-worker, notification-worker) will need updates. We handle message-worker first; other services follow.
- **UDT relocation**: Moving types from `history-service/internal/models/` to `pkg/model/` means history-service imports change. This is a pure refactor with no logic change.
- **Search index event shape = full Message**: The search consumer gets the complete message. Index field selection is the search-indexer's concern, not message-worker's.
- **Deduplication**: `WithMsgID(msg.ID)` on the search index publish prevents duplicate index writes on retries.
- **Atomicity**: If Cassandra write succeeds but search publish fails, the message is NAK'd and retried. Cassandra INSERT is idempotent (same primary key overwrites), so re-persist is safe.
