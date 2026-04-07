package model

import "time"

// Message represents a chat message with the full schema matching the
// messages_by_room Cassandra table.
type Message struct {
	ID                    string                   `json:"id"                              bson:"_id"`
	RoomID                string                   `json:"roomId"                          bson:"roomId"`
	CreatedAt             time.Time                `json:"createdAt"                       bson:"createdAt"`
	Sender                Participant              `json:"sender"                          bson:"sender"`
	TargetUser            *Participant             `json:"targetUser,omitempty"            bson:"targetUser,omitempty"`
	Content               string                   `json:"content"                         bson:"content"`
	Mentions              []Participant            `json:"mentions,omitempty"              bson:"mentions,omitempty"`
	Attachments           [][]byte                 `json:"attachments,omitempty"           bson:"attachments,omitempty"`
	File                  *File                    `json:"file,omitempty"                  bson:"file,omitempty"`
	Card                  *Card                    `json:"card,omitempty"                  bson:"card,omitempty"`
	CardAction            *CardAction              `json:"cardAction,omitempty"            bson:"cardAction,omitempty"`
	TShow                 bool                     `json:"tshow,omitempty"                 bson:"tshow,omitempty"`
	ThreadParentCreatedAt *time.Time               `json:"threadParentCreatedAt,omitempty" bson:"threadParentCreatedAt,omitempty"`
	VisibleTo             string                   `json:"visibleTo,omitempty"             bson:"visibleTo,omitempty"`
	Unread                bool                     `json:"unread,omitempty"                bson:"unread,omitempty"`
	Reactions             map[string][]Participant `json:"reactions,omitempty"             bson:"reactions,omitempty"`
	Deleted               bool                     `json:"deleted,omitempty"               bson:"deleted,omitempty"`
	SysMsgType            string                   `json:"sysMsgType,omitempty"            bson:"sysMsgType,omitempty"`
	SysMsgData            []byte                   `json:"sysMsgData,omitempty"            bson:"sysMsgData,omitempty"`
	FederateFrom          string                   `json:"federateFrom,omitempty"          bson:"federateFrom,omitempty"`
	EditedAt              *time.Time               `json:"editedAt,omitempty"              bson:"editedAt,omitempty"`
	UpdatedAt             *time.Time               `json:"updatedAt,omitempty"             bson:"updatedAt,omitempty"`
}

type SendMessageRequest struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	RequestID string `json:"requestId"`
}
