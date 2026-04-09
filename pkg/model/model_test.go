package model_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Name: "alice", Account: "alice", SiteID: "site-a"}
	roundTrip(t, &u, &model.User{})
}

func TestRoomJSON(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		LastMsgAt:        time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LastMsgID:        "m1",
		LastMentionAllAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}

func TestMessageJSON(t *testing.T) {
	t.Run("with threadParentMessageId", func(t *testing.T) {
		m := model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:               "hello",
			CreatedAt:             time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			ThreadParentMessageID: "parent-msg-uuid",
		}
		roundTrip(t, &m, &model.Message{})
	})

	t.Run("threadParentMessageId omitted when empty", func(t *testing.T) {
		m := model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		}
		data, err := json.Marshal(&m)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["threadParentMessageId"]
		assert.False(t, present, "threadParentMessageId should be omitted when empty")
	})

	t.Run("threadParentMessageCreatedAt omitted when nil", func(t *testing.T) {
		m := model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		}
		data, err := json.Marshal(&m)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["threadParentMessageCreatedAt"]
		assert.False(t, present, "threadParentMessageCreatedAt should be omitted when nil")
	})

	t.Run("with threadParentMessageCreatedAt", func(t *testing.T) {
		raw := `{"id":"m1","roomId":"r1","userId":"u1","userAccount":"alice","content":"reply","createdAt":"2026-01-01T12:00:00Z","threadParentMessageId":"parent-msg-uuid","threadParentMessageCreatedAt":"2026-01-01T11:00:00Z"}`
		var m model.Message
		require.NoError(t, json.Unmarshal([]byte(raw), &m))
		assert.Equal(t, "parent-msg-uuid", m.ThreadParentMessageID)
		require.NotNil(t, m.ThreadParentMessageCreatedAt)
		assert.Equal(t, time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC), m.ThreadParentMessageCreatedAt.UTC())
	})
}

func TestSendMessageRequestJSON(t *testing.T) {
	t.Run("with threadParentMessageId", func(t *testing.T) {
		r := model.SendMessageRequest{
			ID:                    "msg-uuid-1",
			Content:               "hello world",
			RequestID:             "req-1",
			ThreadParentMessageID: "parent-msg-uuid",
		}
		roundTrip(t, &r, &model.SendMessageRequest{})
	})

	t.Run("threadParentMessageId omitted when empty", func(t *testing.T) {
		r := model.SendMessageRequest{
			ID:        "msg-uuid-1",
			Content:   "hello world",
			RequestID: "req-1",
		}
		data, err := json.Marshal(&r)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["threadParentMessageId"]
		assert.False(t, present, "threadParentMessageId should be omitted when empty")
	})

	t.Run("with threadParentMessageCreatedAt", func(t *testing.T) {
		raw := `{"id":"msg-uuid-1","content":"reply","requestId":"req-1","threadParentMessageId":"parent-msg-uuid","threadParentMessageCreatedAt":"2026-01-01T11:00:00Z"}`
		var r model.SendMessageRequest
		require.NoError(t, json.Unmarshal([]byte(raw), &r))
		assert.Equal(t, "parent-msg-uuid", r.ThreadParentMessageID)
		require.NotNil(t, r.ThreadParentMessageCreatedAt)
		assert.Equal(t, time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC), r.ThreadParentMessageCreatedAt.UTC())
	})

	t.Run("threadParentMessageCreatedAt omitted when nil", func(t *testing.T) {
		r := model.SendMessageRequest{
			ID:        "msg-uuid-1",
			Content:   "hello world",
			RequestID: "req-1",
		}
		data, err := json.Marshal(&r)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["threadParentMessageCreatedAt"]
		assert.False(t, present, "threadParentMessageCreatedAt should be omitted when nil")
	})
}

func TestMessageEventJSON(t *testing.T) {
	e := model.MessageEvent{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		SiteID:    "site-a",
		Timestamp: 1735689600000,
	}
	roundTrip(t, &e, &model.MessageEvent{})
}

func TestSubscriptionJSON(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Account: "alice"},
		RoomID:             "r1",
		SiteID:             "site-a",
		Roles:              []model.Role{model.RoleOwner},
		HistorySharedSince: &hss,
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}

	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.Subscription
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, s)
	}
}

func TestRoomTypeValues(t *testing.T) {
	if model.RoomTypeGroup != "group" {
		t.Errorf("RoomTypeGroup = %q", model.RoomTypeGroup)
	}
	if model.RoomTypeDM != "dm" {
		t.Errorf("RoomTypeDM = %q", model.RoomTypeDM)
	}
}

