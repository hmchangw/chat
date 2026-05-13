package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestParseMessagesResponse_HappyPath(t *testing.T) {
	body := json.RawMessage(`{
		"hits": {
			"total": {"value": 2},
			"hits": [
				{"_source": {
					"messageId": "m1", "roomId": "r1", "siteId": "site-a",
					"userId": "u1", "userAccount": "alice", "content": "hello",
					"createdAt": "2026-04-01T12:00:00Z"
				}},
				{"_source": {
					"messageId": "m2", "roomId": "r2", "siteId": "site-b",
					"userId": "u2", "userAccount": "bob", "content": "world",
					"createdAt": "2026-04-02T12:00:00Z",
					"threadParentMessageId": "p1",
					"threadParentMessageCreatedAt": "2026-04-02T11:00:00Z"
				}}
			]
		}
	}`)

	hits, total, err := parseMessagesResponse(body)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total)
	require.Len(t, hits, 2)

	assert.Equal(t, "m1", hits[0].MessageID)
	assert.Equal(t, "alice", hits[0].UserAccount)
	assert.Empty(t, hits[0].ThreadParentID)

	assert.Equal(t, "p1", hits[1].ThreadParentID)
	require.NotNil(t, hits[1].ThreadParentCreatedAt)
	want := time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC)
	assert.True(t, hits[1].ThreadParentCreatedAt.Equal(want))
}

func TestParseMessagesResponse_Empty(t *testing.T) {
	body := json.RawMessage(`{"hits":{"total":{"value":0},"hits":[]}}`)
	hits, total, err := parseMessagesResponse(body)
	require.NoError(t, err)
	assert.EqualValues(t, 0, total)
	assert.Empty(t, hits)
}

func TestParseMessagesResponse_Malformed(t *testing.T) {
	_, _, err := parseMessagesResponse(json.RawMessage(`{not json`))
	assert.Error(t, err)
}

func TestToSearchMessage_WithEnrichment(t *testing.T) {
	hit := messageSearchHit{
		MessageID:   "m1",
		RoomID:      "r1",
		SiteID:      "site-a",
		UserID:      "u1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
	}
	user := &model.User{ID: "u1", Account: "alice", EngName: "Alice Wang", ChineseName: "愛麗絲"}
	room := &model.Room{ID: "r1", Name: "general"}

	got := toSearchMessage(&hit, user, room)

	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "general", got.RoomName)
	assert.Equal(t, "alice", got.UserAccount)
	assert.Equal(t, "Alice Wang", got.UserEngName)
	assert.Equal(t, "愛麗絲", got.UserChineseName)
	assert.Equal(t, "hello", got.Content)
	assert.Equal(t, "site-a", got.SiteID)
}

func TestToSearchMessage_NilEnrichment(t *testing.T) {
	hit := messageSearchHit{
		MessageID:   "m1",
		RoomID:      "r1",
		UserAccount: "alice",
		Content:     "hello",
	}
	// nil user and room — enrichment fields remain zero-value, no panic
	got := toSearchMessage(&hit, nil, nil)

	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "alice", got.UserAccount)
	assert.Empty(t, got.RoomName, "nil room → RoomName stays empty")
	assert.Empty(t, got.UserEngName, "nil user → UserEngName stays empty")
}

func TestToSearchMessage_ThreadParentCopied(t *testing.T) {
	tp := time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC)
	hit := messageSearchHit{
		MessageID:             "m1",
		RoomID:                "r1",
		UserAccount:           "alice",
		ThreadParentID:        "p1",
		ThreadParentCreatedAt: &tp,
	}
	got := toSearchMessage(&hit, nil, nil)
	assert.Equal(t, "p1", got.ThreadParentMessageID)
}

func TestParseSubscriptionRoomIDs_HappyPath(t *testing.T) {
	body := json.RawMessage(`{
		"hits": {
			"total": {"value": 2},
			"hits": [
				{"_source": {
					"roomId": "r1", "roomName": "general", "roomType": "p",
					"userAccount": "alice", "siteId": "site-a",
					"joinedAt": "2026-04-01T12:00:00Z"
				}},
				{"_source": {
					"roomId": "r2", "roomName": "alice-bob", "roomType": "d",
					"userAccount": "alice", "siteId": "site-a",
					"joinedAt": "2026-04-02T12:00:00Z"
				}}
			]
		}
	}`)

	ids, err := parseSubscriptionRoomIDs(body)
	require.NoError(t, err)
	require.Len(t, ids, 2)
	assert.Equal(t, "r1", ids[0])
	assert.Equal(t, "r2", ids[1])
}

func TestParseSubscriptionRoomIDs_Empty(t *testing.T) {
	body := json.RawMessage(`{"hits":{"total":{"value":0},"hits":[]}}`)
	ids, err := parseSubscriptionRoomIDs(body)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestParseSubscriptionRoomIDs_Malformed(t *testing.T) {
	_, err := parseSubscriptionRoomIDs(json.RawMessage(`{`))
	assert.Error(t, err)
}

func TestParseSubscriptionRoomIDs_PreservesOrder(t *testing.T) {
	body := json.RawMessage(`{
		"hits": {
			"total": {"value": 3},
			"hits": [
				{"_source": {"roomId": "r3"}},
				{"_source": {"roomId": "r1"}},
				{"_source": {"roomId": "r2"}}
			]
		}
	}`)
	ids, err := parseSubscriptionRoomIDs(body)
	require.NoError(t, err)
	assert.Equal(t, []string{"r3", "r1", "r2"}, ids, "ES hit order must be preserved for Mongo hydration")
}
