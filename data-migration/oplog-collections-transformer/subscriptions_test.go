package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/migration"
	"github.com/hmchangw/chat/pkg/model"
)

func subEv(op, doc, updateDesc string) oplogEvent {
	ev := oplogEvent{Op: op, Collection: subsColl, EventID: "se1"}
	if doc != "" {
		ev.FullDocument = json.RawMessage(doc)
	}
	if updateDesc != "" {
		ev.UpdateDescription = json.RawMessage(updateDesc)
	}
	ev.DocumentKey = json.RawMessage(`{"_id":"sub1"}`)
	return ev
}

// eventsByType groups the published events by their InboxEvent.Type.
func eventsByType(evts []model.InboxEvent) map[model.InboxEventType]model.InboxEvent {
	m := make(map[model.InboxEventType]model.InboxEvent, len(evts))
	for _, e := range evts {
		m[e.Type] = e
	}
	return m
}

// A full source subscription doc: owner role, muted, favorited, alert, ls < lr.
const fullSubDoc = `{
	"_id":"sub1",
	"u":{"_id":"u1","username":"alice"},
	"rid":"r1",
	"t":"c",
	"name":"general",
	"fname":"General",
	"roles":["owner"],
	"open":true,
	"f":true,
	"disableNotifications":true,
	"alert":true,
	"ls":{"$date":"2024-01-15T09:00:00.000Z"},
	"lr":{"$date":"2024-01-15T10:00:00.000Z"},
	"ts":{"$date":"2024-01-01T00:00:00.000Z"}
}`

// lrMillis is max(ls,lr) for fullSubDoc — lr (10:00Z) is later than ls (09:00Z).
const lrMillis = int64(1705312800000) // 2024-01-15T10:00:00Z
// tsMillis is the JoinedAt for fullSubDoc.
const tsMillis = int64(1704067200000) // 2024-01-01T00:00:00Z

func TestHandleSubscription_Insert_AllEvents(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	err := h.handleSubscription(context.Background(), subEv("insert", fullSubDoc, ""))
	require.NoError(t, err)

	require.Len(t, pub.events, 5)

	// 1. member_added (first in order).
	assert.Equal(t, model.InboxMemberAdded, pub.events[0].Type)
	var ma model.MemberAddEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &ma))
	assert.Equal(t, "member_added", ma.Type)
	assert.Equal(t, "r1", ma.RoomID)
	assert.Equal(t, []string{"alice"}, ma.Accounts)
	assert.Equal(t, model.RoomTypeChannel, ma.RoomType)
	assert.Equal(t, "General", ma.RoomName)
	assert.Equal(t, testSiteID, ma.SiteID)
	assert.Equal(t, tsMillis, ma.JoinedAt)

	byType := eventsByType(pub.events)

	// 2. role_updated (owner → owner).
	roleEvt, ok := byType[model.InboxEventType("role_updated")]
	require.True(t, ok)
	var su model.SubscriptionUpdateEvent
	require.NoError(t, json.Unmarshal(roleEvt.Payload, &su))
	assert.Equal(t, "alice", su.Subscription.User.Account)
	assert.Equal(t, "r1", su.Subscription.RoomID)
	assert.Equal(t, []model.Role{model.RoleOwner}, su.Subscription.Roles)

	// 3. mute (from disableNotifications=true).
	muteEvt, ok := byType[model.InboxSubscriptionMuteToggled]
	require.True(t, ok)
	var mute model.SubscriptionMuteToggledEvent
	require.NoError(t, json.Unmarshal(muteEvt.Payload, &mute))
	assert.Equal(t, "alice", mute.Account)
	assert.Equal(t, "r1", mute.RoomID)
	assert.True(t, mute.Muted)

	// 4. favorite (f=true).
	favEvt, ok := byType[model.InboxSubscriptionFavoriteToggled]
	require.True(t, ok)
	var fav model.SubscriptionFavoriteToggledEvent
	require.NoError(t, json.Unmarshal(favEvt.Payload, &fav))
	assert.True(t, fav.Favorite)

	// 5. subscription_read (LastSeenAt = max(ls,lr), Alert).
	readEvt, ok := byType[model.InboxSubscriptionRead]
	require.True(t, ok)
	var read model.SubscriptionReadEvent
	require.NoError(t, json.Unmarshal(readEvt.Payload, &read))
	assert.Equal(t, "alice", read.Account)
	assert.Equal(t, "r1", read.RoomID)
	assert.Equal(t, lrMillis, read.LastSeenAt)
	assert.True(t, read.Alert)

	// All envelopes carry the home site + local dest.
	for _, e := range pub.events {
		assert.Equal(t, testSiteID, e.SiteID)
		assert.Equal(t, testSiteID, e.DestSiteID)
	}
}

