package model

import "time"

// ThreadRoom represents a thread conversation anchored to a parent message.
// Sorted by LastMsgAt for efficient listing. EnsureIndexes in mongorepo/threadroom.go
// manages the required compound indexes at startup.
type ThreadRoom struct {
	ID                    string    `json:"id"                    bson:"_id"`
	ParentMessageID       string    `json:"parentMessageId"       bson:"parentMessageId"`
	ThreadParentCreatedAt time.Time `json:"threadParentCreatedAt" bson:"threadParentCreatedAt"`
	RoomID                string    `json:"roomId"                bson:"roomId"`
	SiteID                string    `json:"siteId"                bson:"siteId"`
	LastMsgAt             time.Time `json:"lastMsgAt"             bson:"lastMsgAt"`
	LastMsgID             string    `json:"lastMsgId"             bson:"lastMsgId"`
	ReplyAccounts         []string  `json:"replyAccounts"         bson:"replyAccounts"`
	CreatedAt             time.Time `json:"createdAt"             bson:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"             bson:"updatedAt"`
}
