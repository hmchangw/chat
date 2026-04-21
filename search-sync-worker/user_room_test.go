package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

func TestUserRoomCollection_Metadata(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")

	assert.Equal(t, "user-room-sync", coll.ConsumerName())
	assert.Equal(t, "user_room_template", coll.TemplateName())
	assert.NotNil(t, coll.TemplateBody())

	cfg := coll.StreamConfig("site-a")
	assert.Equal(t, "INBOX_site-a", cfg.Name)
	assert.Equal(t, []string{
		"chat.inbox.site-a.*",
		"chat.inbox.site-a.aggregate.>",
	}, cfg.Subjects)
	assert.Empty(t, cfg.Sources)

	filters := coll.FilterSubjects("site-a")
	assert.ElementsMatch(t, []string{
		"chat.inbox.site-a.member_added",
		"chat.inbox.site-a.member_removed",
		"chat.inbox.site-a.aggregate.member_added",
		"chat.inbox.site-a.aggregate.member_removed",
	}, filters)
}

func TestUserRoomCollection_TemplateBody(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	body := coll.TemplateBody()
	require.NotNil(t, body)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	patterns, ok := parsed["index_patterns"].([]any)
	require.True(t, ok)
	assert.Equal(t, "user-room-site-a", patterns[0])

	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	props := mappings["properties"].(map[string]any)

	assert.Contains(t, props, "userAccount")
	assert.Contains(t, props, "rooms")
	assert.Contains(t, props, "roomTimestamps")
	assert.Contains(t, props, "createdAt")
	assert.Contains(t, props, "updatedAt")

	rt := props["roomTimestamps"].(map[string]any)
	assert.Equal(t, "flattened", rt["type"])

	rooms := props["rooms"].(map[string]any)
	assert.Equal(t, "text", rooms["type"])
}

func TestUserRoomCollection_BuildAction_MemberAdded(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseInboxMemberEvent()
	const ts int64 = 1735689600000
	data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, ts)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)

	action := actions[0]
	assert.Equal(t, searchengine.ActionUpdate, action.Action)
	assert.Equal(t, "user-room-site-a", action.Index)
	assert.Equal(t, "alice", action.DocID)
	// Update actions must NOT use external versioning — ES rejects the combo.
	assert.Zero(t, action.Version)
	require.NotNil(t, action.Doc)

	var body map[string]any
	require.NoError(t, json.Unmarshal(action.Doc, &body))

	script, ok := body["script"].(map[string]any)
	require.True(t, ok)
	src := script["source"].(string)
	assert.Contains(t, src, "ctx._source.rooms.add")
	assert.Contains(t, src, "ctx.op = 'none'")
	assert.Contains(t, src, "roomTimestamps")
	assert.Contains(t, src, "params.ts")

	params := script["params"].(map[string]any)
	assert.Equal(t, "r-eng", params["rid"])
	assert.Equal(t, float64(ts), params["ts"])
	assert.NotEmpty(t, params["now"])

	upsert, ok := body["upsert"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "alice", upsert["userAccount"])
	rooms := upsert["rooms"].([]any)
	require.Len(t, rooms, 1)
	assert.Equal(t, "r-eng", rooms[0])

	roomTimestamps := upsert["roomTimestamps"].(map[string]any)
	assert.Equal(t, float64(ts), roomTimestamps["r-eng"])
	assert.NotEmpty(t, upsert["createdAt"])
}

func TestUserRoomCollection_BuildAction_MemberRemoved(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseInboxMemberEvent()
	const ts int64 = 1735689700000
	data := makeInboxMemberEvent(t, model.OutboxMemberRemoved, payload, ts)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)

	action := actions[0]
	assert.Equal(t, searchengine.ActionUpdate, action.Action)
	assert.Equal(t, "user-room-site-a", action.Index)
	assert.Equal(t, "alice", action.DocID)

	var body map[string]any
	require.NoError(t, json.Unmarshal(action.Doc, &body))

	script, ok := body["script"].(map[string]any)
	require.True(t, ok)
	src := script["source"].(string)
	assert.Contains(t, src, "ctx._source.rooms.remove")
	assert.Contains(t, src, "ctx.op = 'none'")
	assert.Contains(t, src, "roomTimestamps")
	assert.NotContains(t, src, "updatedAt")

	params := script["params"].(map[string]any)
	assert.Equal(t, "r-eng", params["rid"])
	assert.Equal(t, float64(ts), params["ts"])
	assert.NotContains(t, params, "now")
	assert.Len(t, params, 2)

	_, hasUpsert := body["upsert"]
	assert.False(t, hasUpsert, "remove update body must not contain upsert")
}

// TestUserRoomCollection_BuildAction_RestrictedRoomSkipped verifies the
// event-level restricted-room short-circuit: when HistorySharedSince is
// non-zero on the event, every account in the bulk is skipped (no actions).
// The search service handles restricted rooms via DB+cache at query time.
func TestUserRoomCollection_BuildAction_RestrictedRoomSkipped(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseInboxMemberEvent()
	payload.Accounts = []string{"alice", "bob"}
	payload.HistorySharedSince = 1735689500000

	data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, 100)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	assert.Empty(t, actions, "restricted-room event should produce no actions")
}

// TestUserRoomCollection_BuildAction_BulkInvite verifies fan-out: one event
// with N accounts produces N distinct user-room update actions (each keyed
// by a different account).
func TestUserRoomCollection_BuildAction_BulkInvite(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseInboxMemberEvent()
	payload.Accounts = []string{"alice", "bob", "carol"}
	data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, 12345)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 3, "3 accounts → 3 update actions")

	seenDocIDs := make(map[string]bool)
	for _, action := range actions {
		assert.Equal(t, searchengine.ActionUpdate, action.Action)
		assert.Equal(t, "user-room-site-a", action.Index)
		assert.Zero(t, action.Version)
		seenDocIDs[action.DocID] = true
	}
	assert.True(t, seenDocIDs["alice"])
	assert.True(t, seenDocIDs["bob"])
	assert.True(t, seenDocIDs["carol"])
}

func TestUserRoomCollection_BuildAction_Errors(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")

	t.Run("malformed outbox event", func(t *testing.T) {
		_, err := coll.BuildAction([]byte("{invalid"))
		assert.Error(t, err)
	})

	t.Run("malformed payload", func(t *testing.T) {
		data, _ := json.Marshal(map[string]any{"type": model.OutboxMemberAdded, "payload": "not-bytes"})
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("empty account in list", func(t *testing.T) {
		payload := baseInboxMemberEvent()
		payload.Accounts = []string{"alice", ""}
		data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("missing room id", func(t *testing.T) {
		payload := baseInboxMemberEvent()
		payload.RoomID = ""
		data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("empty accounts", func(t *testing.T) {
		payload := baseInboxMemberEvent()
		payload.Accounts = nil
		data := makeInboxMemberEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("missing timestamp", func(t *testing.T) {
		data := makeInboxMemberEvent(t, model.OutboxMemberAdded, baseInboxMemberEvent(), 0)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("unsupported event type", func(t *testing.T) {
		data := makeInboxMemberEvent(t, "room_deleted", baseInboxMemberEvent(), 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})
}
