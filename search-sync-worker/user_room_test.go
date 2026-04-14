package main

import (
	"encoding/json"
	"testing"
	"time"

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
	// Baseline stream config carries the two non-overlapping subject
	// patterns from pkg/stream.Inbox — local (`chat.inbox.{site}.*`) and
	// federated (`chat.inbox.{site}.aggregate.>`).
	assert.Equal(t, []string{
		"chat.inbox.site-a.*",
		"chat.inbox.site-a.aggregate.>",
	}, cfg.Subjects)
	// Cross-site Sources are a bootstrap concern layered on in main.go,
	// not on the collection's baseline stream config.
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
	// Template targets the exact configured index name so a custom
	// USER_ROOM_INDEX value still receives the right mapping.
	assert.Equal(t, "user-room-site-a", patterns[0])

	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	props := mappings["properties"].(map[string]any)

	assert.Contains(t, props, "userAccount")
	assert.Contains(t, props, "rooms")
	assert.Contains(t, props, "roomTimestamps")
	assert.Contains(t, props, "createdAt")
	assert.Contains(t, props, "updatedAt")

	// roomTimestamps MUST be flattened or new roomIds cause mapping explosion.
	rt := props["roomTimestamps"].(map[string]any)
	assert.Equal(t, "flattened", rt["type"])

	// rooms keeps its text + keyword multi-field shape so both match queries
	// and terms filters on rooms.keyword continue to work.
	rooms := props["rooms"].(map[string]any)
	assert.Equal(t, "text", rooms["type"])
}

func TestUserRoomCollection_BuildAction_MemberAdded(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseMemberAddedPayload()
	const ts int64 = 1735689600000
	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, ts)

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
	// LWW guard short-circuits stale events via ctx.op = 'none'.
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

	// Upsert seeds roomTimestamps so the first-ever add for a user starts
	// with a non-zero stored timestamp for that room.
	roomTimestamps := upsert["roomTimestamps"].(map[string]any)
	assert.Equal(t, float64(ts), roomTimestamps["r-eng"])
	assert.NotEmpty(t, upsert["createdAt"])
}

func TestUserRoomCollection_BuildAction_MemberRemoved(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseMemberAddedPayload()
	const ts int64 = 1735689700000
	data := makeMemberAddedEvent(t, model.OutboxMemberRemoved, payload, ts)

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
	// Remove path also participates in the LWW guard: a stale remove event
	// arriving after a newer add must not yank the room back out.
	assert.Contains(t, src, "ctx.op = 'none'")
	assert.Contains(t, src, "roomTimestamps")
	// No updatedAt stamp on remove — there's no other user-visible mutation.
	assert.NotContains(t, src, "updatedAt")

	params := script["params"].(map[string]any)
	assert.Equal(t, "r-eng", params["rid"])
	assert.Equal(t, float64(ts), params["ts"])
	// `now` is not needed on remove — no updatedAt to stamp.
	assert.NotContains(t, params, "now")
	assert.Len(t, params, 2)

	// No upsert for removal — if the user doc doesn't exist, there's nothing
	// to remove.
	_, hasUpsert := body["upsert"]
	assert.False(t, hasUpsert, "remove update body must not contain upsert")
}

func TestUserRoomCollection_BuildAction_RestrictedRoomSkipped(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseMemberAddedPayload()
	historyFrom := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	payload.Subscriptions[0].HistorySharedSince = &historyFrom

	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 100)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	assert.Empty(t, actions, "restricted rooms should be skipped")
}

// TestUserRoomCollection_BuildAction_BulkInvite verifies fan-out: one event
// with N subscriptions produces N distinct user-room update actions (each
// keyed by a different user account).
func TestUserRoomCollection_BuildAction_BulkInvite(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseBulkMemberAddedPayload([]struct {
		Account    string
		SubID      string
		Restricted bool
	}{
		{Account: "alice", SubID: "sub-1"},
		{Account: "bob", SubID: "sub-2"},
		{Account: "carol", SubID: "sub-3"},
	})
	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 12345)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 3, "3 unrestricted subscriptions → 3 update actions")

	seenDocIDs := make(map[string]bool)
	for _, action := range actions {
		assert.Equal(t, searchengine.ActionUpdate, action.Action)
		assert.Equal(t, "user-room-site-a", action.Index)
		// ActionUpdate intentionally carries no external version — ES _update
		// is read-modify-write and rejects version_type=external.
		assert.Zero(t, action.Version)
		seenDocIDs[action.DocID] = true
	}
	assert.True(t, seenDocIDs["alice"])
	assert.True(t, seenDocIDs["bob"])
	assert.True(t, seenDocIDs["carol"])
}

// TestUserRoomCollection_BuildAction_BulkInviteMixedRestricted verifies
// per-subscription restricted-room filtering: in a bulk invite where some
// users are restricted and others aren't, only the non-restricted
// subscriptions produce actions.
func TestUserRoomCollection_BuildAction_BulkInviteMixedRestricted(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseBulkMemberAddedPayload([]struct {
		Account    string
		SubID      string
		Restricted bool
	}{
		{Account: "alice", SubID: "sub-1", Restricted: false}, // should index
		{Account: "bob", SubID: "sub-2", Restricted: true},    // should skip
		{Account: "carol", SubID: "sub-3", Restricted: false}, // should index
		{Account: "dave", SubID: "sub-4", Restricted: true},   // should skip
	})
	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 12345)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 2, "only 2 unrestricted subscriptions should fan out")

	seenDocIDs := make(map[string]bool)
	for _, action := range actions {
		seenDocIDs[action.DocID] = true
	}
	assert.True(t, seenDocIDs["alice"])
	assert.True(t, seenDocIDs["carol"])
	assert.False(t, seenDocIDs["bob"], "restricted bob must be filtered out")
	assert.False(t, seenDocIDs["dave"], "restricted dave must be filtered out")
}

// TestUserRoomCollection_BuildAction_AllRestrictedIsNoOp verifies the
// filtered-empty shape: if every subscription in the event is restricted,
// BuildAction returns an empty slice (no error), which the handler acks
// without touching ES.
func TestUserRoomCollection_BuildAction_AllRestrictedIsNoOp(t *testing.T) {
	coll := newUserRoomCollection("user-room-site-a")
	payload := baseBulkMemberAddedPayload([]struct {
		Account    string
		SubID      string
		Restricted bool
	}{
		{Account: "alice", SubID: "sub-1", Restricted: true},
		{Account: "bob", SubID: "sub-2", Restricted: true},
	})
	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 100)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	assert.Empty(t, actions)
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

	t.Run("missing user account", func(t *testing.T) {
		payload := baseMemberAddedPayload()
		payload.Subscriptions[0].User.Account = ""
		data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("missing room id", func(t *testing.T) {
		payload := baseMemberAddedPayload()
		payload.Subscriptions[0].RoomID = ""
		data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("empty subscriptions", func(t *testing.T) {
		payload := baseMemberAddedPayload()
		payload.Subscriptions = nil
		data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 100)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("missing timestamp", func(t *testing.T) {
		data := makeMemberAddedEvent(t, model.OutboxMemberAdded, baseMemberAddedPayload(), 0)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("unsupported event type", func(t *testing.T) {
		data := makeMemberAddedEvent(t, "room_deleted", baseMemberAddedPayload(), 100) // intentionally invalid type
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})
}
