package model

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventCreated          EventType = "created"
	EventUpdated          EventType = "updated"
	EventDeleted          EventType = "deleted"
	EventPinned           EventType = "pinned"
	EventUnpinned         EventType = "unpinned"
	EventReacted          EventType = "reacted"
	EventThreadReplyAdded EventType = "thread_reply_added"
)

// UserStatusUpdated is the cross-site outbox event user-service publishes on status.set; inbox-worker applies it.
type UserStatusUpdated struct {
	Account      string `json:"account"                bson:"account"`
	StatusText   string `json:"statusText"             bson:"statusText"`
	StatusIsShow *bool  `json:"statusIsShow,omitempty" bson:"statusIsShow,omitempty"`
	Timestamp    int64  `json:"timestamp"              bson:"timestamp"`
}

type MessageEvent struct {
	Event   EventType `json:"event,omitempty" bson:"event,omitempty"`
	Message Message   `json:"message"`
	SiteID  string    `json:"siteId"`
	// ReactionDelta is set only when Event == EventReacted.
	ReactionDelta *ReactionDelta `json:"reactionDelta,omitempty" bson:"reactionDelta,omitempty"`
	Timestamp     int64          `json:"timestamp"               bson:"timestamp"`
	// NewTCount is the authoritative tcount after a thread reply is added/deleted; nil for all other event types.
	// bson tag omits omitempty — zero is valid when the last reply is deleted.
	NewTCount *int `json:"newTcount,omitempty" bson:"newTcount"`
}

// ReactionAction is the toggle direction on ReactionDelta.Action; defined type (not alias) for compile-time safety.
type ReactionAction string

const (
	ReactionActionAdded   ReactionAction = "added"
	ReactionActionRemoved ReactionAction = "removed"
)

// ReactionDelta is the per-toggle reaction payload; Actor toggled, reacted-to message's author is on MessageEvent.Message.
type ReactionDelta struct {
	Shortcode string         `json:"shortcode" bson:"shortcode"`
	Action    ReactionAction `json:"action"    bson:"action"`
	Actor     Participant    `json:"actor"     bson:"actor"`
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
	Action       string       `json:"action"` // "added" | "removed" | "role_updated" | "mute_toggled" | "favorite_toggled"
	Timestamp    int64        `json:"timestamp" bson:"timestamp"`
}

// CanonicalMemberEventMuted is the only event type currently published on this stream.
const CanonicalMemberEventMuted = "muted"

// CanonicalMemberEvent is the room-scoped post-mutation event for roomsubcache invalidation (mute-only today).
type CanonicalMemberEvent struct {
	Type      string `json:"type"`
	RoomID    string `json:"roomId"`
	Account   string `json:"account"`
	Muted     bool   `json:"muted"` // post-toggle state; false is a valid (unmuted) value, so no omitempty.
	Timestamp int64  `json:"timestamp"`
}

type UpdateRoleRequest struct {
	RoomID    string `json:"roomId"  bson:"roomId"`
	Account   string `json:"account" bson:"account"`
	NewRole   Role   `json:"newRole" bson:"newRole"`
	Timestamp int64  `json:"timestamp" bson:"timestamp"` // set by room-service at acceptance; stable seed for Nats-Msg-Id
}

// InboxMemberEvent is the INBOX stream payload for "member_added"/"member_removed" events; one event covers N Accounts.
// HistorySharedSince: nil = unrestricted; non-nil = restricted from that timestamp. Emit nil (never &0) — sentinel hss<=0 means unrestricted.
type InboxMemberEvent struct {
	RoomID             string   `json:"roomId"`
	RoomName           string   `json:"roomName"`
	RoomType           RoomType `json:"roomType"`
	SiteID             string   `json:"siteId"`
	Accounts           []string `json:"accounts"`
	HistorySharedSince *int64   `json:"historySharedSince,omitempty"`
	JoinedAt           int64    `json:"joinedAt,omitempty"`
	Timestamp          int64    `json:"timestamp" bson:"timestamp"`
}

