package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(&model.Subscription{UserID: "u1", RoomID: "r1", Role: model.RoleMember}, nil)
	store.EXPECT().
		SaveMessage(gomock.Any(), gomock.Any()).
		Return(nil)
	store.EXPECT().
		UpdateRoomLastMessage(gomock.Any(), "r1", gomock.Any()).
		Return(nil)

	var published []publishedMsg
	publisher := func(subj string, data []byte) error {
		published = append(published, publishedMsg{subj: subj, data: data})
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publish: publisher}

	req := model.SendMessageRequest{RoomID: "r1", Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	reply, err := h.processMessage(context.Background(), "u1", "r1", "site-a", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var msg model.Message
	if err := json.Unmarshal(reply, &msg); err != nil {
		t.Fatalf("unmarshal reply: %v", err)
	}
	if msg.Content != "hello" || msg.UserID != "u1" || msg.RoomID != "r1" {
		t.Errorf("got %+v", msg)
	}
	if msg.ID == "" {
		t.Error("message ID should be set")
	}
	if len(published) == 0 {
		t.Fatal("expected fanout publish")
	}
}

func TestHandler_ProcessMessage_NotSubscribed(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(nil, fmt.Errorf("subscription not found"))

	h := &Handler{store: store, siteID: "site-a", publish: func(string, []byte) error { return nil }}

	req := model.SendMessageRequest{RoomID: "r1", Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	_, err := h.processMessage(context.Background(), "u1", "r1", "site-a", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}

type publishedMsg struct {
	subj string
	data []byte
}
