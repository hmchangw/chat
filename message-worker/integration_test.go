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
	"go.mongodb.org/mongo-driver/v2/mongo"

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
			site_id       TEXT,
			updated_at    TIMESTAMP,
			mentions      SET<FROZEN<"Participant">>,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
			message_id TEXT,
			created_at TIMESTAMP,
			sender     FROZEN<"Participant">,
			msg        TEXT,
			site_id    TEXT,
			updated_at TIMESTAMP,
			mentions   SET<FROZEN<"Participant">>,
			PRIMARY KEY (message_id, created_at)
		) WITH CLUSTERING ORDER BY (created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.thread_messages_by_room (
			room_id            TEXT,
			thread_room_id     TEXT,
			created_at         TIMESTAMP,
			message_id         TEXT,
			thread_message_id  TEXT,
			sender             FROZEN<"Participant">,
			msg                TEXT,
			site_id            TEXT,
			updated_at         TIMESTAMP,
			mentions           SET<FROZEN<"Participant">>,
			PRIMARY KEY ((room_id), thread_room_id, created_at, message_id)
		) WITH CLUSTERING ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)`,
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

func setupMongo(t *testing.T) *mongo.Database {
	t.Helper()
	ctx := context.Background()
	container, err := mongodb.Run(ctx, "mongo:7")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	uri, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	client, err := mongoutil.Connect(ctx, uri)
	require.NoError(t, err)
	t.Cleanup(func() { mongoutil.Disconnect(ctx, client) })

	return client.Database("chat_test")
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
		Content:     "hello @bob",
		CreatedAt:   now,
		Mentions: []model.Participant{{
			UserID:      "u-bob",
			Account:     "bob",
			ChineseName: "鮑勃",
			EngName:     "Bob Chen",
		}},
	}

	err := store.SaveMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("messages_by_room row correct", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_room mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", now, "m-1",
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
		assert.Equal(t, "u-bob", gotMentions[0].ID)
	})

	t.Run("messages_by_id row correct", func(t *testing.T) {
		var gotMsg, gotSiteID string
		var gotUpdatedAt time.Time
		err := cassSession.Query(
			`SELECT msg, site_id, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMsg, &gotSiteID, &gotUpdatedAt)
		require.NoError(t, err)
		assert.Equal(t, "hello @bob", gotMsg)
		assert.Equal(t, "site-a", gotSiteID)
		assert.Equal(t, now, gotUpdatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("messages_by_id mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
		assert.Equal(t, "u-bob", gotMentions[0].ID)
	})
}

func TestCassandraStore_SaveThreadMessage(t *testing.T) {
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
		ID:                    "m-2",
		RoomID:                "r-1",
		UserID:                "u-1",
		UserAccount:           "alice",
		Content:               "reply @bob",
		CreatedAt:             now,
		ThreadParentMessageID: "m-1",
		Mentions: []model.Participant{{
			UserID:      "u-bob",
			Account:     "bob",
			ChineseName: "鮑勃",
			EngName:     "Bob Chen",
		}},
	}

	err := store.SaveThreadMessage(ctx, msg, sender, "site-a")
	require.NoError(t, err)

	t.Run("thread_messages_by_room mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", "m-1", now, "m-2",
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
		assert.Equal(t, "u-bob", gotMentions[0].ID)
	})

	t.Run("messages_by_id mentions persisted for thread", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-2", now,
		).Scan(&gotMentions)
		require.NoError(t, err)
		require.Len(t, gotMentions, 1)
		assert.Equal(t, "bob", gotMentions[0].Account)
		assert.Equal(t, "Bob Chen", gotMentions[0].EngName)
		assert.Equal(t, "u-bob", gotMentions[0].ID)
	})
}

