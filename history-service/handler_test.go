package main

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

func TestHandler_HandleHistory_Success(t *testing.T) {
	store := NewMemoryStore()
	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", Role: model.RoleMember,
		SharedHistorySince: joinTime,
	})

	base := joinTime
	for i := 0; i < 5; i++ {
		store.messages = append(store.messages, model.Message{
			ID: fmt.Sprintf("m%d", i), RoomID: "r1", UserID: "u1",
			Content:   fmt.Sprintf("msg-%d", i),
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}

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
	store := NewMemoryStore()
	h := &Handler{store: store}

	req := model.HistoryRequest{RoomID: "r1", Limit: 10}
	data, _ := json.Marshal(req)

	_, err := h.handleHistory("u1", "r1", data)
	if err == nil {
		t.Fatal("expected error for unsubscribed user")
	}
}

func TestHandler_HandleHistory_SharedHistorySinceFilter(t *testing.T) {
	store := NewMemoryStore()
	joinTime := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	store.subscriptions = append(store.subscriptions, model.Subscription{
		UserID: "u1", RoomID: "r1", SharedHistorySince: joinTime,
	})

	// Messages before and after join
	store.messages = append(store.messages,
		model.Message{ID: "m1", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}, // before join
		model.Message{ID: "m2", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC)}, // after join
		model.Message{ID: "m3", RoomID: "r1", CreatedAt: time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)}, // after join
	)

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
