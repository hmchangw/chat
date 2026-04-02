package model

import "time"

// RoomMemberType is the discriminator for RoomMember (storage vocabulary).
type RoomMemberType string

const (
	RoomMemberTypeIndividual RoomMemberType = "individual"
	RoomMemberTypeOrg        RoomMemberType = "org"
)

// HistoryMode controls whether new members can see past messages.
type HistoryMode string

const (
	HistoryModeNone HistoryMode = "none" // HistorySharedSince = now, blocks old history
	HistoryModeAll  HistoryMode = "all"  // no restriction, full history visible
)

// HistoryConfig is the history section of AddMembersRequest.
type HistoryConfig struct {
	Mode HistoryMode `json:"mode"`
}

// AddMembersRequest is the JSON payload for a member.add NATS request.
type AddMembersRequest struct {
	RoomID   string        `json:"roomId"`
	Users    []string      `json:"users"`    // direct usernames
	Orgs     []string      `json:"orgs"`     // orgIds to expand via hr_data
	Channels []string      `json:"channels"` // roomIds to copy members from
	History  HistoryConfig `json:"history"`
}

// RoomMemberEntry is the nested member sub-document in a RoomMember document.
type RoomMemberEntry struct {
	ID       string         `json:"id"                 bson:"id"`
	Type     RoomMemberType `json:"type"               bson:"type"`
	Username string         `json:"username,omitempty" bson:"username,omitempty"` // individual only
}

// RemoveMemberRequest is the JSON payload for a member.remove NATS request.
type RemoveMemberRequest struct {
	RoomID   string `json:"roomId"          bson:"roomId"`
	Username string `json:"username"        bson:"username"`
	OrgID    string `json:"orgId,omitempty" bson:"orgId,omitempty"`
}

// UpdateRoleRequest is the JSON payload for a member.role-update NATS request.
type UpdateRoleRequest struct {
	RoomID   string `json:"roomId"   bson:"roomId"`
	Username string `json:"username" bson:"username"`
	NewRole  Role   `json:"newRole"  bson:"newRole"`
}

// RoomMember is a document in the room_members collection.
type RoomMember struct {
	ID     string          `json:"id"     bson:"_id"`
	RoomID string          `json:"rid"    bson:"rid"`
	Ts     time.Time       `json:"ts"     bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}
