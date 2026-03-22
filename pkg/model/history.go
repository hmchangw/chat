package model

type HistoryRequest struct {
	RoomID string `json:"roomId"`
	Before string `json:"before,omitempty"` // cursor: message ID
	Limit  int    `json:"limit,omitempty"`
}

type HistoryResponse struct {
	Messages []Message `json:"messages"`
	HasMore  bool      `json:"hasMore"`
}
