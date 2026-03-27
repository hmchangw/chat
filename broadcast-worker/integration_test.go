//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:8")
	if err != nil {
		t.Fatalf("start mongo: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("get mongo uri: %v", err)
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	t.Cleanup(func() { client.Disconnect(ctx) })
	return client.Database("chat_test")
}

type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (p *recordingPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	return nil
}

func TestBroadcastWorker_GroupRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	// Seed room and subscriptions
	db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeGroup, UserCount: 2,
	})
	db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2"}, RoomID: "r1"},
	})

	lookup := &mongoRoomLookup{
		roomCol: db.Collection("rooms"),
		subCol:  db.Collection("subscriptions"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(lookup, pub)

	evt := model.MessageEvent{
		RoomID: "r1",
		SiteID: "site-a",
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello",
			CreatedAt: time.Now().UTC(),
		},
	}
	data, _ := json.Marshal(evt)

	if err := handler.HandleMessage(ctx, data); err != nil {
		t.Fatalf("HandleMessage: %v", err)
	}

	// Should publish metadata update + room message stream
	if len(pub.subjects) != 2 {
		t.Fatalf("got %d publishes, want 2: %v", len(pub.subjects), pub.subjects)
	}

	wantMeta := subject.RoomMetadataUpdate("r1")
	wantStream := subject.RoomMsgStream("r1")
	if pub.subjects[0] != wantMeta {
		t.Errorf("first publish = %q, want %q", pub.subjects[0], wantMeta)
	}
	if pub.subjects[1] != wantStream {
		t.Errorf("second publish = %q, want %q", pub.subjects[1], wantStream)
	}
}
