package model

import "time"

type MessageEvent struct {
	Message Message `json:"message"`
	RoomID  string  `json:"roomId"`
	SiteID  string  `json:"siteId"`
}

type RoomMetadataUpdateEvent struct {
	RoomID        string    `json:"roomId"`
	Name          string    `json:"name"`
	UserCount     int       `json:"userCount"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type SubscriptionUpdateEvent struct {
	UserID       string       `json:"userId"`
	Subscription Subscription `json:"subscription"`
	Action       string       `json:"action"` // "added" | "removed"
}

type InviteMemberRequest struct {
	InviterID string `json:"inviterId"`
	InviteeID string `json:"inviteeId"`
	RoomID    string `json:"roomId"`
	SiteID    string `json:"siteId"`
}

type NotificationEvent struct {
	Type    string  `json:"type"` // "new_message"
	RoomID  string  `json:"roomId"`
	Message Message `json:"message"`
}

type OutboxEvent struct {
	Type       string `json:"type"` // "member_added", "room_sync"
	SiteID     string `json:"siteId"`
	DestSiteID string `json:"destSiteId"`
	Payload    []byte `json:"payload"` // JSON-encoded inner event
}
