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

// MemberAddedPayload is the payload of an OutboxEvent{Type: "member_added" |
// "member_removed"}. It carries one or more Subscriptions against the same
// Room so downstream consumers (inbox-worker, search-sync-worker) can
// index/persist without DB lookups.
//
// The Subscriptions slice supports bulk invites: one admin action adding N
// users to a single room produces a single event with N Subscriptions, all
// sharing the same Room. For single-user operations the slice has length 1.
// All subscriptions in one event target the same Room — if you need to touch
// multiple rooms, publish one event per room.
//
// Downstream consumers that care about the full set (spotlight-sync,
// user-room-sync) fan the event out into one ES bulk action per
// subscription. Consumers that only care about the Room (e.g. future
// room-sync) can ignore the Subscriptions slice and act on the Room field.
type MemberAddedPayload struct {
	Subscriptions []Subscription `json:"subscriptions"`
	Room          Room           `json:"room"`
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

// OutboxEventType is the type tag on an OutboxEvent used to route it to the
// correct handler on the destination site.
type OutboxEventType = string

const (
	OutboxMemberAdded   OutboxEventType = "member_added"
	OutboxMemberRemoved OutboxEventType = "member_removed"
)

type OutboxEvent struct {
	Type       OutboxEventType `json:"type"`
	SiteID     string          `json:"siteId"`
	DestSiteID string          `json:"destSiteId"`
	Payload    []byte          `json:"payload"` // JSON-encoded inner event
	Timestamp  int64           `json:"timestamp" bson:"timestamp"`
}

type MemberAddEvent struct {
	Type               string   `json:"type"               bson:"type"`
	RoomID             string   `json:"roomId"             bson:"roomId"`
	Accounts           []string `json:"accounts"           bson:"accounts"`
	SiteID             string   `json:"siteId"             bson:"siteId"`
	JoinedAt           int64    `json:"joinedAt"           bson:"joinedAt"`
	HistorySharedSince int64    `json:"historySharedSince" bson:"historySharedSince"`
	Timestamp          int64    `json:"timestamp"          bson:"timestamp"`
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

type MemberRemoveEvent struct {
	Type      string   `json:"type"            bson:"type"`
	RoomID    string   `json:"roomId"          bson:"roomId"`
	Accounts  []string `json:"accounts"        bson:"accounts"`
	SiteID    string   `json:"siteId"          bson:"siteId"`
	OrgID     string   `json:"orgId,omitempty" bson:"orgId,omitempty"`
	Timestamp int64    `json:"timestamp"       bson:"timestamp"`
}
