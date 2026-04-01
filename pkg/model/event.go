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

// Participant represents a user with display name info for client rendering.
type Participant struct {
	UserID      string `json:"userId,omitempty" bson:"userId,omitempty"`
	Username    string `json:"username" bson:"username"`
	ChineseName string `json:"chineseName" bson:"chineseName"`
	EngName     string `json:"engName" bson:"engName"`
}

// Employee holds employee data looked up from the employee MongoDB collection.
type Employee struct {
	AccountName string `bson:"accountName"`
	Name        string `bson:"name"`
	EngName     string `bson:"engName"`
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
	Timestamp time.Time     `json:"timestamp"`

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
	VersionID  string `json:"versionId"`
	PublicKey  []byte `json:"publicKey"`
	PrivateKey []byte `json:"privateKey"`
}
