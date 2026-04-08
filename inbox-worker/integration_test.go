//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
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

func (p *recordingPublisher) Publish(_ context.Context, subj string, _ []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	return nil
}

func TestInboxWorker_MemberAdded_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	// Create outbox event for member_added
	invite := model.InviteMemberRequest{InviterID: "u1", InviteeID: "u2", RoomID: "r1", SiteID: "site-b"}
	inviteData, _ := json.Marshal(invite)
	evt := model.OutboxEvent{Type: "member_added", SiteID: "site-a", DestSiteID: "site-b", Payload: inviteData}
	evtData, _ := json.Marshal(evt)

	if err := handler.HandleEvent(ctx, evtData); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify subscription was created in MongoDB
	var sub model.Subscription
	err := db.Collection("subscriptions").FindOne(ctx, bson.M{"u._id": "u2", "roomId": "r1"}).Decode(&sub)
	if err != nil {
		t.Fatalf("subscription not found: %v", err)
	}
	if sub.Role != model.RoleMember {
		t.Errorf("subscription Role = %v, want member", sub.Role)
	}

	// Verify notification was published
	if len(pub.subjects) != 1 {
		t.Fatalf("got %d publishes, want 1", len(pub.subjects))
	}
}

func TestInboxWorker_RoomSync_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	room := model.Room{ID: "r1", Name: "synced-room", Type: model.RoomTypeGroup, UserCount: 5}
	roomData, _ := json.Marshal(room)
	evt := model.OutboxEvent{Type: "room_sync", Payload: roomData}
	evtData, _ := json.Marshal(evt)

	if err := handler.HandleEvent(ctx, evtData); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify room was upserted
	var got model.Room
	err := db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r1"}).Decode(&got)
	if err != nil {
		t.Fatalf("room not found: %v", err)
	}
	if got.Name != "synced-room" {
		t.Errorf("Name = %q, want synced-room", got.Name)
	}
}