func TestRoleValues(t *testing.T) {
	if model.RoleOwner != "owner" {
		t.Errorf("RoleOwner = %q", model.RoleOwner)
	}
	if model.RoleMember != "member" {
		t.Errorf("RoleMember = %q", model.RoleMember)
	}
}

func TestMemberTypeValues(t *testing.T) {
	if model.RoomMemberTypeIndividual != "individual" {
		t.Errorf("RoomMemberTypeIndividual = %q", model.RoomMemberTypeIndividual)
	}
	if model.RoomMemberTypeOrg != "org" {
		t.Errorf("RoomMemberTypeOrg = %q", model.RoomMemberTypeOrg)
	}
	if model.HistoryModeNone != "none" {
		t.Errorf("HistoryModeNone = %q", model.HistoryModeNone)
	}
	if model.HistoryModeAll != "all" {
		t.Errorf("HistoryModeAll = %q", model.HistoryModeAll)
	}
}

func TestRoomEventJSON(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID: "msg-1", RoomID: "room-1", UserID: "user-1",
		UserAccount: "alice", Content: "hello", CreatedAt: now,
	}

	t.Run("all fields populated", func(t *testing.T) {
		src := model.RoomEvent{
			Type:       model.RoomEventNewMessage,
			RoomID:     "room-1",
			Timestamp:  now.UnixMilli(),
			RoomName:   "General",
			RoomType:   model.RoomTypeGroup,
			SiteID:     "site-a",
			UserCount:  5,
			LastMsgAt:  now,
			LastMsgID:  "msg-1",
			Mentions:   []model.Participant{{Account: "user-2", ChineseName: "user-2", EngName: "user-2"}, {Account: "user-3", ChineseName: "user-3", EngName: "user-3"}},
			MentionAll: true,
			HasMention: true,
			Message:    &model.ClientMessage{Message: msg, Sender: &model.Participant{UserID: "user-1", Account: "alice", ChineseName: "愛麗絲", EngName: "Alice Wang"}},
		}

		data, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var dst model.RoomEvent
		if err := json.Unmarshal(data, &dst); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})

	t.Run("nil message and empty mentions omitted", func(t *testing.T) {
		src := model.RoomEvent{
			Type:      model.RoomEventNewMessage,
			RoomID:    "room-2",
			Timestamp: now.UnixMilli(),
			RoomName:  "Lobby",
			RoomType:  model.RoomTypeGroup,
			SiteID:    "site-b",
			UserCount: 3,
			LastMsgAt: now,
			LastMsgID: "msg-2",
		}

		data, err := json.Marshal(src)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal raw: %v", err)
		}
		for _, key := range []string{"mentions", "mentionAll", "hasMention", "message"} {
			if _, ok := raw[key]; ok {
				t.Errorf("expected %q to be omitted from JSON", key)
			}
		}

		var dst model.RoomEvent
		if err := json.Unmarshal(data, &dst); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(src, dst) {
			t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
		}
	})
}

func TestRoomEventTypeValues(t *testing.T) {
	if model.RoomEventNewMessage != "new_message" {
		t.Errorf("RoomEventNewMessage = %q", model.RoomEventNewMessage)
	}
}

func TestParticipantJSON(t *testing.T) {
	t.Run("with userID", func(t *testing.T) {
		p := model.Participant{
			UserID:      "u1",
			Account:     "alice",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		}
		roundTrip(t, &p, &model.Participant{})
	})

	t.Run("without userID omitted", func(t *testing.T) {
		p := model.Participant{
			Account:     "bob",
			ChineseName: "鮑勃",
			EngName:     "Bob Chen",
		}
		data, err := json.Marshal(p)
		require.NoError(t, err)

		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, hasUserID := raw["userId"]
		assert.False(t, hasUserID, "userId should be omitted when empty")

		var dst model.Participant
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, p, dst)
	})
}

func TestClientMessageJSON(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cm := model.ClientMessage{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: now,
		},
		Sender: &model.Participant{
			UserID:      "u1",
			Account:     "alice",
			ChineseName: "愛麗絲",
			EngName:     "Alice Wang",
		},
	}
	data, err := json.Marshal(cm)
	require.NoError(t, err)

	var dst model.ClientMessage
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, cm, dst)

	// Verify inline embedding — message fields should be at top level
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	assert.Contains(t, raw, "id")
	assert.Contains(t, raw, "roomId")
	assert.Contains(t, raw, "sender")
}

func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		Version:    42,
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
		Timestamp:  1735689600000,
	}

	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var dst model.RoomKeyEvent
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}

