package models

import "github.com/hmchangw/chat/pkg/model"

// SubscriptionListRequest is the body of subscription.list.
// Type ∈ {current, rooms, apps}. UpdatedWithinDays nil ⇒ no age filter.
type SubscriptionListRequest struct {
	Type              string `json:"type"`
	Favorite          *bool  `json:"favorite,omitempty"`
	UpdatedWithinDays *int   `json:"updatedWithinDays,omitempty"`
}

// SubscriptionListResponse is returned by subscription.list and subscription.getChannels.
type SubscriptionListResponse struct {
	Subscriptions []SubscriptionListItem `json:"subscriptions"`
	Total         int                    `json:"total"`
}

// SubscriptionListItem is one heterogeneous row in a subscription list:
//   - channel → just the embedded base Subscription
//   - dm      → base + a top-level hrInfo object
//   - botDM   → base + the app-metadata overlay flattened at top level
//
// The embedded *Subscription promotes all base fields/JSON. A nil embedded
// *AppMeta is omitted by encoding/json; a non-nil one flattens its app fields
// (appId, description, assistant, …) to the top level. There are no json-tag
// collisions between Subscription, AppMeta, and hrInfo.
type SubscriptionListItem struct {
	*model.Subscription
	*model.AppMeta
	HRInfo *model.SubscriptionHRInfo `json:"hrInfo,omitempty"`
}

// GetChannelsRequest is the body of subscription.getChannels (exactly one of the two set).
type GetChannelsRequest struct {
	MembersContain string   `json:"membersContain,omitempty"`
	AccountNames   []string `json:"accountNames,omitempty"`
}

// GetDMRequest is the body of subscription.getDM.
type GetDMRequest struct {
	AccountName string `json:"accountName"`
}

// DMResponse wraps the enriched DM subscription returned by subscription.getDM.
type DMResponse struct {
	Subscription model.DMSubscription `json:"subscription"`
}

// GetByRoomIDRequest is the body of subscription.getByRoomID.
type GetByRoomIDRequest struct {
	RoomID string `json:"roomId"`
}

// CountRequest is the body of subscription.count (Unread nil/false ⇒ total).
type CountRequest struct {
	Unread *bool `json:"unread,omitempty"`
}

// CountResponse is returned by subscription.count.
type CountResponse struct {
	Count int `json:"count"`
}
