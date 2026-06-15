package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestEnrichWithRoomInfo_LocalAndCrossSite(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", Name: "eng-sub", LastSeenAt: &seen, UserCount: 5, Alert: true, HasMention: true},
		{ID: "b", RoomID: "r2", SiteID: "site-b", LastSeenAt: &seen},
	}
	mentionAt := int64(200)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Eng", UserCount: 7, LastMsgAt: &newer, LastMsgID: "m-7", LastMentionAllAt: &mentionAt}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", []string{"r2"}).
		Return([]model.RoomInfo{{RoomID: "r2", Found: true, Name: "Ops", UserCount: 3, LastMsgAt: &newer, LastMsgID: "m-3"}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.Equal(t, "eng-sub", subs[0].Name, "subscription name must survive enrichment")
	assert.True(t, subs[0].Alert, "stored alert preserved")
	assert.True(t, subs[0].HasMention, "stored hasMention preserved")
	require.NotNil(t, subs[0].Room)
	assert.Equal(t, "Eng", subs[0].Room.Name)
	assert.Equal(t, 7, subs[0].Room.UserCount) // RPC value, not the $lookup baseline (5)
	assert.Equal(t, "m-7", subs[0].Room.LastMsgID)
	require.NotNil(t, subs[1].Room)
	assert.Equal(t, "Ops", subs[1].Room.Name)
	assert.False(t, subs[1].Alert, "no stored alert ⇒ stays false; never set from room data")
	assert.Equal(t, 3, subs[1].Room.UserCount) // cross-site sub now gets room fields via RPC
	assert.Equal(t, "m-3", subs[1].Room.LastMsgID)
}

// TestEnrichWithRoomInfo_RPCZeroFields pins the nested contract: a found room's RPC
// entry is authoritative for the room object even when fields are zero — the $lookup
// baseline stays on the internal flattened fields only.
func TestEnrichWithRoomInfo_RPCZeroFields(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	subs := []model.Subscription{{ID: "a", RoomID: "r1", SiteID: "site-a", UserCount: 5, LastMsgID: "m-base"}}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Eng"}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	require.NotNil(t, subs[0].Room)
	assert.Equal(t, "Eng", subs[0].Room.Name)
	assert.Equal(t, 5, subs[0].UserCount, "internal baseline untouched")
	assert.Equal(t, "m-base", subs[0].LastMsgID, "internal baseline untouched")
}

func TestEnrichWithRoomInfo_NotFoundFallsBackToBaseline(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	subs := []model.Subscription{{ID: "a", RoomID: "r1", SiteID: "site-a", UserCount: 5, LastMsgID: "m-base"}}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: false}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.Len(t, subs, 1)
	assert.False(t, subs[0].Alert)
	require.NotNil(t, subs[0].Room, "not-found room must still yield a baseline room object")
	assert.Empty(t, subs[0].Room.Name)
	assert.Equal(t, 5, subs[0].Room.UserCount)
	assert.Equal(t, "m-base", subs[0].Room.LastMsgID)
}

func TestEnrichWithRoomInfo_RPCFailDegradesSiteKeepsOthers(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{ID: "b", RoomID: "r2", SiteID: "site-b", LastSeenAt: &seen, Alert: true},
	}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).Return(nil, errors.New("down"))
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", []string{"r2"}).
		Return([]model.RoomInfo{{RoomID: "r2", Found: true, Name: "Ops", LastMsgAt: &newer}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	require.NotNil(t, subs[0].Room, "degraded site falls back to the baseline room object")
	assert.Empty(t, subs[0].Room.Name)
	assert.False(t, subs[0].Alert, "no stored alert ⇒ stays false")
	require.NotNil(t, subs[1].Room)
	assert.Equal(t, "Ops", subs[1].Room.Name) // site-b still enriched
	assert.True(t, subs[1].Alert, "stored alert preserved through enrichment")
}

// TestEnrichWithRoomInfo_CrossSiteDegraded_SiteIDOnly pins that a degraded CROSS-SITE
// row gets a baseline room with ONLY siteId — the local Mongo $lookup has no room doc
// for cross-site rooms, so userCount/lastMsgId/etc. would be meaningless and must be
// dropped. A degraded LOCAL row keeps its baseline values.
func TestEnrichWithRoomInfo_CrossSiteDegraded_SiteIDOnly(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	lastMsg := time.UnixMilli(400).UTC()
	mention := time.UnixMilli(300).UTC()
	subs := []model.Subscription{
		// local degraded row — keeps baseline.
		{ID: "loc", RoomID: "r1", SiteID: "site-a", UserCount: 9, LastMsgAt: &lastMsg, LastMsgID: "m1", LastMentionAllAt: &mention},
		// cross-site degraded row — baseline values must be dropped.
		{ID: "remote", RoomID: "r2", SiteID: "site-b", UserCount: 4, LastMsgAt: &lastMsg, LastMsgID: "m2", LastMentionAllAt: &mention},
	}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).Return(nil, errors.New("down"))
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", []string{"r2"}).Return(nil, errors.New("down"))
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)

	require.NotNil(t, subs[0].Room)
	assert.Equal(t, "site-a", subs[0].Room.SiteID)
	assert.Equal(t, 9, subs[0].Room.UserCount, "local degraded row keeps baseline userCount")
	assert.Equal(t, "m1", subs[0].Room.LastMsgID, "local degraded row keeps baseline lastMsgId")
	require.NotNil(t, subs[0].Room.LastMsgAt)
	require.NotNil(t, subs[0].Room.LastMentionAllAt)

	require.NotNil(t, subs[1].Room)
	assert.Equal(t, "site-b", subs[1].Room.SiteID, "cross-site degraded row carries siteId")
	assert.Zero(t, subs[1].Room.UserCount, "cross-site degraded row must NOT carry baseline userCount")
	assert.Empty(t, subs[1].Room.LastMsgID, "cross-site degraded row must NOT carry baseline lastMsgId")
	assert.Nil(t, subs[1].Room.LastMsgAt, "cross-site degraded row must NOT carry baseline lastMsgAt")
	assert.Nil(t, subs[1].Room.LastMentionAllAt, "cross-site degraded row must NOT carry baseline lastMentionAllAt")
}

