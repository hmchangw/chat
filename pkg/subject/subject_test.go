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
		{"MsgSend", subject.MsgSend("u1", "r1", "site-a"),
			"chat.user.u1.room.r1.site-a.msg.send"},
		{"UserResponse", subject.UserResponse("u1", "req-1"),
			"chat.user.u1.response.req-1"},
		{"RoomMetadataUpdate", subject.RoomMetadataUpdate("r1"),
			"chat.room.r1.event.metadata.update"},
		{"RoomMsgStream", subject.RoomMsgStream("r1"),
			"chat.room.r1.stream.msg"},
		{"UserRoomUpdate", subject.UserRoomUpdate("u1"),
			"chat.user.u1.event.room.update"},
		{"UserMsgStream", subject.UserMsgStream("u1"),
			"chat.user.u1.stream.msg"},
		{"MemberInvite", subject.MemberInvite("u1", "r1", "site-a"),
			"chat.user.u1.request.room.r1.site-a.member.invite"},
		{"MsgHistory", subject.MsgHistory("u1", "r1", "site-a"),
			"chat.user.u1.request.room.r1.site-a.msg.history"},
		{"SubscriptionUpdate", subject.SubscriptionUpdate("u1"),
			"chat.user.u1.event.subscription.update"},
		{"RoomMetadataChanged", subject.RoomMetadataChanged("u1"),
			"chat.user.u1.event.room.metadata.update"},
		{"Notification", subject.Notification("u1"),
			"chat.user.u1.notification"},
		{"Outbox", subject.Outbox("site-a", "site-b", "member_added"),
			"outbox.site-a.to.site-b.member_added"},
		{"Fanout", subject.Fanout("site-a", "r1", "m1"),
			"fanout.site-a.r1.m1"},
		{"RoomsCreate", subject.RoomsCreate("u1"),
			"chat.user.u1.request.rooms.create"},
		{"RoomsList", subject.RoomsList("u1"),
			"chat.user.u1.request.rooms.list"},
		{"RoomsGet", subject.RoomsGet("u1", "r1"),
			"chat.user.u1.request.rooms.get.r1"},
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
		name       string
		subj       string
		wantUserID string
		wantRoomID string
		wantOK     bool
	}{
		{"invite", "chat.user.u1.request.room.r1.site-a.member.invite", "u1", "r1", true},
		{"history", "chat.user.u1.request.room.r1.site-a.msg.history", "u1", "r1", true},
		{"msg_send", "chat.user.u1.room.r1.site-a.msg.send", "u1", "r1", true},
		{"too_short", "chat.user.u1", "", "", false},
		{"no_room", "chat.user.u1.request.foo.bar", "", "", false},
		{"bad_prefix", "foo.user.u1.room.r1", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uid, rid, ok := subject.ParseUserRoomSubject(tt.subj)
			if ok != tt.wantOK || uid != tt.wantUserID || rid != tt.wantRoomID {
				t.Errorf("ParseUserRoomSubject(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.subj, uid, rid, ok, tt.wantUserID, tt.wantRoomID, tt.wantOK)
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
