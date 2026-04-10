# message-worker: MongoDB User Lookup & Cassandra Schema Update — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enrich message-worker sender with user display names from MongoDB and persist to the correct Cassandra tables (`messages_by_room`, `messages_by_id`) with proper UDT sender data.

**Architecture:** Three layers of change in dependency order — (1) update shared User model and create `pkg/userstore`, (2) update message-worker store interfaces, handler, and Cassandra writes, (3) wire MongoDB in main.go. All new code follows TDD.

**Tech Stack:** Go 1.25, MongoDB (`go.mongodb.org/mongo-driver/v2`), Cassandra (`gocql`), NATS JetStream, `go.uber.org/mock`, `testcontainers-go` (mongodb + cassandra modules).

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/model/user.go` | Remove `Name`; add `EngName`, `ChineseName`, `EmployeeID` |
| Modify | `pkg/model/model_test.go` | Update `TestUserJSON` for new fields |
| Create | `pkg/userstore/userstore.go` | `mongoStore` with `FindUserByID` + `FindUsersByAccounts` |
| Create | `pkg/userstore/integration_test.go` | Integration tests for both methods |
| Modify | `message-worker/store.go` | Add `UserStore` interface; update `Store.SaveMessage` signature |
| Modify | `message-worker/store_cassandra.go` | `cassParticipant` UDT struct + two-table `SaveMessage` |
| Modify | `message-worker/handler.go` | Inject `UserStore`; lookup user in `processMessage` |
| Modify | `message-worker/handler_test.go` | Table-driven tests covering 5 scenarios |
| Modify | `message-worker/integration_test.go` | Add MongoDB container; update Cassandra schema |
| Modify | `message-worker/main.go` | Add MongoDB config, connect, wire, shutdown |
| Regenerate | `message-worker/mock_store_test.go` | Auto-generated — never edit manually |

---

## Task 1: Update User Model

**Files:**
- Modify: `pkg/model/user.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Update `TestUserJSON` to use new fields (Red)**

In `pkg/model/model_test.go`, replace the `TestUserJSON` function:

```go
func TestUserJSON(t *testing.T) {
	u := model.User{
		ID:          "u1",
		Account:     "alice",
		SiteID:      "site-a",
		EngName:     "Alice Wang",
		ChineseName: "愛麗絲",
		EmployeeID:  "EMP001",
	}
	roundTrip(t, &u, &model.User{})
}
```

- [ ] **Step 2: Run test — confirm it fails**

```bash
make test SERVICE=pkg/model
```

Expected: FAIL — `unknown field 'EngName'` or similar compile error because `User` still has `Name`.

- [ ] **Step 3: Update `pkg/model/user.go`**

Replace the entire file:

```go
package model

type User struct {
	ID          string `json:"id"           bson:"_id"`
	Account     string `json:"account"      bson:"account"`
	SiteID      string `json:"siteId"       bson:"siteId"`
	EngName     string `json:"engName"      bson:"engName"`
	ChineseName string `json:"chineseName"  bson:"chineseName"`
	EmployeeID  string `json:"employeeId"   bson:"employeeId"`
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
make test SERVICE=pkg/model
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/user.go pkg/model/model_test.go
git commit -m "feat(model): replace Name with EngName, ChineseName, EmployeeID on User"
```

---

## Task 2: Create `pkg/userstore`

**Files:**
- Create: `pkg/userstore/userstore.go`
- Create: `pkg/userstore/integration_test.go`

- [ ] **Step 1: Write failing integration tests**

Create `pkg/userstore/integration_test.go`:

