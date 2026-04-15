# Move Cassandra Message Struct to pkg/model/cassandra Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the Cassandra message types from `history-service/internal/models` into a shared `pkg/model/cassandra` package so other services can reuse them.

**Architecture:** Create `pkg/model/cassandra/` sub-package containing the `Message` struct, all UDT types (`Participant`, `File`, `Card`, `CardAction`, `QuotedParentMessage`), their `MarshalUDT`/`UnmarshalUDT` methods, the generic UDT reflection helpers, and the `init()` safety check. Then update all consumers in `history-service` to import from the new location. The request/response types (`LoadHistoryRequest`, etc.) stay in `history-service/internal/models` since they are service-specific.

**Tech Stack:** Go 1.25, `github.com/gocql/gocql`

---

### Task 1: Create pkg/model/cassandra/udt.go — generic UDT helpers

**Files:**
- Create: `pkg/model/cassandra/udt.go`
- Test: `pkg/model/cassandra/udt_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/model/cassandra/udt_test.go`:

```go
package cassandra

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVerifyUDTTags_PanicsOnMissingTag(t *testing.T) {
	type BadUDT struct {
		Name string `cql:"name"`
		Oops string // no cql tag
	}
	assert.Panics(t, func() { verifyUDTTags(&BadUDT{}) })
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/model/cassandra/ -run TestVerifyUDTTags -v`
Expected: FAIL — `verifyUDTTags` not defined

- [ ] **Step 3: Write minimal implementation**

Create `pkg/model/cassandra/udt.go`:

```go
package cassandra

import (
	"fmt"
	"reflect"

	"github.com/gocql/gocql"
)

// unmarshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and unmarshals data into it. Unknown names are ignored.
func unmarshalUDTField(ptr any, name string, info gocql.TypeInfo, data []byte) error {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return fmt.Errorf("unmarshal UDT field %q: expected non-nil pointer, got %T", name, ptr)
	}
	v := rv.Elem()
	if v.Kind() != reflect.Struct {
		return fmt.Errorf("unmarshal UDT field %q: expected pointer to struct, got pointer to %s", name, v.Kind())
	}
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			if err := gocql.Unmarshal(info, data, v.Field(i).Addr().Interface()); err != nil {
				return fmt.Errorf("unmarshal UDT field %q: %w", name, err)
			}
			return nil
		}
	}
	return nil
}

// marshalUDTField uses the `cql` struct tag to find the field matching the
// Cassandra column name and marshals its value. Unknown names return (nil, nil).
func marshalUDTField(ptr any, name string, info gocql.TypeInfo) ([]byte, error) {
	rv := reflect.ValueOf(ptr)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil, fmt.Errorf("marshal UDT field %q: expected non-nil pointer, got %T", name, ptr)
	}
	v := rv.Elem()
	if v.Kind() != reflect.Struct {
		return nil, fmt.Errorf("marshal UDT field %q: expected pointer to struct, got pointer to %s", name, v.Kind())
	}
	t := v.Type()
	for i := range t.NumField() {
		if t.Field(i).Tag.Get("cql") == name {
			data, err := gocql.Marshal(info, v.Field(i).Interface())
			if err != nil {
				return nil, fmt.Errorf("marshal UDT field %q: %w", name, err)
			}
			return data, nil
		}
	}
	return nil, nil
}

// verifyUDTTags panics at init time if any struct field is missing a `cql` tag.
// Call this in an init() block for each UDT type to catch tag typos early.
func verifyUDTTags(samplePtr any) {
	rv := reflect.TypeOf(samplePtr)
	if rv.Kind() != reflect.Ptr {
		panic(fmt.Sprintf("cassandra: verifyUDTTags requires a pointer, got %T", samplePtr))
	}
	t := rv.Elem()
	if t.Kind() != reflect.Struct {
		panic(fmt.Sprintf("cassandra: verifyUDTTags requires a pointer to struct, got pointer to %s", t.Kind()))
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Tag.Get("cql") == "" {
			panic(fmt.Sprintf("cassandra: struct %s field %s is missing a `cql` tag", t.Name(), f.Name))
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/model/cassandra/ -run TestVerifyUDTTags -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/model/cassandra/udt.go pkg/model/cassandra/udt_test.go
git commit -m "feat(model): add pkg/model/cassandra with generic UDT helpers"
```

