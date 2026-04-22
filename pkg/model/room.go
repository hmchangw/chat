package model

import "time"

type RoomType string

const (
	RoomTypeGroup      RoomType = "group"
	RoomTypeChannel    RoomType = "channel"
	RoomTypeDM         RoomType = "dm"
	RoomTypeDiscussion RoomType = "discussion"
)

type Room struct {
	ID               string    `json:"id" bson:"_id"`
	Name             string    `json:"name" bson:"name"`
	Type             RoomType  `json:"type" bson:"type"`
	CreatedBy        string    `json:"createdBy" bson:"createdBy"`
	SiteID           string    `json:"siteId" bson:"siteId"`
	UserCount        int       `json:"userCount" bson:"userCount"`
	LastMsgAt        time.Time `json:"lastMsgAt" bson:"lastMsgAt"`
	LastMsgID        string    `json:"lastMsgId" bson:"lastMsgId"`
	LastMentionAllAt time.Time `json:"lastMentionAllAt" bson:"lastMentionAllAt"`
	CreatedAt        time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt" bson:"updatedAt"`
	Restricted       bool      `json:"restricted,omitempty" bson:"restricted,omitempty"`
}

type CreateRoomRequest struct {
	Name             string   `json:"name"`
	Type             RoomType `json:"type"`
	CreatedBy        string   `json:"createdBy"`
	CreatedByAccount string   `json:"createdByAccount"`
	SiteID           string   `json:"siteId"`
	Members          []string `json:"members,omitempty"`
}

type ListRoomsResponse struct {
	Rooms []Room `json:"rooms"`
}
