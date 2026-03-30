package models

import "github.com/hmchangw/chat/pkg/model"

// LoadHistoryRequest is the payload for loading message history before a timestamp.
type LoadHistoryRequest struct {
	RoomID   string `json:"roomId"`
	Before   string `json:"before"`   // RFC3339Nano cursor — fetch messages before this
	Limit    int    `json:"limit"`    // default 50
	LastSeen string `json:"lastSeen"` // RFC3339Nano — last message seen by user
	Cursor   string `json:"cursor"`   // pagination cursor from previous response
}

// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages    []model.Message `json:"messages"`
	FirstUnread *model.Message  `json:"firstUnread,omitempty"`
	NextCursor  string          `json:"nextCursor,omitempty"`
	HasNext     bool            `json:"hasNext"`
}

// LoadNextMessagesRequest is the payload for loading messages after a timestamp.
type LoadNextMessagesRequest struct {
	RoomID string `json:"roomId"`
	After  string `json:"after"`  // RFC3339Nano cursor — fetch messages after this (empty for latest)
	Limit  int    `json:"limit"`  // default 50
	Cursor string `json:"cursor"` // pagination cursor from previous response
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages   []model.Message `json:"messages"`
	NextCursor string          `json:"nextCursor,omitempty"`
	HasNext    bool            `json:"hasNext"`
}

// LoadSurroundingMessagesRequest is the payload for loading messages around a central message.
type LoadSurroundingMessagesRequest struct {
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"` // central message ID
	Limit     int    `json:"limit"`     // total messages including central
}

// LoadSurroundingMessagesResponse contains messages before and after the central message.
type LoadSurroundingMessagesResponse struct {
	Before []model.Message `json:"before"`
	After  []model.Message `json:"after"` // includes the central message
}

// GetMessageByIDRequest is the payload for fetching a single message.
type GetMessageByIDRequest struct {
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
}
