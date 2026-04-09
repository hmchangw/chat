//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/hmchangw/chat/pkg/model"
)

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
	if err := session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages (room_id text, created_at timestamp, id text, user_id text, user_account text, content text, PRIMARY KEY (room_id, created_at)) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec(); err != nil {
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

func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	msg := model.Message{
		ID:          "m1",
		RoomID:      "r1",
		UserID:      "u1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   now.UnixMilli(),
	}
	err := store.SaveMessage(ctx, msg)
	require.NoError(t, err)

	var gotContent, gotUserID string
	err = cassSession.Query(
		`SELECT content, user_id FROM messages WHERE room_id = ? AND created_at = ?`,
		"r1", now,
	).Scan(&gotContent, &gotUserID)
	require.NoError(t, err)
	assert.Equal(t, "hello", gotContent)
	assert.Equal(t, "u1", gotUserID)
}