// NotificationEvent is the single-user reaction notification on chat.user.{account}.notification.
// Distinct from PushNotificationEvent (batched mobile); this is the legacy envelope the FE listens on.
type NotificationEvent struct {
	Type          string         `json:"type"` // "reaction"
	RoomID        string         `json:"roomId"`
	Message       Message        `json:"message"`
	ReactionDelta *ReactionDelta `json:"reactionDelta,omitempty" bson:"reactionDelta,omitempty"`
	Timestamp     int64          `json:"timestamp"               bson:"timestamp"`
}

// OutboxEventType is the type tag on an OutboxEvent, used to route it to the correct handler on the destination site.
type OutboxEventType = string

const (
	OutboxMemberAdded                 OutboxEventType = "member_added"
	OutboxMemberRemoved               OutboxEventType = "member_removed"
	OutboxSubscriptionRead            OutboxEventType = "subscription_read"
	OutboxSubscriptionMuteToggled     OutboxEventType = "subscription_mute_toggled"
	OutboxSubscriptionFavoriteToggled OutboxEventType = "subscription_favorite_toggled"
	OutboxThreadSubscriptionUpserted  OutboxEventType = "thread_subscription_upserted"
	OutboxThreadRead                  OutboxEventType = "thread_read"
	OutboxRoomRenamed                 OutboxEventType = "room_renamed"
	OutboxRoomRestricted              OutboxEventType = "room_restricted"
	OutboxUserStatusUpdated           OutboxEventType = "user_status_updated"
)

// SubscriptionReadEvent is the OutboxEvent.Payload for "subscription_read"; sent home→user site on read.
// LastSeenAt is UnixMilli (UTC) for cross-language wire safety.
type SubscriptionReadEvent struct {
	Account    string `json:"account"    bson:"account"`
	RoomID     string `json:"roomId"     bson:"roomId"`
	LastSeenAt int64  `json:"lastSeenAt" bson:"lastSeenAt"`
	Alert      bool   `json:"alert"      bson:"alert"`
	Timestamp  int64  `json:"timestamp"  bson:"timestamp"`
}

// ThreadReadEvent is the OutboxEvent.Payload for "thread_read"; source ships authoritative NewThreadUnread+Alert.
type ThreadReadEvent struct {
	Account         string   `json:"account"`
	RoomID          string   `json:"roomId"`
	ThreadRoomID    string   `json:"threadRoomId"`
	ParentMessageID string   `json:"parentMessageId"`
	NewThreadUnread []string `json:"newThreadUnread"`
	Alert           bool     `json:"alert"`
	LastSeenAt      int64    `json:"lastSeenAt"`
	Timestamp       int64    `json:"timestamp"`
}

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
	RoomName           string   `json:"roomName"           bson:"roomName"`
	RoomType           RoomType `json:"roomType,omitempty" bson:"roomType,omitempty"`
	Accounts           []string `json:"accounts"           bson:"accounts"`
	SiteID             string   `json:"siteId"             bson:"siteId"`
	RequesterAccount   string   `json:"requesterAccount,omitempty" bson:"requesterAccount,omitempty"`
	JoinedAt           int64    `json:"joinedAt"           bson:"joinedAt"`
	HistorySharedSince *int64   `json:"historySharedSince,omitempty" bson:"historySharedSince,omitempty"`
	Timestamp          int64    `json:"timestamp"          bson:"timestamp"`
}

// Participant represents a user with display name info for client rendering.
// DisplayName is pre-composed (push-service senders only); raw EngName/ChineseName are used in fan-out shapes.
type Participant struct {
	UserID      string `json:"userId,omitempty"      bson:"userId,omitempty"`
	Account     string `json:"account"               bson:"account"`
	SiteID      string `json:"siteId,omitempty"      bson:"siteId,omitempty"`
	ChineseName string `json:"chineseName"           bson:"chineseName"`
	EngName     string `json:"engName"               bson:"engName"`
	DisplayName string `json:"displayName,omitempty" bson:"displayName,omitempty"`
}

// ClientMessage wraps Message with enriched sender info for client consumption.
type ClientMessage struct {
	Message `json:",inline" bson:",inline"`
	Sender  *Participant `json:"sender,omitempty"`
}

type RoomEventType string

