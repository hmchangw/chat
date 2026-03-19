# Shared Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract shared models, store interface, and NATS subject constants into a reusable `pkg/` library that both Room Service and Message Service depend on.

**Architecture:** Create a `pkg/` directory at the repo root containing three packages: `pkg/models` (data types and request/response structs), `pkg/subjects` (NATS subject constants and helpers), and `pkg/natsutil` (shared NATS reply helpers). The existing `model/`, `store/` interface, and handler constants move here. Each microservice will import from `pkg/` and provide its own `main.go`, handler, and store implementation.

**Tech Stack:** Go 1.24.7, NATS (`github.com/nats-io/nats.go v1.49.0`)

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `pkg/models/room.go` | `Room` struct, `CreateRoomRequest`, `ListRoomsResponse` |
| Create | `pkg/models/message.go` | `Message` struct, `SendMessageRequest`, `ListMessagesRequest`, `ListMessagesResponse` |
| Create | `pkg/models/user.go` | `User` struct |
| Create | `pkg/models/error.go` | `ErrorResponse` struct |
| Create | `pkg/subjects/subjects.go` | NATS subject constants and `RoomBroadcastSubject()` helper |
| Create | `pkg/natsutil/reply.go` | `ReplyJSON()` and `ReplyError()` helpers |
| Create | `pkg/models/room_test.go` | JSON serialization tests for room types |
| Create | `pkg/models/message_test.go` | JSON serialization tests for message types |
| Create | `pkg/subjects/subjects_test.go` | Subject constant and helper tests |
| Create | `pkg/natsutil/reply_test.go` | Reply helper tests |
| Delete (later) | `model/model.go` | Replaced by `pkg/models/*` |

---

### Task 1: Create `pkg/models/user.go`

**Files:**
- Create: `pkg/models/user.go`

- [ ] **Step 1: Create the user model file**

```go
package models

import "time"

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./pkg/models/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/models/user.go
git commit -m "feat(pkg): add shared User model"
```

---

### Task 2: Create `pkg/models/room.go` with tests

**Files:**
- Create: `pkg/models/room_test.go`
- Create: `pkg/models/room.go`

- [ ] **Step 1: Write the failing test**

```go
package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/models"
)

func TestRoomJSON(t *testing.T) {
	room := models.Room{
		ID:        "1",
		Name:      "general",
		CreatedBy: "user-1",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(room)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got models.Room
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != room {
		t.Errorf("got %+v, want %+v", got, room)
	}
}

func TestCreateRoomRequestJSON(t *testing.T) {
	req := models.CreateRoomRequest{Name: "random", CreatedBy: "user-2"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got models.CreateRoomRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != req {
		t.Errorf("got %+v, want %+v", got, req)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test ./pkg/models/ -run TestRoom -v`
Expected: FAIL — types not defined yet

- [ ] **Step 3: Write the room model**

```go
package models

import "time"

type Room struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateRoomRequest struct {
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
}

type ListRoomsResponse struct {
	Rooms []Room `json:"rooms"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./pkg/models/ -run TestRoom -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/models/room.go pkg/models/room_test.go
git commit -m "feat(pkg): add shared Room model with request/response types"
```

---

### Task 3: Create `pkg/models/message.go` with tests

**Files:**
- Create: `pkg/models/message_test.go`
- Create: `pkg/models/message.go`

- [ ] **Step 1: Write the failing test**

```go
package models_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/models"
)

