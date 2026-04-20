package model

import "time"

type ThreadSubscription struct {
	ID              string     `json:"id"              bson:"_id"`
	ParentMessageID string     `json:"parentMessageId" bson:"parentMessageId"`
	RoomID          string     `json:"roomId"          bson:"roomId"`
	ThreadRoomID    string     `json:"threadRoomId"    bson:"threadRoomId"`
	UserID          string     `json:"userId"          bson:"userId"`
	UserAccount     string     `json:"userAccount"     bson:"userAccount"`
	SiteID          string     `json:"siteId"          bson:"siteId"`
	LastSeenAt      *time.Time `json:"lastSeenAt"      bson:"lastSeenAt"`
	CreatedAt       time.Time  `json:"createdAt"       bson:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"       bson:"updatedAt"`
}