func TestEnrichWithRoomInfo_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	// No GetRoomsInfo expectation: empty input must short-circuit before any RPC.
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), []model.Subscription{})
}

func TestEnrichWithRoomInfo_DegradedSite_PreservesStoredFlags(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	lastMsg := time.UnixMilli(200).UTC()
	mention := time.UnixMilli(300).UTC()
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", Alert: true, HasMention: true,
			UserCount: 4, LastMsgAt: &lastMsg, LastMsgID: "m1", LastMentionAllAt: &mention},
	}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).Return(nil, errors.New("down"))
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	require.NotNil(t, subs[0].Room)
	require.NotNil(t, subs[0].Room.LastMentionAllAt, "baseline mention surfaces on the room object")
	assert.Equal(t, mention.UnixMilli(), subs[0].Room.LastMentionAllAt.UnixMilli())
	assert.True(t, subs[0].Alert, "stored alert preserved on a degraded site")
	assert.True(t, subs[0].HasMention, "stored hasMention preserved on a degraded site")
}

// TestEnrichWithRoomInfo_NeverRecomputesFlags pins the contract that enrichment
// leaves alert/hasMention alone even when room timestamps would imply otherwise:
// the stored values are the opposite of what a lastMsgAt/lastSeenAt compare gives.
func TestEnrichWithRoomInfo_NeverRecomputesFlags(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(999)
	mentionAt := int64(999)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen, Alert: false, HasMention: false},
	}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Eng", LastMsgAt: &newer, LastMentionAllAt: &mentionAt}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.False(t, subs[0].Alert, "room lastMsgAt newer than lastSeen must NOT flip stored alert")
	assert.False(t, subs[0].HasMention, "room lastMentionAllAt newer than lastSeen must NOT flip stored hasMention")
}

func TestUnread(t *testing.T) {
	seen := time.UnixMilli(100).UTC()
	older := int64(50)
	newer := int64(200)
	cases := []struct {
		name     string
		lastSeen *time.Time
		ms       *int64
		want     bool
	}{
		{"nil ms is never unread", &seen, nil, false},
		{"nil lastSeen with msg is unread", nil, &newer, true},
		{"msg newer than lastSeen is unread", &seen, &newer, true},
		{"msg older than lastSeen is read", &seen, &older, false},
		{"msg equal to lastSeen is read", &seen, ptrInt64(seen.UTC().UnixMilli()), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, unread(tc.lastSeen, tc.ms))
		})
	}
}

func ptrInt64(v int64) *int64 { return &v }

func TestEnrichWithRoomInfo_DedupsRoomIDs(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{ID: "b", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen}, // same room, second sub
	}
	// EXPECT exactly ["r1"], not ["r1","r1"] — gomock fails the call on arg mismatch.
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Eng", LastMsgAt: &newer}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	require.NotNil(t, subs[0].Room)
	require.NotNil(t, subs[1].Room)
	assert.Equal(t, "Eng", subs[0].Room.Name)
	assert.Equal(t, "Eng", subs[1].Room.Name) // both subs enriched from the single deduped RPC
}
