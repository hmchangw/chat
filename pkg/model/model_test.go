package model_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestUserJSON(t *testing.T) {
	u := model.User{ID: "u1", Name: "alice", Username: "alice", SiteID: "site-a"}
	roundTrip(t, &u, &model.User{})
}

func TestRoomJSON(t *testing.T) {
	r := model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup,
		CreatedBy: "u1", SiteID: "site-a", UserCount: 5,
		Origin:           "site-a",
		LastMsgAt:        time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		LastMsgID:        "m1",
		LastMentionAllAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		CreatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &r, &model.Room{})
}

func TestMessageJSON(t *testing.T) {
	m := model.Message{
		ID: "m1", RoomID: "r1", UserID: "u1", Username: "alice",
		Content:   "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &m, &model.Message{})
}

func TestSendMessageRequestJSON(t *testing.T) {
	r := model.SendMessageRequest{
		ID:        "msg-uuid-1",
		Content:   "hello world",
		RequestID: "req-1",
	}
	roundTrip(t, &r, &model.SendMessageRequest{})
}

func TestMessageEventJSON(t *testing.T) {
	e := model.MessageEvent{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Username: "alice",
			Content:   "hello",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		},
		SiteID: "site-a",
	}
	roundTrip(t, &e, &model.MessageEvent{})
}

func TestSubscriptionJSON(t *testing.T) {
	s := model.Subscription{
		ID:     "s1",
		User:   model.SubscriptionUser{ID: "u1", Username: "alice"},
		RoomID: "r1", SiteID: "site-a",
		Role:               model.RoleOwner,
		SharedHistorySince: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		JoinedAt:           time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		HasMention:         true,
	}
	roundTrip(t, &s, &model.Subscription{})
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

func TestRoomEventJSON(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID: "msg-1", RoomID: "room-1", UserID: "user-1",
		Username: "alice", Content: "hello", CreatedAt: now,
	}

	t.Run("all fields populated", func(t *testing.T) {
		src := model.RoomEvent{
			Type:       model.RoomEventNewMessage,
			RoomID:     "room-1",
			Timestamp:  now,
			RoomName:   "General",
			RoomType:   model.RoomTypeGroup,
			Origin:     "site-a",
			UserCount:  5,
			LastMsgAt:  now,
			LastMsgID:  "msg-1",
			Mentions:   []string{"user-2", "user-3"},
			MentionAll: true,
			HasMention: true,
			Message:    &msg,
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
			Timestamp: now,
			RoomName:  "Lobby",
			RoomType:  model.RoomTypeGroup,
			Origin:    "site-b",
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

func TestRoomKeyEventJSON(t *testing.T) {
	src := model.RoomKeyEvent{
		RoomID:     "room-1",
		VersionID:  "v-abc-123",
		PublicKey:  []byte{0x04, 0x01, 0x02, 0x03},
		PrivateKey: []byte{0x0a, 0x0b, 0x0c},
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
