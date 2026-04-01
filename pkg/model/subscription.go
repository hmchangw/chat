package model

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

type SubscriptionUser struct {
	ID       string `json:"id" bson:"_id"`
	Username string `json:"username" bson:"username"`
}

type Subscription struct {
	ID                 string           `json:"id" bson:"_id"`
	User               SubscriptionUser `json:"u" bson:"u"`
	RoomID             string           `json:"roomId" bson:"roomId"`
	SiteID             string           `json:"siteId" bson:"siteId"`
	Roles              []Role           `json:"roles" bson:"roles"`
	SharedHistorySince time.Time        `json:"sharedHistorySince" bson:"sharedHistorySince"`
	JoinedAt           time.Time        `json:"joinedAt" bson:"joinedAt"`
	LastSeenAt         time.Time        `json:"lastSeenAt" bson:"lastSeenAt"`
	HasMention         bool             `json:"hasMention" bson:"hasMention"`
}
