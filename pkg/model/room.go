package model

import "time"

type RoomType string

const (
	RoomTypeGroup RoomType = "group"
	RoomTypeDM    RoomType = "dm"
)

type Room struct {
	ID        string    `json:"id" bson:"_id"`
	Name      string    `json:"name" bson:"name"`
	Type      RoomType  `json:"type" bson:"type"`
	CreatedBy string    `json:"createdBy" bson:"createdBy"`
	SiteID    string    `json:"siteId" bson:"siteId"`
	UserCount int       `json:"userCount" bson:"userCount"`
	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
}

type CreateRoomRequest struct {
	Name      string   `json:"name"`
	Type      RoomType `json:"type"`
	CreatedBy string   `json:"createdBy"`
	SiteID    string   `json:"siteId"`
	Members   []string `json:"members,omitempty"`
}

type ListRoomsResponse struct {
	Rooms []Room `json:"rooms"`
}
