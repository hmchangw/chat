# Room Key Sender Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create `pkg/roomkeysender`, a library that publishes a room's versioned encryption key pair to a user's NATS subject.

**Architecture:** Thin publisher wrapper — a `Sender` struct accepts a `Publisher` interface (matching the pattern used by `broadcast-worker`, `inbox-worker`, `notification-worker`) and a `Send` method that marshals a `model.RoomKeyEvent` to JSON and publishes to `chat.user.{account}.event.room.key`. A new `RoomKeyUpdate` subject builder is added to `pkg/subject`. A new `RoomKeyEvent` model is added to `pkg/model`.

**Tech Stack:** Go 1.24, NATS (publish interface), `encoding/json`, `stretchr/testify`

**Spec:** `docs/superpowers/specs/2026-03-31-room-key-sender-design.md`

---

## File Structure

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `pkg/subject/subject.go` | Add `RoomKeyUpdate(account)` subject builder |
| Modify | `pkg/subject/subject_test.go` | Add test case for new builder |
| Modify | `pkg/model/event.go` | Add `RoomKeyEvent` struct |
| Modify | `pkg/model/model_test.go` | Add round-trip JSON test for `RoomKeyEvent` |
| Create | `pkg/roomkeysender/roomkeysender.go` | `Publisher` interface, `Sender` struct, `Send` method |
| Create | `pkg/roomkeysender/roomkeysender_test.go` | Unit tests with mock publisher |

---

### Task 1: Add `RoomKeyUpdate` subject builder

**Files:**
- Modify: `pkg/subject/subject.go`
- Modify: `pkg/subject/subject_test.go`

- [ ] **Step 1: Write the failing test**

Add one entry to the `TestSubjectBuilders` table in `pkg/subject/subject_test.go`. Insert it after the `UserRoomEvent` entry (line 48):

```go
{"RoomKeyUpdate", subject.RoomKeyUpdate("alice"),
    "chat.user.alice.event.room.key"},
```

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/subject`
Expected: Compilation error — `subject.RoomKeyUpdate` is undefined.

- [ ] **Step 3: Write minimal implementation**

Add the following function to `pkg/subject/subject.go`, after the `UserRoomEvent` function (after line 86):

```go
func RoomKeyUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.key", account)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/subject`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/subject/subject.go pkg/subject/subject_test.go
git commit -m "feat(subject): add RoomKeyUpdate subject builder"
```

---

### Task 2: Add `RoomKeyEvent` model

**Files:**
- Modify: `pkg/model/event.go`
- Modify: `pkg/model/model_test.go`

- [ ] **Step 1: Write the failing test**

Add a new test function to `pkg/model/model_test.go`, after `TestRoomEventTypeValues` (after line 150):

```go
func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		VersionID:  "v-abc-123",
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
	}
	roundTrip(t, &src, &model.RoomKeyEvent{})
}
```

Note: `RoomKeyEvent` contains `[]byte` fields which are not `comparable` via the generic `roundTrip` helper, but Go 1.24 allows `[]byte` in `comparable` constraints for struct equality. However, `[]byte` fields make the struct non-comparable with `==`. Use `reflect.DeepEqual` instead:

```go
func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		VersionID:  "v-abc-123",
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
	}

	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.RoomKeyEvent
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}
```

The file already imports `reflect` (used by `TestRoomEventJSON`), so no new imports are needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `make test SERVICE=pkg/model`
Expected: Compilation error — `model.RoomKeyEvent` is undefined.

- [ ] **Step 3: Write minimal implementation**

Add the following struct to `pkg/model/event.go`, after the `RoomEvent` struct (after line 70):

```go
type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	VersionID  string `json:"versionId"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `make test SERVICE=pkg/model`
Expected: All tests PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/model/event.go pkg/model/model_test.go
git commit -m "feat(model): add RoomKeyEvent struct"
```

---

### Task 3: Create `pkg/roomkeysender` — tests first

**Files:**
- Create: `pkg/roomkeysender/roomkeysender_test.go`
- Create: `pkg/roomkeysender/roomkeysender.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/roomkeysender/roomkeysender_test.go`:

