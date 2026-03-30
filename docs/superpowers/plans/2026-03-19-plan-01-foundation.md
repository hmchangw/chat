# Plan 1: Foundation & Shared Packages

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create all shared `pkg/` packages, update `go.mod`, and remove old monolith files. This plan has zero external service dependencies — all packages are unit-testable in isolation.

**Tech Stack:** Go 1.25, NATS `github.com/nats-io/nats.go`, MongoDB `go.mongodb.org/mongo-driver/v2`, Cassandra `github.com/gocql/gocql`, OpenTelemetry, Prometheus, UUID

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Modify | `go.mod` | Update to Go 1.25, add all dependencies |
| Delete | `main.go`, `handler/`, `model/`, `store/`, `server/` | Remove old monolith |
| Create | `pkg/model/user.go` | `User` struct |
| Create | `pkg/model/room.go` | `Room`, `RoomType`, `CreateRoomRequest`, `ListRoomsResponse` |
| Create | `pkg/model/message.go` | `Message`, `SendMessageRequest` |
| Create | `pkg/model/subscription.go` | `Subscription`, `Role` |
| Create | `pkg/model/event.go` | `MessageEvent`, `RoomMetadataUpdateEvent`, `SubscriptionUpdateEvent`, `InviteMemberRequest`, `NotificationEvent`, `OutboxEvent` |
| Create | `pkg/model/history.go` | `HistoryRequest`, `HistoryResponse` |
| Create | `pkg/model/error.go` | `ErrorResponse` |
| Create | `pkg/model/model_test.go` | JSON round-trip tests for all types |
| Create | `pkg/subject/subject.go` | All NATS subject builder functions |
| Create | `pkg/subject/subject_test.go` | Tests for each builder |
| Create | `pkg/natsutil/reply.go` | `ReplyJSON`, `ReplyError`, `MarshalResponse`, `MarshalError` |
| Create | `pkg/natsutil/carrier.go` | `HeaderCarrier` — OTel `TextMapCarrier` for NATS headers |
| Create | `pkg/natsutil/reply_test.go` | Tests for marshal helpers |
| Create | `pkg/natsutil/carrier_test.go` | Tests for HeaderCarrier |
| Create | `pkg/mongoutil/mongo.go` | `Connect`, `Disconnect` |
| Create | `pkg/cassutil/cass.go` | `Connect`, `Close` |
| Create | `pkg/stream/stream.go` | JetStream `StreamConfig` builders |
| Create | `pkg/stream/stream_test.go` | Tests for stream configs |
| Create | `pkg/otelutil/otel.go` | `InitTracer`, `InitMeter` |
| Create | `pkg/shutdown/shutdown.go` | `Wait` — graceful shutdown |
| Create | `pkg/shutdown/shutdown_test.go` | Test shutdown invokes cleanup funcs |

---

### Task 1: Update `go.mod` and remove old monolith

- [ ] **Step 1: Delete old monolith files**

```bash
rm main.go
rm -r handler/ model/ store/ server/
rm -r docs/superpowers/plans/2026-03-19-shared-library.md docs/superpowers/plans/2026-03-19-room-service.md docs/superpowers/plans/2026-03-19-message-service.md
```

- [ ] **Step 2: Update `go.mod`**

Replace `go.mod` with:

```
module github.com/hmchangw/chat

go 1.25

require (
	github.com/gocql/gocql v1.7.0
	github.com/google/uuid v1.6.0
	github.com/nats-io/nats.go v1.41.1
	github.com/prometheus/client_golang v1.22.0
	github.com/synadia-io/callout.go v0.2.1
	go.mongodb.org/mongo-driver/v2 v2.1.0
	go.opentelemetry.io/otel v1.35.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.35.0
	go.opentelemetry.io/otel/exporters/prometheus v0.57.0
	go.opentelemetry.io/otel/sdk v1.35.0
	go.opentelemetry.io/otel/sdk/metric v1.35.0
)
```