func TestCassandraStore_GetMessageSender(t *testing.T) {
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
		ID:          "m-sender-test",
		RoomID:      "r-1",
		UserID:      "u-1",
		UserAccount: "alice",
		Content:     "hello",
		CreatedAt:   now,
	}
	require.NoError(t, store.SaveMessage(ctx, msg, sender, "site-a"))

	t.Run("existing message returns sender", func(t *testing.T) {
		got, err := store.GetMessageSender(ctx, "m-sender-test")
		require.NoError(t, err)
		assert.Equal(t, "u-1", got.ID)
		assert.Equal(t, "alice", got.Account)
		assert.Equal(t, "Alice Wang", got.EngName)
		assert.Equal(t, "愛麗絲", got.CompanyName)
	})

	t.Run("non-existent message returns error", func(t *testing.T) {
		_, err := store.GetMessageSender(ctx, "does-not-exist")
		require.Error(t, err)
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

func TestThreadStoreMongo_CreateThreadRoom(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store, err := newThreadStoreMongo(ctx, db)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	room := &model.ThreadRoom{
		ID:              "tr-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r-1",
		SiteID:          "site-a",
		LastMsgAt:       now,
		LastMsgID:       "msg-reply-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("first insert succeeds", func(t *testing.T) {
		err := store.CreateThreadRoom(ctx, room)
		require.NoError(t, err)

		got, err := store.GetThreadRoomByParentMessageID(ctx, "msg-parent")
		require.NoError(t, err)
		assert.Equal(t, "tr-1", got.ID)
		assert.Equal(t, "msg-parent", got.ParentMessageID)
		assert.Equal(t, "r-1", got.RoomID)
		assert.Equal(t, "site-a", got.SiteID)
		assert.Equal(t, "msg-reply-1", got.LastMsgID)
	})

	t.Run("duplicate insert returns errThreadRoomExists", func(t *testing.T) {
		dup := &model.ThreadRoom{
			ID:              "tr-2",
			ParentMessageID: "msg-parent",
			RoomID:          "r-1",
			SiteID:          "site-a",
			LastMsgAt:       now,
			LastMsgID:       "msg-reply-2",
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		err := store.CreateThreadRoom(ctx, dup)
		require.ErrorIs(t, err, errThreadRoomExists)
	})
}

func TestThreadStoreMongo_GetThreadRoomByParentMessageID(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store, err := newThreadStoreMongo(ctx, db)
	require.NoError(t, err)

	t.Run("not found returns error", func(t *testing.T) {
		_, err := store.GetThreadRoomByParentMessageID(ctx, "does-not-exist")
		require.Error(t, err)
	})
}

func TestThreadStoreMongo_UpsertThreadSubscription(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store, err := newThreadStoreMongo(ctx, db)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	sub := &model.ThreadSubscription{
		ID:              "ts-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r-1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-1",
		UserAccount:     "alice",
		SiteID:          "site-a",
		LastSeenAt:      now,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("first upsert creates document", func(t *testing.T) {
		err := store.UpsertThreadSubscription(ctx, sub)
		require.NoError(t, err)

		var got model.ThreadSubscription
		err = db.Collection("threadSubscriptions").FindOne(ctx, bson.M{
			"threadRoomId": "tr-1",
			"userId":       "u-1",
		}).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, "ts-1", got.ID)
		assert.Equal(t, "alice", got.UserAccount)
		assert.Equal(t, now, got.CreatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("second upsert updates fields but preserves createdAt and ID", func(t *testing.T) {
		later := now.Add(5 * time.Minute)
		sub2 := &model.ThreadSubscription{
			ID:              "ts-should-be-ignored",
			ParentMessageID: "msg-parent",
			RoomID:          "r-1",
			ThreadRoomID:    "tr-1",
			UserID:          "u-1",
			UserAccount:     "alice",
			SiteID:          "site-a",
			LastSeenAt:      later,
			CreatedAt:       later,
			UpdatedAt:       later,
		}
		err := store.UpsertThreadSubscription(ctx, sub2)
		require.NoError(t, err)

		var got model.ThreadSubscription
		err = db.Collection("threadSubscriptions").FindOne(ctx, bson.M{
			"threadRoomId": "tr-1",
			"userId":       "u-1",
		}).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, "ts-1", got.ID, "ID should not change on upsert")
		assert.Equal(t, now, got.CreatedAt.UTC().Truncate(time.Millisecond), "createdAt should not change on upsert")
		assert.Equal(t, later, got.LastSeenAt.UTC().Truncate(time.Millisecond))
		assert.Equal(t, later, got.UpdatedAt.UTC().Truncate(time.Millisecond))
	})
}

func TestThreadStoreMongo_UpdateThreadRoomLastMessage(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store, err := newThreadStoreMongo(ctx, db)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Millisecond)
	room := &model.ThreadRoom{
		ID:              "tr-update",
		ParentMessageID: "msg-parent-update",
		RoomID:          "r-1",
		SiteID:          "site-a",
		LastMsgAt:       now,
		LastMsgID:       "msg-1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	require.NoError(t, store.CreateThreadRoom(ctx, room))

	later := now.Add(10 * time.Minute)
	err = store.UpdateThreadRoomLastMessage(ctx, "tr-update", "msg-5", later)
	require.NoError(t, err)

	got, err := store.GetThreadRoomByParentMessageID(ctx, "msg-parent-update")
	require.NoError(t, err)
	assert.Equal(t, "msg-5", got.LastMsgID)
	assert.Equal(t, later, got.LastMsgAt.UTC().Truncate(time.Millisecond))
}
