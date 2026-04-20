//go:build integration

package main

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:4.4.15")
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

func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
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
		userCol: db.Collection("users"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	// Seed user for lookup
	_, err := db.Collection("users").InsertOne(ctx, model.User{ID: "u2", Account: "u2", SiteID: "site-b"})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Create outbox event for member_added
	change := model.MemberAddEvent{
		Type: "member_added", RoomID: "r1", Accounts: []string{"u2"}, SiteID: "site-b",
		JoinedAt:           time.Now().UTC().UnixMilli(),
		HistorySharedSince: time.Now().UTC().UnixMilli(),
		Timestamp:          time.Now().UTC().UnixMilli(),
	}
	changeData, _ := json.Marshal(change)
	evt := model.OutboxEvent{Type: "member_added", SiteID: "site-a", DestSiteID: "site-b", Payload: changeData}
	evtData, _ := json.Marshal(evt)

	if err := handler.HandleEvent(ctx, evtData); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	// Verify subscription was created in MongoDB
	var sub model.Subscription
	err = db.Collection("subscriptions").FindOne(ctx, bson.M{"u._id": "u2", "roomId": "r1"}).Decode(&sub)
	if err != nil {
		t.Fatalf("subscription not found: %v", err)
	}
	if len(sub.Roles) == 0 || sub.Roles[0] != model.RoleMember {
		t.Errorf("Roles = %v, want [member]", sub.Roles)
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
		userCol: db.Collection("users"),
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

func TestInboxWorker_RoleUpdated_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
		userCol: db.Collection("users"),
	}
	pub := &recordingPublisher{}
	handler := NewHandler(store, pub)

	_, err := db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
		RoomID: "room-1", SiteID: "site-a", Roles: []model.Role{model.RoleMember},
	})
	if err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	subEvt := model.SubscriptionUpdateEvent{
		UserID: "u2",
		Subscription: model.Subscription{
			ID: "s1", User: model.SubscriptionUser{ID: "u2", Account: "bob"},
			RoomID: "room-1", SiteID: "site-a", Roles: []model.Role{model.RoleOwner},
		},
		Action: "role_updated", Timestamp: time.Now().UTC().UnixMilli(),
	}
	subEvtData, _ := json.Marshal(subEvt)

	evt := model.OutboxEvent{
		Type: "role_updated", SiteID: "site-a", DestSiteID: "site-b",
		Payload: subEvtData, Timestamp: time.Now().UTC().UnixMilli(),
	}
	evtData, _ := json.Marshal(evt)

	err = handler.HandleEvent(ctx, evtData)
	if err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}

	var sub model.Subscription
	err = db.Collection("subscriptions").FindOne(ctx, bson.M{"u.account": "bob", "roomId": "room-1"}).Decode(&sub)
	if err != nil {
		t.Fatalf("subscription not found: %v", err)
	}
	if !slices.Contains(sub.Roles, model.RoleOwner) {
		t.Errorf("roles = %v, want to contain owner", sub.Roles)
	}

	// No SubscriptionUpdateEvent is published — room-worker already handles
	// user notification via NATS supercluster routing.
	if len(pub.subjects) != 0 {
		t.Fatalf("expected 0 publishes (room-worker handles notification), got %d", len(pub.subjects))
	}
}

func TestInboxWorker_MemberRemoved_Integration(t *testing.T) {
	db := setupMongo(t)
	store := &mongoInboxStore{
		subCol:  db.Collection("subscriptions"),
		roomCol: db.Collection("rooms"),
	}
	pub := &recordingPublisher{}
	h := NewHandler(store, pub)

	ctx := context.Background()

	_, err := store.subCol.InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "bob"},
		RoomID: "r1", SiteID: "site-a", Roles: []model.Role{model.RoleMember},
		JoinedAt: time.Now().UTC(),
	})
	require.NoError(t, err)

	memberEvt := model.MemberRemoveEvent{
		Type: "member-removed", RoomID: "r1", Accounts: []string{"bob"}, SiteID: "site-a",
	}
	payload, _ := json.Marshal(memberEvt)
	evt := model.OutboxEvent{
		Type: "member_removed", SiteID: "site-a", DestSiteID: "site-b",
		Payload: payload, Timestamp: time.Now().UnixMilli(),
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, h.HandleEvent(ctx, data))

	// Subscription deleted — room_members lives only on the room's site.
	count, err := store.subCol.CountDocuments(ctx, bson.M{"u._id": "u1", "roomId": "r1"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// No publish — room-worker handles user notification via NATS supercluster.
	assert.Empty(t, pub.subjects)
}