```go
//go:build integration

package userstore

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func setupMongo(t *testing.T) *mongo.Collection {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { client.Disconnect(ctx) })

	return client.Database("userstore_test").Collection("users")
}

func TestMongoStore_FindUserByID(t *testing.T) {
	col := setupMongo(t)
	store := NewMongoStore(col)
	ctx := context.Background()

	_, err := col.InsertOne(ctx, bson.M{
		"_id":         "u-1",
		"account":     "alice",
		"siteId":      "site-a",
		"engName":     "Alice Wang",
		"chineseName": "愛麗絲",
		"employeeId":  "EMP001",
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		id        string
		wantErr   bool
		wantNotFound bool
		wantAccount  string
	}{
		{
			name:        "found",
			id:          "u-1",
			wantAccount: "alice",
		},
		{
			name:         "not found",
			id:           "nonexistent",
			wantErr:      true,
			wantNotFound: true,
		},
		{
			name:         "empty id returns not found",
			id:           "",
			wantErr:      true,
			wantNotFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.FindUserByID(ctx, tt.id)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantNotFound {
					assert.True(t, errors.Is(err, ErrUserNotFound), "expected ErrUserNotFound, got: %v", err)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantAccount, got.Account)
			assert.Equal(t, "Alice Wang", got.EngName)
			assert.Equal(t, "愛麗絲", got.ChineseName)
		})
	}
}

func TestMongoStore_FindUsersByAccounts(t *testing.T) {
	col := setupMongo(t)
	store := NewMongoStore(col)
	ctx := context.Background()

	_, err := col.InsertMany(ctx, []any{
		bson.M{"_id": "u-1", "account": "alice", "siteId": "site-a", "engName": "Alice Wang", "chineseName": "愛麗絲", "employeeId": "EMP001"},
		bson.M{"_id": "u-2", "account": "bob", "siteId": "site-a", "engName": "Bob Chen", "chineseName": "鮑勃", "employeeId": "EMP002"},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		accounts  []string
		wantCount int
	}{
		{name: "all found", accounts: []string{"alice", "bob"}, wantCount: 2},
		{name: "partial match", accounts: []string{"alice", "nobody"}, wantCount: 1},
		{name: "empty slice", accounts: []string{}, wantCount: 0},
		{name: "no match", accounts: []string{"nobody"}, wantCount: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.FindUsersByAccounts(ctx, tt.accounts)
			require.NoError(t, err)
			assert.Len(t, got, tt.wantCount)
		})
	}
}
```

- [ ] **Step 2: Run integration tests — confirm they fail**

```bash
make test-integration SERVICE=pkg/userstore
```

Expected: FAIL — package `userstore` does not exist.

- [ ] **Step 3: Implement `pkg/userstore/userstore.go`**

Create `pkg/userstore/userstore.go`:

```go
package userstore

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// ErrUserNotFound is returned by FindUserByID when no user matches the given ID.
var ErrUserNotFound = errors.New("user not found")

type mongoStore struct {
	col *mongo.Collection
}

// NewMongoStore returns a new MongoDB-backed user store using col as the users collection.
func NewMongoStore(col *mongo.Collection) *mongoStore {
	return &mongoStore{col: col}
}

// FindUserByID returns the user with the given ID.
// Returns ErrUserNotFound (wrapped) if no document matches.
func (s *mongoStore) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	var u model.User
	if err := s.col.FindOne(ctx, bson.M{"_id": id}).Decode(&u); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("find user %s: %w", id, ErrUserNotFound)
		}
		return nil, fmt.Errorf("find user %s: %w", id, err)
	}
	return &u, nil
}

// FindUsersByAccounts returns all users whose account field is in accounts.
func (s *mongoStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	cursor, err := s.col.Find(ctx, bson.M{"account": bson.M{"$in": accounts}})
	if err != nil {
		return nil, fmt.Errorf("find users by accounts: %w", err)
	}
	defer cursor.Close(ctx)
	var users []model.User
	if err := cursor.All(ctx, &users); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return users, nil
}
```

- [ ] **Step 4: Run integration tests — confirm they pass**

```bash
make test-integration SERVICE=pkg/userstore
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/userstore/
git commit -m "feat(userstore): add shared MongoDB user store with FindUserByID and FindUsersByAccounts"
```

---

## Task 3: Update `message-worker` Store Interfaces + `cassParticipant` Struct

**Files:**
- Modify: `message-worker/store.go`
- Modify: `message-worker/store_cassandra.go`
- Regenerate: `message-worker/mock_store_test.go`