func TestHandleSubscription_Insert_EmptyRoles_NoRoleEvent(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	doc := `{"_id":"sub1","u":{"_id":"u1","username":"alice"},"rid":"r1","t":"c","fname":"General",
		"open":true,"f":false,"disableNotifications":false,"alert":false,
		"ls":{"$date":"2024-01-15T09:00:00.000Z"},"lr":{"$date":"2024-01-15T09:00:00.000Z"},
		"ts":{"$date":"2024-01-01T00:00:00.000Z"}}`
	err := h.handleSubscription(context.Background(), subEv("insert", doc, ""))
	require.NoError(t, err)

	require.Len(t, pub.events, 4)
	byType := eventsByType(pub.events)
	_, hasRole := byType[model.InboxEventType("role_updated")]
	assert.False(t, hasRole)
	_, hasAdd := byType[model.InboxMemberAdded]
	assert.True(t, hasAdd)
}

func TestHandleSubscription_Insert_FederatedSiteID(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	doc := `{"_id":"sub1","u":{"_id":"u1","username":"alice"},"rid":"r1","t":"c","fname":"General",
		"roles":["owner"],"open":true,
		"federation":{"origin":"0030204.tchat-test.test.company.com"},
		"ts":{"$date":"2024-01-01T00:00:00.000Z"}}`
	err := h.handleSubscription(context.Background(), subEv("insert", doc, ""))
	require.NoError(t, err)

	require.NotEmpty(t, pub.events)
	for _, e := range pub.events {
		assert.Equal(t, "0030204", e.SiteID)
	}
	var ma model.MemberAddEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &ma))
	assert.Equal(t, "0030204", ma.SiteID)
}

func TestHandleSubscription_Update_FavoriteOnly(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(fullSubDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "", `{"updatedFields":{"f":true}}`))
	require.NoError(t, err)

	require.Len(t, pub.events, 1)
	assert.Equal(t, model.InboxSubscriptionFavoriteToggled, pub.events[0].Type)
	var fav model.SubscriptionFavoriteToggledEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &fav))
	assert.True(t, fav.Favorite)
}

func TestHandleSubscription_Update_OpenFalse_MemberRemoved(t *testing.T) {
	pub := &fakePublisher{}
	closedDoc := `{"_id":"sub1","u":{"_id":"u1","username":"alice"},"rid":"r1","t":"c","fname":"General","open":false}`
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(closedDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "", `{"updatedFields":{"open":false}}`))
	require.NoError(t, err)

	require.Len(t, pub.events, 1)
	assert.Equal(t, model.InboxMemberRemoved, pub.events[0].Type)
	var mr model.MemberRemoveEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &mr))
	assert.Equal(t, "member_removed", mr.Type)
	assert.Equal(t, "r1", mr.RoomID)
	assert.Equal(t, []string{"alice"}, mr.Accounts)
	assert.Equal(t, testSiteID, mr.SiteID)
}

func TestHandleSubscription_Update_OpenTrue_Resubscribe(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(fullSubDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "", `{"updatedFields":{"open":true}}`))
	require.NoError(t, err)

	// Re-subscribe rebuilds full state: same 5 events as an insert.
	require.Len(t, pub.events, 5)
	byType := eventsByType(pub.events)
	_, hasAdd := byType[model.InboxMemberAdded]
	assert.True(t, hasAdd)
	_, hasRole := byType[model.InboxEventType("role_updated")]
	assert.True(t, hasRole)
	_, hasRead := byType[model.InboxSubscriptionRead]
	assert.True(t, hasRead)
}

func TestHandleSubscription_Update_ReadField(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(fullSubDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "", `{"updatedFields":{"ls":{"$date":"2024-01-15T09:00:00.000Z"}}}`))
	require.NoError(t, err)

	require.Len(t, pub.events, 1)
	assert.Equal(t, model.InboxSubscriptionRead, pub.events[0].Type)
	var read model.SubscriptionReadEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &read))
	assert.Equal(t, lrMillis, read.LastSeenAt) // max(ls,lr) from current doc
	assert.True(t, read.Alert)
}

