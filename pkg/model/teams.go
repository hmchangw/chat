package model

// Request/reply contracts for the Microsoft Teams integration RPCs handled by
// room-service. Two endpoints (room call, user call) build a Teams deep link
// with no external I/O; the meetings endpoint creates a Graph onlineMeeting and
// is idempotent per room.

// TeamsRoomCallRequest is the request body for the room-call deep-link RPC.
// The room is carried on the NATS subject, so the body may be empty; the field
// is accepted for clients that prefer to pass it explicitly.
type TeamsRoomCallRequest struct {
	// RoomID is optional — the authoritative room is the subject's {roomID}.
	RoomID string `json:"roomId,omitempty"`
}

// TeamsUserCallRequest is the request body for the 1:1 user-call deep-link RPC.
type TeamsUserCallRequest struct {
	// AccountName is the target user's account; its email is derived as
	// account@TEAMS_EMAIL_DOMAIN.
	AccountName string `json:"accountName"`
}

// TeamsCallReply is the reply for both deep-link RPCs (calls/room, calls/user).
type TeamsCallReply struct {
	JoinURL string `json:"joinUrl"`
}

// TeamsMeetingRequest is the request body for the meetings RPC. The room is
// carried on the NATS subject; the body may be empty.
type TeamsMeetingRequest struct {
	// RoomID is optional — the authoritative room is the subject's {roomID}.
	RoomID string `json:"roomId,omitempty"`
}

// TeamsMeetingReply is the reply for the meetings RPC: the Graph onlineMeeting
// ID and its join URL.
type TeamsMeetingReply struct {
	ID      string `json:"id"`
	JoinURL string `json:"joinUrl"`
}
