package model

import "time"

type EventType string

const (
	EventCreated EventType = "created"
	EventUpdated EventType = "updated"
	EventDeleted EventType = "deleted"
)

type MessageEvent struct {
	Event     EventType `json:"event,omitempty" bson:"event,omitempty"`
	Message   Message   `json:"message"`
	SiteID    string    `json:"siteId"`
	Timestamp int64     `json:"timestamp" bson:"timestamp"`
}

type RoomMetadataUpdateEvent struct {
	RoomID        string    `json:"roomId"`
	Name          string    `json:"name"`
	UserCount     int       `json:"userCount"`
	LastMessageAt time.Time `json:"lastMessageAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Timestamp     int64     `json:"timestamp" bson:"timestamp"`
}

type SubscriptionUpdateEvent struct {
	UserID       string       `json:"userId"`
	Subscription Subscription `json:"subscription"`
	Action       string       `json:"action"` // "added" | "removed"
	Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}

type UpdateRoleRequest struct {
	RoomID  string `json:"roomId"  bson:"roomId"`
	Account string `json:"account" bson:"account"`
	NewRole Role   `json:"newRole" bson:"newRole"`
}

type InviteMemberRequest struct {
	InviterID      string `json:"inviterId"`
	InviteeID      string `json:"inviteeId"`
	InviteeAccount string `json:"inviteeAccount"`
	RoomID         string `json:"roomId"`
	SiteID         string `json:"siteId"`
	Timestamp      int64  `json:"timestamp" bson:"timestamp"`
}

type NotificationEvent struct {
	Type      string  `json:"type"` // "new_message"
	RoomID    string  `json:"roomId"`
	Message   Message `json:"message"`
	Timestamp int64   `json:"timestamp" bson:"timestamp"`
}

type OutboxEvent struct {
	Type       string `json:"type"` // "member_added", "room_sync"
	SiteID     string `json:"siteId"`
	DestSiteID string `json:"destSiteId"`
	Payload    []byte `json:"payload"` // JSON-encoded inner event
	Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}

// Participant represents a user with display name info for client rendering.
type Participant struct {
	UserID      string `json:"userId,omitempty" bson:"userId,omitempty"`
	Account     string `json:"account" bson:"account"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EngName     string `json:"engName" bson:"engName"`
}

// ClientMessage wraps Message with enriched sender info for client consumption.
type ClientMessage struct {
	Message `json:",inline" bson:",inline"`
	Sender  *Participant `json:"sender,omitempty"`
}

type RoomEventType string

const (
	RoomEventNewMessage RoomEventType = "new_message"
)

type RoomEvent struct {
	Type      RoomEventType `json:"type"`
	RoomID    string        `json:"roomId"`
	Timestamp int64         `json:"timestamp" bson:"timestamp"`

	RoomName  string    `json:"roomName"`
	RoomType  RoomType  `json:"roomType"`
	SiteID    string    `json:"siteId"`
	UserCount int       `json:"userCount"`
	LastMsgAt time.Time `json:"lastMsgAt"`
	LastMsgID string    `json:"lastMsgId"`

	Mentions   []Participant `json:"mentions,omitempty"`
	MentionAll bool          `json:"mentionAll,omitempty"`

	HasMention bool `json:"hasMention,omitempty"`

	Message *ClientMessage `json:"message,omitempty"`
}

type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	Version    int    `json:"version"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
	Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}

type MemberChangeEvent struct {
	Type      string   `json:"type"      bson:"type"`
	RoomID    string   `json:"roomId"    bson:"roomId"`
	Accounts  []string `json:"accounts"  bson:"accounts"`
	SiteID    string   `json:"siteId"    bson:"siteId"`
	Timestamp int64    `json:"timestamp" bson:"timestamp"`
}
