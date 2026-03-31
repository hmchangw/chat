package model

import "time"

type Message struct {
	ID        string    `json:"id" bson:"_id"`
	RoomID    string    `json:"roomId" bson:"roomId"`
	UserID    string    `json:"userId" bson:"userId"`
	Content   string    `json:"content" bson:"content"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
}

type SendMessageRequest struct {
	RoomID    string `json:"roomId"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
