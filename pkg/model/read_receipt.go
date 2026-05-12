package model

type ReadReceiptRequest struct {
	MessageID string `json:"messageId"`
}

type ReadReceiptEntry struct {
	UserID      string `json:"userId"`
	Account     string `json:"account"`
	ChineseName string `json:"chineseName"`
	EngName     string `json:"engName"`
}

type ReadReceiptResponse struct {
	Readers []ReadReceiptEntry `json:"readers"`
}