---

### Task 2: Create pkg/model/cassandra/message.go — Message struct + UDT types

**Files:**
- Create: `pkg/model/cassandra/message.go`
- Test: `pkg/model/cassandra/message_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/model/cassandra/message_test.go`:

```go
package cassandra

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// roundTrip marshals src to JSON and unmarshals into dst, verifying they match.
func roundTrip[T any](t *testing.T, src T) T {
	t.Helper()
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var dst T
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, src, dst)
	return dst
}

func TestParticipant_JSON(t *testing.T) {
	p := Participant{
		ID:          "u1",
		EngName:     "Alice Smith",
		CompanyName: "Acme Corp",
		AppID:       "app-1",
		AppName:     "MyApp",
		IsBot:       true,
		Account:     "alice",
	}
	roundTrip(t, p)
}

func TestParticipant_JSON_Minimal(t *testing.T) {
	p := Participant{ID: "u1", Account: "alice"}
	got := roundTrip(t, p)
	assert.Empty(t, got.EngName)
	assert.False(t, got.IsBot)
}

func TestFile_JSON(t *testing.T) {
	f := File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"}
	roundTrip(t, f)
}

func TestCard_JSON(t *testing.T) {
	c := Card{Template: "approval", Data: []byte(`{"key":"value"}`)}
	roundTrip(t, c)
}

func TestCard_JSON_NilData(t *testing.T) {
	c := Card{Template: "simple"}
	roundTrip(t, c)
}

func TestCardAction_JSON(t *testing.T) {
	ca := CardAction{
		Verb:        "approve",
		Text:        "Approve",
		CardID:      "c1",
		DisplayText: "Click to approve",
		HideExecLog: true,
		CardTmID:    "tm1",
		Data:        []byte(`{"action":"yes"}`),
	}
	roundTrip(t, ca)
}

func TestCardAction_JSON_Minimal(t *testing.T) {
	ca := CardAction{Verb: "click"}
	got := roundTrip(t, ca)
	assert.Empty(t, got.Text)
	assert.Empty(t, got.CardID)
	assert.False(t, got.HideExecLog)
}

func TestQuotedParentMessage_JSON(t *testing.T) {
	q := QuotedParentMessage{
		MessageID:   "m1",
		RoomID:      "r1",
		Sender:      Participant{ID: "u1", Account: "alice"},
		CreatedAt:   time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC),
		Msg:         "original message",
		Mentions:    []Participant{{ID: "u2", Account: "bob"}},
		Attachments: [][]byte{[]byte("file1")},
		MessageLink: "https://chat.example.com/r1/m1",
	}
	roundTrip(t, q)
}

func TestQuotedParentMessage_JSON_Minimal(t *testing.T) {
	q := QuotedParentMessage{
		MessageID: "m1",
		RoomID:    "r1",
		Sender:    Participant{ID: "u1"},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	got := roundTrip(t, q)
	assert.Empty(t, got.Msg)
	assert.Nil(t, got.Mentions)
	assert.Nil(t, got.Attachments)
	assert.Empty(t, got.MessageLink)
}

func TestMessage_JSON(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	edited := now.Add(5 * time.Minute)
	updated := now.Add(10 * time.Minute)
	threadParent := now.Add(-1 * time.Hour)

	msg := Message{
		RoomID:                "r1",
		CreatedAt:             now,
		MessageID:             "m1",
		Sender:                Participant{ID: "u1", Account: "alice", IsBot: false},
		TargetUser:            &Participant{ID: "u2", Account: "bob"},
		Msg:                   "hello world",
		Mentions:              []Participant{{ID: "u3", Account: "charlie"}},
		Attachments:           [][]byte{[]byte("attach1")},
		File:                  &File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"},
		Card:                  &Card{Template: "approval", Data: []byte(`{"k":"v"}`)},
		CardAction:            &CardAction{Verb: "approve", CardID: "c1"},
		TShow:                 true,
		ThreadParentID:        "m-parent",
		ThreadParentCreatedAt: &threadParent,
		QuotedParentMessage: &QuotedParentMessage{
			MessageID: "m-quoted", RoomID: "r1",
			Sender:    Participant{ID: "u5", Account: "eve"},
			CreatedAt: now.Add(-30 * time.Minute), Msg: "original",
		},
		VisibleTo:  "u1",
		Unread:     true,
		Reactions:  map[string][]Participant{"thumbsup": {{ID: "u2", Account: "bob"}}},
		Deleted:    false,
		Type:       "user_joined",
		SysMsgData: []byte(`{"userId":"u3"}`),
		SiteID:     "site-remote",
		EditedAt:   &edited,
		UpdatedAt:  &updated,
	}

	got := roundTrip(t, msg)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "alice", got.Sender.Account)
	assert.Equal(t, "bob", got.TargetUser.Account)
	assert.Len(t, got.Mentions, 1)
	assert.Len(t, got.Attachments, 1)
	assert.Equal(t, "doc.pdf", got.File.Name)
	assert.Equal(t, "approval", got.Card.Template)
	assert.Equal(t, "approve", got.CardAction.Verb)
	assert.True(t, got.TShow)
	assert.Equal(t, "m-parent", got.ThreadParentID)
	assert.Equal(t, threadParent, *got.ThreadParentCreatedAt)
	require.NotNil(t, got.QuotedParentMessage)
	assert.Equal(t, "m-quoted", got.QuotedParentMessage.MessageID)
	assert.Equal(t, "u1", got.VisibleTo)
	assert.True(t, got.Unread)
	assert.Len(t, got.Reactions["thumbsup"], 1)
	assert.Equal(t, "user_joined", got.Type)
	assert.Equal(t, "site-remote", got.SiteID)
	assert.Equal(t, edited, *got.EditedAt)
	assert.Equal(t, updated, *got.UpdatedAt)
}

func TestMessage_JSON_Minimal(t *testing.T) {
	msg := Message{
		RoomID:    "r1",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MessageID: "m1",
		Sender:    Participant{ID: "u1", Account: "alice"},
		Msg:       "hi",
	}
	got := roundTrip(t, msg)
	assert.Nil(t, got.TargetUser)
	assert.Nil(t, got.File)
	assert.Nil(t, got.Card)
	assert.Nil(t, got.CardAction)
	assert.Nil(t, got.Mentions)
	assert.Nil(t, got.Attachments)
	assert.Nil(t, got.Reactions)
	assert.Nil(t, got.EditedAt)
	assert.Nil(t, got.UpdatedAt)
	assert.Nil(t, got.ThreadParentCreatedAt)
	assert.Nil(t, got.QuotedParentMessage)
	assert.Empty(t, got.ThreadParentID)
	assert.False(t, got.TShow)
	assert.False(t, got.Unread)
	assert.False(t, got.Deleted)
}

func TestUnmarshalUDT_UnknownField(t *testing.T) {
	assert.NoError(t, (&Participant{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&File{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&Card{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&CardAction{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&QuotedParentMessage{}).UnmarshalUDT("nonexistent", nil, nil))
}

func TestMarshalUDT_UnknownField(t *testing.T) {
	data, err := (&Participant{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&File{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&Card{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&CardAction{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&QuotedParentMessage{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/model/cassandra/ -run 'TestParticipant|TestFile|TestCard|TestMessage|TestUnmarshal|TestMarshal' -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Write the implementation**

Create `pkg/model/cassandra/message.go`:

```go
package cassandra