- [ ] **Step 3: Run `go mod tidy`** to resolve indirect deps and generate `go.sum`

- [ ] **Step 4: Commit**

```bash
git add -A && git commit -m "chore: remove monolith, update go.mod for distributed architecture"
```

---

### Task 2: `pkg/model/` — Domain models

- [ ] **Step 1: Write tests** — `pkg/model/model_test.go`

```go
package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Name: "alice", SiteID: "site-a"}
	roundTrip(t, &u, &model.User{})
}

func TestRoomJSON(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}

func TestMessageJSON(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &m, &model.Message{})
}

func TestSubscriptionJSON(t *testing.T) {
	s := model.Subscription{
		ID: "s1", UserID: "u1", RoomID: "r1", SiteID: "site-a",
		Role:               model.RoleOwner,
		HistorySharedSince: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &s, &model.Subscription{})
}

func TestRoomTypeValues(t *testing.T) {
	if model.RoomTypeGroup != "group" {
		t.Errorf("RoomTypeGroup = %q", model.RoomTypeGroup)
	}
	if model.RoomTypeDM != "dm" {
		t.Errorf("RoomTypeDM = %q", model.RoomTypeDM)
	}
}

func TestRoleValues(t *testing.T) {
	if model.RoleOwner != "owner" {
		t.Errorf("RoleOwner = %q", model.RoleOwner)
	}
	if model.RoleMember != "member" {
		t.Errorf("RoleMember = %q", model.RoleMember)
	}
}

// roundTrip marshals src to JSON, unmarshals into dst, and compares.
func roundTrip[T comparable](t *testing.T, src *T, dst *T) {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *dst != *src {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL** (types not defined)

```bash
go test ./pkg/model/ -v
```

- [ ] **Step 3: Create `pkg/model/user.go`**

```go
package model

type User struct {
	ID     string `json:"id" bson:"_id"`
	Name   string `json:"name" bson:"name"`
	SiteID string `json:"siteId" bson:"siteId"`
}
```

- [ ] **Step 4: Create `pkg/model/room.go`**

```go
package model

import "time"

type RoomType string

const (
	RoomTypeGroup RoomType = "group"
	RoomTypeDM    RoomType = "dm"
)

type Room struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Type      RoomType  `json:"type" bson:"type"`
	CreatedBy string    `json:"createdBy" bson:"createdBy"`
	SiteID    string    `json:"siteId" bson:"siteId"`
	UserCount int       `json:"userCount" bson:"userCount"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}

type CreateRoomRequest struct {
	Name      string   `json:"name"`
	Type      RoomType `json:"type"`
	CreatedBy string   `json:"createdBy"`
	SiteID    string   `json:"siteId"`
	Members   []string `json:"members,omitempty"`
}

type ListRoomsResponse struct {
	Rooms []Room `json:"rooms"`
}
```

- [ ] **Step 5: Create `pkg/model/message.go`**

```go
package model

import "time"

type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"roomId"`
	UserID    string    `json:"userId"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type SendMessageRequest struct {
	RoomID    string `json:"roomId"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
```

- [ ] **Step 6: Create `pkg/model/subscription.go`**

```go
package model

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

type Subscription struct {
	ID                 string    `json:"id" bson:"_id"`
	UserID             string    `json:"userId" bson:"userId"`
	RoomID             string    `json:"roomId" bson:"roomId"`
	SiteID             string    `json:"siteId" bson:"siteId"`
	Role               Role      `json:"role" bson:"role"`
	HistorySharedSince time.Time `json:"historySharedSince" bson:"historySharedSince"`
	JoinedAt           time.Time `json:"joinedAt" bson:"joinedAt"`
}
```

- [ ] **Step 7: Create `pkg/model/event.go`**

```go
package model

import "time"

type MessageEvent struct {
	Message Message `json:"message"`
	RoomID  string  `json:"roomId"`
	SiteID  string  `json:"siteId"`
}

