//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, testimages.Mongo)
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate mongo container: %v", err)
		}
	})

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("get mongo uri: %v", err)
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(ctx); err != nil {
			t.Logf("disconnect mongo client: %v", err)
		}
	})
	return client.Database("notification_worker_test")
}

type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (p *recordingPublisher) Publish(_ context.Context, subj string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	return nil
}

func TestNotificationWorker_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	// Seed subscriptions. Both User.ID (sender filter) and User.Account
	// (subject builder) must be set — handler reads Account when emitting,
	// but filters by ID.
	_, err := db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "u1"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "u2"}, RoomID: "r1"},
		model.Subscription{ID: "s3", User: model.SubscriptionUser{ID: "u3", Account: "u3"}, RoomID: "r1"},
	})
	require.NoError(t, err, "seed subscriptions")

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
		subject.Notification("u2"): true,
		subject.Notification("u3"): true,
	}
	for _, s := range pub.subjects {
		if !expected[s] {
			t.Errorf("unexpected publish to %q", s)
		}
	}
}