- [ ] **Step 1: Rewrite `message-worker/store.go`**

```go
package main

import (
	"context"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -destination=mock_store_test.go -package=main . Store,UserStore

// Store defines Cassandra persistence operations for the message worker.
type Store interface {
	SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant, siteID string) error
}

// UserStore defines MongoDB user lookup operations for the message worker.
type UserStore interface {
	FindUserByID(ctx context.Context, id string) (*model.User, error)
}
```

- [ ] **Step 2: Rewrite `message-worker/store_cassandra.go`**

Replace the entire file with the `cassParticipant` struct (with `MarshalUDT`) and a stub `SaveMessage` that will be completed in Task 5:

```go
package main

import (
	"context"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// cassParticipant maps to the Cassandra "Participant" UDT.
type cassParticipant struct {
	ID          string
	EngName     string
	CompanyName string // ChineseName
	Account     string
	AppID       string
	AppName     string
	IsBot       bool
}

// MarshalUDT implements gocql.UDTMarshaler for cassParticipant.
func (p *cassParticipant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "account":
		return gocql.Marshal(info, p.Account)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	default:
		return nil, nil
	}
}

// CassandraStore implements Store using a Cassandra session.
type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{cassSession: session}
}

// SaveMessage inserts the message into messages_by_room and messages_by_id.
// Implementation completed in Task 5.
func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant, siteID string) error {
	return nil
}
```

- [ ] **Step 3: Regenerate mocks**

```bash
make generate SERVICE=message-worker
```

Expected: `message-worker/mock_store_test.go` regenerated with `MockStore` (SaveMessage takes `cassParticipant, siteID string`) and `MockUserStore` (FindUserByID).

- [ ] **Step 4: Verify compilation**

```bash
make build SERVICE=message-worker
```