type RoomMetadataUpdateEvent struct {
	RoomID        string    `json:"roomId"`
	Name          string    `json:"name"`
	UserCount     int       `json:"userCount"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SubscriptionUpdateEvent struct {
	UserID       string       `json:"userId"`
	Subscription Subscription `json:"subscription"`
	Action       string       `json:"action"` // "added" | "removed"
}

type InviteMemberRequest struct {
	InviterID string `json:"inviterId"`
	InviteeID string `json:"inviteeId"`
	RoomID    string `json:"roomId"`
	SiteID    string `json:"siteId"`
}

type NotificationEvent struct {
	Type    string  `json:"type"` // "new_message"
	RoomID  string  `json:"roomId"`
	Message Message `json:"message"`
}

type OutboxEvent struct {
	Type       string `json:"type"` // "member_added", "room_sync"
	SiteID     string `json:"siteId"`
	DestSiteID string `json:"destSiteId"`
	Payload    []byte `json:"payload"` // JSON-encoded inner event
}
```

- [ ] **Step 8: Create `pkg/model/history.go`**

```go
package model

type HistoryRequest struct {
	RoomID string `json:"roomId"`
	Before string `json:"before,omitempty"` // cursor: message ID
	Limit  int    `json:"limit,omitempty"`
}

type HistoryResponse struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
}
```

- [ ] **Step 9: Create `pkg/model/error.go`**

```go
package model

type ErrorResponse struct {
	Error string `json:"error"`
}
```

- [ ] **Step 10: Run tests — expect PASS**

```bash
go test ./pkg/model/ -v
```

- [ ] **Step 11: Commit**

```bash
git add pkg/model/ && git commit -m "feat(pkg/model): add domain models for distributed chat"
```

---

### Task 3: `pkg/subject/` — NATS subject builders

- [ ] **Step 1: Write tests** — `pkg/subject/subject_test.go`

```go
package subject_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/subject"
)

func TestSubjectBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MsgSend", subject.MsgSend("u1", "r1", "site-a"),
			"chat.user.u1.room.r1.site-a.msg.send"},
		{"UserResponse", subject.UserResponse("u1", "req-1"),
			"chat.user.u1.response.req-1"},
		{"RoomMetadataUpdate", subject.RoomMetadataUpdate("r1"),
			"chat.room.r1.event.metadata.update"},
		{"RoomMsgStream", subject.RoomMsgStream("r1"),
			"chat.room.r1.stream.msg"},
		{"UserRoomUpdate", subject.UserRoomUpdate("u1"),
			"chat.user.u1.event.room.update"},
		{"UserMsgStream", subject.UserMsgStream("u1"),
			"chat.user.u1.stream.msg"},
		{"MemberInvite", subject.MemberInvite("u1", "r1", "site-a"),
			"chat.user.u1.request.room.r1.site-a.member.invite"},
		{"MsgHistory", subject.MsgHistory("u1", "r1", "site-a"),
			"chat.user.u1.request.room.r1.site-a.msg.history"},
		{"SubscriptionUpdate", subject.SubscriptionUpdate("u1"),
			"chat.user.u1.event.subscription.update"},
		{"RoomMetadataChanged", subject.RoomMetadataChanged("u1"),
			"chat.user.u1.event.room.metadata.update"},
		{"Notification", subject.Notification("u1"),
			"chat.user.u1.notification"},
		{"Outbox", subject.Outbox("site-a", "site-b", "member_added"),
			"outbox.site-a.to.site-b.member_added"},
		{"Fanout", subject.Fanout("site-a", "r1", "m1"),
			"fanout.site-a.r1.m1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestWildcardPatterns(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MsgSendWild", subject.MsgSendWildcard("site-a"),
			"chat.user.*.room.*.site-a.msg.send"},
		{"MemberInviteWild", subject.MemberInviteWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.member.>"},
		{"MsgHistoryWild", subject.MsgHistoryWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.msg.history"},
		{"FanoutWild", subject.FanoutWildcard("site-a"),
			"fanout.site-a.>"},
		{"OutboxWild", subject.OutboxWildcard("site-a"),
			"outbox.site-a.>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `pkg/subject/subject.go`**

```go
package subject

import "fmt"

// --- Specific subject builders ---

func MsgSend(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.room.%s.%s.msg.send", userID, roomID, siteID)
}

func UserResponse(userID, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", userID, requestID)
}

func RoomMetadataUpdate(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.metadata.update", roomID)
}

func RoomMsgStream(roomID string) string {
	return fmt.Sprintf("chat.room.%s.stream.msg", roomID)
}

func UserRoomUpdate(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.room.update", userID)
}

func UserMsgStream(userID string) string {
	return fmt.Sprintf("chat.user.%s.stream.msg", userID)
}

func MemberInvite(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.invite", userID, roomID, siteID)
}

func MsgHistory(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", userID, roomID, siteID)
}

func SubscriptionUpdate(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.subscription.update", userID)
}

func RoomMetadataChanged(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.room.metadata.update", userID)
}

func Notification(userID string) string {
	return fmt.Sprintf("chat.user.%s.notification", userID)
}

func Outbox(siteID, destSiteID, eventType string) string {
	return fmt.Sprintf("outbox.%s.to.%s.%s", siteID, destSiteID, eventType)
}

func Fanout(siteID, roomID, msgID string) string {
	return fmt.Sprintf("fanout.%s.%s.%s", siteID, roomID, msgID)
}

// --- Wildcard patterns for subscriptions ---

func MsgSendWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.room.*.%s.msg.send", siteID)
}

func MemberInviteWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.>", siteID)
}

func MsgHistoryWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.history", siteID)
}

func FanoutWildcard(siteID string) string {
	return fmt.Sprintf("fanout.%s.>", siteID)
}

func OutboxWildcard(siteID string) string {
	return fmt.Sprintf("outbox.%s.>", siteID)
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/ && git commit -m "feat(pkg/subject): add NATS subject builder functions"
```

---

### Task 4: `pkg/natsutil/` — Reply helpers & OTel carrier

- [ ] **Step 1: Write tests** — `pkg/natsutil/reply_test.go`

```go
package natsutil_test

import (
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestMarshalResponse(t *testing.T) {
	room := model.Room{ID: "1", Name: "general"}
	data, err := natsutil.MarshalResponse(room)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got model.Room
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "1" || got.Name != "general" {
		t.Errorf("got %+v", got)
	}
}

func TestMarshalError(t *testing.T) {
	data := natsutil.MarshalError("something went wrong")
	var got model.ErrorResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != "something went wrong" {
		t.Errorf("got %q", got.Error)
	}
}
```

- [ ] **Step 2: Write tests** — `pkg/natsutil/carrier_test.go`

```go
package natsutil_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/natsutil"
	"github.com/nats-io/nats.go"
)

func TestHeaderCarrier(t *testing.T) {
	hdr := nats.Header{}
	c := natsutil.NewHeaderCarrier(&hdr)

	c.Set("traceparent", "00-abc-def-01")
	if got := c.Get("traceparent"); got != "00-abc-def-01" {
		t.Errorf("Get = %q", got)
	}

	keys := c.Keys()
	if len(keys) != 1 || keys[0] != "traceparent" {
		t.Errorf("Keys = %v", keys)
	}
}
```

- [ ] **Step 3: Run tests — expect FAIL**

- [ ] **Step 4: Create `pkg/natsutil/reply.go`**

```go
package natsutil

import (
	"encoding/json"
	"log"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/nats-io/nats.go"
)

func MarshalResponse(v any) ([]byte, error) {
	return json.Marshal(v)
}

func MarshalError(errMsg string) []byte {
	data, _ := json.Marshal(model.ErrorResponse{Error: errMsg})
	return data
}

func ReplyJSON(msg *nats.Msg, v any) {
	data, err := MarshalResponse(v)
	if err != nil {
		ReplyError(msg, "marshal error: "+err.Error())
		return
	}
	if err := msg.Respond(data); err != nil {
		log.Printf("reply failed: %v", err)
	}
}

func ReplyError(msg *nats.Msg, errMsg string) {
	if err := msg.Respond(MarshalError(errMsg)); err != nil {
		log.Printf("error reply failed: %v", err)
	}
}
```

- [ ] **Step 5: Create `pkg/natsutil/carrier.go`**

```go
package natsutil

import "github.com/nats-io/nats.go"

// HeaderCarrier implements propagation.TextMapCarrier for NATS headers.
type HeaderCarrier struct {
	hdr *nats.Header
}

func NewHeaderCarrier(hdr *nats.Header) *HeaderCarrier {
	return &HeaderCarrier{hdr: hdr}
}

func (c *HeaderCarrier) Get(key string) string {
	return c.hdr.Get(key)
}

func (c *HeaderCarrier) Set(key, value string) {
	c.hdr.Set(key, value)
}

func (c *HeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(*c.hdr))
	for k := range *c.hdr {
		keys = append(keys, k)
	}
	return keys
}
```

- [ ] **Step 6: Run tests — expect PASS**

- [ ] **Step 7: Commit**

```bash
git add pkg/natsutil/ && git commit -m "feat(pkg/natsutil): add NATS reply helpers and OTel header carrier"
```

---

### Task 5: `pkg/mongoutil/` — MongoDB connect wrapper

- [ ] **Step 1: Create `pkg/mongoutil/mongo.go`** (no unit test — integration dependency)

```go
package mongoutil

