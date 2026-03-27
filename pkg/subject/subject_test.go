package subject_test

import (
	"testing"

	"github.com/hmchangw/chat/pkg/subject"
)

func TestSubjectBuilders(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MsgSend", subject.MsgSend("alice", "r1", "site-a"),
			"chat.user.alice.room.r1.site-a.msg.send"},
		{"UserResponse", subject.UserResponse("alice", "req-1"),
			"chat.user.alice.response.req-1"},
		{"RoomMetadataUpdate", subject.RoomMetadataUpdate("r1"),
			"chat.room.r1.event.metadata.update"},
		{"RoomMsgStream", subject.RoomMsgStream("r1"),
			"chat.room.r1.stream.msg"},
		{"UserRoomUpdate", subject.UserRoomUpdate("alice"),
			"chat.user.alice.event.room.update"},
		{"UserMsgStream", subject.UserMsgStream("alice"),
			"chat.user.alice.stream.msg"},
		{"MemberInvite", subject.MemberInvite("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.member.invite"},
		{"MsgHistory", subject.MsgHistory("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.msg.history"},
		{"SubscriptionUpdate", subject.SubscriptionUpdate("alice"),
			"chat.user.alice.event.subscription.update"},
		{"RoomMetadataChanged", subject.RoomMetadataChanged("alice"),
			"chat.user.alice.event.room.metadata.update"},
		{"Notification", subject.Notification("alice"),
			"chat.user.alice.notification"},
		{"Outbox", subject.Outbox("site-a", "site-b", "member_added"),
			"outbox.site-a.to.site-b.member_added"},
		{"Fanout", subject.Fanout("site-a", "r1"),
			"fanout.site-a.r1"},
		{"RoomsCreate", subject.RoomsCreate("alice"),
			"chat.user.alice.request.rooms.create"},
		{"RoomsList", subject.RoomsList("alice"),
			"chat.user.alice.request.rooms.list"},
		{"RoomsGet", subject.RoomsGet("alice", "r1"),
			"chat.user.alice.request.rooms.get.r1"},
		{"RoomEvent", subject.RoomEvent("r1"), "chat.room.r1.event"},
		{"UserRoomEvent", subject.UserRoomEvent("alice"), "chat.user.alice.event.room"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestParseUserRoomSubject(t *testing.T) {
	tests := []struct {
		name         string
		subj         string
		wantUsername string
		wantRoomID   string
		wantOK       bool
	}{
		{"invite", "chat.user.alice.request.room.r1.site-a.member.invite", "alice", "r1", true},
		{"history", "chat.user.alice.request.room.r1.site-a.msg.history", "alice", "r1", true},
		{"msg_send", "chat.user.alice.room.r1.site-a.msg.send", "alice", "r1", true},
		{"too_short", "chat.user.alice", "", "", false},
		{"no_room", "chat.user.alice.request.foo.bar", "", "", false},
		{"bad_prefix", "foo.user.alice.room.r1", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			username, rid, ok := subject.ParseUserRoomSubject(tt.subj)
			if ok != tt.wantOK || username != tt.wantUsername || rid != tt.wantRoomID {
				t.Errorf("ParseUserRoomSubject(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.subj, username, rid, ok, tt.wantUsername, tt.wantRoomID, tt.wantOK)
			}
		})
	}
}

func TestWildcardPatterns(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"MsgSendWild", subject.MsgSendWildcard("site-a"),
			"chat.user.*.room.*.site-a.msg.send"},
		{"MemberInviteWild", subject.MemberInviteWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.member.>"},
		{"MsgHistoryWild", subject.MsgHistoryWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.msg.history"},
		{"FanoutWild", subject.FanoutWildcard("site-a"),
			"fanout.site-a.>"},
		{"OutboxWild", subject.OutboxWildcard("site-a"),
			"outbox.site-a.>"},
		{"RoomsCreateWild", subject.RoomsCreateWildcard(),
			"chat.user.*.request.rooms.create"},
		{"RoomsListWild", subject.RoomsListWildcard(),
			"chat.user.*.request.rooms.list"},
		{"RoomsGetWild", subject.RoomsGetWildcard(),
			"chat.user.*.request.rooms.get.*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