const (
	RoomEventNewMessage            RoomEventType = "new_message"
	RoomEventMessageEdited         RoomEventType = "message_edited"
	RoomEventMessageDeleted        RoomEventType = "message_deleted"
	RoomEventMessagePinned         RoomEventType = "message_pinned"
	RoomEventMessageUnpinned       RoomEventType = "message_unpinned"
	RoomEventRoomRenamed           RoomEventType = "room_renamed"
	RoomEventRoomRestricted        RoomEventType = "room_restricted"
	RoomEventMessageReacted        RoomEventType = "message_reacted"
	RoomEventThreadMetadataUpdated RoomEventType = "thread_metadata_updated"
)

// ThreadAction identifies what operation triggered a ThreadMetadataUpdatedEvent.
type ThreadAction string

const (
	ThreadActionReplyAdded   ThreadAction = "reply_added"
	ThreadActionReplyDeleted ThreadAction = "reply_deleted"
)

// RoomEvent is the live fan-out event for new messages (RoomEventNewMessage).
// Edits, deletes, pins, and unpins use flat event types to avoid zero-valued base fields.
type RoomEvent struct {
	Type           RoomEventType `json:"type"`
	RoomID         string        `json:"roomId"`
	Timestamp      int64         `json:"timestamp" bson:"timestamp"`
	EventTimestamp int64         `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`

	RoomName  string    `json:"roomName"`
	RoomType  RoomType  `json:"roomType"`
	SiteID    string    `json:"siteId"`
	UserCount int       `json:"userCount"`
	LastMsgAt time.Time `json:"lastMsgAt"`
	LastMsgID string    `json:"lastMsgId"`

	Mentions   []Participant `json:"mentions,omitempty"`
	MentionAll bool          `json:"mentionAll,omitempty"`

	HasMention bool `json:"hasMention,omitempty"`

	Message          *ClientMessage  `json:"message,omitempty"`
	EncryptedMessage json.RawMessage `json:"encryptedMessage,omitempty"`
}

// EditRoomEvent is the live event for a message edit; flat shape, no zero-valued base fields.
// EncryptedNewContent carries ciphertext for encrypted rooms; NewContent holds the plaintext otherwise.
type EditRoomEvent struct {
	Type                RoomEventType   `json:"type" bson:"type"`
	RoomID              string          `json:"roomId" bson:"roomId"`
	SiteID              string          `json:"siteId" bson:"siteId"`
	Timestamp           int64           `json:"timestamp" bson:"timestamp"`
	EventTimestamp      int64           `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	MessageID           string          `json:"messageId" bson:"messageId"`
	NewContent          string          `json:"newContent,omitempty" bson:"newContent,omitempty"`
	EncryptedNewContent json.RawMessage `json:"encryptedNewContent,omitempty" bson:"encryptedNewContent,omitempty"`
	EditedBy            string          `json:"editedBy" bson:"editedBy"`
	EditedAt            time.Time       `json:"editedAt" bson:"editedAt"`
	UpdatedAt           time.Time       `json:"updatedAt" bson:"updatedAt"`
}

// DeleteRoomEvent is the live event for a message deletion; flat shape, no zero-valued base fields.
type DeleteRoomEvent struct {
	Type           RoomEventType `json:"type" bson:"type"`
	RoomID         string        `json:"roomId" bson:"roomId"`
	SiteID         string        `json:"siteId" bson:"siteId"`
	Timestamp      int64         `json:"timestamp" bson:"timestamp"`
	EventTimestamp int64         `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	MessageID      string        `json:"messageId" bson:"messageId"`
	DeletedBy      string        `json:"deletedBy" bson:"deletedBy"`
	DeletedAt      time.Time     `json:"deletedAt" bson:"deletedAt"`
	UpdatedAt      time.Time     `json:"updatedAt" bson:"updatedAt"`
}

