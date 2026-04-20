package model

import "time"

type RoomMemberType string

const (
	RoomMemberIndividual RoomMemberType = "individual"
	RoomMemberOrg        RoomMemberType = "org"
)

type HistoryMode string

const (
	HistoryModeNone HistoryMode = "none"
	HistoryModeAll  HistoryMode = "all"
)

type HistoryConfig struct {
	Mode HistoryMode `json:"mode" bson:"mode"`
}

type AddMembersRequest struct {
	RoomID           string        `json:"roomId"           bson:"roomId"`
	Users            []string      `json:"users"            bson:"users"`
	Orgs             []string      `json:"orgs"             bson:"orgs"`
	Channels         []string      `json:"channels"         bson:"channels"`
	History          HistoryConfig `json:"history"          bson:"history"`
	RequesterID      string        `json:"requesterId"      bson:"requesterId"`
	RequesterAccount string        `json:"requesterAccount" bson:"requesterAccount"`
	Timestamp        int64         `json:"timestamp"        bson:"timestamp"`
}

type RoomMember struct {
	ID     string          `json:"id"     bson:"_id"`
	RoomID string          `json:"rid"    bson:"rid"`
	Ts     time.Time       `json:"ts"     bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}

type RoomMemberEntry struct {
	ID      string         `json:"id"                bson:"id"`
	Type    RoomMemberType `json:"type"              bson:"type"`
	Account string         `json:"account,omitempty" bson:"account,omitempty"`

	// Display fields — never persisted (bson:"-"); populated only when
	// ListRoomMembers is called with enrich=true. Elided from JSON when zero.
	EngName     string `json:"engName,omitempty"     bson:"-"`
	ChineseName string `json:"chineseName,omitempty" bson:"-"`
	IsOwner     bool   `json:"isOwner,omitempty"     bson:"-"`
	SectName    string `json:"sectName,omitempty"    bson:"-"`
	MemberCount int    `json:"memberCount,omitempty" bson:"-"`
}

type RemoveMemberRequest struct {
	RoomID    string `json:"roomId"             bson:"roomId"`
	Requester string `json:"requester"          bson:"requester"`
	Account   string `json:"account,omitempty"  bson:"account,omitempty"`
	OrgID     string `json:"orgId,omitempty"    bson:"orgId,omitempty"`
}

type SysMsgUser struct {
	Account     string `json:"account"`
	EngName     string `json:"engName"`
	ChineseName string `json:"chineseName"`
}

type MemberLeft struct {
	User SysMsgUser `json:"user"`
}

type MemberRemoved struct {
	User              *SysMsgUser `json:"user,omitempty"`
	OrgID             string      `json:"orgId,omitempty"`
	SectName          string      `json:"sectName,omitempty"`
	RemovedUsersCount int         `json:"removedUsersCount"`
}

type MembersAdded struct {
	Individuals     []string `json:"individuals"`
	Orgs            []string `json:"orgs"`
	Channels        []string `json:"channels"`
	AddedUsersCount int      `json:"addedUsersCount"`
}

type ListRoomMembersRequest struct {
	Limit  *int `json:"limit,omitempty"`
	Offset *int `json:"offset,omitempty"`
	Enrich bool `json:"enrich,omitempty"`
}

type ListRoomMembersResponse struct {
	Members []RoomMember `json:"members"`
}
