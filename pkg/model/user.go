package model

type User struct {
	ID          string `json:"id"           bson:"_id"`
	Account     string `json:"account"      bson:"account"`
	SiteID      string `json:"siteId"       bson:"siteId"`
	EngName     string `json:"engName"      bson:"engName"`
	ChineseName string `json:"chineseName"  bson:"chineseName"`
	EmployeeID  string `json:"employeeId"   bson:"employeeId"`
}
