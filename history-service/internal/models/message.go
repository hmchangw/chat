package models

import "github.com/hmchangw/chat/pkg/model/cassandra"

// Message is the Cassandra message record, now defined in pkg/model/cassandra.
type Message = cassandra.Message

// Participant is the Cassandra Participant UDT, now defined in pkg/model/cassandra.
type Participant = cassandra.Participant

// File is the Cassandra File UDT, now defined in pkg/model/cassandra.
type File = cassandra.File

// Card is the Cassandra Card UDT, now defined in pkg/model/cassandra.
type Card = cassandra.Card

// CardAction is the Cassandra CardAction UDT, now defined in pkg/model/cassandra.
type CardAction = cassandra.CardAction

// QuotedParentMessage is the Cassandra QuotedParentMessage UDT, now defined in pkg/model/cassandra.
type QuotedParentMessage = cassandra.QuotedParentMessage

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
	MessageID string `json:"messageId"` // central message ID
	Limit     int    `json:"limit"`     // total messages including central
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
}

// GetThreadMessagesRequest is the payload for loading replies in a thread.
type GetThreadMessagesRequest struct {
	ThreadMessageID string `json:"threadMessageId"` // parent message ID (old Meteor `tmid`)
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit"`
}

// GetThreadMessagesResponse is the response for GetThreadMessages.
type GetThreadMessagesResponse struct {
	Messages   []Message `json:"messages"`
	NextCursor string    `json:"nextCursor,omitempty"`
	HasNext    bool      `json:"hasNext"`
}
