//go:build integration

package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
	"github.com/hmchangw/chat/pkg/userstore"
)

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	stmts := []string{
		`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`,
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN, account TEXT)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
			room_id       TEXT,
			created_at    TIMESTAMP,
			message_id    TEXT,
			sender        FROZEN<"Participant">,
			msg           TEXT,
			type          TEXT,
			sys_msg_data  BLOB,
			site_id       TEXT,
			updated_at    TIMESTAMP,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
			message_id   TEXT,
			created_at   TIMESTAMP,
			sender       FROZEN<"Participant">,
			msg          TEXT,
			type         TEXT,
			sys_msg_data BLOB,
			site_id      TEXT,
			updated_at   TIMESTAMP,
			PRIMARY KEY (message_id, created_at)
		) WITH CLUSTERING ORDER BY (created_at DESC)`,
	}
	for _, stmt := range stmts {
		require.NoError(t, session.Query(stmt).Exec())
	}

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sender := &cassParticipant{
		ID:          "u-1",
		EngName:     "Alice Wang",
		CompanyName: "愛麗絲",
		Account:     "alice",
	}
	msg := &model.Message{
		ID:          "m-1",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello world",
		CreatedAt:   now,
	}

	err := store.SaveMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("messages_by_room row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello world", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_id row exists with correct fields", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello world", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})
}

func TestHandler_Integration(t *testing.T) {
	ctx := context.Background()

	// Start Cassandra
	cassSession := setupCassandra(t)

	// Start MongoDB
	mongoContainer, err := mongodb.Run(ctx, "mongo:7")
	require.NoError(t, err)
	t.Cleanup(func() { mongoContainer.Terminate(ctx) })

	mongoURI, err := mongoContainer.ConnectionString(ctx)
	require.NoError(t, err)

	mongoClient, err := mongoutil.Connect(ctx, mongoURI)
	require.NoError(t, err)
	t.Cleanup(func() { mongoutil.Disconnect(ctx, mongoClient) })

	userCol := mongoClient.Database("chat_test").Collection("users")
	_, err = userCol.InsertOne(ctx, bson.M{
		"_id":         "u-1",
		"account":     "alice",
		"siteId":      "site-a",
		"engName":     "Alice Wang",
		"chineseName": "愛麗絲",
		"employeeId":  "EMP001",
	})
	require.NoError(t, err)

	store := NewCassandraStore(cassSession)
	us := userstore.NewMongoStore(userCol)
	h := NewHandler(store, us)

	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Message: model.Message{
			ID:          "m-2",
			RoomID:      "r-2",
			UserID:      "u-1",
			UserAccount: "alice",
			Content:     "integration test message",
			CreatedAt:   now,
		},
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
	}

	data, err := json.Marshal(evt)
	require.NoError(t, err)

	err = h.processMessage(ctx, data)
	require.NoError(t, err)

	var gotMsg string
	err = cassSession.Query(
		`SELECT msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		"r-2", now, "m-2",
	).Scan(&gotMsg)
	require.NoError(t, err)
	assert.Equal(t, "integration test message", gotMsg)
}
