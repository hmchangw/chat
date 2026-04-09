package model

type Message struct {
	ID                           string `json:"id"                                   bson:"_id"`
	RoomID                       string `json:"roomId"                               bson:"roomId"`
	UserID                       string `json:"userId"                               bson:"userId"`
	UserAccount                  string `json:"userAccount"                          bson:"userAccount"`
	Content                      string `json:"content"                              bson:"content"`
	CreatedAt                    int64  `json:"createdAt"                            bson:"createdAt"`
	ThreadParentMessageID        string `json:"threadParentMessageId,omitempty"      bson:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *int64 `json:"threadParentMessageCreatedAt,omitempty" bson:"threadParentMessageCreatedAt,omitempty"`
}

type SendMessageRequest struct {
	ID                           string `json:"id"`
	Content                      string `json:"content"`
	RequestID                    string `json:"requestId"`
	ThreadParentMessageID        string `json:"threadParentMessageId,omitempty"`
	ThreadParentMessageCreatedAt *int64 `json:"threadParentMessageCreatedAt,omitempty"`
}
