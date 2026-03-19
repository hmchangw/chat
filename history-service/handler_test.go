package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"go.uber.org/mock/gomock"
)

func TestHandler_HandleHistory_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockHistoryStore(ctrl)

	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(&model.Subscription{
			UserID: "u1", RoomID: "r1", Role: model.RoleMember,
			SharedHistorySince: joinTime,
		}, nil)

	var msgs []model.Message
	for i := 4; i >= 0; i-- {
		msgs = append(msgs, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1", UserID: "u1",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: joinTime.Add(time.Duration(i) * time.Minute),
		})
	}
	// Handler requests limit+1 (4) to detect hasMore
	store.EXPECT().
		ListMessages(gomock.Any(), "r1", joinTime, gomock.Any(), 4).
		Return(msgs[:4], nil) // return 4 messages → hasMore=true

	h := &Handler{store: store}

	req := model.HistoryRequest{RoomID: "r1", Limit: 3}
	data, _ := json.Marshal(req)

	resp, err := h.handleHistory("u1", "r1", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var histResp model.HistoryResponse
	json.Unmarshal(resp, &histResp)
	if len(histResp.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(histResp.Messages))
	}
	if !histResp.HasMore {
		t.Error("HasMore should be true")
	}
}

func TestHandler_HandleHistory_NotSubscribed(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockHistoryStore(ctrl)

	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(nil, fmt.Errorf("subscription not found"))

	h := &Handler{store: store}

	req := model.HistoryRequest{RoomID: "r1", Limit: 10}
	data, _ := json.Marshal(req)

	_, err := h.handleHistory("u1", "r1", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}

func TestHandler_HandleHistory_SharedHistorySinceFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockHistoryStore(ctrl)

	joinTime := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	store.EXPECT().
		GetSubscription(gomock.Any(), "u1", "r1").
		Return(&model.Subscription{
			UserID: "u1", RoomID: "r1",
			SharedHistorySince: joinTime,
		}, nil)

	// Only messages after join should be returned by the store
	store.EXPECT().
		ListMessages(gomock.Any(), "r1", joinTime, gomock.Any(), 101).
		Return([]model.Message{
			{ID: "m3", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)},
			{ID: "m2", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)},
		}, nil)

	h := &Handler{store: store}
	req := model.HistoryRequest{RoomID: "r1", Limit: 100}
	data, _ := json.Marshal(req)

	resp, err := h.handleHistory("u1", "r1", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var histResp model.HistoryResponse
	json.Unmarshal(resp, &histResp)
	if len(histResp.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (only after join)", len(histResp.Messages))
	}
}
