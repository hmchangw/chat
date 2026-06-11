package models

import "github.com/hmchangw/chat/pkg/model"

// SetAppSubscriptionRequest is the body of subscription.setAppSubscription (PUT-like; Subscribed is the desired end-state).
type SetAppSubscriptionRequest struct {
	AppID      string `json:"appId"`
	Subscribed bool   `json:"subscribed"`
}

// AppListItem is an app plus the requesting user's subscription flag.
// Embedded App flattens on the wire (one extra top-level isSubscribed field).
// `bson:",inline"` is REQUIRED: mongorepo.ListApps decodes the apps aggregation
// ($addFields isSubscribed) directly into []AppListItem, and mongo-driver/v2 does
// NOT auto-inline anonymous embedded structs (unlike encoding/json) — without it
// the App fields (id/name/...) decode empty.
type AppListItem struct {
	model.App    `bson:",inline"`
	IsSubscribed bool `json:"isSubscribed" bson:"isSubscribed"`
}

// AppsListRequest is the optional body of apps.list; server defaults/caps apply (default limit 20, max 100).
type AppsListRequest struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// AppsListResponse is returned by apps.list. Total is the full catalog count, not the page size.
type AppsListResponse struct {
	Apps  []AppListItem `json:"apps"`
	Total int           `json:"total"`
}

// OKResponse is the generic success body (subscription.setAppSubscription).
type OKResponse struct {
	Success bool `json:"success"`
}
