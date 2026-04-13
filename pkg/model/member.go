package model

import "time"

type RoomMemberType string

const (
	RoomMemberTypeIndividual RoomMemberType = "individual"
	RoomMemberTypeOrg        RoomMemberType = "org"
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
	RoomID   string        `json:"roomId"   bson:"roomId"`
	Users    []string      `json:"users"    bson:"users"`
	Orgs     []string      `json:"orgs"     bson:"orgs"`
	Channels []string      `json:"channels" bson:"channels"`
	History  HistoryConfig `json:"history"  bson:"history"`
}

type RoomMemberEntry struct {
	ID      string         `json:"id"                bson:"id"`
	Type    RoomMemberType `json:"type"              bson:"type"`
	Account string         `json:"account,omitempty" bson:"account,omitempty"`
}

type RemoveMemberRequest struct {
	RoomID  string `json:"roomId"            bson:"roomId"`
	Account string `json:"account,omitempty" bson:"account,omitempty"`
	OrgID   string `json:"orgId,omitempty"   bson:"orgId,omitempty"`
}

type UpdateRoleRequest struct {
	RoomID  string `json:"roomId"  bson:"roomId"`
	Account string `json:"account" bson:"account"`
	NewRole Role   `json:"newRole" bson:"newRole"`
}

type RoomMember struct {
	ID     string          `json:"id"     bson:"_id"`
	RoomID string          `json:"rid"    bson:"rid"`
	Ts     time.Time       `json:"ts"     bson:"ts"`
	Member RoomMemberEntry `json:"member" bson:"member"`
}

type MembersAdded struct {
	Individuals     []string `json:"individuals"`
	Orgs            []string `json:"orgs"`
	Channels        []string `json:"channels"`
	AddedUsersCount int      `json:"addedUsersCount"`
}

type MembersRemoved struct {
	Account           string `json:"account,omitempty"`
	OrgID             string `json:"orgId,omitempty"`
	RemovedUsersCount int    `json:"removedUsersCount"`
}
