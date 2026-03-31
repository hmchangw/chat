package model

// MemberEntryType is the discriminator for MemberEntry (request vocabulary).
type MemberEntryType string

// RoomMemberType is the discriminator for RoomMember (storage vocabulary).
type RoomMemberType string

const (
	MemberEntryTypeUser MemberEntryType = "user"
	MemberEntryTypeOrg  MemberEntryType = "org"
)

const (
	RoomMemberTypeIndividual RoomMemberType = "individual"
	RoomMemberTypeOrg        RoomMemberType = "org"
)

// MemberEntry represents one entry in an AddMembersRequest.
// Type is "user" for individual users or "org" for organizations.
type MemberEntry struct {
	Type     MemberEntryType `json:"type"`
	Username string          `json:"username,omitempty"`
	OrgID    string          `json:"orgId,omitempty"`
}

// AddMembersRequest is the JSON payload for a member.add NATS request.
type AddMembersRequest struct {
	RoomID  string        `json:"roomId"`
	Members []MemberEntry `json:"members"`
}

// RoomMember is a document in the room_members collection.
// Type is "org" or "individual".
type RoomMember struct {
	ID       string         `json:"id" bson:"_id"`
	RoomID   string         `json:"roomId" bson:"roomId"`
	Type     RoomMemberType `json:"type" bson:"type"`
	OrgID    string         `json:"orgId,omitempty" bson:"orgId,omitempty"`
	UserID   string         `json:"userId,omitempty" bson:"userId,omitempty"`
	Username string         `json:"username,omitempty" bson:"username,omitempty"`
}