func TestHandleSubscription_Update_UnrecognizedField_Skip(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(fullSubDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "", `{"updatedFields":{"_updatedAt":{"$date":"2024-01-15T09:00:00.000Z"}}}`))
	assert.ErrorIs(t, err, migration.ErrSkipped)
	assert.Empty(t, pub.events)
}

func TestHandleSubscription_Delete_EmitsSubscriptionDeleted(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	err := h.handleSubscription(context.Background(), subEv("delete", "", ""))
	require.NoError(t, err)

	require.Len(t, pub.events, 1)
	evt := pub.events[0]
	assert.Equal(t, model.InboxSubscriptionDeleted, evt.Type)
	assert.Equal(t, testSiteID, evt.DestSiteID)

	var del model.SubscriptionDeletedEvent
	require.NoError(t, json.Unmarshal(evt.Payload, &del))
	assert.Equal(t, "sub1", del.SubID) // documentKey._id from subEv
}

func TestHandleSubscription_Delete_BadDocumentKey_Poison(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	ev := oplogEvent{Op: "delete", Collection: subsColl, EventID: "se1", DocumentKey: json.RawMessage(`{}`)}
	err := h.handleSubscription(context.Background(), ev)
	assert.ErrorIs(t, err, migration.ErrPoison)
	assert.Empty(t, pub.events)
}

func TestHandleSubscription_Insert_MemberAddedCarriesSubID(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	require.NoError(t, h.handleSubscription(context.Background(), subEv("insert", fullSubDoc, "")))

	require.NotEmpty(t, pub.events)
	var ma model.MemberAddEvent
	require.NoError(t, json.Unmarshal(pub.events[0].Payload, &ma))
	assert.Equal(t, "sub1", ma.SubID) // source subscription _id from fullSubDoc
}

func TestHandleSubscription_MaxLsLr(t *testing.T) {
	tests := []struct {
		name string
		ls   string
		lr   string
		want int64
	}{
		{
			name: "lr later than ls",
			ls:   "2024-01-15T09:00:00.000Z",
			lr:   "2024-01-15T10:00:00.000Z",
			want: 1705312800000, // 10:00Z
		},
		{
			name: "ls later than lr",
			ls:   "2024-01-15T11:00:00.000Z",
			lr:   "2024-01-15T10:00:00.000Z",
			want: 1705316400000, // 11:00Z
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pub := &fakePublisher{}
			h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})
			doc := `{"_id":"sub1","u":{"_id":"u1","username":"alice"},"rid":"r1","t":"c","fname":"General","open":true,` +
				`"alert":true,"ls":{"$date":"` + tc.ls + `"},"lr":{"$date":"` + tc.lr + `"},` +
				`"ts":{"$date":"2024-01-01T00:00:00.000Z"}}`
			err := h.handleSubscription(context.Background(), subEv("insert", doc, ""))
			require.NoError(t, err)

			byType := eventsByType(pub.events)
			readEvt, ok := byType[model.InboxSubscriptionRead]
			require.True(t, ok)
			var read model.SubscriptionReadEvent
			require.NoError(t, json.Unmarshal(readEvt.Payload, &read))
			assert.Equal(t, tc.want, read.LastSeenAt)
		})
	}
}

func TestHandleSubscription_Update_RolesAndMute(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{doc: json.RawMessage(fullSubDoc)})

	err := h.handleSubscription(context.Background(), subEv("update", "",
		`{"updatedFields":{"roles":["owner"],"disableNotifications":true}}`))
	require.NoError(t, err)

	require.Len(t, pub.events, 2)
	byType := eventsByType(pub.events)
	_, hasRole := byType[model.InboxEventType("role_updated")]
	assert.True(t, hasRole)
	_, hasMute := byType[model.InboxSubscriptionMuteToggled]
	assert.True(t, hasMute)
}

func TestHandleSubscription_Replace_AllEvents(t *testing.T) {
	pub := &fakePublisher{}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	err := h.handleSubscription(context.Background(), subEv("replace", fullSubDoc, ""))
	require.NoError(t, err)
	assert.Len(t, pub.events, 5)
}

func TestHandleSubscription_PublishError(t *testing.T) {
	pub := &fakePublisher{err: errors.New("inbox down")}
	h := newTestHandler(pub, &fakeTarget{}, &fakeLookup{})

	err := h.handleSubscription(context.Background(), subEv("insert", fullSubDoc, ""))
	require.Error(t, err)
	assert.NotErrorIs(t, err, migration.ErrSkipped)
}
