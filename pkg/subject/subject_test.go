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
		{"SubscriptionUpdate", subject.SubscriptionUpdate("alice"),
			"chat.user.alice.event.subscription.update"},
		{"RoomMetadataChanged", subject.RoomMetadataChanged("alice"),
			"chat.user.alice.event.room.metadata.update"},
		{"Notification", subject.Notification("alice"),
			"chat.user.alice.notification"},
		{"Outbox", subject.Outbox("site-a", "site-b", "member_added"),
			"outbox.site-a.to.site-b.member_added"},
		{"MsgCanonicalCreated", subject.MsgCanonicalCreated("site-a"),
			"chat.msg.canonical.site-a.created"},
		{"RoomsCreate", subject.RoomsCreate("alice"),
			"chat.user.alice.request.rooms.create"},
		{"RoomsList", subject.RoomsList("alice"),
			"chat.user.alice.request.rooms.list"},
		{"RoomsGet", subject.RoomsGet("alice", "r1"),
			"chat.user.alice.request.rooms.get.r1"},
		{"RoomEvent", subject.RoomEvent("r1"), "chat.room.r1.event"},
		{"RoomMemberEvent", subject.RoomMemberEvent("r1"), "chat.room.r1.event.member"},
		{"UserRoomEvent", subject.UserRoomEvent("alice"), "chat.user.alice.event.room"},
		{"RoomKeyUpdate", subject.RoomKeyUpdate("alice"),
			"chat.user.alice.event.room.key"},
		{"MemberAdd", subject.MemberAdd("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.member.add"},
		{"MsgHistory", subject.MsgHistory("alice", "r1", "site-a"),
			"chat.user.alice.request.room.r1.site-a.msg.history"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestMemberRemove(t *testing.T) {
	got := subject.MemberRemove("alice", "r1", "site-a")
	want := "chat.user.alice.request.room.r1.site-a.member.remove"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMemberRemoveWildcard(t *testing.T) {
	got := subject.MemberRemoveWildcard("site-a")
	want := "chat.user.*.request.room.*.site-a.member.remove"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMemberRoleUpdate(t *testing.T) {
	got := subject.MemberRoleUpdate("alice", "r1", "site-a")
	want := "chat.user.alice.request.room.r1.site-a.member.role-update"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMemberRoleUpdateWildcard(t *testing.T) {
	got := subject.MemberRoleUpdateWildcard("site-a")
	want := "chat.user.*.request.room.*.site-a.member.role-update"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseUserRoomSubject(t *testing.T) {
	tests := []struct {
		name        string
		subj        string
		wantAccount string
		wantRoomID  string
		wantOK      bool
	}{
		{"invite", "chat.user.alice.request.room.r1.site-a.member.invite", "alice", "r1", true},
		{"member_add", "chat.user.alice.request.room.r1.site-a.member.add", "alice", "r1", true},
		{"history", "chat.user.alice.request.room.r1.site-a.msg.history", "alice", "r1", true},
		{"msg_send", "chat.user.alice.room.r1.site-a.msg.send", "alice", "r1", true},
		{"too_short", "chat.user.alice", "", "", false},
		{"no_room", "chat.user.alice.request.foo.bar", "", "", false},
		{"bad_prefix", "foo.user.alice.room.r1", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, rid, ok := subject.ParseUserRoomSubject(tt.subj)
			if ok != tt.wantOK || account != tt.wantAccount || rid != tt.wantRoomID {
				t.Errorf("ParseUserRoomSubject(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.subj, account, rid, ok, tt.wantAccount, tt.wantRoomID, tt.wantOK)
			}
		})
	}
}

func TestParseUserRoomSiteSubject(t *testing.T) {
	tests := []struct {
		name        string
		subj        string
		wantAccount string
		wantRoomID  string
		wantSiteID  string
		wantOK      bool
	}{
		{"valid msg send", "chat.user.alice.room.r1.site-a.msg.send", "alice", "r1", "site-a", true},
		{"different values", "chat.user.bob.room.room-42.site-b.msg.send", "bob", "room-42", "site-b", true},
		{"too few parts", "chat.user.alice.room.r1.site-a", "", "", "", false},
		{"bad prefix", "foo.user.alice.room.r1.site-a.msg.send", "", "", "", false},
		{"not user", "chat.blah.alice.room.r1.site-a.msg.send", "", "", "", false},
		{"no room token", "chat.user.alice.notroom.r1.site-a.msg.send", "", "", "", false},
		{"empty", "", "", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, roomID, siteID, ok := subject.ParseUserRoomSiteSubject(tt.subj)
			if ok != tt.wantOK || account != tt.wantAccount || roomID != tt.wantRoomID || siteID != tt.wantSiteID {
				t.Errorf("ParseUserRoomSiteSubject(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					tt.subj, account, roomID, siteID, ok, tt.wantAccount, tt.wantRoomID, tt.wantSiteID, tt.wantOK)
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
			"chat.user.*.request.room.*.site-a.member.invite"},
		{"MsgHistoryWild", subject.MsgHistoryWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.msg.history"},
		{"MsgCanonicalWild", subject.MsgCanonicalWildcard("site-a"),
			"chat.msg.canonical.site-a.>"},
		{"OutboxWild", subject.OutboxWildcard("site-a"),
			"outbox.site-a.>"},
		{"RoomsCreateWild", subject.RoomsCreateWildcard(),
			"chat.user.*.request.rooms.create"},
		{"RoomsListWild", subject.RoomsListWildcard(),
			"chat.user.*.request.rooms.list"},
		{"RoomsGetWild", subject.RoomsGetWildcard(),
			"chat.user.*.request.rooms.get.*"},
		{"MemberAddWild", subject.MemberAddWildcard("site-a"),
			"chat.user.*.request.room.*.site-a.member.add"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
