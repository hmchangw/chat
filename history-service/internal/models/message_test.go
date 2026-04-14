package models

import (
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
