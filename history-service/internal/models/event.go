package models

import "encoding/json"

// MessageEditedEvent is the live event published to chat.room.{roomID}.event
// after a successful edit. For channel rooms the edit content is encrypted via
// roomcrypto.Encode and placed in EncryptedNewMsg (NewMsg is empty). For rooms
// without a key (DM or key not yet provisioned), NewMsg carries the plaintext.
// Per CLAUDE.md, every NATS event carries a Timestamp (event publish time).
type MessageEditedEvent struct {
	Type            string          `json:"type"`
	Timestamp       int64           `json:"timestamp"` // UTC millis, event publish time
	RoomID          string          `json:"roomId"`
	MessageID       string          `json:"messageId"`
	NewMsg          string          `json:"newMsg,omitempty"`          // plaintext; empty when EncryptedNewMsg is set
	EncryptedNewMsg json.RawMessage `json:"encryptedNewMsg,omitempty"` // roomcrypto.EncryptedMessage JSON; set for encrypted rooms
	EditedBy        string          `json:"editedBy"`
	EditedAt        int64           `json:"editedAt"` // UTC millis, domain time
}

// MessageDeletedEvent is the live event published to chat.room.{roomID}.event
// after a successful soft delete. Per CLAUDE.md, every NATS event carries a
// Timestamp (event publish time). DeletedAt is the domain time when the
// delete occurred; both are populated from a single time.Now().UTC() in the
// handler. DeletedBy equals the sender account under sender-only auth and is
// included for client rendering convenience — it is not persisted to Cassandra.
type MessageDeletedEvent struct {
	Type      string `json:"type"`
	Timestamp int64  `json:"timestamp"`
	RoomID    string `json:"roomId"`
	MessageID string `json:"messageId"`
	DeletedBy string `json:"deletedBy"`
	DeletedAt int64  `json:"deletedAt"`
}
