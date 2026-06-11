package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/user-service/models"
)

func TestListSubscriptions_Types(t *testing.T) {
	for _, typ := range []string{"current", "rooms", "apps"} {
		t.Run(typ, func(t *testing.T) {
			svc, subs, _, _, rooms, _ := newSvc(t)
			subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", typ, gomock.Any(), false, mongoutil.OffsetPageRequest{Offset: 0, Limit: 40}).
				Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{{ID: "s1"}}, Total: 1}, nil)
			rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: typ})
			require.NoError(t, err)
			assert.Equal(t, int64(1), resp.Total)
		})
	}
}

func TestListSubscriptions_PageBounds(t *testing.T) {
	cases := []struct {
		name    string
		offset  int
		limit   int
		wantOff int64
		wantLim int64
	}{
		{"omitted defaults to 40", 0, 0, 0, 40},
		{"explicit passthrough", 80, 25, 80, 25},
		{"limit capped at maxSubs", 0, 5000, 0, 1000},
		{"negatives clamped", -3, -9, 0, 40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false,
				mongoutil.OffsetPageRequest{Offset: tc.wantOff, Limit: tc.wantLim}).
				Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
			_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
				models.SubscriptionListRequest{Type: "rooms", Offset: tc.offset, Limit: tc.limit})
			require.NoError(t, err)
		})
	}
}

func TestListSubscriptions_TotalIsFullCount(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{
			Data:  []model.Subscription{{ID: "s1"}, {ID: "s2"}},
			Total: 57,
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "rooms"})
	require.NoError(t, err)
	assert.Equal(t, int64(57), resp.Total, "total must be the full filtered count, not the page length")
	assert.Len(t, resp.Subscriptions, 2)
}

func TestListSubscriptions_FavoriteForwarded(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), true, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"),
		models.SubscriptionListRequest{Type: "current", Favorite: ptrBool(true)})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.Total)
	assert.NotNil(t, resp.Subscriptions, "empty page must marshal to [] not null")
}

func TestListSubscriptions_FavoriteFalseNotForwarded(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
		models.SubscriptionListRequest{Type: "current", Favorite: ptrBool(false)})
	require.NoError(t, err)
}

func TestListSubscriptions_EnrichesPage(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{
			Data:  []model.Subscription{{ID: "s1", SiteID: "site-a", RoomID: "r1", Name: "Stale"}},
			Total: 1,
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "rooms"})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "Renamed", resp.Subscriptions[0].Name, "page rows must be room-info-enriched")
}

func TestListSubscriptions_BadType(t *testing.T) {
	for _, typ := range []string{"", "bogus"} {
		t.Run(typ, func(t *testing.T) {
			svc, _, _, _, _, _ := newSvc(t)
			_, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: typ})
			requireCode(t, err, errcode.CodeBadRequest)
		})
	}
}

func TestListSubscriptions_WithinDaysRange(t *testing.T) {
	cases := []struct {
		name string
		days int
		ok   bool
	}{
		{"negative rejected", -1, false},
		{"zero allowed", 0, true},
		{"max allowed", 3650, true},
		{"above max rejected", 3651, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			if tc.ok {
				subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "rooms", gomock.Any(), false, gomock.Any()).
					Return(mongoutil.OffsetPage[model.Subscription]{Data: []model.Subscription{}}, nil)
			}
			_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
				models.SubscriptionListRequest{Type: "rooms", UpdatedWithinDays: &tc.days})
			if tc.ok {
				require.NoError(t, err)
			} else {
				requireCode(t, err, errcode.CodeBadRequest)
			}
		})
	}
}

func TestListSubscriptions_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), false, gomock.Any()).
		Return(mongoutil.OffsetPage[model.Subscription]{}, errors.New("db down"))
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "current"})
	requireCode(t, err, errcode.CodeInternal)
}

func ptrBool(b bool) *bool { return &b }

func TestGetChannels_ExactlyOne(t *testing.T) {
	t.Run("both_empty", func(t *testing.T) {
		svc, _, _, _, _, _ := newSvc(t)
		_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{})
		requireCode(t, err, errcode.CodeBadRequest)
	})
	t.Run("both_set", func(t *testing.T) {
		svc, _, _, _, _, _ := newSvc(t)
		_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "x", AccountNames: []string{"y"}})
		requireCode(t, err, errcode.CodeBadRequest)
	})
}

func TestGetChannels_TooManyAccountNames(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	names := make([]string, maxAccountNames+1)
	for i := range names {
		names[i] = "u"
	}
	// No store expectation — the cap must reject before FindChannelsByMembers.
	_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: names})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetChannels_AccountNamesAtCap(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	names := make([]string, maxAccountNames)
	for i := range names {
		names[i] = "u"
	}
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", names, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: names})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Total)
}

func TestGetChannels_ByMembersContain(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Total)
}

func TestGetChannels_ByAccountNames(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol", "dave"}, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: []string{"carol", "dave"}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Total)
}

func TestGetChannels_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return(nil, errors.New("db down"))
	_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: ""})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetDM_InvalidTarget(t *testing.T) {
	for _, target := range []string{"p_system", "helper.bot", "p_", ".bot", "p_.bot"} {
		t.Run(target, func(t *testing.T) {
			svc, _, _, _, _, _ := newSvc(t)
			_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: target})
			requireCode(t, err, errcode.CodeBadRequest)
			assert.True(t, errcode.HasReason(err, errcode.UserInvalidDMTarget))
		})
	}
}

