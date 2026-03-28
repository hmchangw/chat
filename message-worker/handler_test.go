package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_ProcessMessage_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{User: model.SubscriptionUser{ID: "u1", Username: "alice"}, RoomID: "r1", Role: model.RoleMember}, nil)
	store.EXPECT().
		SaveMessage(gomock.Any(), gomock.Any()).
		Return(nil)

	var published []*nats.Msg
	publisher := func(msg *nats.Msg) error {
		published = append(published, msg)
		return nil
	}

	h := &Handler{store: store, siteID: "site-a", publishMsg: publisher}

	req := model.SendMessageRequest{Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	reply, err := h.processMessage(context.Background(), "alice", "r1", "site-a", data)
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
		GetSubscription(gomock.Any(), "alice", "r1").
		Return(nil, fmt.Errorf("subscription not found"))

	h := &Handler{store: store, siteID: "site-a", publishMsg: func(*nats.Msg) error { return nil }}

	req := model.SendMessageRequest{Content: "hello", RequestID: "req-1"}
	data, _ := json.Marshal(req)

	_, err := h.processMessage(context.Background(), "alice", "r1", "site-a", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}
