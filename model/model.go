package model

import "time"

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Room struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

type Message struct {
	ID        string    `json:"id"`
	RoomID    string    `json:"room_id"`
	UserID    string    `json:"user_id"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// Request/response types for NATS request/reply

type CreateRoomRequest struct {
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
}

type ListRoomsRequest struct{}

type ListRoomsResponse struct {
	Rooms []Room `json:"rooms"`
}

type SendMessageRequest struct {
	RoomID  string `json:"room_id"`
	UserID  string `json:"user_id"`
	Content string `json:"content"`
}

type ListMessagesRequest struct {
	RoomID string `json:"room_id"`
	Limit  int    `json:"limit,omitempty"`
}

type ListMessagesResponse struct {
	Messages []Message `json:"messages"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
