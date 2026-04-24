//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
)

func setupMongo(t *testing.T) *mongo.Database {
	return testutil.MongoDB(t, "notification_worker_test")
}

type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	return nil
}

func TestNotificationWorker_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	// Seed subscriptions
	db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "carol"}, RoomID: "r1"},
	})

	memberLookup := &mongoMemberLookup{col: db.Collection("subscriptions")}
	pub := &recordingPublisher{}
	handler := NewHandler(memberLookup, pub)

	evt := model.MessageEvent{
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello",
			CreatedAt: time.Now().UTC(),
		},
	}
	data, _ := json.Marshal(evt)

	if err := handler.HandleMessage(ctx, data); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	// Should notify u2 and u3 (not u1 who is the sender)
	if len(pub.subjects) != 2 {
		t.Fatalf("got %d notifications, want 2: %v", len(pub.subjects), pub.subjects)
	}

	expected := map[string]bool{
		subject.Notification("bob"):   true,
		subject.Notification("carol"): true,
	}
	for _, s := range pub.subjects {
		if !expected[s] {
			t.Errorf("unexpected publish to %q", s)
		}
	}
}
