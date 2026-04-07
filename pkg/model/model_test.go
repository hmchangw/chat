package model_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

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
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	edited := now.Add(5 * time.Minute)
	updated := now.Add(10 * time.Minute)
	threadParent := now.Add(-1 * time.Hour)

	msg := model.Message{
		ID:                    "m1",
		RoomID:                "r1",
		CreatedAt:             now,
		Sender:                model.Participant{ID: "u1", UserName: "alice"},
		TargetUser:            &model.Participant{ID: "u2", UserName: "bob"},
		Content:               "hello world",
		Mentions:              []model.Participant{{ID: "u3", UserName: "charlie"}},
		Attachments:           [][]byte{[]byte("attach1")},
		File:                  &model.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"},
		Card:                  &model.Card{Template: "approval", Data: []byte(`{"k":"v"}`)},
		CardAction:            &model.CardAction{Verb: "approve", CardID: "c1"},
		TShow:                 true,
		ThreadParentCreatedAt: &threadParent,
		VisibleTo:             "u1",
		Unread:                true,
		Reactions:             map[string][]model.Participant{"thumbsup": {{ID: "u2", UserName: "bob"}}},
		Deleted:               false,
		SysMsgType:            "user_joined",
		SysMsgData:            []byte(`{"userId":"u3"}`),
		FederateFrom:          "site-remote",
		EditedAt:              &edited,
		UpdatedAt:             &updated,
	}

	roundTripDeep(t, &msg, &model.Message{})
}

func TestMessageJSON_Minimal(t *testing.T) {
	msg := model.Message{
		ID:        "m1",
		RoomID:    "r1",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Sender:    model.Participant{ID: "u1", UserName: "alice"},
		Content:   "hi",
	}
	got := roundTripDeep(t, &msg, &model.Message{})
	if got.TargetUser != nil {
		t.Error("expected nil TargetUser")
	}
	if got.File != nil {
		t.Error("expected nil File")
	}
	if got.Mentions != nil {
		t.Error("expected nil Mentions")
	}
	if got.Reactions != nil {
		t.Error("expected nil Reactions")
	}
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
			ID:        "m1",
			RoomID:    "r1",
			CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			Sender:    model.Participant{ID: "u1", UserName: "alice"},
			Content:   "hello",
		},
		SiteID: "site-a",
	}
	roundTripDeep(t, &e, &model.MessageEvent{})
}

func TestSubscriptionJSON(t *testing.T) {
	hss := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := model.Subscription{
		ID:                 "s1",
		User:               model.SubscriptionUser{ID: "u1", Username: "alice"},
		RoomID:             "r1",
		SiteID:             "site-a",
		Role:               model.RoleOwner,
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

func TestRoomEventJSON(t *testing.T) {
	now := time.Date(2026, 3, 26, 12, 0, 0, 0, time.UTC)
	msg := model.Message{
		ID:        "msg-1",
		RoomID:    "room-1",
		Sender:    model.Participant{ID: "user-1", UserName: "alice"},
		Content:   "hello",
		CreatedAt: now,
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
		Version:    42,
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

func TestParticipantJSON(t *testing.T) {
	p := model.Participant{
		ID:          "u1",
		UserName:    "alice",
		EngName:     "Alice Smith",
		CompanyName: "Acme Corp",
		AppID:       "app-1",
		AppName:     "MyApp",
		IsBot:       true,
	}
	roundTrip(t, &p, &model.Participant{})
}

func TestParticipantJSON_Minimal(t *testing.T) {
	p := model.Participant{ID: "u1", UserName: "alice"}
	roundTrip(t, &p, &model.Participant{})
}

func TestFileJSON(t *testing.T) {
	f := model.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"}
	roundTrip(t, &f, &model.File{})
}

func TestCardJSON(t *testing.T) {
	c := model.Card{Template: "approval", Data: []byte(`{"key":"value"}`)}
	roundTripDeep(t, &c, &model.Card{})
}

func TestCardJSON_NilData(t *testing.T) {
	c := model.Card{Template: "simple"}
	roundTripDeep(t, &c, &model.Card{})
}

func TestCardActionJSON(t *testing.T) {
	ca := model.CardAction{
		Verb:        "approve",
		Text:        "Approve",
		CardID:      "c1",
		DisplayText: "Click to approve",
		HideExecLog: true,
		CardTmID:    "tm1",
		Data:        []byte(`{"action":"yes"}`),
	}
	roundTripDeep(t, &ca, &model.CardAction{})
}

func TestCardActionJSON_Minimal(t *testing.T) {
	ca := model.CardAction{Verb: "click"}
	roundTripDeep(t, &ca, &model.CardAction{})
}

func TestUnmarshalUDT_UnknownField(t *testing.T) {
	assert.NoError(t, (&model.Participant{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&model.File{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&model.Card{}).UnmarshalUDT("nonexistent", nil, nil))
	assert.NoError(t, (&model.CardAction{}).UnmarshalUDT("nonexistent", nil, nil))
}

func TestMarshalUDT_UnknownField(t *testing.T) {
	data, err := (&model.Participant{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&model.File{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&model.Card{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)

	data, err = (&model.CardAction{}).MarshalUDT("nonexistent", nil)
	assert.NoError(t, err)
	assert.Nil(t, data)
}

// roundTrip marshals src to JSON, unmarshals into dst, and compares using ==.
// Use for types satisfying comparable (no slices/maps).
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

// roundTripDeep marshals src to JSON, unmarshals into dst, and compares using
// reflect.DeepEqual. Use for types with slices, maps, or byte fields.
func roundTripDeep[T any](t *testing.T, src *T, dst *T) *T {
	t.Helper()
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(*dst, *src) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", *dst, *src)
	}
	return dst
}
