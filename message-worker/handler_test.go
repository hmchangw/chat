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

	msg := model.Message{
		ID:       "msg-1",
		RoomID:   "r1",
		UserID:   "u1",
		Username: "alice",
		Content:  "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	store.EXPECT().
		SaveMessage(gomock.Any(), &msg).
		Return(nil)

	h := &Handler{store: store}

	data, _ := json.Marshal(evt)
	if err := h.processMessage(context.Background(), data); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandler_ProcessMessage_SaveError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	msg := model.Message{
		ID:       "msg-1",
		RoomID:   "r1",
		UserID:   "u1",
		Username: "alice",
		Content:  "hello",
	}
	evt := model.MessageEvent{Message: msg, SiteID: "site-a"}

	store.EXPECT().
		SaveMessage(gomock.Any(), gomock.Any()).
		Return(fmt.Errorf("cassandra unavailable"))

	h := &Handler{store: store}

	data, _ := json.Marshal(evt)
	if err := h.processMessage(context.Background(), data); err == nil {
		t.Fatal("expected error for save failure")
	}
}

func TestHandler_ProcessMessage_MalformedJSON(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockMessageStore(ctrl)

	h := &Handler{store: store}

	if err := h.processMessage(context.Background(), []byte("{invalid")); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
