package models

import "time"

// Message represents a full message row from the messages_by_room Cassandra table.
type Message struct {
	RoomID                string                   `json:"roomId"`
	CreatedAt             time.Time                `json:"createdAt"`
	MessageID             string                   `json:"messageId"`
	Sender                Participant              `json:"sender"`
	TargetUser            *Participant             `json:"targetUser,omitempty"`
	Msg                   string                   `json:"msg"`
	Mentions              []Participant            `json:"mentions,omitempty"`
	Attachments           [][]byte                 `json:"attachments,omitempty"`
	File                  *File                    `json:"file,omitempty"`
	Card                  *Card                    `json:"card,omitempty"`
	CardAction            *CardAction              `json:"cardAction,omitempty"`
	TShow                 bool                     `json:"tshow,omitempty"`
	ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty"`
	VisibleTo             string                   `json:"visibleTo,omitempty"`
	Unread                bool                     `json:"unread,omitempty"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"`
	Deleted               bool                     `json:"deleted,omitempty"`
	SysMsgType            string                   `json:"sysMsgType,omitempty"`
	SysMsgData            []byte                   `json:"sysMsgData,omitempty"`
	FederateFrom          string                   `json:"federateFrom,omitempty"`
	EditedAt              *time.Time               `json:"editedAt,omitempty"`
	UpdatedAt             *time.Time               `json:"updatedAt,omitempty"`
}

// LoadHistoryRequest is the payload for loading message history before a timestamp.
type LoadHistoryRequest struct {
	Before *int64 `json:"before,omitempty"` // UTC millis — fetch messages before this (nil = now)
	Limit  int    `json:"limit"`            // default 20
}

// LoadHistoryResponse is the response for LoadHistory.
type LoadHistoryResponse struct {
	Messages []Message `json:"messages"`
}

// LoadNextMessagesRequest is the payload for loading messages after a timestamp.
type LoadNextMessagesRequest struct {
	After  *int64 `json:"after,omitempty"` // UTC millis — fetch messages after this (nil = no lower bound)
	Limit  int    `json:"limit"`           // default 50
	Cursor string `json:"cursor"`          // pagination cursor from previous response
}

// LoadNextMessagesResponse is the response for LoadNextMessages.
type LoadNextMessagesResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	HasNext    bool      `json:"hasNext"`
}

// LoadSurroundingMessagesRequest is the payload for loading messages around a central message.
type LoadSurroundingMessagesRequest struct {
	MessageID string `json:"messageId"`           // central message ID
	CreatedAt *int64 `json:"createdAt,omitempty"` // optional UTC millis hint for O(1) lookup
	Limit     int    `json:"limit"`               // total messages including central
}

// LoadSurroundingMessagesResponse contains messages around the central message.
type LoadSurroundingMessagesResponse struct {
	Messages   []Message `json:"messages"`
	MoreBefore bool      `json:"moreBefore"`
	MoreAfter  bool      `json:"moreAfter"`
}

// GetMessageByIDRequest is the payload for fetching a single message.
type GetMessageByIDRequest struct {
	MessageID string `json:"messageId"`
	CreatedAt *int64 `json:"createdAt,omitempty"` // optional UTC millis hint for O(1) lookup
}
