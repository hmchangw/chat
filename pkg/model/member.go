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
	RoomID   string        `json:"roomId"   bson:"roomId"`
	Users    []string      `json:"users"    bson:"users"`
	Orgs     []string      `json:"orgs"     bson:"orgs"`
	Channels []string      `json:"channels" bson:"channels"`
	History  HistoryConfig `json:"history"  bson:"history"`
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
