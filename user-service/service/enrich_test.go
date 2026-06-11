package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestEnrichWithRoomInfo_LocalAndCrossSite(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen, UserCount: 5},
		{ID: "b", RoomID: "r2", SiteID: "site-b", LastSeenAt: &seen},
	}
	mentionAt := int64(200)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Eng", LastMsgAt: &newer, LastMentionAllAt: &mentionAt}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", []string{"r2"}).
		Return([]model.RoomInfo{{RoomID: "r2", Found: true, Name: "Ops", LastMsgAt: &newer}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.Equal(t, "Eng", subs[0].Name)
	assert.True(t, subs[0].Alert)         // lastMsgAt 200 > lastSeen 100
	assert.True(t, subs[0].HasMention)    // lastMentionAllAt 200 > lastSeen 100
	assert.Equal(t, 5, subs[0].UserCount) // preserved from $lookup
	assert.Equal(t, "Ops", subs[1].Name)
	assert.True(t, subs[1].Alert)
}

func TestEnrichWithRoomInfo_NotFoundKeepsSub(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	subs := []model.Subscription{{ID: "a", RoomID: "r1", SiteID: "site-a"}}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: false}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.Len(t, subs, 1)
	assert.Empty(t, subs[0].Name)
	assert.False(t, subs[0].Alert)
}

func TestEnrichWithRoomInfo_RPCFailDegradesSiteKeepsOthers(t *testing.T) {
	svc, _, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs := []model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{ID: "b", RoomID: "r2", SiteID: "site-b", LastSeenAt: &seen},
	}
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).Return(nil, errors.New("down"))
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", []string{"r2"}).
		Return([]model.RoomInfo{{RoomID: "r2", Found: true, Name: "Ops", LastMsgAt: &newer}}, nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), subs)
	assert.Empty(t, subs[0].Name) // site-a degraded
	assert.False(t, subs[0].Alert)
	assert.Equal(t, "Ops", subs[1].Name) // site-b still enriched
	assert.True(t, subs[1].Alert)
}

func TestEnrichWithRoomInfo_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	// No GetRoomsInfo expectation: empty input must short-circuit before any RPC.
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), nil)
	svc.enrichWithRoomInfo(ctx("alice", "site-a"), []model.Subscription{})
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
	assert.Equal(t, "Eng", subs[0].Name)
	assert.Equal(t, "Eng", subs[1].Name) // both subs enriched from the single deduped RPC
}
