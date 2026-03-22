//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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

	// Create keyspace and table
	if err := session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec(); err != nil {
		t.Fatalf("create keyspace: %v", err)
	}
	if err := session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages (room_id text, created_at timestamp, id text, user_id text, content text, PRIMARY KEY (room_id, created_at)) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec(); err != nil {
		t.Fatalf("create table: %v", err)
	}

	// Reconnect with keyspace
	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("create keyspace session: %v", err)
	}
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func TestMongoStore_Integration(t *testing.T) {
	db := setupMongo(t)
	cassSession := setupCassandra(t)
	store := NewMongoStore(db, cassSession)
	ctx := context.Background()

	// Seed subscription
	db.Collection("subscriptions").InsertOne(ctx, model.Subscription{
		ID: "s1", UserID: "u1", RoomID: "r1", Role: model.RoleMember,
	})

	// Test GetSubscription
	sub, err := store.GetSubscription(ctx, "u1", "r1")
	if err != nil {
		t.Fatalf("GetSubscription: %v", err)
	}
	if sub.Role != model.RoleMember {
		t.Errorf("Role = %q", sub.Role)
	}

	// Test SaveMessage
	now := time.Now().UTC().Truncate(time.Millisecond)
	msg := model.Message{ID: "m1", RoomID: "r1", UserID: "u1", Content: "hello", CreatedAt: now}
	if err := store.SaveMessage(ctx, msg); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	// Verify message in Cassandra
	var gotContent string
	if err := cassSession.Query(`SELECT content FROM messages WHERE room_id = ? AND created_at = ?`, "r1", now).Scan(&gotContent); err != nil {
		t.Fatalf("query message: %v", err)
	}
	if gotContent != "hello" {
		t.Errorf("content = %q, want hello", gotContent)
	}

	// Test UpdateRoomLastMessage
	db.Collection("rooms").InsertOne(ctx, bson.M{"_id": "r1", "name": "general"})
	if err := store.UpdateRoomLastMessage(ctx, "r1", now); err != nil {
		t.Fatalf("UpdateRoomLastMessage: %v", err)
	}
}
