//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
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

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	if err != nil {
		t.Fatalf("start cassandra: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	if err != nil {
		t.Fatalf("get cassandra host: %v", err)
	}

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("create cassandra session: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	if err := session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec(); err != nil {
		t.Fatalf("create keyspace: %v", err)
	}
	if err := session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages (room_id text, created_at timestamp, id text, user_id text, content text, PRIMARY KEY (room_id, created_at)) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec(); err != nil {
		t.Fatalf("create table: %v", err)
	}

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("create keyspace session: %v", err)
	}
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func TestRealStore_Integration(t *testing.T) {
	db := setupMongo(t)
	cassSession := setupCassandra(t)
	store := NewRealStore(db, cassSession)
	ctx := context.Background()

	// Seed subscription
	joinTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", User: model.SubscriptionUser{ID: "u1"}, RoomID: "r1", Role: model.RoleMember,
		SharedHistorySince: joinTime,
	})

	// Test GetSubscription
	sub, err := store.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if sub.User.ID != "u1" {
		t.Errorf("User.ID = %q", sub.User.ID)
	}

	// Seed messages in Cassandra
	base := joinTime
	for i := 0; i < 5; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		cassSession.Query(`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
			"r1", ts, "m"+string(rune('0'+i)), "u1", "msg-"+string(rune('0'+i))).Exec()
	}

	// Test ListMessages
	msgs, err := store.ListMessages(ctx, "r1", joinTime, time.Now(), 3)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	// Should be newest first (DESC)
	if msgs[0].CreatedAt.Before(msgs[1].CreatedAt) {
		t.Error("messages not in descending order")
	}
}
