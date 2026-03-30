package subject

import (
	"fmt"
	"strings"
)

// ParseUserRoomSubject extracts username and roomID from subjects matching
// the pattern "chat.user.{username}.*.room.{roomID}.…".
// Returns username, roomID, and ok=true on success.
func ParseUserRoomSubject(subj string) (username, roomID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 5 || parts[0] != "chat" || parts[1] != "user" {
		return "", "", false
	}
	username = parts[2]
	// Find "room" token after user position
	for i := 3; i < len(parts)-1; i++ {
		if parts[i] == "room" {
			return username, parts[i+1], true
		}
	}
	return "", "", false
}

// --- Specific subject builders ---

func MsgSend(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.room.%s.%s.msg.send", username, roomID, siteID)
}

func UserResponse(username, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", username, requestID)
}

func RoomMetadataUpdate(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.metadata.update", roomID)
}

func RoomMsgStream(roomID string) string {
	return fmt.Sprintf("chat.room.%s.stream.msg", roomID)
}

func UserRoomUpdate(username string) string {
	return fmt.Sprintf("chat.user.%s.event.room.update", username)
}

func UserMsgStream(username string) string {
	return fmt.Sprintf("chat.user.%s.stream.msg", username)
}

func MemberInvite(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.invite", username, roomID, siteID)
}

func MsgHistory(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", username, roomID, siteID)
}

func MsgNext(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.next", username, roomID, siteID)
}

func MsgSurrounding(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.surrounding", username, roomID, siteID)
}

func MsgGet(username, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.get", username, roomID, siteID)
}

func SubscriptionUpdate(username string) string {
	return fmt.Sprintf("chat.user.%s.event.subscription.update", username)
}

func RoomMetadataChanged(username string) string {
	return fmt.Sprintf("chat.user.%s.event.room.metadata.update", username)
}

func Notification(username string) string {
	return fmt.Sprintf("chat.user.%s.notification", username)
}

func Outbox(siteID, destSiteID, eventType string) string {
	return fmt.Sprintf("outbox.%s.to.%s.%s", siteID, destSiteID, eventType)
}

func Fanout(siteID, roomID string) string {
	return fmt.Sprintf("fanout.%s.%s", siteID, roomID)
}

func RoomEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event", roomID)
}

func UserRoomEvent(username string) string {
	return fmt.Sprintf("chat.user.%s.event.room", username)
}

// --- Room CRUD request builders ---

func RoomsCreate(username string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.create", username)
}

func RoomsList(username string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.list", username)
}

func RoomsGet(username, roomID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.get.%s", username, roomID)
}

// --- Wildcard patterns for subscriptions ---

func MsgSendWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.room.*.%s.msg.send", siteID)
}

func MemberInviteWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.>", siteID)
}

func MsgHistoryWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.history", siteID)
}

func MsgNextWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.next", siteID)
}

func MsgSurroundingWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.surrounding", siteID)
}

func MsgGetWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.get", siteID)
}

func FanoutWildcard(siteID string) string {
	return fmt.Sprintf("fanout.%s.>", siteID)
}

func OutboxWildcard(siteID string) string {
	return fmt.Sprintf("outbox.%s.>", siteID)
}

func RoomsCreateWildcard() string {
	return "chat.user.*.request.rooms.create"
}

func RoomsListWildcard() string {
	return "chat.user.*.request.rooms.list"
}

func RoomsGetWildcard() string {
	return "chat.user.*.request.rooms.get.*"
}
