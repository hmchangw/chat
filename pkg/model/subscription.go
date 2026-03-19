package model

import "time"

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

type Subscription struct {
	ID                 string    `json:"id" bson:"_id"`
	UserID             string    `json:"userId" bson:"userId"`
	RoomID             string    `json:"roomId" bson:"roomId"`
	SiteID             string    `json:"siteId" bson:"siteId"`
	Role               Role      `json:"role" bson:"role"`
	SharedHistorySince time.Time `json:"sharedHistorySince" bson:"sharedHistorySince"`
	JoinedAt           time.Time `json:"joinedAt" bson:"joinedAt"`
}