import (
	"context"
	"fmt"
	"log"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	log.Printf("connected to MongoDB at %s", uri)
	return client, nil
}

func Disconnect(ctx context.Context, client *mongo.Client) {
	if err := client.Disconnect(ctx); err != nil {
		log.Printf("mongo disconnect: %v", err)
	}
}
```

- [ ] **Step 2: Verify compiles** — `go build ./pkg/mongoutil/`

- [ ] **Step 3: Commit**

```bash
git add pkg/mongoutil/ && git commit -m "feat(pkg/mongoutil): add MongoDB connect/disconnect wrapper"
```

---

### Task 6: `pkg/cassutil/` — Cassandra connect wrapper

- [ ] **Step 1: Create `pkg/cassutil/cass.go`** (no unit test — integration dependency)

```go
package cassutil

import (
	"fmt"
	"log"
	"time"

	"github.com/gocql/gocql"
)

func Connect(hosts []string, keyspace string) (*gocql.Session, error) {
	cluster := gocql.NewCluster(hosts...)
	cluster.Keyspace = keyspace
	cluster.Consistency = gocql.LocalQuorum
	cluster.Timeout = 10 * time.Second

	session, err := cluster.CreateSession()
	if err != nil {
		return nil, fmt.Errorf("cassandra connect: %w", err)
	}
	log.Printf("connected to Cassandra keyspace %q", keyspace)
	return session, nil
}

func Close(session *gocql.Session) {
	session.Close()
}
```

- [ ] **Step 2: Verify compiles** — `go build ./pkg/cassutil/`

- [ ] **Step 3: Commit**

```bash
git add pkg/cassutil/ && git commit -m "feat(pkg/cassutil): add Cassandra connect/close wrapper"
```

---

### Task 7: `pkg/stream/` — JetStream stream configs

- [ ] **Step 1: Write tests** — `pkg/stream/stream_test.go`

```go
package stream_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/stream"
)

