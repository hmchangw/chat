package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// fakeParentResolver is a test double for parentCreatedAtResolver.
type fakeParentResolver struct {
	createdAt time.Time
	found     bool
	err       error
	calls     int
	lastID    string
}

func (f *fakeParentResolver) GetMessageCreatedAt(_ context.Context, messageID string) (time.Time, bool, error) {
	f.calls++
	f.lastID = messageID
	return f.createdAt, f.found, f.err
}

func threadReplyEventData(t *testing.T, event model.EventType, clientParentCreatedAt time.Time) []byte {
	t.Helper()
	evt := model.MessageEvent{
		Event: event,
		Message: model.Message{
			ID: "reply-1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:                      "a reply",
			CreatedAt:                    time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC),
			ThreadParentMessageID:        "parent-1",
			ThreadParentMessageCreatedAt: &clientParentCreatedAt,
		},
		SiteID:    "site-a",
		Timestamp: 1737964678390,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return data
}

func decodeThreadParentCreatedAt(t *testing.T, doc json.RawMessage) *time.Time {
	t.Helper()
	var idx MessageSearchIndex
	require.NoError(t, json.Unmarshal(doc, &idx))
	return idx.ThreadParentCreatedAt
}

func TestMessageCollection_BuildAction_ReresolvesThreadParent(t *testing.T) {
	clientVal := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC) // forged / wrong
	authoritative := time.Date(2024, 6, 1, 9, 30, 0, 0, time.UTC)

	t.Run("overwrites client value with authoritative when parent found", func(t *testing.T) {
		resolver := &fakeParentResolver{createdAt: authoritative, found: true}
		coll := newMessageCollection("msgs-v1", time.Time{})
		coll.parentResolver = resolver

		actions, err := coll.BuildAction(threadReplyEventData(t, model.EventCreated, clientVal))
		require.NoError(t, err)
		require.Len(t, actions, 1)

		got := decodeThreadParentCreatedAt(t, actions[0].Doc)
		require.NotNil(t, got)
		assert.True(t, got.Equal(authoritative), "indexed value must be authoritative, got %v", got)
		assert.Equal(t, "parent-1", resolver.lastID)
	})

	t.Run("keeps client value when parent not yet persisted", func(t *testing.T) {
		resolver := &fakeParentResolver{found: false}
		coll := newMessageCollection("msgs-v1", time.Time{})
		coll.parentResolver = resolver

		actions, err := coll.BuildAction(threadReplyEventData(t, model.EventCreated, clientVal))
		require.NoError(t, err)
		require.Len(t, actions, 1)

		got := decodeThreadParentCreatedAt(t, actions[0].Doc)
		require.NotNil(t, got)
		assert.True(t, got.Equal(clientVal), "should fall back to client value on miss")
	})

	t.Run("returns error when resolver fails (NAK/retry)", func(t *testing.T) {
		resolver := &fakeParentResolver{err: errors.New("cassandra down")}
		coll := newMessageCollection("msgs-v1", time.Time{})
		coll.parentResolver = resolver

		_, err := coll.BuildAction(threadReplyEventData(t, model.EventCreated, clientVal))
		require.Error(t, err)
	})

	t.Run("does not resolve for a non-thread message", func(t *testing.T) {
		resolver := &fakeParentResolver{createdAt: authoritative, found: true}
		coll := newMessageCollection("msgs-v1", time.Time{})
		coll.parentResolver = resolver

		evt := model.MessageEvent{
			Event:     model.EventCreated,
			Message:   model.Message{ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hi", CreatedAt: time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)},
			SiteID:    "site-a",
			Timestamp: 1737964678390,
		}
		data, _ := json.Marshal(evt)
		_, err := coll.BuildAction(data)
		require.NoError(t, err)
		assert.Zero(t, resolver.calls, "resolver must not be called for non-thread messages")
	})

	t.Run("does not resolve for a delete (delete-by-id needs no parent stamp)", func(t *testing.T) {
		resolver := &fakeParentResolver{createdAt: authoritative, found: true}
		coll := newMessageCollection("msgs-v1", time.Time{})
		coll.parentResolver = resolver

		_, err := coll.BuildAction(threadReplyEventData(t, model.EventDeleted, clientVal))
		require.NoError(t, err)
		assert.Zero(t, resolver.calls, "resolver must not be called on delete")
	})

	t.Run("nil resolver keeps client value (feature disabled)", func(t *testing.T) {
		coll := newMessageCollection("msgs-v1", time.Time{}) // no resolver set
		actions, err := coll.BuildAction(threadReplyEventData(t, model.EventCreated, clientVal))
		require.NoError(t, err)
		require.Len(t, actions, 1)
		got := decodeThreadParentCreatedAt(t, actions[0].Doc)
		require.NotNil(t, got)
		assert.True(t, got.Equal(clientVal))
	})
}