func TestNotificationEventJSON(t *testing.T) {
	src := model.NotificationEvent{
		Type:   "new_message",
		RoomID: "room-1",
		Message: model.Message{
			ID: "m1", RoomID: "room-1", UserID: "u1", UserAccount: "alice",
			Content: "hello", CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		Timestamp: 1735689600000,
	}
	roundTrip(t, &src, &model.NotificationEvent{})
}

func TestSubscriptionUpdateEventJSON(t *testing.T) {
	src := model.SubscriptionUpdateEvent{
		UserID: "u1",
		Subscription: model.Subscription{
			ID:       "s1",
			User:     model.SubscriptionUser{ID: "u1", Account: "alice"},
			RoomID:   "r1",
			SiteID:   "site-a",
			Roles:    []model.Role{model.RoleMember},
			JoinedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Action:    "added",
		Timestamp: 1735689600000,
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.SubscriptionUpdateEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}

func TestInviteMemberRequestJSON(t *testing.T) {
	src := model.InviteMemberRequest{
		InviterID:      "u1",
		InviteeID:      "u2",
		InviteeAccount: "bob",
		RoomID:         "r1",
		SiteID:         "site-a",
		Timestamp:      1735689600000,
	}
	roundTrip(t, &src, &model.InviteMemberRequest{})
}

func TestOutboxEventJSON(t *testing.T) {
	src := model.OutboxEvent{
		Type:       "member_added",
		SiteID:     "site-a",
		DestSiteID: "site-b",
		Payload:    []byte(`{"inviterId":"u1"}`),
		Timestamp:  1735689600000,
	}
	data, err := json.Marshal(&src)
	require.NoError(t, err)
	var dst model.OutboxEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	if !reflect.DeepEqual(src, dst) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", dst, src)
	}
}

func TestRoomMetadataUpdateEventJSON(t *testing.T) {
	src := model.RoomMetadataUpdateEvent{
		RoomID:        "r1",
		Name:          "general",
		UserCount:     5,
		LastMessageAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Timestamp:     1735689600000,
	}
	roundTrip(t, &src, &model.RoomMetadataUpdateEvent{})
}

func TestMemberChangeEventJSON(t *testing.T) {
	src := model.MemberChangeEvent{
		Type:     "member-added",
		RoomID:   "room-1",
		Accounts: []string{"alice", "bob"},
		SiteID:   "site-a",
	}
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var dst model.MemberChangeEvent
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, src, dst)
}

func TestRoomMemberJSON(t *testing.T) {
	t.Run("org member", func(t *testing.T) {
		src := model.RoomMember{ID: "m1", RoomID: "r1", Member: model.RoomMemberEntry{ID: "org-eng", Type: model.RoomMemberTypeOrg}}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var dst model.RoomMember
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})
	t.Run("individual member", func(t *testing.T) {
		src := model.RoomMember{ID: "m2", RoomID: "r1", Member: model.RoomMemberEntry{Type: model.RoomMemberTypeIndividual, Username: "alice"}}
		data, err := json.Marshal(src)
		require.NoError(t, err)
		var dst model.RoomMember
		require.NoError(t, json.Unmarshal(data, &dst))
		assert.Equal(t, src, dst)
	})
}

func TestAddMembersRequestJSON(t *testing.T) {
	src := model.AddMembersRequest{
		RoomID:  "r1",
		Users:   []string{"alice"},
		Orgs:    []string{"org-eng"},
		History: model.HistoryConfig{Mode: model.HistoryModeNone},
	}
	data, err := json.Marshal(src)
	require.NoError(t, err)
	var dst model.AddMembersRequest
	require.NoError(t, json.Unmarshal(data, &dst))
	assert.Equal(t, src, dst)
}

func TestRemoveMemberRequestJSON(t *testing.T) {
	src := model.RemoveMemberRequest{
		RoomID:   "r1",
		Username: "alice",
		OrgID:    "org-eng",
	}
	roundTrip(t, &src, &model.RemoveMemberRequest{})
}

func TestRemoveMemberRequestJSON_NoOrg(t *testing.T) {
	src := model.RemoveMemberRequest{
		RoomID:   "r1",
		Username: "alice",
	}
	data, err := json.Marshal(src)
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	if _, ok := raw["orgId"]; ok {
		t.Error("expected orgId to be omitted when empty")
	}

	var dst model.RemoveMemberRequest
	err = json.Unmarshal(data, &dst)
	require.NoError(t, err)
	assert.Equal(t, src, dst)
}

func TestUpdateRoleRequestJSON(t *testing.T) {
	src := model.UpdateRoleRequest{
		RoomID:   "r1",
		Username: "alice",
		NewRole:  model.RoleMember,
	}
	roundTrip(t, &src, &model.UpdateRoleRequest{})
}

// roundTrip marshals src to JSON, unmarshals into dst, and compares.
func roundTrip[T comparable](t *testing.T, src *T, dst *T) {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *dst != *src {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
	}
}
