package model

import "time"

type Message struct {
	ID                    string    `json:"id"                              bson:"_id"`
	RoomID                string    `json:"roomId"                          bson:"roomId"`
	UserID                string    `json:"userId"                          bson:"userId"`
	Username              string    `json:"username"                        bson:"username"`
	Content               string    `json:"content"                         bson:"content"`
	CreatedAt             time.Time `json:"createdAt"                       bson:"createdAt"`
	ThreadParentMessageID string    `json:"threadParentMessageId,omitempty" bson:"threadParentMessageId,omitempty"`
}

type SendMessageRequest struct {
	ID                    string `json:"id"`
	Content               string `json:"content"`
	RequestID             string `json:"requestId"`
	ThreadParentMessageID string `json:"threadParentMessageId,omitempty"`
}