Expected: builds successfully (handler.go still has old Store dependency — it won't compile yet). If it fails on handler.go, that's expected and will be fixed in Task 4.

> Note: `handler.go` currently constructs `NewHandler(store)` with one arg. After Task 4 it will take two args. If compilation fails here due to handler.go, proceed to Task 4 immediately.

- [ ] **Step 5: Commit**

```bash
git add message-worker/store.go message-worker/store_cassandra.go message-worker/mock_store_test.go
git commit -m "feat(message-worker): add UserStore interface, cassParticipant UDT, regenerate mocks"
```

---

## Task 4: TDD — Handler User Lookup

**Files:**
- Modify: `message-worker/handler_test.go`
- Modify: `message-worker/handler.go`

- [ ] **Step 1: Rewrite `handler_test.go` with table-driven tests (Red)**

Replace the entire file:

```go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

func TestHandler_ProcessMessage(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	user := &model.User{
		ID:          "u-1",
		Account:     "alice",
		SiteID:      "site-a",
		EngName:     "Alice Wang",
		ChineseName: "愛麗絲",
	}
	msg := model.Message{
		ID:          "msg-1",
		RoomID:      "r1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   now,
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a", Timestamp: now.UnixMilli()}
	validData, _ := json.Marshal(evt)

	expectedSender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     msg.UserAccount,
	}

	tests := []struct {
		name          string
		data          []byte
		setupMocks    func(store *MockStore, userStore *MockUserStore)
		wantErr       bool
	}{
		{
			name: "happy path — user found and message saved",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), msg, expectedSender, "site-a").Return(nil)
			},
		},
		{
			name: "user not found — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, fmt.Errorf("find user u-1: %w", userstore.ErrUserNotFound))
			},
			wantErr: true,
		},
		{
			name: "user store DB error — NAK without saving",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").
					Return(nil, errors.New("mongo: connection refused"))
			},
			wantErr: true,
		},
		{
			name: "save error — NAK after user lookup",
			data: validData,
			setupMocks: func(store *MockStore, us *MockUserStore) {
				us.EXPECT().FindUserByID(gomock.Any(), "u-1").Return(user, nil)
				store.EXPECT().SaveMessage(gomock.Any(), msg, expectedSender, "site-a").
					Return(errors.New("cassandra: write timeout"))
			},
			wantErr: true,
		},
		{
			name:       "malformed JSON — NAK immediately",
			data:       []byte("{invalid"),
			setupMocks: func(store *MockStore, us *MockUserStore) {},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockStore := NewMockStore(ctrl)
			mockUserStore := NewMockUserStore(ctrl)
			tt.setupMocks(mockStore, mockUserStore)

			h := NewHandler(mockStore, mockUserStore)
			err := h.processMessage(context.Background(), tt.data)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — confirm they fail**

```bash
make test SERVICE=message-worker
```

Expected: FAIL — `NewHandler` takes 1 argument, `Handler` has no `userStore` field.

- [ ] **Step 3: Rewrite `handler.go`**

Replace the entire file:

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

type Handler struct {
	store     Store
	userStore UserStore
}

func NewHandler(store Store, userStore UserStore) *Handler {
	return &Handler{store: store, userStore: userStore}
}

// HandleJetStreamMsg processes a JetStream message from the MESSAGES_CANONICAL stream.
func (h *Handler) HandleJetStreamMsg(ctx context.Context, msg jetstream.Msg) {
	if err := h.processMessage(ctx, msg.Data()); err != nil {
		slog.Error("process message failed", "error", err)
		if err := msg.Nak(); err != nil {
			slog.Error("failed to nack message", "error", err)
		}
		return
	}
	if err := msg.Ack(); err != nil {
		slog.Error("failed to ack message", "err", err)
	}
}

func (h *Handler) processMessage(ctx context.Context, data []byte) error {
	var evt model.MessageEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		return fmt.Errorf("unmarshal message event: %w", err)
	}

	user, err := h.userStore.FindUserByID(ctx, evt.Message.UserID)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", evt.Message.UserID, err)
	}

	sender := cassParticipant{
		ID:          user.ID,
		EngName:     user.EngName,
		CompanyName: user.ChineseName,
		Account:     evt.Message.UserAccount,
	}

	if err := h.store.SaveMessage(ctx, evt.Message, sender, evt.SiteID); err != nil {
		return fmt.Errorf("save message: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests — confirm they pass**

```bash
make test SERVICE=message-worker
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add message-worker/handler.go message-worker/handler_test.go
git commit -m "feat(message-worker): inject UserStore, lookup sender by user ID before persisting"
```

---

## Task 5: Implement Two-Table Cassandra Insert

**Files:**
- Modify: `message-worker/store_cassandra.go`

- [ ] **Step 1: Replace the `SaveMessage` stub with the real two-table implementation**

Replace the entire `store_cassandra.go` file:

```go
package main

import (
	"context"
	"fmt"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/model"
)

// cassParticipant maps to the Cassandra "Participant" UDT.
type cassParticipant struct {
	ID          string
	EngName     string
	CompanyName string // ChineseName
	Account     string
	AppID       string
	AppName     string
	IsBot       bool
}

// MarshalUDT implements gocql.UDTMarshaler for cassParticipant.
func (p *cassParticipant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	switch name {
	case "id":
		return gocql.Marshal(info, p.ID)
	case "eng_name":
		return gocql.Marshal(info, p.EngName)
	case "company_name":
		return gocql.Marshal(info, p.CompanyName)
	case "account":
		return gocql.Marshal(info, p.Account)
	case "app_id":
		return gocql.Marshal(info, p.AppID)
	case "app_name":
		return gocql.Marshal(info, p.AppName)
	case "is_bot":
		return gocql.Marshal(info, p.IsBot)
	default:
		return nil, nil
	}
}

// CassandraStore implements Store using a Cassandra session.
type CassandraStore struct {
	cassSession *gocql.Session
}

func NewCassandraStore(session *gocql.Session) *CassandraStore {
	return &CassandraStore{cassSession: session}
}

// SaveMessage inserts msg into both messages_by_room and messages_by_id.
// updated_at is set to msg.CreatedAt (equals created_at on first insert — message not yet edited).
// If either insert fails the error is returned immediately; JetStream will redeliver the message.
func (s *CassandraStore) SaveMessage(ctx context.Context, msg model.Message, sender cassParticipant, siteID string) error {
	if err := s.cassSession.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.RoomID, msg.CreatedAt, msg.ID, &sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_room %s: %w", msg.ID, err)
	}

	if err := s.cassSession.Query(
		`INSERT INTO messages_by_id (message_id, created_at, sender, msg, site_id, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.CreatedAt, &sender, msg.Content, siteID, msg.CreatedAt,
	).WithContext(ctx).Exec(); err != nil {
		return fmt.Errorf("insert messages_by_id %s: %w", msg.ID, err)
	}

	return nil
}
```

- [ ] **Step 2: Run unit tests — confirm they still pass**

```bash
make test SERVICE=message-worker
```

Expected: PASS (unit tests use the mock store, not the real cassandra impl)

- [ ] **Step 3: Commit**

```bash
git add message-worker/store_cassandra.go
git commit -m "feat(message-worker): insert into messages_by_room and messages_by_id with Participant UDT"
```

---

## Task 6: Update Integration Tests

**Files:**
- Modify: `message-worker/integration_test.go`

- [ ] **Step 1: Rewrite `integration_test.go`**

Replace the entire file:

```go
//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	stmts := []string{
		`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`,
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN, account TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
			room_id       TEXT,
			created_at    TIMESTAMP,
			message_id    TEXT,
			sender        FROZEN<"Participant">,
			msg           TEXT,
			site_id       TEXT,
			updated_at    TIMESTAMP,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
			message_id TEXT,
			created_at TIMESTAMP,
			sender     FROZEN<"Participant">,
			msg        TEXT,
			site_id    TEXT,
			updated_at TIMESTAMP,
			PRIMARY KEY (message_id, created_at)
		) WITH CLUSTERING ORDER BY (created_at DESC)`,
	}
	for _, stmt := range stmts {
		require.NoError(t, session.Query(stmt).Exec())
	}

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func setupMongoUsers(t *testing.T) *mongo.Collection {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	t.Cleanup(func() { client.Disconnect(ctx) })

	return client.Database("chat_test").Collection("users")
}

func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sender := cassParticipant{
		ID:          "u-1",
		EngName:     "Alice Wang",
		CompanyName: "愛麗絲",
		Account:     "alice",
	}
	msg := model.Message{
		ID:          "m-1",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello world",
		CreatedAt:   now,
	}

	err := store.SaveMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("messages_by_room row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello world", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_id row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello world", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})
}

func TestHandler_Integration(t *testing.T) {
	cassSession := setupCassandra(t)
	userCol := setupMongoUsers(t)
	ctx := context.Background()

	_, err := userCol.InsertOne(ctx, bson.M{
		"_id":         "u-1",
		"account":     "alice",
		"siteId":      "site-a",
		"engName":     "Alice Wang",
		"chineseName": "愛麗絲",
		"employeeId":  "EMP001",
	})
	require.NoError(t, err)

	store := NewCassandraStore(cassSession)
	us := userstore.NewMongoStore(userCol)
	h := NewHandler(store, us)

	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Message: model.Message{
			ID:          "m-2",
			RoomID:      "r-2",
			UserID:      "u-1",
			UserAccount: "alice",
			Content:     "integration test message",
			CreatedAt:   now,
		},
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	err = h.processMessage(ctx, data)
	require.NoError(t, err)

	var gotMsg string
	err = cassSession.Query(
		`SELECT msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		"r-2", now, "m-2",
	).Scan(&gotMsg)
	require.NoError(t, err)
	assert.Equal(t, "integration test message", gotMsg)
}
```

- [ ] **Step 2: Run integration tests — confirm they pass**

```bash
make test-integration SERVICE=message-worker
```

Expected: PASS — both Cassandra tables contain rows, handler integration test passes.

- [ ] **Step 3: Commit**

```bash
git add message-worker/integration_test.go
git commit -m "test(message-worker): update integration tests for two-table Cassandra schema and MongoDB user lookup"
```

---

## Task 7: Wire MongoDB in `main.go`

**Files:**
- Modify: `message-worker/main.go`

- [ ] **Step 1: Replace `main.go`**

Replace the entire file:

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
	"github.com/nats-io/nats.go/jetstream"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"
	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/otelutil"
	"github.com/hmchangw/chat/pkg/shutdown"
	"github.com/hmchangw/chat/pkg/stream"
	"github.com/hmchangw/chat/pkg/userstore"
)

type config struct {
	NatsURL           string `env:"NATS_URL,required"`
	SiteID            string `env:"SITE_ID,required"`
	CassandraHosts    string `env:"CASSANDRA_HOSTS"    envDefault:"localhost"`
	CassandraKeyspace string `env:"CASSANDRA_KEYSPACE" envDefault:"chat"`
	MongoURI          string `env:"MONGO_URI,required"`
	MongoDB           string `env:"MONGO_DB"           envDefault:"chat"`
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

	tracerShutdown, err := otelutil.InitTracer(ctx, "message-worker")
	if err != nil {
		slog.Error("init tracer failed", "error", err)
		os.Exit(1)
	}

	nc, err := otelnats.Connect(cfg.NatsURL)
	if err != nil {
		slog.Error("nats connect failed", "error", err)
		os.Exit(1)
	}
	js, err := oteljetstream.New(nc)
	if err != nil {
		slog.Error("jetstream init failed", "error", err)
		os.Exit(1)
	}

	cassSession, err := cassutil.Connect(strings.Split(cfg.CassandraHosts, ","), cfg.CassandraKeyspace)
	if err != nil {
		slog.Error("cassandra connect failed", "error", err)
		os.Exit(1)
	}

	mongoClient, err := mongoutil.Connect(ctx, cfg.MongoURI)
	if err != nil {
		slog.Error("mongo connect failed", "error", err)
		os.Exit(1)
	}
	db := mongoClient.Database(cfg.MongoDB)

	store := NewCassandraStore(cassSession)
	us := userstore.NewMongoStore(db.Collection("users"))
	handler := NewHandler(store, us)

	canonicalCfg := stream.MessagesCanonical(cfg.SiteID)
	if _, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     canonicalCfg.Name,
		Subjects: canonicalCfg.Subjects,
	}); err != nil {
		slog.Error("create MESSAGES_CANONICAL stream failed", "error", err)
		os.Exit(1)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, canonicalCfg.Name, jetstream.ConsumerConfig{
		Durable:   "message-worker",
		AckPolicy: jetstream.AckExplicitPolicy,
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
			msgCtx, msg, err := iter.Next()
			if err != nil {
				return
			}
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				handler.HandleJetStreamMsg(msgCtx, msg)
			}()
		}
	}()

	slog.Info("message-worker running", "site", cfg.SiteID)

	shutdown.Wait(ctx, 25*time.Second,
		func(ctx context.Context) error {
			iter.Stop()
			return nil
		},
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
		func(ctx context.Context) error { return tracerShutdown(ctx) },
		func(ctx context.Context) error { return nc.Drain() },
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(ctx context.Context) error { cassutil.Close(cassSession); return nil },
	)
}
```

- [ ] **Step 2: Build to verify compilation**

```bash
make build SERVICE=message-worker
```

Expected: builds successfully — `bin/message-worker` created.

- [ ] **Step 3: Run all unit tests**

```bash
make test SERVICE=message-worker
make test SERVICE=pkg/model
make test SERVICE=pkg/userstore
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add message-worker/main.go
git commit -m "feat(message-worker): add MongoDB wiring for user store lookup"
```

---

## Task 8: Push and Final Verification

- [ ] **Step 1: Run full unit test suite**

```bash
make test
```

Expected: PASS — no regressions across the repo.

- [ ] **Step 2: Run integration tests for changed packages**

```bash
make test-integration SERVICE=message-worker
make test-integration SERVICE=pkg/userstore
```

Expected: both PASS.

- [ ] **Step 3: Push**

```bash
git push
```
