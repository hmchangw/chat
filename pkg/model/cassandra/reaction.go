package cassandra

// MessageReactionRow maps to the chat.message_reactions Cassandra table.
type MessageReactionRow struct {
	MessageID string        `json:"messageId" cql:"message_id"`
	Emoji     string        `json:"emoji"     cql:"emoji"`
	Users     []Participant `json:"users"     cql:"users"`
}
