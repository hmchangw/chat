# Message-Worker Positioning — Implementation Plan

## Overview

Update message-worker to match its position in the pipeline: persist full-schema messages to Cassandra (`messages_by_room` with 22 columns and UDT types). Promote shared UDT types to `pkg/model/` so both message-worker and history-service use the same definitions.

Search indexing is handled by a separate future `search-indexer` service consuming directly from MESSAGES_CANONICAL — see `PROPOSAL-search-indexing.md`.

---

## Worklist

| # | Task | Files |
|---|------|-------|
| 1 | Move UDT types (`Participant`, `File`, `Card`, `CardAction`) from `history-service/internal/models/` to `pkg/model/` | `pkg/model/types.go`, `pkg/model/utils.go` |
| 2 | Expand `model.Message` to full 22-field schema | `pkg/model/message.go` |
| 3 | Update `model.MessageEvent` if needed | `pkg/model/event.go` |
| 4 | Update `Store` interface — `SaveMessage` accepts expanded `model.Message` | `message-worker/store.go` |
| 5 | Rewrite `CassandraStore.SaveMessage` — insert all 22 columns into `messages_by_room` | `message-worker/store_cassandra.go` |
| 6 | Write handler unit tests (TDD red-green) | `message-worker/handler_test.go` |
| 7 | Regenerate mocks | `message-worker/mock_store_test.go` |
| 8 | Update integration test — new schema with UDTs, `messages_by_room` table | `message-worker/integration_test.go` |
| 9 | Update `history-service` to import UDTs from `pkg/model/` instead of local `models` | `history-service/internal/models/types.go`, `history-service/internal/cassrepo/` |
| 10 | Lint, test, verify | `make lint && make test SERVICE=message-worker` |

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

### `message-worker/handler.go` — persist only
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

### `message-worker/handler.go` — persist only (unchanged responsibility)
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

> Handler signature stays the same. The expanded `model.Message` flows through transparently.

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
    ├──► message-worker       (durable: "message-worker")       ──► Cassandra (messages_by_room)
    │                                                                    │
    │                                                                    ▼
    │                                                               history-service (reads)
    ├──► broadcast-worker     (durable: "broadcast-worker")     ──► room delivery
    ├──► notification-worker  (durable: "notification-worker")  ──► push notifications
    └──► search-indexer       (durable: "search-indexer")       ──► ES / OpenSearch (future)
```

---

## Risk & Notes

- **Breaking change to `model.Message`**: `UserID`/`Username` replaced by `Sender` (`Participant`). All consumers of `model.Message` (message-gatekeeper, broadcast-worker, notification-worker) will need updates. We handle message-worker first; other services follow.
- **UDT relocation**: Moving types from `history-service/internal/models/` to `pkg/model/` means history-service imports change. This is a pure refactor with no logic change.
- **Cassandra idempotency**: INSERT with the same primary key `(room_id, created_at, message_id)` overwrites — safe for retries after NAK.
