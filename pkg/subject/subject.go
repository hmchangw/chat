package subject

import (
	"fmt"
	"strings"
)

// ParseUserRoomSubject extracts the user account and roomID from subjects
// matching the pattern "chat.user.{account}.*.room.{roomID}.…".
// Returns the user account, roomID, and ok=true on success.
func ParseUserRoomSubject(subj string) (account, roomID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 5 || parts[0] != "chat" || parts[1] != "user" {
		return "", "", false
	}
	account = parts[2]
	// Find "room" token after user position
	for i := 3; i < len(parts)-1; i++ {
		if parts[i] == "room" {
			return account, parts[i+1], true
		}
	}
	return "", "", false
}

func ParseUserRoomSiteSubject(subj string) (account, roomID, siteID string, ok bool) {
	parts := strings.Split(subj, ".")
	if len(parts) < 7 || parts[0] != "chat" || parts[1] != "user" || parts[3] != "room" {
		return "", "", "", false
	}
	return parts[2], parts[4], parts[5], true
}

// --- Specific subject builders ---

func MsgSend(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.room.%s.%s.msg.send", account, roomID, siteID)
}

func UserResponse(account, requestID string) string {
	return fmt.Sprintf("chat.user.%s.response.%s", account, requestID)
}

func RoomMetadataUpdate(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.metadata.update", roomID)
}

func RoomMsgStream(roomID string) string {
	return fmt.Sprintf("chat.room.%s.stream.msg", roomID)
}

func UserRoomUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.update", account)
}

func UserMsgStream(account string) string {
	return fmt.Sprintf("chat.user.%s.stream.msg", account)
}

func MemberInvite(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.invite", account, roomID, siteID)
}

// MemberAdd returns the specific subject for a member.add request.
func MemberAdd(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.add", account, roomID, siteID)
}

func MsgHistory(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.msg.history", account, roomID, siteID)
}

func SubscriptionUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.subscription.update", account)
}

func RoomMetadataChanged(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.metadata.update", account)
}

func Notification(account string) string {
	return fmt.Sprintf("chat.user.%s.notification", account)
}

func Outbox(siteID, destSiteID, eventType string) string {
	return fmt.Sprintf("outbox.%s.to.%s.%s", siteID, destSiteID, eventType)
}

func MsgCanonicalCreated(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.created", siteID)
}

func RoomEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event", roomID)
}

func RoomMemberEvent(roomID string) string {
	return fmt.Sprintf("chat.room.%s.event.member", roomID)
}

func UserRoomEvent(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room", account)
}

func RoomKeyUpdate(account string) string {
	return fmt.Sprintf("chat.user.%s.event.room.key", account)
}

// --- Room CRUD request builders ---

func RoomsCreate(account string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.create", account)
}

func RoomsList(account string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.list", account)
}

func RoomsGet(account, roomID string) string {
	return fmt.Sprintf("chat.user.%s.request.rooms.get.%s", account, roomID)
}

// --- Wildcard patterns for subscriptions ---

func MsgSendWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.room.*.%s.msg.send", siteID)
}

// MemberInviteWildcard returns the wildcard subscription pattern for member.invite requests.
func MemberInviteWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.invite", siteID)
}

// MemberAddWildcard returns the wildcard subscription pattern for member.add requests.
func MemberAddWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.add", siteID)
}

// MemberRemove returns the specific subject for a member.remove request.
func MemberRemove(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.remove", account, roomID, siteID)
}

// MemberRemoveWildcard returns the wildcard subscription pattern for member.remove requests.
func MemberRemoveWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.remove", siteID)
}

// MemberRoleUpdate returns the specific subject for a member.role-update request.
func MemberRoleUpdate(account, roomID, siteID string) string {
	return fmt.Sprintf("chat.user.%s.request.room.%s.%s.member.role-update", account, roomID, siteID)
}

// MemberRoleUpdateWildcard returns the wildcard subscription pattern for member.role-update requests.
func MemberRoleUpdateWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.member.role-update", siteID)
}

func MsgHistoryWildcard(siteID string) string {
	return fmt.Sprintf("chat.user.*.request.room.*.%s.msg.history", siteID)
}

func MsgCanonicalWildcard(siteID string) string {
	return fmt.Sprintf("chat.msg.canonical.%s.>", siteID)
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

// --- natsrouter patterns (use {param} placeholders for named extraction) ---

func MsgHistoryPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.history", siteID)
}

func MsgNextPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.next", siteID)
}

func MsgSurroundingPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.surrounding", siteID)
}

func MsgGetPattern(siteID string) string {
	return fmt.Sprintf("chat.user.{account}.request.room.{roomID}.%s.msg.get", siteID)
}