import (
	"time"

	"github.com/gocql/gocql"
)

func init() {
	verifyUDTTags(&Participant{})
	verifyUDTTags(&File{})
	verifyUDTTags(&Card{})
	verifyUDTTags(&CardAction{})
	verifyUDTTags(&QuotedParentMessage{})
}

// Participant maps to the Cassandra "Participant" UDT.
type Participant struct {
	ID          string `json:"id"                    cql:"id"`
	EngName     string `json:"engName,omitempty"     cql:"eng_name"`
	CompanyName string `json:"companyName,omitempty" cql:"company_name"`
	AppID       string `json:"appId,omitempty"       cql:"app_id"`
	AppName     string `json:"appName,omitempty"     cql:"app_name"`
	IsBot       bool   `json:"isBot,omitempty"       cql:"is_bot"`
	Account     string `json:"account,omitempty"     cql:"account"`
}

func (p *Participant) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(p, name, info, data)
}
func (p *Participant) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(p, name, info)
}

// File maps to the Cassandra "File" UDT.
type File struct {
	ID   string `json:"id"   cql:"id"`
	Name string `json:"name" cql:"name"`
	Type string `json:"type" cql:"type"`
}

func (f *File) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(f, name, info, data)
}
func (f *File) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(f, name, info)
}

// Card maps to the Cassandra "Card" UDT.
type Card struct {
	Template string `json:"template"        cql:"template"`
	Data     []byte `json:"data,omitempty"  cql:"data"`
}

