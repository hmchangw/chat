package model

type User struct {
	ID       string `json:"id" bson:"_id"`
	Name     string `json:"name" bson:"name"`
	Username string `json:"username" bson:"username"`
	SiteID   string `json:"siteId" bson:"siteId"`
}
