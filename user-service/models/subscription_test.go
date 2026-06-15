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
		Subscriptions: []SubscriptionListItem{
			{Subscription: &model.Subscription{ID: "s1", RoomID: "r1", SiteID: "site-a", Name: "General", JoinedAt: joined}},
		},
		Total: 1,
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	var out SubscriptionListResponse
	require.NoError(t, json.Unmarshal(b, &out))
	require.Len(t, out.Subscriptions, 1)
	require.NotNil(t, out.Subscriptions[0].Subscription)
	require.Equal(t, "s1", out.Subscriptions[0].ID)
	require.Equal(t, "General", out.Subscriptions[0].Name)
	require.Nil(t, out.Subscriptions[0].AppMeta, "channel row carries no app overlay")
	require.Nil(t, out.Subscriptions[0].HRInfo, "channel row carries no hrInfo")
	require.Equal(t, 1, out.Total)
}

// TestSubscriptionListItem_HeterogeneousRows pins the wire shape per room type:
// channel = base only; dm adds top-level hrInfo; botDM flattens app metadata
// (appId/description/assistant/…) and carries NO hrInfo.
func TestSubscriptionListItem_HeterogeneousRows(t *testing.T) {
	t.Run("channel row is base only", func(t *testing.T) {
		item := SubscriptionListItem{
			Subscription: &model.Subscription{ID: "c1", RoomID: "rc1", SiteID: "site-a", Name: "general", RoomType: model.RoomTypeChannel},
		}
		raw := marshalToMap(t, item)
		require.Equal(t, "general", raw["name"])
		_, hasHR := raw["hrInfo"]
		require.False(t, hasHR, "channel row must not carry hrInfo")
		for _, k := range []string{"appId", "description", "assistant", "version"} {
			_, present := raw[k]
			require.False(t, present, "channel row must not carry app field %q", k)
		}
	})

	t.Run("dm row adds top-level hrInfo", func(t *testing.T) {
		item := SubscriptionListItem{
			Subscription: &model.Subscription{ID: "d1", RoomID: "rd1", SiteID: "site-a", Name: "bob", RoomType: model.RoomTypeDM},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "鮑勃", EngName: "Bob Chen"},
		}
		raw := marshalToMap(t, item)
		require.Equal(t, "bob", raw["name"])
		hr, ok := raw["hrInfo"].(map[string]any)
		require.True(t, ok, "dm row must carry a top-level hrInfo object")
		require.Equal(t, "鮑勃", hr["name"])
		require.Equal(t, "Bob Chen", hr["engName"])
		_, hasApp := raw["appId"]
		require.False(t, hasApp, "dm row must not carry app metadata")
	})

	t.Run("botDM row flattens app metadata and carries no hrInfo", func(t *testing.T) {
		item := SubscriptionListItem{
			Subscription: &model.Subscription{ID: "b1", RoomID: "rb1", SiteID: "site-a", Name: "Helper App", RoomType: model.RoomTypeBotDM},
			AppMeta: model.AppMetaFromApp(&model.App{
				ID:          "app-helper",
				Name:        "Helper App",
				Description: "does helpful things",
				AvatarURL:   "https://cdn/helper.png",
				Assistant:   &model.AppAssistant{Enabled: true, Name: "helper.bot", Username: "Helper"},
				Version:     "1.0.0",
			}),
		}
		raw := marshalToMap(t, item)
		require.Equal(t, "Helper App", raw["name"], "base subscription name carries the app display name")
		require.Equal(t, "app-helper", raw["appId"], "appId must flatten to the top level")
		require.Equal(t, "does helpful things", raw["description"])
		require.Equal(t, "1.0.0", raw["version"])
		assistant, ok := raw["assistant"].(map[string]any)
		require.True(t, ok, "assistant must flatten to the top level")
		require.Equal(t, "helper.bot", assistant["name"])
		_, hasHR := raw["hrInfo"]
		require.False(t, hasHR, "botDM row must not carry hrInfo")
	})
}

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(b, &raw))
	return raw
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
