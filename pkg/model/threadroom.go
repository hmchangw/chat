package model

import "time"

type ThreadRoom struct {
	ID                    string    `json:"id"                    bson:"_id"`
	ParentMessageID       string    `json:"parentMessageId"       bson:"parentMessageId"`
	ThreadParentCreatedAt time.Time `json:"threadParentCreatedAt" bson:"threadParentCreatedAt"`
	RoomID                string    `json:"roomId"                bson:"roomId"`
	SiteID                string    `json:"siteId"                bson:"siteId"`
	LastMsgAt             time.Time `json:"lastMsgAt"             bson:"lastMsgAt"`
	LastMsgID             string    `json:"lastMsgId"             bson:"lastMsgId"`
	ReplyAccounts         []string  `json:"replyAccounts"         bson:"replyAccounts"`
	ReplyCount            int       `json:"replyCount"            bson:"replyCount"`
	CountedReplies        []string  `json:"countedReplies"        bson:"countedReplies"`
	CreatedAt             time.Time `json:"createdAt"             bson:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"             bson:"updatedAt"`
}

// CountedRepliesCap bounds the ThreadRoom.CountedReplies dedup guard. It only
// needs to exceed the number of replies that can arrive within a JetStream
// redelivery window, so the guarded reply-count $inc can dedup redeliveries
// without the array growing unbounded on busy threads.
const CountedRepliesCap = 500