func (c *Card) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(c, name, info, data)
}
func (c *Card) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(c, name, info)
}

// CardAction maps to the Cassandra "CardAction" UDT.
type CardAction struct {
	Verb        string `json:"verb"                  cql:"verb"`
	Text        string `json:"text,omitempty"        cql:"text"`
	CardID      string `json:"cardId,omitempty"      cql:"card_id"`
	DisplayText string `json:"displayText,omitempty" cql:"display_text"`
	HideExecLog bool   `json:"hideExecLog,omitempty" cql:"hide_exec_log"`
	CardTmID    string `json:"cardTmId,omitempty"    cql:"card_tmid"`
	Data        []byte `json:"data,omitempty"        cql:"data"`
}

func (ca *CardAction) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(ca, name, info, data)
}
func (ca *CardAction) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(ca, name, info)
}

// QuotedParentMessage maps to the Cassandra "QuotedParentMessage" UDT.
type QuotedParentMessage struct {
	MessageID   string        `json:"messageId"              cql:"message_id"`
	RoomID      string        `json:"roomId"                 cql:"room_id"`
	Sender      Participant   `json:"sender"                 cql:"sender"`
	CreatedAt   time.Time     `json:"createdAt"              cql:"created_at"`
	Msg         string        `json:"msg,omitempty"          cql:"msg"`
	Mentions    []Participant `json:"mentions,omitempty"     cql:"mentions"`
	Attachments [][]byte      `json:"attachments,omitempty"  cql:"attachments"`
	MessageLink string        `json:"messageLink,omitempty"  cql:"message_link"`
}

func (q *QuotedParentMessage) UnmarshalUDT(name string, info gocql.TypeInfo, data []byte) error {
	return unmarshalUDTField(q, name, info, data)
}
func (q *QuotedParentMessage) MarshalUDT(name string, info gocql.TypeInfo) ([]byte, error) {
	return marshalUDTField(q, name, info)
}

// Message represents a full message row from the messages_by_room Cassandra table.
type Message struct {
	RoomID                string                   `json:"roomId"`
	CreatedAt             time.Time                `json:"createdAt"`
	MessageID             string                   `json:"messageId"`
	Sender                Participant              `json:"sender"`
	TargetUser            *Participant             `json:"targetUser,omitempty"`
	Msg                   string                   `json:"msg"`
	Mentions              []Participant            `json:"mentions,omitempty"`
	Attachments           [][]byte                 `json:"attachments,omitempty"`
	File                  *File                    `json:"file,omitempty"`
	Card                  *Card                    `json:"card,omitempty"`
	CardAction            *CardAction              `json:"cardAction,omitempty"`
	TShow                 bool                     `json:"tshow,omitempty"`
	ThreadParentID        string                   `json:"threadParentId,omitempty"`
	ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty"`
	QuotedParentMessage   *QuotedParentMessage     `json:"quotedParentMessage,omitempty"`
	VisibleTo             string                   `json:"visibleTo,omitempty"`
	Unread                bool                     `json:"unread,omitempty"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"`
	Deleted               bool                     `json:"deleted,omitempty"`
	Type                  string                   `json:"type,omitempty"`
	SysMsgData            []byte                   `json:"sysMsgData,omitempty"`
	SiteID                string                   `json:"siteId,omitempty"`
	EditedAt              *time.Time               `json:"editedAt,omitempty"`
	UpdatedAt             *time.Time               `json:"updatedAt,omitempty"`
}
```

- [ ] **Step 4: Run all tests to verify they pass**

Run: `go test ./pkg/model/cassandra/ -v -race`
Expected: ALL PASS

- [ ] **Step 5: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add pkg/model/cassandra/message.go pkg/model/cassandra/message_test.go
git commit -m "feat(model): add Message and UDT types to pkg/model/cassandra"
```

