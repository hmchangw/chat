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
type GetThreadParentMessagesResponse struct {
	ParentMessages []Message `json:"parentMessages"`
	Total          int64     `json:"total"`
}
