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
			tcount        INT,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
			message_id              TEXT,
			created_at              TIMESTAMP,
			room_id                 TEXT,
			sender                  FROZEN<"Participant">,
			msg                     TEXT,
			site_id                 TEXT,
			updated_at              TIMESTAMP,
			mentions                SET<FROZEN<"Participant">>,
			thread_room_id          TEXT,
			thread_parent_id        TEXT,
			thread_parent_created_at TIMESTAMP,
			tcount                  INT,
			PRIMARY KEY (message_id, created_at)
		) WITH CLUSTERING ORDER BY (created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS chat_test.thread_messages_by_room (
			room_id          TEXT,
			thread_room_id   TEXT,
			created_at       TIMESTAMP,
			message_id       TEXT,
			thread_parent_id TEXT,
			sender           FROZEN<"Participant">,
			msg              TEXT,
			site_id          TEXT,
			updated_at       TIMESTAMP,
			mentions         SET<FROZEN<"Participant">>,
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

	t.Run("messages_by_id room_id persisted", func(t *testing.T) {
		var gotRoomID string
		err := cassSession.Query(
			`SELECT room_id FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-1", now,
		).Scan(&gotRoomID)
		require.NoError(t, err)
		assert.Equal(t, "r-1", gotRoomID)
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

	const threadRoomID = "tr-test-1"
	err := store.SaveThreadMessage(ctx, msg, sender, "site-a", threadRoomID)
	require.NoError(t, err)

	t.Run("thread_messages_by_room mentions persisted", func(t *testing.T) {
		var gotMentions []*cassParticipant
		err := cassSession.Query(
			`SELECT mentions FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
			"r-1", threadRoomID, now, "m-2",
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

	t.Run("messages_by_id thread fields persisted", func(t *testing.T) {
		var gotThreadRoomID, gotThreadParentID string
		err := cassSession.Query(
			`SELECT thread_room_id, thread_parent_id FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-2", now,
		).Scan(&gotThreadRoomID, &gotThreadParentID)
		require.NoError(t, err)
		assert.Equal(t, threadRoomID, gotThreadRoomID)
		assert.Equal(t, "m-1", gotThreadParentID)
	})

	t.Run("messages_by_id room_id persisted for thread message", func(t *testing.T) {
		var gotRoomID string
		err := cassSession.Query(
			`SELECT room_id FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"m-2", now,
		).Scan(&gotRoomID)
		require.NoError(t, err)
		assert.Equal(t, "r-1", gotRoomID)
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
	threadStore := newThreadStoreMongo(mongoClient.Database("chat_test"))
	h := NewHandler(store, us, threadStore)

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

func TestHandler_Integration_ThreadReply(t *testing.T) {
	ctx := context.Background()

	cassSession := setupCassandra(t)
	db := setupMongo(t)

	userCol := db.Collection("users")
	_, err := userCol.InsertMany(ctx, []interface{}{
		bson.M{
			"_id":         "u-parent",
			"account":     "parent-user",
			"siteId":      "site-a",
			"engName":     "Parent User",
			"chineseName": "家長",
			"employeeId":  "EMP001",
		},
		bson.M{
			"_id":         "u-replier",
			"account":     "replier",
			"siteId":      "site-a",
			"engName":     "Replier User",
			"chineseName": "回覆者",
			"employeeId":  "EMP002",
		},
	})
	require.NoError(t, err)

	store := NewCassandraStore(cassSession)
	us := userstore.NewMongoStore(userCol)
	ts := newThreadStoreMongo(db)
	require.NoError(t, ts.EnsureIndexes(ctx))
	h := NewHandler(store, us, ts)

	now := time.Now().UTC().Truncate(time.Millisecond)

	// First: save the parent message to Cassandra so GetMessageSender can find it
	parentMsg := &model.Message{
		ID:          "msg-parent",
		RoomID:      "r-1",
		UserID:      "u-parent",
		UserAccount: "parent-user",
		Content:     "parent message",
		CreatedAt:   now.Add(-1 * time.Minute),
	}
	parentSender := &cassParticipant{
		ID:      "u-parent",
		EngName: "Parent User",
		Account: "parent-user",
	}
	require.NoError(t, store.SaveMessage(ctx, parentMsg, parentSender, "site-a"))

	// Second: process a thread reply (first reply path)
	replyEvt := model.MessageEvent{
		Message: model.Message{
			ID:                    "msg-reply-1",
			RoomID:                "r-1",
			UserID:                "u-replier",
			UserAccount:           "replier",
			Content:               "first thread reply",
			CreatedAt:             now,
			ThreadParentMessageID: "msg-parent",
		},
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
	}
	data, err := json.Marshal(replyEvt)
	require.NoError(t, err)
	require.NoError(t, h.processMessage(ctx, data))

	t.Run("thread room created", func(t *testing.T) {
		var room model.ThreadRoom
		err := db.Collection("threadRooms").FindOne(ctx, bson.M{
			"parentMessageId": "msg-parent",
		}).Decode(&room)
		require.NoError(t, err)
		assert.Equal(t, "msg-parent", room.ParentMessageID)
		assert.Equal(t, "r-1", room.RoomID)
		assert.Equal(t, "site-a", room.SiteID)
		assert.Equal(t, "msg-reply-1", room.LastMsgID)
	})

	t.Run("parent author subscribed", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"userId":          "u-parent",
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	t.Run("replier subscribed", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"userId":          "u-replier",
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)
	})

	// Third: process a second thread reply (subsequent path)
	reply2Evt := model.MessageEvent{
		Message: model.Message{
			ID:                    "msg-reply-2",
			RoomID:                "r-1",
			UserID:                "u-replier",
			UserAccount:           "replier",
			Content:               "second thread reply",
			CreatedAt:             now.Add(5 * time.Minute),
			ThreadParentMessageID: "msg-parent",
		},
		SiteID:    "site-a",
		Timestamp: now.Add(5 * time.Minute).UnixMilli(),
	}
	data2, err := json.Marshal(reply2Evt)
	require.NoError(t, err)
	require.NoError(t, h.processMessage(ctx, data2))

	t.Run("thread room lastMsgId updated", func(t *testing.T) {
		var room model.ThreadRoom
		err := db.Collection("threadRooms").FindOne(ctx, bson.M{
			"parentMessageId": "msg-parent",
		}).Decode(&room)
		require.NoError(t, err)
		assert.Equal(t, "msg-reply-2", room.LastMsgID)
	})

	t.Run("still only two subscriptions after second reply", func(t *testing.T) {
		count, err := db.Collection("threadSubscriptions").CountDocuments(ctx, bson.M{
			"parentMessageId": "msg-parent",
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), count)
	})
}

func TestThreadStoreMongo_CreateThreadRoom(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

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
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

	t.Run("not found returns errThreadRoomNotFound", func(t *testing.T) {
		_, err := store.GetThreadRoomByParentMessageID(ctx, "does-not-exist")
		require.ErrorIs(t, err, errThreadRoomNotFound)
	})
}

func TestThreadStoreMongo_InsertThreadSubscription(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

	now := time.Now().UTC().Truncate(time.Millisecond)
	sub := &model.ThreadSubscription{
		ID:              "ts-1",
		ParentMessageID: "msg-parent",
		RoomID:          "r-1",
		ThreadRoomID:    "tr-1",
		UserID:          "u-1",
		UserAccount:     "alice",
		SiteID:          "site-a",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	t.Run("insert creates document with correct fields", func(t *testing.T) {
		err := store.InsertThreadSubscription(ctx, sub)
		require.NoError(t, err)

		var got model.ThreadSubscription
		err = db.Collection("threadSubscriptions").FindOne(ctx, bson.M{
			"threadRoomId": "tr-1",
			"userId":       "u-1",
		}).Decode(&got)
		require.NoError(t, err)
		assert.Equal(t, "ts-1", got.ID)
		assert.Equal(t, "alice", got.UserAccount)
		assert.True(t, got.LastSeenAt.IsZero(), "lastSeenAt should be zero on insert")
		assert.Equal(t, now, got.CreatedAt.UTC().Truncate(time.Millisecond))
	})

	t.Run("duplicate insert returns error", func(t *testing.T) {
		dup := &model.ThreadSubscription{
			ID:           "ts-dup",
			ThreadRoomID: "tr-1",
			UserID:       "u-1",
		}
		err := store.InsertThreadSubscription(ctx, dup)
		require.Error(t, err, "second insert with same (threadRoomId, userId) must fail")
	})
}

func TestThreadStoreMongo_ThreadSubscriptionExists(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("returns false when subscription absent", func(t *testing.T) {
		exists, err := store.ThreadSubscriptionExists(ctx, "tr-nonexistent", "u-1")
		require.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("returns true after insertion", func(t *testing.T) {
		sub := &model.ThreadSubscription{
			ID:           "ts-exists",
			ThreadRoomID: "tr-exists",
			UserID:       "u-exists",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		require.NoError(t, store.InsertThreadSubscription(ctx, sub))

		exists, err := store.ThreadSubscriptionExists(ctx, "tr-exists", "u-exists")
		require.NoError(t, err)
		assert.True(t, exists)
	})
}

func TestThreadStoreMongo_UpdateThreadRoomLastMessage(t *testing.T) {
	ctx := context.Background()
	db := setupMongo(t)
	store := newThreadStoreMongo(db)
	require.NoError(t, store.EnsureIndexes(ctx))

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
	err := store.UpdateThreadRoomLastMessage(ctx, "tr-update", "msg-5", later)
	require.NoError(t, err)

	got, err := store.GetThreadRoomByParentMessageID(ctx, "msg-parent-update")
	require.NoError(t, err)
	assert.Equal(t, "msg-5", got.LastMsgID)
	assert.Equal(t, later, got.LastMsgAt.UTC().Truncate(time.Millisecond))
}

func TestCassandraStore_SaveThreadMessage_IncrementsParentTcount(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	replyCreatedAt := parentCreatedAt.Add(5 * time.Minute)

	parentSender := &cassParticipant{ID: "u-parent", Account: "alice", EngName: "Alice"}
	parentMsg := &model.Message{
		ID:        "tcount-parent",
		RoomID:    "tcount-room",
		UserID:    "u-parent",
		CreatedAt: parentCreatedAt,
		Content:   "parent message",
	}
	require.NoError(t, store.SaveMessage(ctx, parentMsg, parentSender, "site-a"))

	replySender := &cassParticipant{ID: "u-replier", Account: "bob", EngName: "Bob"}
	replyMsg := &model.Message{
		ID:                           "tcount-reply-1",
		RoomID:                       "tcount-room",
		UserID:                       "u-replier",
		Content:                      "first reply",
		CreatedAt:                    replyCreatedAt,
		ThreadParentMessageID:        "tcount-parent",
		ThreadParentMessageCreatedAt: &parentCreatedAt,
	}
	require.NoError(t, store.SaveThreadMessage(ctx, replyMsg, replySender, "site-a", "tr-tcount-1"))

	t.Run("tcount incremented to 1 in messages_by_id", func(t *testing.T) {
		var tcount int
		err := cassSession.Query(
			`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"tcount-parent", parentCreatedAt,
		).Scan(&tcount)
		require.NoError(t, err)
		assert.Equal(t, 1, tcount)
	})

	t.Run("tcount incremented to 1 in messages_by_room", func(t *testing.T) {
		var tcount int
		err := cassSession.Query(
			`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"tcount-room", parentCreatedAt, "tcount-parent",
		).Scan(&tcount)
		require.NoError(t, err)
		assert.Equal(t, 1, tcount)
	})

	// A second reply must increment tcount to 2.
	reply2CreatedAt := replyCreatedAt.Add(5 * time.Minute)
	replyMsg2 := &model.Message{
		ID:                           "tcount-reply-2",
		RoomID:                       "tcount-room",
		UserID:                       "u-replier",
		Content:                      "second reply",
		CreatedAt:                    reply2CreatedAt,
		ThreadParentMessageID:        "tcount-parent",
		ThreadParentMessageCreatedAt: &parentCreatedAt,
	}
	require.NoError(t, store.SaveThreadMessage(ctx, replyMsg2, replySender, "site-a", "tr-tcount-1"))

	t.Run("tcount incremented to 2 in messages_by_id after second reply", func(t *testing.T) {
		var tcount int
		err := cassSession.Query(
			`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"tcount-parent", parentCreatedAt,
		).Scan(&tcount)
		require.NoError(t, err)
		assert.Equal(t, 2, tcount)
	})

	t.Run("tcount incremented to 2 in messages_by_room after second reply", func(t *testing.T) {
		var tcount int
		err := cassSession.Query(
			`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
			"tcount-room", parentCreatedAt, "tcount-parent",
		).Scan(&tcount)
		require.NoError(t, err)
		assert.Equal(t, 2, tcount)
	})

	t.Run("nil ThreadParentMessageCreatedAt skips tcount update without error", func(t *testing.T) {
		noTsReply := &model.Message{
			ID:                    "tcount-reply-nots",
			RoomID:                "tcount-room",
			UserID:                "u-replier",
			Content:               "reply without parent ts",
			CreatedAt:             reply2CreatedAt.Add(5 * time.Minute),
			ThreadParentMessageID: "tcount-parent",
			// ThreadParentMessageCreatedAt intentionally nil
		}
		err := store.SaveThreadMessage(ctx, noTsReply, replySender, "site-a", "tr-tcount-1")
		assert.NoError(t, err)

		// tcount must stay at 2 — nil timestamp skips the increment
		var tcount int
		err = cassSession.Query(
			`SELECT tcount FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
			"tcount-parent", parentCreatedAt,
		).Scan(&tcount)
		require.NoError(t, err)
		assert.Equal(t, 2, tcount)
	})
}