```go
package roomkeysender_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeysender"
)

// mockPublisher captures the subject and data from the last Publish call.
type mockPublisher struct {
	subject string
	data    []byte
	err     error // error to return from Publish
}

func (m *mockPublisher) Publish(subject string, data []byte) error {
	m.subject = subject
	m.data = data
	return m.err
}

func TestSender_Send(t *testing.T) {
	pub65 := make([]byte, 65)
	pub65[0] = 0x04
	for i := 1; i < 65; i++ {
		pub65[i] = byte(i)
	}
	priv32 := make([]byte, 32)
	for i := range priv32 {
		priv32[i] = byte(i + 100)
	}

	tests := []struct {
		name       string
		account   string
		evt        model.RoomKeyEvent
		publishErr error
		wantSubj   string
		wantErr    string
	}{
		{
			name:     "valid send",
			username: "alice",
			evt: model.RoomKeyEvent{
				RoomID:     "room-1",
				VersionID:  "v-abc-123",
				PublicKey:  pub65,
				PrivateKey: priv32,
			},
			wantSubj: "chat.user.alice.event.room.key",
		},
		{
			name:     "different user produces different subject",
			username: "bob",
			evt: model.RoomKeyEvent{
				RoomID:     "room-2",
				VersionID:  "v-def-456",
				PublicKey:  []byte{0x04, 0x01},
				PrivateKey: []byte{0x0a},
			},
			wantSubj: "chat.user.bob.event.room.key",
		},
		{
			name:     "publish error is wrapped and returned",
			username: "carol",
			evt: model.RoomKeyEvent{
				RoomID:     "room-3",
				VersionID:  "v-ghi-789",
				PublicKey:  []byte{0x04},
				PrivateKey: []byte{0x01},
			},
			publishErr: errors.New("connection lost"),
			wantErr:    "publish room key event",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &mockPublisher{err: tt.publishErr}
			sender := roomkeysender.NewSender(pub)

			err := sender.Send(tt.account, tt.evt)

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.ErrorIs(t, err, tt.publishErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantSubj, pub.subject)

			// Verify payload round-trips correctly.
			var got model.RoomKeyEvent
			require.NoError(t, json.Unmarshal(pub.data, &got))
			assert.Equal(t, tt.evt.RoomID, got.RoomID)
			assert.Equal(t, tt.evt.VersionID, got.VersionID)
			assert.Equal(t, tt.evt.PublicKey, got.PublicKey)
			assert.Equal(t, tt.evt.PrivateKey, got.PrivateKey)
		})
	}
}
```

- [ ] **Step 2: Create a stub implementation file so the test compiles**

Create `pkg/roomkeysender/roomkeysender.go` with just enough to compile:

```go
package roomkeysender
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `make test SERVICE=pkg/roomkeysender`
Expected: Compilation error — `roomkeysender.NewSender` is undefined.

- [ ] **Step 4: Write the full implementation**

Replace `pkg/roomkeysender/roomkeysender.go` with:

```go
package roomkeysender

import (
	"encoding/json"
	"fmt"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// Publisher abstracts NATS publishing so the sender is testable.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Sender publishes room key events to user NATS subjects.
type Sender struct {
	pub Publisher
}

// NewSender creates a Sender backed by the given publisher.
func NewSender(pub Publisher) *Sender {
	return &Sender{pub: pub}
}

// Send publishes evt to the room key update subject for account.
func (s *Sender) Send(account string, evt model.RoomKeyEvent) error {
	data, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal room key event: %w", err)
	}
	subj := subject.RoomKeyUpdate(account)
	if err := s.pub.Publish(subj, data); err != nil {
		return fmt.Errorf("publish room key event: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `make test SERVICE=pkg/roomkeysender`
Expected: All 3 subtests PASS.

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: No lint errors.

- [ ] **Step 7: Commit**

```bash
git add pkg/roomkeysender/roomkeysender.go pkg/roomkeysender/roomkeysender_test.go
git commit -m "feat(roomkeysender): add room key sender library"
```

---

### Task 4: Final verification

- [ ] **Step 1: Run all unit tests**

Run: `make test`
Expected: All tests PASS across all packages.

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: No lint errors.

- [ ] **Step 3: Push**

```bash
git push -u origin claude/room-key-library-S5JBX
```