// PinRoomEvent is the live event for a message pin; flat shape, mirrors EditRoomEvent/DeleteRoomEvent.
type PinRoomEvent struct {
	Type           RoomEventType `json:"type" bson:"type"`
	RoomID         string        `json:"roomId" bson:"roomId"`
	SiteID         string        `json:"siteId" bson:"siteId"`
	Timestamp      int64         `json:"timestamp" bson:"timestamp"`
	EventTimestamp int64         `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	MessageID      string        `json:"messageId" bson:"messageId"`
	PinnedBy       *Participant  `json:"pinnedBy,omitempty" bson:"pinnedBy,omitempty"`
	PinnedAt       time.Time     `json:"pinnedAt" bson:"pinnedAt"`
}

// UnpinRoomEvent is the live event published when a message is unpinned.
type UnpinRoomEvent struct {
	Type           RoomEventType `json:"type" bson:"type"`
	RoomID         string        `json:"roomId" bson:"roomId"`
	SiteID         string        `json:"siteId" bson:"siteId"`
	Timestamp      int64         `json:"timestamp" bson:"timestamp"`
	EventTimestamp int64         `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	MessageID      string        `json:"messageId" bson:"messageId"`
	UnpinnedBy     *Participant  `json:"unpinnedBy,omitempty" bson:"unpinnedBy,omitempty"`
	UnpinnedAt     time.Time     `json:"unpinnedAt" bson:"unpinnedAt"`
}

