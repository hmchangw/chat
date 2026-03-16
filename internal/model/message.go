package model

import (
	"time"

	"github.com/gocql/gocql"
)

type Message struct {
	ConversationID gocql.UUID `json:"conversation_id"`
	MessageID      gocql.UUID `json:"message_id"`
	SenderID       gocql.UUID `json:"sender_id"`
	Body           string     `json:"body"`
	CreatedAt      time.Time  `json:"created_at"`
}