---

### Task 3: Update history-service/internal/models to import from pkg/model/cassandra

This task replaces the local type definitions in `history-service/internal/models` with type aliases pointing to `pkg/model/cassandra`, then removes them entirely.

**Files:**
- Modify: `history-service/internal/models/message.go` — remove `Message` struct, import from `cassandra`
- Delete: `history-service/internal/models/types.go` — all types moved
- Delete: `history-service/internal/models/utils.go` — all helpers moved
- Delete: `history-service/internal/models/message_test.go` — tests moved
- Delete: `history-service/internal/models/types_test.go` — tests moved

- [ ] **Step 1: Update message.go — replace Message with type alias, keep request/response types**

Replace `history-service/internal/models/message.go` with:

```go
package models

import "github.com/hmchangw/chat/pkg/model/cassandra"

// Message is the Cassandra message record, now defined in pkg/model/cassandra.
type Message = cassandra.Message

// Participant is the Cassandra Participant UDT, now defined in pkg/model/cassandra.
type Participant = cassandra.Participant

// File is the Cassandra File UDT, now defined in pkg/model/cassandra.
type File = cassandra.File

// Card is the Cassandra Card UDT, now defined in pkg/model/cassandra.
type Card = cassandra.Card

// CardAction is the Cassandra CardAction UDT, now defined in pkg/model/cassandra.
type CardAction = cassandra.CardAction

// QuotedParentMessage is the Cassandra QuotedParentMessage UDT, now defined in pkg/model/cassandra.
type QuotedParentMessage = cassandra.QuotedParentMessage

// LoadHistoryRequest is the payload for loading message history before a timestamp.
type LoadHistoryRequest struct {
	Before *int64 `json:"before,omitempty"` // UTC millis — fetch messages before this (nil = now)
	Limit  int    `json:"limit"`            // default 20
}

// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages []Message `json:"messages"`
}

// LoadNextMessagesRequest is the payload for loading messages after a timestamp.
type LoadNextMessagesRequest struct {
	After  *int64 `json:"after,omitempty"` // UTC millis — fetch messages after this (nil = no lower bound)
	Limit  int    `json:"limit"`           // default 50
	Cursor string `json:"cursor"`          // pagination cursor from previous response
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	HasNext    bool      `json:"hasNext"`
}

// LoadSurroundingMessagesRequest is the payload for loading messages around a central message.
type LoadSurroundingMessagesRequest struct {
	MessageID string `json:"messageId"` // central message ID
	Limit     int    `json:"limit"`     // total messages including central
}

// LoadSurroundingMessagesResponse contains messages around the central message.
type LoadSurroundingMessagesResponse struct {
	Messages   []Message `json:"messages"`
	MoreBefore bool      `json:"moreBefore"`
	MoreAfter  bool      `json:"moreAfter"`
}

// GetMessageByIDRequest is the payload for fetching a single message.
type GetMessageByIDRequest struct {
	MessageID string `json:"messageId"`
}
```

- [ ] **Step 2: Delete types.go and utils.go — code now lives in pkg/model/cassandra**

```bash
rm history-service/internal/models/types.go
rm history-service/internal/models/utils.go
```

- [ ] **Step 3: Delete old test files — tests now live in pkg/model/cassandra**

```bash
rm history-service/internal/models/message_test.go
rm history-service/internal/models/types_test.go
```

- [ ] **Step 4: Verify all history-service tests pass**

Run: `make test SERVICE=history-service`
Expected: ALL PASS — all consumers of `models.Message` keep working because type aliases are transparent.

- [ ] **Step 5: Run full lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 6: Verify the new package tests still pass**

Run: `go test ./pkg/model/cassandra/ -v -race`
Expected: ALL PASS

- [ ] **Step 7: Commit**

```bash
git add history-service/internal/models/message.go
git add -u history-service/internal/models/
git commit -m "refactor(history-service): replace local types with aliases to pkg/model/cassandra"
```

---

### Task 4: Final verification — full test suite and lint

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `make test`
Expected: ALL PASS across all services

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Push**

```bash
git push -u origin claude/move-message-struct-mVRT1
```