func TestGetDM_NotFound(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(nil, nil)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeNotFound)
	assert.True(t, errcode.HasReason(err, errcode.UserSubscriptionNotFound))
}

func TestGetDM_OK(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").
		Return(&model.DMSubscription{
			Subscription: &model.Subscription{ID: "d1"},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "bob", EngName: "Bob"},
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	require.NoError(t, err)
	assert.Equal(t, "d1", resp.Subscription.ID)
	assert.Equal(t, "Bob", resp.Subscription.HRInfo.EngName)
}

func TestGetDM_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(nil, errors.New("db down"))
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_NilEmbeddedSubscription(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(
		&model.DMSubscription{Subscription: nil, HRInfo: &model.SubscriptionHRInfo{Account: "bob"}},
		nil,
	)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_Enriched(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").
		Return(&model.DMSubscription{
			Subscription: &model.Subscription{ID: "d1", SiteID: "site-a", RoomID: "r1"},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "bob", EngName: "Bob"},
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", resp.Subscription.Name, "enriched name must propagate through GetDM write-back")
	require.NotNil(t, resp.Subscription.HRInfo, "HRInfo must survive the enrichment write-back")
	assert.Equal(t, "Bob", resp.Subscription.HRInfo.EngName)
}

func TestGetByRoomID_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	_, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: ""})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetByRoomID_NotFound(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").Return(nil, nil)
	resp, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), resp.Total)
	assert.Empty(t, resp.Subscriptions)
	assert.NotNil(t, resp.Subscriptions, "empty result must be a non-nil slice")
}

func TestGetByRoomID_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").Return(nil, errors.New("db down"))
	_, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetByRoomID_OK_Enriched(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{ID: "s1", SiteID: "site-a", RoomID: "r1", Name: "Stale"}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), resp.Total)
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "s1", resp.Subscriptions[0].ID)
	assert.Equal(t, "Renamed", resp.Subscriptions[0].Name, "enriched name must propagate through the 1-elem slice")
}

func TestGetChannels_Empty(t *testing.T) {
	for _, name := range []string{"nil_slice", "empty_slice"} {
		t.Run(name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			var returned []model.Subscription
			if name == "empty_slice" {
				returned = []model.Subscription{}
			}
			subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return(returned, nil)
			resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
			require.NoError(t, err)
			assert.Equal(t, int64(0), resp.Total)
		})
	}
}

func TestCount_Total(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(7, nil)
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{})
	require.NoError(t, err)
	assert.Equal(t, 7, resp.Count)
}

func TestCount_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(0, errors.New("db down"))
	_, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{})
	requireCode(t, err, errcode.CodeInternal)
}

func TestCountUnread_Happy(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(2, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).
		Return([]model.Subscription{{RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, LastMsgAt: &newer}}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Count)
}

func TestCountUnread_FallbackToTotal(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(5, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 5).
		Return([]model.Subscription{{RoomID: "r1", SiteID: "site-a"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.Any()).Return(nil, errors.New("down"))
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 5, resp.Count) // fell back to total
}

func TestCountUnread_GetActiveStoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(3, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 3).Return(nil, errors.New("db down"))
	yes := true
	_, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	requireCode(t, err, errcode.CodeInternal)
}

func TestCountUnread_MultiSite(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(4, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 4).Return([]model.Subscription{
		{RoomID: "ra1", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "ra2", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "rb1", SiteID: "site-b", LastSeenAt: &seen},
		{RoomID: "rb2", SiteID: "site-b", LastSeenAt: &seen},
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.InAnyOrder([]string{"ra1", "ra2"})).
		Return([]model.RoomInfo{
			{RoomID: "ra1", Found: true, LastMsgAt: &newer}, // unread
			{RoomID: "ra2", Found: true, LastMsgAt: nil},    // read
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", gomock.InAnyOrder([]string{"rb1", "rb2"})).
		Return([]model.RoomInfo{
			{RoomID: "rb1", Found: true, LastMsgAt: &newer}, // unread
			{RoomID: "rb2", Found: true, LastMsgAt: nil},    // read
		}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count, "one unread on site-a and one on site-b must sum to 2")
}

func TestCountUnread_AllRead(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(300).UTC()
	older := int64(100) // older than seen → not unread
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(2, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).Return([]model.Subscription{
		{RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "r2", SiteID: "site-a", LastSeenAt: &seen},
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.Any()).
		Return([]model.RoomInfo{
			{RoomID: "r1", Found: true, LastMsgAt: &older},
			{RoomID: "r2", Found: true, LastMsgAt: nil},
		}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
}

func TestCountUnread_EmptyActive(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(0, nil)
	// Zero active subs must short-circuit before GetActiveSubscriptions (min(0,maxSubs)=0 → rejected $limit:0).
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
}

func TestCountUnread_DedupsRoomIDs(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).Return([]model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{ID: "b", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen}, // same room
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, LastMsgAt: &newer}}, nil)
	resp, err := svc.countUnread(ctx("alice", "site-a"), "alice", 2)
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count) // both subs counted unread; RPC roomIDs deduped to ["r1"]
}

func TestCount_UnreadFalse(t *testing.T) {
	for _, name := range []string{"nil", "false"} {
		t.Run(name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(9, nil)
			// No GetActiveSubscriptions expectation — short-circuit must fire before calling it.
			var unreadPtr *bool
			if name == "false" {
				f := false
				unreadPtr = &f
			}
			resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: unreadPtr})
			require.NoError(t, err)
			assert.Equal(t, 9, resp.Count)
		})
	}
}