// ThreadMetadataUpdatedEvent is published per-user when a thread reply is added or deleted,
// letting clients update the reply-count badge without re-fetching the full message.
type ThreadMetadataUpdatedEvent struct {
	Type            RoomEventType `json:"type" bson:"type"`
	RoomID          string        `json:"roomId" bson:"roomId"`
	SiteID          string        `json:"siteId" bson:"siteId"`
	Timestamp       int64         `json:"timestamp" bson:"timestamp"`
	EventTimestamp  int64         `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	ParentMessageID string        `json:"parentMessageId" bson:"parentMessageId"`
	ReplyMessageID  string        `json:"replyMessageId" bson:"replyMessageId"`
	NewTCount       int           `json:"newTcount" bson:"newTcount"`
	Action          ThreadAction  `json:"action" bson:"action"`
}

// RoomRenamedRoomEvent is the live event for a channel rename; flat shape.
// Drives the client's local subscription `name` update — no separate subscription.update fires.
type RoomRenamedRoomEvent struct {
	Type      RoomEventType `json:"type" bson:"type"`
	RoomID    string        `json:"roomId" bson:"roomId"`
	SiteID    string        `json:"siteId" bson:"siteId"`
	Timestamp int64         `json:"timestamp" bson:"timestamp"`
	NewName   string        `json:"newName" bson:"newName"`
	ByAccount string        `json:"byAccount" bson:"byAccount"`
	RenamedAt time.Time     `json:"renamedAt" bson:"renamedAt"`
}

// RoomRestrictedRoomEvent is the live event for restricted/externalAccess flag changes; flat shape.
// OwnerAccount is non-empty only on the unrestricted→restricted transition.
type RoomRestrictedRoomEvent struct {
	Type           RoomEventType `json:"type" bson:"type"`
	RoomID         string        `json:"roomId" bson:"roomId"`
	SiteID         string        `json:"siteId" bson:"siteId"`
	Timestamp      int64         `json:"timestamp" bson:"timestamp"`
	Restricted     bool          `json:"restricted" bson:"restricted"`
	ExternalAccess bool          `json:"externalAccess" bson:"externalAccess"`
	OwnerAccount   string        `json:"ownerAccount,omitempty" bson:"ownerAccount,omitempty"`
	ByAccount      string        `json:"byAccount" bson:"byAccount"`
	ChangedAt      time.Time     `json:"changedAt" bson:"changedAt"`
}

// ReactRoomEvent is the live event for a reaction toggle; Actor carries the full Participant for display-name rendering.
type ReactRoomEvent struct {
	Type           RoomEventType  `json:"type" bson:"type"`
	RoomID         string         `json:"roomId" bson:"roomId"`
	SiteID         string         `json:"siteId" bson:"siteId"`
	Timestamp      int64          `json:"timestamp" bson:"timestamp"`
	EventTimestamp int64          `json:"eventTimestamp,omitempty" bson:"eventTimestamp,omitempty"`
	MessageID      string         `json:"messageId" bson:"messageId"`
	Shortcode      string         `json:"shortcode" bson:"shortcode"`
	Action         ReactionAction `json:"action"    bson:"action"`
	Actor          Participant    `json:"actor"     bson:"actor"`
	ReactedAt      time.Time      `json:"reactedAt" bson:"reactedAt"`
	UpdatedAt      time.Time      `json:"updatedAt" bson:"updatedAt"`
}

// RemovedSubscriptionRef is the minimal identity on a "removed" subscription.update event — only fields needed to
// drop the room from the sidebar; avoids shipping zero-valued full Subscription fields.
type RemovedSubscriptionRef struct {
	RoomID   string           `json:"roomId" bson:"roomId"`
	RoomType RoomType         `json:"roomType" bson:"roomType"`
	U        SubscriptionUser `json:"u" bson:"u"`
}

// SubscriptionRemovedEvent is the subscription.update payload when a member loses a subscription.
// Embeds RemovedSubscriptionRef instead of full Subscription to avoid zero-valued fields on the wire.
type SubscriptionRemovedEvent struct {
	UserID       string                 `json:"userId,omitempty" bson:"userId,omitempty"`
	Subscription RemovedSubscriptionRef `json:"subscription" bson:"subscription"`
	Action       string                 `json:"action" bson:"action"` // always "removed"
	Timestamp    int64                  `json:"timestamp" bson:"timestamp"`
}

type RoomKeyEvent struct {
	RoomID     string `json:"roomId"`
	Version    int    `json:"version"`
	PrivateKey []byte `json:"privateKey"`
	Timestamp  int64  `json:"timestamp" bson:"timestamp"`
}

// RoomKeyEnsureRequest is the payload for the room key ensure RPC.
type RoomKeyEnsureRequest struct {
	RoomID string `json:"roomId"`
}

// RoomKeyEnsureResponse is the reply from the room key ensure RPC; confirms the room has a key at the given Version.
type RoomKeyEnsureResponse struct {
	RoomID  string `json:"roomId"`
	Version int    `json:"version"`
}

// RoomKeyGetRequest is the payload for the room key get RPC; nil Version returns the current key,
// non-nil returns the historical key at that version (subject to roomkeystore grace window).
type RoomKeyGetRequest struct {
	Version *int `json:"version,omitempty"`
}

// RoomKeyGetResponse is the reply from the room key get RPC; PrivateKey is the 32-byte AES-256-GCM secret.
type RoomKeyGetResponse struct {
	RoomID     string `json:"roomId"`
	Version    int    `json:"version"`
	PrivateKey []byte `json:"privateKey"`
}

// MuteToggleResponse is the sync reply for the mute.toggle RPC.
type MuteToggleResponse struct {
	Status string `json:"status"`
	Muted  bool   `json:"muted"`
}

// SubscriptionMuteToggledEvent is the OutboxEvent.Payload for type "subscription_mute_toggled".
type SubscriptionMuteToggledEvent struct {
	Account   string `json:"account"              bson:"account"`
	RoomID    string `json:"roomId"               bson:"roomId"`
	Muted     bool   `json:"muted" bson:"muted"`
	Timestamp int64  `json:"timestamp"            bson:"timestamp"`
}

// FavoriteToggleResponse is the sync reply for the favorite.toggle RPC.
type FavoriteToggleResponse struct {
	Status   string `json:"status"`
	Favorite bool   `json:"favorite"`
}

// SubscriptionFavoriteToggledEvent is the OutboxEvent.Payload for type "subscription_favorite_toggled".
type SubscriptionFavoriteToggledEvent struct {
	Account   string `json:"account"              bson:"account"`
	RoomID    string `json:"roomId"               bson:"roomId"`
	Favorite  bool   `json:"favorite"             bson:"favorite"`
	Timestamp int64  `json:"timestamp"            bson:"timestamp"`
}

type MemberRemoveEvent struct {
	Type      string   `json:"type"            bson:"type"`
	RoomID    string   `json:"roomId"          bson:"roomId"`
	Accounts  []string `json:"accounts"        bson:"accounts"`
	SiteID    string   `json:"siteId"          bson:"siteId"`
	OrgID     string   `json:"orgId,omitempty" bson:"orgId,omitempty"`
	Timestamp int64    `json:"timestamp"       bson:"timestamp"`
}

// AsyncJobResult signals to the requester's client that an async room-worker job has completed.
type AsyncJobResult struct {
	RequestID string `json:"requestId"`
	Operation string `json:"operation"`
	Status    string `json:"status"`
	RoomID    string `json:"roomId,omitempty"`
	Error     string `json:"error,omitempty"`
	// Code and Reason mirror the errcode envelope; typed as string so pkg/model
	// does not import pkg/errcode.
	Code      string `json:"code,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

const (
	AsyncJobOpRoomCreate           = "room.create"
	AsyncJobOpRoomMemberAdd        = "room.member.add"
	AsyncJobOpRoomMemberRemove     = "room.member.remove"
	AsyncJobOpRoomMemberRemoveOrg  = "room.member.remove_org"
	AsyncJobOpRoomMemberRoleUpdate = "room.member.role_update"
	AsyncJobOpRoomRename           = "room.rename"
)

const (
	// MessageTypeRoomCreated is the system-message type emitted on room creation (channels only).
	MessageTypeRoomCreated = "room_created"
	// MessageTypeMembersAdded is the system-message type emitted when members are added.
	MessageTypeMembersAdded = "members_added"
	// MessageTypeMemberRemoved is the system-message type emitted when a member is removed.
	MessageTypeMemberRemoved = "member_removed"
	// MessageTypeMemberLeft is the system-message type emitted when a member self-leaves.
	MessageTypeMemberLeft = "member_left"
	// MessageTypeRoomRenamed is the system-message type emitted when a channel is renamed.
	MessageTypeRoomRenamed = "room_renamed"
	// MessageTypeRoomRestricted is the system-message type emitted when a
	// channel's Restricted/ExternalAccess flags change.
	MessageTypeRoomRestricted = "room_restricted"
)

const (
	// AsyncJobStatusOK indicates a successful async job result.
	AsyncJobStatusOK = "ok"
	// AsyncJobStatusError indicates a failed async job result.
	AsyncJobStatusError = "error"
)

// CreateRoomReply is the sync NATS reply returned after publishing the canonical create event.
type CreateRoomReply struct {
	Status   string `json:"status"`
	RoomID   string `json:"roomId"`
	RoomType string `json:"roomType"`
}

// CreateRoomReplyAccepted means validated + queued; persistence happens later in room-worker.
const CreateRoomReplyAccepted = "accepted"

// CreateRoomStatusExists indicates the DM already existed; clients treat it as success and open that room.
const CreateRoomStatusExists = "exists"

// RoomRenamedOutboxPayload is wrapped in OutboxEvent.Payload for OutboxRoomRenamed.
type RoomRenamedOutboxPayload struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	NewName   string `json:"newName"   bson:"newName"`
	Timestamp int64  `json:"timestamp" bson:"timestamp"`
}

// RoomRestrictedOutboxPayload is wrapped in OutboxEvent.Payload for OutboxRoomRestricted.
// Non-empty OwnerAccount + Restricted=true causes the destination to promote that account to sole owner.
type RoomRestrictedOutboxPayload struct {
	RoomID         string `json:"roomId"                 bson:"roomId"`
	Restricted     bool   `json:"restricted"             bson:"restricted"`
	ExternalAccess bool   `json:"externalAccess"         bson:"externalAccess"`
	OwnerAccount   string `json:"ownerAccount,omitempty" bson:"ownerAccount,omitempty"`
	Timestamp      int64  `json:"timestamp"              bson:"timestamp"`
}

// StatusReply is the response for fire-and-forget RPCs; Status is "ok" or "accepted".
type StatusReply struct {
	Status string `json:"status"`
}

// StatusWithRequestReply is StatusReply plus the echoed request ID for async-result correlation (rename, restricted).
type StatusWithRequestReply struct {
	Status    string `json:"status"`
	RequestID string `json:"requestId"`
}

// RoomRenameRequest is the rename RPC body; roomID comes from the subject, not the body.
type RoomRenameRequest struct {
	NewName string `json:"newName"`
}
