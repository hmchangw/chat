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

	store.EXPECT().
		CreateSubscription(gomock.Any(), gomock.Any()).
		Return(nil)
	store.EXPECT().
		IncrementUserCount(gomock.Any(), "r1").
		Return(nil)
	store.EXPECT().
		GetRoom(gomock.Any(), "r1").
		Return(&model.Room{ID: "r1", Name: "general", UserCount: 2, SiteID: "site-a"}, nil)
	store.EXPECT().
		ListByRoom(gomock.Any(), "r1").
		Return([]model.Subscription{
			{UserID: "u1", RoomID: "r1", Role: model.RoleOwner},
			{UserID: "u2", RoomID: "r1", Role: model.RoleMember},
		}, nil)

	var published []publishedMsg
	h := &Handler{store: store, siteID: "site-a", publish: func(subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}}

	req := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-a"}
	data, _ := json.Marshal(req)

	if err := h.processInvite(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify notifications published (subscription update + room metadata for existing members)
	if len(published) < 2 {
		t.Errorf("expected at least 2 publishes, got %d", len(published))
	}
}

type publishedMsg struct {
	subj string
	data []byte
}
