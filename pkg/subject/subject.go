package subject

import (
	"fmt"
	"strings"
)

// ParseUserRoomSubject extracts userID and roomID from subjects matching
// the pattern "chat.user.{userID}.*.room.{roomID}.…".
// Returns userID, roomID, and ok=true on success.
func ParseUserRoomSubject(subj string) (userID, roomID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 5 || parts[0] != "chat" || parts[1] != "user" {
		return "", "", false
	}
	userID = parts[2]
	// Find "room" token after user position
	for i := 3; i < len(parts)-1; i++ {
		if parts[i] == "room" {
			return userID, parts[i+1], true
		}
	}
	return "", "", false
}

// --- Specific subject builders ---

func MsgSend(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.room.%s.%s.msg.send", userID, roomID, siteID)
}

func UserResponse(userID, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", userID, requestID)
}

func RoomMetadataUpdate(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.metadata.update", roomID)
}

func RoomMsgStream(roomID string) string {
	return fmt.Sprintf("chat.room.%s.stream.msg", roomID)
}

func UserRoomUpdate(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.room.update", userID)
}

func UserMsgStream(userID string) string {
	return fmt.Sprintf("chat.user.%s.stream.msg", userID)
}

func MemberInvite(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.invite", userID, roomID, siteID)
}

func MsgHistory(userID, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", userID, roomID, siteID)
}

func SubscriptionUpdate(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.subscription.update", userID)
}

func RoomMetadataChanged(userID string) string {
	return fmt.Sprintf("chat.user.%s.event.room.metadata.update", userID)
}

func Notification(userID string) string {
	return fmt.Sprintf("chat.user.%s.notification", userID)
}

func Outbox(siteID, destSiteID, eventType string) string {
	return fmt.Sprintf("outbox.%s.to.%s.%s", siteID, destSiteID, eventType)
}

func Fanout(siteID, roomID string) string {
	return fmt.Sprintf("fanout.%s.%s", siteID, roomID)
}

// --- Room CRUD request builders ---

func RoomsCreate(userID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.create", userID)
}

func RoomsList(userID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.list", userID)
}

func RoomsGet(userID, roomID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.get.%s", userID, roomID)
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
