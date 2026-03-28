package model

import "time"

type MessageEvent struct {
	Message Message `json:"message"`
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
	InviterID       string `json:"inviterId"`
	InviteeID       string `json:"inviteeId"`
	InviteeUsername string `json:"inviteeUsername"`
	RoomID          string `json:"roomId"`
	SiteID          string `json:"siteId"`
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

type RoomEventType string

const (
	RoomEventNewMessage RoomEventType = "new_message"
)

type RoomEvent struct {
	Type      RoomEventType `json:"type"`
	RoomID    string        `json:"roomId"`
	Timestamp time.Time     `json:"timestamp"`

	RoomName  string    `json:"roomName"`
	RoomType  RoomType  `json:"roomType"`
	Origin    string    `json:"origin"`
	UserCount int       `json:"userCount"`
	LastMsgAt time.Time `json:"lastMsgAt"`
	LastMsgID string    `json:"lastMsgId"`

	Mentions   []string `json:"mentions,omitempty"`
	MentionAll bool     `json:"mentionAll,omitempty"`

	HasMention bool `json:"hasMention,omitempty"`

	Message *Message `json:"message,omitempty"`
}
