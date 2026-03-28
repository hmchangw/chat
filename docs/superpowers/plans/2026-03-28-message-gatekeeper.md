# Message Gatekeeper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a message-gatekeeper microservice that centralizes message validation, replaces the FANOUT stream with MESSAGE_SSOT, and simplifies all downstream workers.

**Architecture:** A new `message-gatekeeper` service consumes from the existing `MESSAGES` stream, validates messages (UUID, content size, room membership), and publishes to a new `MESSAGE_SSOT` stream. All downstream workers (message-worker, broadcast-worker, notification-worker) consume from MESSAGE_SSOT. The FANOUT stream is eliminated entirely.

**Tech Stack:** Go 1.24, NATS JetStream, MongoDB, Cassandra, `go.uber.org/mock`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-03-27-message-gatekeeper-design.md`

---

## File Structure

### New Files
| File | Purpose |
|------|---------|
| `message-gatekeeper/store.go` | Store interface with `GetSubscription` + mockgen directive |
| `message-gatekeeper/store_mongo.go` | MongoDB implementation of Store |
| `message-gatekeeper/handler.go` | Message validation, MESSAGE_SSOT publishing, reply routing |
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
| `pkg/stream/stream.go` | Add `MessageSSOT`, remove `Fanout` |
| `pkg/stream/stream_test.go` | Update test table |
| `pkg/subject/subject.go` | Add `ParseUserRoomSiteSubject`, `MsgSSOTCreated`, `MsgSSOTWildcard`; remove `Fanout`, `FanoutWildcard` |
| `pkg/subject/subject_test.go` | Add new test cases, remove Fanout cases |
| `message-worker/store.go` | Rename to `Store`, remove `GetSubscription`, `SaveMessage` takes value |
| `message-worker/handler.go` | Simplified: unmarshal `MessageEvent` + `SaveMessage` only |
| `message-worker/handler_test.go` | Rewrite for new handler |
| `message-worker/main.go` | Remove MongoDB, switch to MESSAGE_SSOT stream |
| `message-worker/integration_test.go` | Cassandra-only tests |
| `broadcast-worker/handler.go` | `evt.RoomID` → `evt.Message.RoomID` |
| `broadcast-worker/handler_test.go` | Remove top-level `RoomID` from `MessageEvent` |
| `broadcast-worker/main.go` | `stream.Fanout` → `stream.MessageSSOT` |
| `notification-worker/handler.go` | `evt.RoomID` → `evt.Message.RoomID` |
| `notification-worker/handler_test.go` | Remove top-level `RoomID` from `MessageEvent` |
| `notification-worker/main.go` | `stream.Fanout` → `stream.MessageSSOT` |
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

- [ ] **Step 1: Update TestMessageJSON to expect Username field**

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
		ID: "m1", RoomID: "r1", UserID: "u1", Username: "alice",
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
			ID: "m1", RoomID: "r1", UserID: "u1", Username: "alice",
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
		Username: "alice", Content: "hello", CreatedAt: now,
	}
```

- [ ] **Step 4: Run tests to verify they fail (Red phase)**

```bash
make test SERVICE=pkg/model
```

Expected: FAIL — `model.Message` has no field `Username`, `model.SendMessageRequest` has no field `ID`.

- [ ] **Step 5: Update Message struct with Username and bson tags**

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
	Username  string    `json:"username"  bson:"username"`
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
git commit -m "feat: update domain models — add Username to Message, add ID to SendMessageRequest, remove redundant RoomID"
```

---

### Task 2: Update Stream Definitions

**Files:**
- Modify: `pkg/stream/stream.go`
- Modify: `pkg/stream/stream_test.go`

- [ ] **Step 1: Update test — add MessageSSOT, remove Fanout (Red)**

In `pkg/stream/stream_test.go`, replace:

```go
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"Fanout", stream.Fanout(siteID), "FANOUT_site-a", "fanout.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.user.*.request.room.*.site-a.member.>"},
```

With:

```go
		{"Messages", stream.Messages(siteID), "MESSAGES_site-a", "chat.user.*.room.*.site-a.msg.>"},
		{"MessageSSOT", stream.MessageSSOT(siteID), "MESSAGE_SSOT_site-a", "chat.msg.ssot.site-a.>"},
		{"Rooms", stream.Rooms(siteID), "ROOMS_site-a", "chat.user.*.request.room.*.site-a.member.>"},
```

Run: `make test SERVICE=pkg/stream` — Expected: FAIL (MessageSSOT undefined)

- [ ] **Step 2: Add MessageSSOT, remove Fanout (Green)**

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
func MessageSSOT(siteID string) Config {
	return Config{
		Name:     fmt.Sprintf("MESSAGE_SSOT_%s", siteID),
		Subjects: []string{fmt.Sprintf("chat.msg.ssot.%s.>", siteID)},
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
git commit -m "feat: add MESSAGE_SSOT stream, remove FANOUT stream"
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
		{"MsgSSOTCreated", subject.MsgSSOTCreated("site-a"),
			"chat.msg.ssot.site-a.created"},
```

In `TestWildcardPatterns`, replace:

```go
		{"FanoutWild", subject.FanoutWildcard("site-a"),
			"fanout.site-a.>"},
```

With:

```go
		{"MsgSSOTWild", subject.MsgSSOTWildcard("site-a"),
			"chat.msg.ssot.site-a.>"},
```

- [ ] **Step 2: Add TestParseUserRoomSiteSubject**

Insert before `TestWildcardPatterns` in `pkg/subject/subject_test.go`:

```go
func TestParseUserRoomSiteSubject(t *testing.T) {
	tests := []struct {
		name         string
		subj         string
		wantUsername string
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
			username, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(tt.subj)
			if ok != tt.wantOK || username != tt.wantUsername || roomID != tt.wantRoomID || siteID != tt.wantSiteID {
				t.Errorf("ParseUserRoomSiteSubject(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					tt.subj, username, roomID, siteID, ok, tt.wantUsername, tt.wantRoomID, tt.wantSiteID, tt.wantOK)
			}
		})
	}
}
```

Run: `make test SERVICE=pkg/subject` — Expected: FAIL

- [ ] **Step 3: Implement new builders and parser (Green)**

In `pkg/subject/subject.go`, add after `ParseUserRoomSubject`:

```go
// ParseUserRoomSiteSubject extracts username, roomID, and siteID from subjects
// matching "chat.user.{username}.room.{roomID}.{siteID}.…" (at least 7 parts).
func ParseUserRoomSiteSubject(subj string) (username, roomID, siteID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 7 || parts[0] != "chat" || parts[1] != "user" || parts[3] != "room" {
		return "", "", "", false
	}
	return parts[2], parts[4], parts[5], true
}
```

Replace `Fanout` function with `MsgSSOTCreated`:

```go
// Replace:
func Fanout(siteID, roomID string) string {
	return fmt.Sprintf("fanout.%s.%s", siteID, roomID)
}

// With:
func MsgSSOTCreated(siteID string) string {
	return fmt.Sprintf("chat.msg.ssot.%s.created", siteID)
}
```

Replace `FanoutWildcard` function with `MsgSSOTWildcard`:

```go
// Replace:
func FanoutWildcard(siteID string) string {
	return fmt.Sprintf("fanout.%s.>", siteID)
}

// With:
func MsgSSOTWildcard(siteID string) string {
	return fmt.Sprintf("chat.msg.ssot.%s.>", siteID)
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
git commit -m "feat: add MESSAGE_SSOT subject builders and ParseUserRoomSiteSubject, remove Fanout"
```
