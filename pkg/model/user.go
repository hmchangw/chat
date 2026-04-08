package model

type User struct {
	ID      string `json:"id" bson:"_id"`
	Name    string `json:"name" bson:"name"`
	Account string `json:"account" bson:"account"`
	SiteID  string `json:"siteId" bson:"siteId"`
}
