package model

// LoadHistoryRequest is the payload for loading message history before a timestamp.
type LoadHistoryRequest struct {
	RoomID   string `json:"roomId"   bson:"roomId"`
	Before   string `json:"before"   bson:"before"`   // RFC3339Nano cursor — fetch messages before this
	Limit    int    `json:"limit"    bson:"limit"`    // default 50
	LastSeen string `json:"lastSeen" bson:"lastSeen"` // RFC3339Nano — last message seen by user
}

// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages    []Message `json:"messages"              bson:"messages"`
	FirstUnread *Message  `json:"firstUnread,omitempty" bson:"firstUnread,omitempty"`
	HasMore     bool      `json:"hasMore"               bson:"hasMore"`
}

// LoadNextMessagesRequest is the payload for loading messages after a timestamp.
type LoadNextMessagesRequest struct {
	RoomID string `json:"roomId" bson:"roomId"`
	After  string `json:"after"  bson:"after"` // RFC3339Nano cursor — fetch messages after this (empty for latest)
	Limit  int    `json:"limit"  bson:"limit"` // default 50
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages []Message `json:"messages" bson:"messages"`
	HasMore  bool      `json:"hasMore"  bson:"hasMore"`
}

// LoadSurroundingMessagesRequest is the payload for loading messages around a central message.
type LoadSurroundingMessagesRequest struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	MessageID string `json:"messageId" bson:"messageId"` // central message ID
	Limit     int    `json:"limit"     bson:"limit"`     // total messages including central
}

// LoadSurroundingMessagesResponse contains messages before and after the central message.
type LoadSurroundingMessagesResponse struct {
	Before []Message `json:"before" bson:"before"`
	After  []Message `json:"after"  bson:"after"` // includes the central message
}

// GetMessageByIDRequest is the payload for fetching a single message.
type GetMessageByIDRequest struct {
	RoomID    string `json:"roomId"    bson:"roomId"`
	MessageID string `json:"messageId" bson:"messageId"`
}
