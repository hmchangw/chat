package main

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessInvite(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockSubscriptionStore(ctrl)

	var createdSub *model.Subscription
	store.EXPECT().
		CreateSubscription(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, s *model.Subscription) error {
			createdSub = s
			return nil
		})
	store.EXPECT().
		IncrementUserCount(gomock.Any(), "r1").
		Return(nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 2, SiteID: "site-a"}, nil)
	store.EXPECT().
		ListByRoom(gomock.Any(), "r1").
		Return([]model.Subscription{
			{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Roles: []model.Role{model.RoleOwner}},
			{User: model.SubscriptionUser{ID: "u2", Username: "bob"}, RoomID: "r1", Roles: []model.Role{model.RoleMember}},
		}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", InviteeUsername: "bob", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	if err := h.processInvite(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify created subscription has the correct username
	if createdSub == nil {
		t.Fatal("expected subscription to be created")
	}
	if createdSub.User.Username != "bob" {
		t.Errorf("expected subscription username %q, got %q", "bob", createdSub.User.Username)
	}

	// Verify notifications published (subscription update + room metadata for existing members)
	if len(published) < 3 {
		t.Errorf("expected at least 3 publishes, got %d", len(published))
	}

	subjectSet := make(map[string]bool)
	for _, p := range published {
		subjectSet[p.subj] = true
	}

	if !subjectSet["chat.user.bob.event.subscription.update"] {
		t.Errorf("expected subscription update published to chat.user.bob.event.subscription.update, subjects: %v", subjectSet)
	}
	if !subjectSet["chat.user.alice.event.room.metadata.update"] {
		t.Errorf("expected room metadata published to chat.user.alice.event.room.metadata.update, subjects: %v", subjectSet)
	}
	if !subjectSet["chat.user.bob.event.room.metadata.update"] {
		t.Errorf("expected room metadata published to chat.user.bob.event.room.metadata.update, subjects: %v", subjectSet)
	}
}

type publishedMsg struct {
	subj string
	data []byte
}
