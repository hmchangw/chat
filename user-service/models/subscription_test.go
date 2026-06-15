package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestSubscriptionListRequest_RoundTrip(t *testing.T) {
	fav := true
	days := 7
	in := SubscriptionListRequest{Type: "rooms", Favorite: &fav, UpdatedWithinDays: &days}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out SubscriptionListRequest
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestSubscriptionListResponse_RoundTrip(t *testing.T) {
	joined := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	in := SubscriptionListResponse{
		Subscriptions: []model.Subscription{
			{ID: "s1", RoomID: "r1", SiteID: "site-a", Name: "General", JoinedAt: joined},
		},
		Total: 1,
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out SubscriptionListResponse
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestGetChannelsRequest_RoundTrip(t *testing.T) {
	in := GetChannelsRequest{AccountNames: []string{"alice", "bob"}}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out GetChannelsRequest
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestDMResponse_RoundTrip(t *testing.T) {
	joined := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	in := DMResponse{
		Subscription: model.DMSubscription{
			Subscription: &model.Subscription{ID: "d1", RoomID: "r1", SiteID: "site-a", JoinedAt: joined},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "Bob Chen", EngName: "Bob"},
		},
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out DMResponse
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestCountResponse_RoundTrip(t *testing.T) {
	in := CountResponse{Count: 42}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out CountResponse
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestGetDMRequest_RoundTrip(t *testing.T) {
	in := GetDMRequest{AccountName: "bob"}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out GetDMRequest
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestGetByRoomIDRequest_RoundTrip(t *testing.T) {
	in := GetByRoomIDRequest{RoomID: "r1"}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out GetByRoomIDRequest
	require.NoError(t, json.Unmarshal(b, &out))
	require.Equal(t, in, out)
}

func TestCountRequest_RoundTrip(t *testing.T) {
	t.Run("UnreadNil omits key", func(t *testing.T) {
		in := CountRequest{Unread: nil}
		b, err := json.Marshal(in)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(b, &raw))
		_, present := raw["unread"]
		require.False(t, present, "unread key must be absent when Unread is nil")
		var out CountRequest
		require.NoError(t, json.Unmarshal(b, &out))
		require.Equal(t, in, out)
	})

	t.Run("UnreadTrue round-trips to non-nil true", func(t *testing.T) {
		unread := true
		in := CountRequest{Unread: &unread}
		b, err := json.Marshal(in)
		require.NoError(t, err)
		var out CountRequest
		require.NoError(t, json.Unmarshal(b, &out))
		require.Equal(t, in, out)
		require.NotNil(t, out.Unread, "Unread must not be nil after round-trip")
		require.True(t, *out.Unread, "Unread must be true after round-trip")
	})

	t.Run("UnreadFalse preserves explicit false on the wire", func(t *testing.T) {
		in := CountRequest{Unread: ptrBool(false)}
		b, err := json.Marshal(in)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(b, &raw))
		val, present := raw["unread"]
		require.True(t, present, "non-nil Unread must be present even when false")
		require.Equal(t, false, val, "explicit false must be preserved on the wire")
		var out CountRequest
		require.NoError(t, json.Unmarshal(b, &out))
		require.Equal(t, in, out)
		require.NotNil(t, out.Unread, "Unread must not be nil after round-trip")
		require.False(t, *out.Unread, "Unread must be false after round-trip")
	})
}