func TestStreamConfigs(t *testing.T) {
	siteID := "site-a"

	tests := []struct {
		name     string
		cfg      stream.Config
		wantName string
		wantSubj string
	}{
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"Fanout", stream.Fanout(siteID), "FANOUT_site-a", "fanout.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.user.*.request.room.*.site-a.member.>"},
		{"Outbox", stream.Outbox(siteID), "OUTBOX_site-a", "outbox.site-a.>"},
		{"Inbox", stream.Inbox(siteID), "INBOX_site-a", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.cfg.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.cfg.Name, tt.wantName)
			}
			if tt.wantSubj != "" {
				if len(tt.cfg.Subjects) == 0 || tt.cfg.Subjects[0] != tt.wantSubj {
					t.Errorf("Subjects = %v, want [%q]", tt.cfg.Subjects, tt.wantSubj)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

- [ ] **Step 3: Create `pkg/stream/stream.go`**

```go
package stream

import "fmt"

// Config holds the JetStream stream configuration parameters.
type Config struct {
	Name     string
	Subjects []string
}

func Messages(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("MESSAGES_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.user.*.room.*.%s.msg.>", siteID)},
	}
}

func Fanout(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("FANOUT_%s", siteID),
		Subjects: []string{fmt.Sprintf("fanout.%s.>", siteID)},
	}
}

func Rooms(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("ROOMS_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.user.*.request.room.*.%s.member.>", siteID)},
	}
}

func Outbox(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("OUTBOX_%s", siteID),
		Subjects: []string{fmt.Sprintf("outbox.%s.>", siteID)},
	}
}

// Inbox uses JetStream Sources from other sites' OUTBOX streams (no local subjects).
func Inbox(siteID string) Config {
	return Config{
		Name: fmt.Sprintf("INBOX_%s", siteID),
	}
}
```

- [ ] **Step 4: Run tests — expect PASS**

- [ ] **Step 5: Commit**

```bash
git add pkg/stream/ && git commit -m "feat(pkg/stream): add JetStream stream config builders"
```

---

### Task 8: `pkg/otelutil/` — OpenTelemetry init

- [ ] **Step 1: Create `pkg/otelutil/otel.go`** (no unit test — OTel setup is integration-level)

```go
package otelutil

import (
	"context"
	"fmt"

	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer creates and registers a TracerProvider with OTLP gRPC exporter.
// Returns a shutdown function.
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp),
		trace.WithResource(res),
	)

	return tp.Shutdown, nil
}

// InitMeter creates a MeterProvider with Prometheus exporter.
// Returns a shutdown function.
func InitMeter(serviceName string) (func(context.Context) error, error) {
	exp, err := promexporter.New()
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}

	mp := metric.NewMeterProvider(metric.WithReader(exp))
	return mp.Shutdown, nil
}
```

- [ ] **Step 2: Verify compiles** — `go build ./pkg/otelutil/`

- [ ] **Step 3: Commit**

```bash
git add pkg/otelutil/ && git commit -m "feat(pkg/otelutil): add OTel tracer and Prometheus meter init"
```

---

### Task 9: `pkg/shutdown/` — Graceful shutdown handler

- [ ] **Step 1: Write test** — `pkg/shutdown/shutdown_test.go`

```go
package shutdown_test

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/shutdown"
)

func TestWaitCallsCleanup(t *testing.T) {
	called := false
	cleanup := func(ctx context.Context) error {
		called = true
		return nil
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		syscall.Kill(syscall.Getpid(), syscall.SIGINT)
	}()

	shutdown.Wait(context.Background(), cleanup)

	if !called {
		t.Error("cleanup function was not called")
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

- [ ] **Step 3: Create `pkg/shutdown/shutdown.go`**

```go
package shutdown

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// Wait blocks until SIGINT or SIGTERM, then calls each shutdown function.
func Wait(ctx context.Context, shutdownFuncs ...func(context.Context) error) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")

	for _, fn := range shutdownFuncs {
		if err := fn(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}
}
```

- [ ] **Step 4: Run test — expect PASS**

- [ ] **Step 5: Run full pkg test suite**

```bash
go test ./pkg/... -v
```

- [ ] **Step 6: Commit**

```bash
git add pkg/shutdown/ && git commit -m "feat(pkg/shutdown): add graceful shutdown signal handler"
```
