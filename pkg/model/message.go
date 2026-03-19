package model

import "time"

type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"roomId"`
	UserID    string    `json:"userId"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type SendMessageRequest struct {
	RoomID    string `json:"roomId"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
