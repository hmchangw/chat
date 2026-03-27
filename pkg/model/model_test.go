package model_test

import (
	"encoding/json"
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
		ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello",
		CreatedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}
	roundTrip(t, &m, &model.Message{})
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
