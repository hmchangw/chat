package model

import (
	"time"

	"github.com/google/uuid"
)

type Message struct {
	ID        uuid.UUID `json:"id"`
	Sender    string    `json:"sender"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type CreateMessageRequest struct {
	Sender  string `json:"sender"`
	Content string `json:"content"`
}

type UpdateMessageRequest struct {
	Content string `json:"content"`
}
