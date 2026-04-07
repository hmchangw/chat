package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessage_JSON(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	edited := now.Add(5 * time.Minute)
	updated := now.Add(10 * time.Minute)
	threadParent := now.Add(-1 * time.Hour)

	msg := Message{
		RoomID:                "r1",
		CreatedAt:             now,
		MessageID:             "m1",
		Sender:                Participant{ID: "u1", UserName: "alice", IsBot: false},
		TargetUser:            &Participant{ID: "u2", UserName: "bob"},
		Msg:                   "hello world",
		Mentions:              []Participant{{ID: "u3", UserName: "charlie"}},
		Attachments:           [][]byte{[]byte("attach1")},
		File:                  &File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"},
		Card:                  &Card{Template: "approval", Data: []byte(`{"k":"v"}`)},
		CardAction:            &CardAction{Verb: "approve", CardID: "c1"},
		TShow:                 true,
		ThreadParentCreatedAt: &threadParent,
		VisibleTo:             "u1",
		Unread:                true,
		Reactions:             map[string][]Participant{"thumbsup": {{ID: "u2", UserName: "bob"}}},
		Deleted:               false,
		SysMsgType:            "user_joined",
		SysMsgData:            []byte(`{"userId":"u3"}`),
		FederateFrom:          "site-remote",
		EditedAt:              &edited,
		UpdatedAt:             &updated,
	}

	got := roundTrip(t, msg)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "alice", got.Sender.UserName)
	assert.Equal(t, "bob", got.TargetUser.UserName)
	assert.Len(t, got.Mentions, 1)
	assert.Len(t, got.Attachments, 1)
	assert.Equal(t, "doc.pdf", got.File.Name)
	assert.Equal(t, "approval", got.Card.Template)
	assert.Equal(t, "approve", got.CardAction.Verb)
	assert.True(t, got.TShow)
	assert.Equal(t, threadParent, *got.ThreadParentCreatedAt)
	assert.Equal(t, "u1", got.VisibleTo)
	assert.True(t, got.Unread)
	assert.Len(t, got.Reactions["thumbsup"], 1)
	assert.Equal(t, "user_joined", got.SysMsgType)
	assert.Equal(t, "site-remote", got.FederateFrom)
	assert.Equal(t, edited, *got.EditedAt)
	assert.Equal(t, updated, *got.UpdatedAt)
}

func TestMessage_JSON_Minimal(t *testing.T) {
	msg := Message{
		RoomID:    "r1",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		MessageID: "m1",
		Sender:    Participant{ID: "u1", UserName: "alice"},
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
	assert.False(t, got.TShow)
	assert.False(t, got.Unread)
	assert.False(t, got.Deleted)
}

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