func TestMessageJSON(t *testing.T) {
	msg := models.Message{
		ID:        "10",
		RoomID:    "1",
		UserID:    "user-1",
		Content:   "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got models.Message
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != msg {
		t.Errorf("got %+v, want %+v", got, msg)
	}
}

func TestSendMessageRequestJSON(t *testing.T) {
	req := models.SendMessageRequest{RoomID: "1", UserID: "user-1", Content: "hi"}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got models.SendMessageRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != req {
		t.Errorf("got %+v, want %+v", got, req)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test ./pkg/models/ -run TestMessage -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Write the message model**

```go
package models

import "time"

type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type SendMessageRequest struct {
	RoomID  string `json:"room_id"`
	UserID  string `json:"user_id"`
	Content string `json:"content"`
}

type ListMessagesRequest struct {
	RoomID string `json:"room_id"`
	Limit  int    `json:"limit,omitempty"`
}

type ListMessagesResponse struct {
	Messages []Message `json:"messages"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./pkg/models/ -run TestMessage -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/models/message.go pkg/models/message_test.go
git commit -m "feat(pkg): add shared Message model with request/response types"
```

---

### Task 4: Create `pkg/models/error.go`

**Files:**
- Create: `pkg/models/error.go`

- [ ] **Step 1: Create the error response type**

```go
package models

type ErrorResponse struct {
	Error string `json:"error"`
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd /home/user/chat && go build ./pkg/models/`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add pkg/models/error.go
git commit -m "feat(pkg): add shared ErrorResponse type"
```

---

### Task 5: Create `pkg/subjects/subjects.go` with tests

**Files:**
- Create: `pkg/subjects/subjects_test.go`
- Create: `pkg/subjects/subjects.go`

- [ ] **Step 1: Write the failing test**

```go
package subjects_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/subjects"
)

func TestRoomBroadcastSubject(t *testing.T) {
	got := subjects.RoomBroadcast("room-42")
	want := "chat.room.room-42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubjectConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"RoomsCreate", subjects.RoomsCreate, "chat.rooms.create"},
		{"RoomsList", subjects.RoomsList, "chat.rooms.list"},
		{"MessagesSend", subjects.MessagesSend, "chat.messages.send"},
		{"MessagesList", subjects.MessagesList, "chat.messages.list"},
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

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test ./pkg/subjects/ -v`
Expected: FAIL — package not found

- [ ] **Step 3: Write the subjects package**

```go
package subjects

import "fmt"

const (
	RoomsCreate  = "chat.rooms.create"
	RoomsList    = "chat.rooms.list"
	MessagesSend = "chat.messages.send"
	MessagesList = "chat.messages.list"

	// RoomBroadcastFmt is the format string for room pub/sub subjects.
	RoomBroadcastFmt = "chat.room.%s"
)

// RoomBroadcast returns the pub/sub subject for a specific room.
func RoomBroadcast(roomID string) string {
	return fmt.Sprintf(RoomBroadcastFmt, roomID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./pkg/subjects/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/subjects/
git commit -m "feat(pkg): add shared NATS subject constants and helpers"
```

---

### Task 6: Create `pkg/natsutil/reply.go` with tests

**Files:**
- Create: `pkg/natsutil/reply_test.go`
- Create: `pkg/natsutil/reply.go`

- [ ] **Step 1: Write the failing test**

```go
package natsutil_test

import (
	"encoding/json"
	"testing"

	"github.com/hmchangw/chat/pkg/models"
	"github.com/hmchangw/chat/pkg/natsutil"
)

func TestMarshalResponse(t *testing.T) {
	room := models.Room{ID: "1", Name: "general"}
	data, err := natsutil.MarshalResponse(room)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var got models.Room
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != "1" || got.Name != "general" {
		t.Errorf("got %+v", got)
	}
}

func TestMarshalError(t *testing.T) {
	data := natsutil.MarshalError("something went wrong")

	var got models.ErrorResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Error != "something went wrong" {
		t.Errorf("got %q", got.Error)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/user/chat && go test ./pkg/natsutil/ -v`
Expected: FAIL — package not found

- [ ] **Step 3: Write the natsutil package**

```go
package natsutil

import (
	"encoding/json"
	"log"

	"github.com/hmchangw/chat/pkg/models"
	"github.com/nats-io/nats.go"
)

// MarshalResponse marshals v to JSON bytes.
func MarshalResponse(v any) ([]byte, error) {
	return json.Marshal(v)
}

// MarshalError returns a JSON-encoded ErrorResponse.
func MarshalError(errMsg string) []byte {
	data, _ := json.Marshal(models.ErrorResponse{Error: errMsg})
	return data
}

// ReplyJSON sends a JSON-encoded response to a NATS message.
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

// ReplyError sends a JSON-encoded error response to a NATS message.
func ReplyError(msg *nats.Msg, errMsg string) {
	if err := msg.Respond(MarshalError(errMsg)); err != nil {
		log.Printf("error reply failed: %v", err)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/user/chat && go test ./pkg/natsutil/ -v`
Expected: PASS

- [ ] **Step 5: Run all pkg tests**

Run: `cd /home/user/chat && go test ./pkg/... -v`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/natsutil/
git commit -m "feat(pkg): add shared NATS reply helpers"
```
