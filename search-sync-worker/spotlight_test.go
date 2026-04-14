package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

func makeMemberAddedEvent(t *testing.T, typ string, payload *model.MemberAddedPayload, ts int64) []byte {
	t.Helper()
	payloadData, err := json.Marshal(payload)
	require.NoError(t, err)
	evt := model.OutboxEvent{
		Type:       typ,
		SiteID:     "site-a",
		DestSiteID: "site-a",
		Payload:    payloadData,
		Timestamp:  ts,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	return data
}

func baseMemberAddedPayload() *model.MemberAddedPayload {
	joinedAt := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	return &model.MemberAddedPayload{
		Subscription: model.Subscription{
			ID:       "sub-1",
			User:     model.SubscriptionUser{ID: "u-alice", Account: "alice"},
			RoomID:   "r-eng",
			SiteID:   "site-a",
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: joinedAt,
		},
		Room: model.Room{
			ID:        "r-eng",
			Name:      "engineering",
			Type:      model.RoomTypeGroup,
			CreatedBy: "u-admin",
			SiteID:    "site-a",
		},
	}
}

func TestSpotlightCollection_Metadata(t *testing.T) {
	coll := newSpotlightCollection("spotlight-site-a-v1-chat")

	assert.Equal(t, "spotlight-sync", coll.ConsumerName())
	assert.Equal(t, "spotlight_template", coll.TemplateName())

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

func TestSpotlightCollection_TemplateBody(t *testing.T) {
	coll := newSpotlightCollection("spotlight-site-a-v1-chat")
	body := coll.TemplateBody()
	require.NotNil(t, body)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	patterns, ok := parsed["index_patterns"].([]any)
	require.True(t, ok)
	// Template targets the exact configured index name so a custom
	// SPOTLIGHT_INDEX value still receives the right mapping.
	assert.Equal(t, "spotlight-site-a-v1-chat", patterns[0])

	tmpl := parsed["template"].(map[string]any)
	mappings := tmpl["mappings"].(map[string]any)
	props := mappings["properties"].(map[string]any)
	assert.Contains(t, props, "subscriptionId")
	assert.Contains(t, props, "userAccount")
	assert.Contains(t, props, "roomId")
	assert.Contains(t, props, "roomName")
	assert.Contains(t, props, "joinedAt")
	assert.Equal(t, false, mappings["dynamic"])

	roomName := props["roomName"].(map[string]any)
	assert.Equal(t, "search_as_you_type", roomName["type"])
	assert.Equal(t, "custom_analyzer", roomName["analyzer"])
}

func TestSpotlightTemplateProperties_MatchesStruct(t *testing.T) {
	props := esPropertiesFromStruct[SpotlightSearchIndex]()

	typ := reflect.TypeOf(SpotlightSearchIndex{})
	esFieldCount := 0
	for i := range typ.NumField() {
		field := typ.Field(i)
		esTag := field.Tag.Get("es")
		if esTag == "" || esTag == "-" {
			continue
		}
		esFieldCount++
		jsonTag := field.Tag.Get("json")
		name, _, _ := strings.Cut(jsonTag, ",")
		_, ok := props[name]
		assert.True(t, ok, "template missing property for field %s (json %s)", field.Name, name)
	}
	assert.Equal(t, esFieldCount, len(props))
}

func TestSpotlightCollection_BuildAction_MemberAdded(t *testing.T) {
	coll := newSpotlightCollection("spotlight-site-a-v1-chat")
	payload := baseMemberAddedPayload()
	data := makeMemberAddedEvent(t, model.OutboxMemberAdded, payload, 1000)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)

	action := actions[0]
	assert.Equal(t, searchengine.ActionIndex, action.Action)
	assert.Equal(t, "spotlight-site-a-v1-chat", action.Index)
	assert.Equal(t, "sub-1", action.DocID)
	assert.Equal(t, int64(1000), action.Version)
	require.NotNil(t, action.Doc)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(action.Doc, &doc))
	assert.Equal(t, "sub-1", doc["subscriptionId"])
	assert.Equal(t, "u-alice", doc["userId"])
	assert.Equal(t, "alice", doc["userAccount"])
	assert.Equal(t, "r-eng", doc["roomId"])
	assert.Equal(t, "engineering", doc["roomName"])
	assert.Equal(t, "group", doc["roomType"])
	assert.Equal(t, "site-a", doc["siteId"])
}

func TestSpotlightCollection_BuildAction_MemberRemoved(t *testing.T) {
	coll := newSpotlightCollection("spotlight-site-a-v1-chat")
	payload := baseMemberAddedPayload()
	data := makeMemberAddedEvent(t, model.OutboxMemberRemoved, payload, 2000)

	actions, err := coll.BuildAction(data)
	require.NoError(t, err)
	require.Len(t, actions, 1)

	action := actions[0]
	assert.Equal(t, searchengine.ActionDelete, action.Action)
	assert.Equal(t, "spotlight-site-a-v1-chat", action.Index)
	assert.Equal(t, "sub-1", action.DocID)
	assert.Equal(t, int64(2000), action.Version)
	assert.Nil(t, action.Doc)
}

func TestSpotlightCollection_BuildAction_Errors(t *testing.T) {
	coll := newSpotlightCollection("spotlight-site-a-v1-chat")

	t.Run("malformed outbox event", func(t *testing.T) {
		_, err := coll.BuildAction([]byte("{invalid"))
		assert.Error(t, err)
	})

	t.Run("malformed payload", func(t *testing.T) {
		evt := model.OutboxEvent{
			Type:      model.OutboxMemberAdded,
			Payload:   []byte("not json"),
			Timestamp: 100,
		}
		data, _ := json.Marshal(evt)
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})

	t.Run("missing subscription id", func(t *testing.T) {
		payload := baseMemberAddedPayload()
		payload.Subscription.ID = ""
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
		data := makeMemberAddedEvent(t, "room_created", baseMemberAddedPayload(), 100) // intentionally invalid type
		_, err := coll.BuildAction(data)
		assert.Error(t, err)
	})
}
