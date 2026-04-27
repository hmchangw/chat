package models

// ThreadFilter selects which threads' parent messages to return.
type ThreadFilter string

const (
	ThreadFilterAll       ThreadFilter = "all"
	ThreadFilterFollowing ThreadFilter = "following"
	ThreadFilterUnread    ThreadFilter = "unread"
)

// GetThreadParentMessagesRequest is the NATS request payload for listing thread parent messages in a room.
type GetThreadParentMessagesRequest struct {
	Filter ThreadFilter `json:"filter"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
}

// GetThreadParentMessagesResponse is the NATS response for GetThreadParentMessages.
// ParentMessages are the top-level messages that started each thread, ordered by most
// recent reply activity (MongoDB sort).
// Total is the MongoDB facet count for the full matching result set, intended for
// pagination UI. It is intentionally independent of len(ParentMessages): defense-in-depth
// Cassandra hydration may drop rows that are missing or fall outside the access window,
// making len(ParentMessages) ≤ len(page) ≤ Total.
type GetThreadParentMessagesResponse struct {
	ParentMessages []Message `json:"parentMessages"`
	Total          int64     `json:"total"`
}
