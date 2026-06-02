//go:build integration

package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
	"github.com/hmchangw/chat/pkg/subject"
	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/pkg/userstore"
)

func setupMongo(t *testing.T) *mongo.Database {
	return testutil.MongoDB(t, "broadcast_worker_test")
}

type recordingPublisher struct {
	mu      sync.Mutex
	records []publishRecord
}

func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.records = append(p.records, publishRecord{subject: subj, data: data})
	return nil
}

func (p *recordingPublisher) getRecords() []publishRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]publishRecord, len(p.records))
	copy(cp, p.records)
	return cp
}

func seedUsers(t *testing.T, db *mongo.Database) {
	t.Helper()
	_, err := db.Collection("users").InsertMany(context.Background(), []interface{}{
		model.User{ID: "u-alice", Account: "alice", SiteID: "site-a", EngName: "Alice Wang", ChineseName: "愛麗絲", EmployeeID: "E001"},
		model.User{ID: "u-bob", Account: "bob", SiteID: "site-a", EngName: "Bob Chen", ChineseName: "鮑勃", EmployeeID: "E002"},
	})
	require.NoError(t, err)
}

type fakeRoomKeyProvider struct {
	pair *roomkeystore.VersionedKeyPair
}

func (f *fakeRoomKeyProvider) Get(_ context.Context, _ string) (*roomkeystore.VersionedKeyPair, error) {
	return f.pair, nil
}

func TestBroadcastWorker_ChannelRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r1", Name: "general", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r1"},
		model.Subscription{ID: "s2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r1"},
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, us, pub, keyStore, true)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "alice", Content: "hello", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 1)
	assert.Equal(t, subject.RoomEvent("r1"), records[0].subject)

	roomEvt, msg := decryptClientMessage(t, records[0].data, key)
	assert.Equal(t, "site-a", roomEvt.SiteID)
	require.NotNil(t, msg)
	require.NotNil(t, msg.Sender)
	assert.Equal(t, "u1", msg.Sender.UserID)

	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r1"}).Decode(&room))
	assert.Equal(t, "m1", room.LastMsgID)
	require.NotNil(t, room.LastMsgAt)
	assert.WithinDuration(t, msgTime, *room.LastMsgAt, time.Millisecond)
}

func TestBroadcastWorker_ChannelRoom_MentionAll_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r2", Name: "announcements", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, us, pub, keyStore, true)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "m2", RoomID: "r2", UserID: "u1", UserAccount: "alice", Content: "hello @All", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r2"}).Decode(&room))
	require.NotNil(t, room.LastMentionAllAt)
	assert.WithinDuration(t, msgTime, *room.LastMentionAllAt, time.Millisecond)
}

func TestBroadcastWorker_ChannelRoom_IndividualMention_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r3", Name: "dev", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s5", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "r3"},
		model.Subscription{ID: "s6", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "r3"},
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	key := testRoomKey(t)
	keyStore := &fakeRoomKeyProvider{pair: key}
	handler := NewHandler(store, us, pub, keyStore, true)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "m3", RoomID: "r3", UserID: "u1", UserAccount: "alice", Content: "hey @bob", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	roomEvt, _ := decryptClientMessage(t, records[0].data, key)
	require.Len(t, roomEvt.Mentions, 1)
	assert.Equal(t, "bob", roomEvt.Mentions[0].Account)
	assert.Equal(t, "鮑勃", roomEvt.Mentions[0].ChineseName)
	assert.Equal(t, "Bob Chen", roomEvt.Mentions[0].EngName)
	assert.Equal(t, "u-bob", roomEvt.Mentions[0].UserID)

	var subBob model.Subscription
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"u.account": "bob", "roomId": "r3"}).Decode(&subBob))
	assert.True(t, subBob.HasMention)

	var subAlice model.Subscription
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx, bson.M{"u.account": "alice", "roomId": "r3"}).Decode(&subAlice))
	assert.False(t, subAlice.HasMention)
}

func TestBroadcastWorker_DMRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "dm-1", Name: "", Type: model.RoomTypeDM, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "s7", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "dm-1"},
		model.Subscription{ID: "s8", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "dm-1"},
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	keyStore := &fakeRoomKeyProvider{pair: nil}
	handler := NewHandler(store, us, pub, keyStore, true)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "m4", RoomID: "dm-1", UserID: "u1", UserAccount: "alice", Content: "hey", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 2)
	var subjects []string
	for _, rec := range records {
		subjects = append(subjects, rec.subject)
	}
	assert.ElementsMatch(t, []string{
		subject.UserRoomEvent("alice"),
		subject.UserRoomEvent("bob"),
	}, subjects)

	for _, rec := range records {
		var roomEvt model.RoomEvent
		require.NoError(t, json.Unmarshal(rec.data, &roomEvt))
		require.NotNil(t, roomEvt.Message)
		require.NotNil(t, roomEvt.Message.Sender)
		assert.Equal(t, "u1", roomEvt.Message.Sender.UserID)
		assert.Equal(t, "alice", roomEvt.Message.Sender.Account)
		assert.Equal(t, "愛麗絲", roomEvt.Message.Sender.ChineseName)
	}

	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "dm-1"}).Decode(&room))
	assert.Equal(t, "m4", room.LastMsgID)
	require.NotNil(t, room.LastMsgAt)
	assert.WithinDuration(t, msgTime, *room.LastMsgAt, time.Millisecond)
}

func TestBroadcastWorker_ChannelRoom_EncryptionDisabled_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "rNoEnc", Name: "plain", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertMany(ctx, []interface{}{
		model.Subscription{ID: "sN1", User: model.SubscriptionUser{ID: "u1", Account: "alice"}, RoomID: "rNoEnc"},
		model.Subscription{ID: "sN2", User: model.SubscriptionUser{ID: "u2", Account: "bob"}, RoomID: "rNoEnc"},
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}

	// nil keyStore — encryption is disabled, handler must not consult it
	handler := NewHandler(store, us, pub, nil, false)

	msgTime := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: "site-a",
		Message: model.Message{
			ID: "mNoEnc", RoomID: "rNoEnc", UserID: "u1", UserAccount: "alice", Content: "plaintext please", CreatedAt: msgTime,
		},
	}
	data, _ := json.Marshal(evt)

	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 1)
	assert.Equal(t, subject.RoomEvent("rNoEnc"), records[0].subject)

	var roomEvt model.RoomEvent
	require.NoError(t, json.Unmarshal(records[0].data, &roomEvt))
	assert.Equal(t, "site-a", roomEvt.SiteID)
	require.NotNil(t, roomEvt.Message, "plaintext channel event must carry Message")
	assert.Empty(t, roomEvt.EncryptedMessage, "plaintext channel event must NOT carry EncryptedMessage")
	assert.Equal(t, "mNoEnc", roomEvt.Message.ID)
	assert.Equal(t, "plaintext please", roomEvt.Message.Content)
	require.NotNil(t, roomEvt.Message.Sender)
	assert.Equal(t, "u1", roomEvt.Message.Sender.UserID)
	assert.Equal(t, "alice", roomEvt.Message.Sender.Account)

	var room model.Room
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "rNoEnc"}).Decode(&room))
	assert.Equal(t, "mNoEnc", room.LastMsgID)
	require.NotNil(t, room.LastMsgAt)
	assert.WithinDuration(t, msgTime, *room.LastMsgAt, time.Millisecond)
}

func TestBroadcastWorker_PersistsLastMessage_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-last", Name: "general", Type: model.RoomTypeChannel, UserCount: 2, SiteID: "site-a",
	})
	require.NoError(t, err)
	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	cached, err := newCachedMetaStore(store, 10, time.Minute)
	require.NoError(t, err)

	pub := &recordingPublisher{}
	h := NewHandler(cached, userstore.NewMongoStore(db.Collection("users")), pub, &fakeRoomKeyProvider{}, false)

	msgTime := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	evt := model.MessageEvent{
		Event:     model.EventCreated,
		Timestamp: msgTime.UnixMilli(),
		Message: model.Message{
			ID:          "msg-last",
			RoomID:      "r-last",
			UserID:      "u-alice",
			UserAccount: "alice",
			Content:     "hi",
			CreatedAt:   msgTime,
		},
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	require.NoError(t, h.HandleMessage(ctx, data))

	// Verify the room doc now has lastMsgAt/lastMsgId persisted.
	var got struct {
		LastMsgAt time.Time `bson:"lastMsgAt"`
		LastMsgID string    `bson:"lastMsgId"`
	}
	err = db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-last"}).Decode(&got)
	require.NoError(t, err)
	assert.Equal(t, "msg-last", got.LastMsgID)
	assert.WithinDuration(t, msgTime, got.LastMsgAt, time.Millisecond)
}

func TestBroadcastWorker_ListThreadSubscriptions_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	parentMsgID := "parent-msg-1"
	siteID := "site-a"

	_, err := db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "ts1", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "alice", UserID: "u-alice", SiteID: siteID,
		},
		model.ThreadSubscription{
			ID: "ts2", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
		// different parent — must NOT be returned
		model.ThreadSubscription{
			ID: "ts3", ParentMessageID: "other-parent", RoomID: "r1",
			ThreadRoomID: "tr2", UserAccount: "charlie", UserID: "u-charlie", SiteID: siteID,
		},
		// different siteID — must NOT be returned
		model.ThreadSubscription{
			ID: "ts4", ParentMessageID: parentMsgID, RoomID: "r1",
			ThreadRoomID: "tr1", UserAccount: "diana", UserID: "u-diana", SiteID: "site-b",
		},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	subs, err := store.ListThreadSubscriptions(ctx, parentMsgID, siteID)
	require.NoError(t, err)
	require.Len(t, subs, 2)
	accounts := []string{subs[0].UserAccount, subs[1].UserAccount}
	assert.ElementsMatch(t, []string{"alice", "bob"}, accounts)
}

func TestBroadcastWorker_ThreadSubscriptionsIndex_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	require.NoError(t, store.EnsureIndexes(ctx))

	col := db.Collection("thread_subscriptions")
	cursor, err := col.Indexes().List(ctx)
	require.NoError(t, err)
	defer cursor.Close(ctx)

	type indexSpec struct {
		Key bson.D `bson:"key"`
	}
	var indexes []indexSpec
	require.NoError(t, cursor.All(ctx, &indexes))

	// Look for a compound index on {parentMessageId:1, siteId:1}.
	found := false
	for _, idx := range indexes {
		if len(idx.Key) == 2 &&
			idx.Key[0].Key == "parentMessageId" && idx.Key[0].Value == int32(1) &&
			idx.Key[1].Key == "siteId" && idx.Key[1].Value == int32(1) {
			found = true
			break
		}
	}
	assert.True(t, found, "expected compound index on {parentMessageId:1, siteId:1} in thread_subscriptions")
}

func TestBroadcastWorker_ThreadCreated_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	const parentMsgID = "tc-parent-1"
	const siteID = "site-a"
	const sender = "alice"

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-tc", Name: "general", Type: model.RoomTypeChannel, UserCount: 3, SiteID: siteID,
	})
	require.NoError(t, err)

	_, err = db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "tc-ts1", ParentMessageID: parentMsgID, RoomID: "r-tc",
			ThreadRoomID: "tr-tc1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
		model.ThreadSubscription{
			ID: "tc-ts2", ParentMessageID: parentMsgID, RoomID: "r-tc",
			ThreadRoomID: "tr-tc1", UserAccount: "carol", UserID: "u-carol", SiteID: siteID,
		},
	})
	require.NoError(t, err)

	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub, &fakeRoomKeyProvider{}, false)

	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: siteID,
		Message: model.Message{
			ID:                    "tc-reply-1",
			RoomID:                "r-tc",
			UserID:                "u-alice",
			UserAccount:           sender,
			Content:               "first thread reply",
			CreatedAt:             now,
			ThreadParentMessageID: parentMsgID,
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 2, "two subscribers should each receive the event")

	var subjects []string
	for _, rec := range records {
		subjects = append(subjects, rec.subject)
	}
	assert.ElementsMatch(t, []string{
		subject.UserRoomEvent("bob"),
		subject.UserRoomEvent("carol"),
	}, subjects)

	// Each payload should be a valid RoomEvent carrying the reply message.
	for _, rec := range records {
		var roomEvt model.RoomEvent
		require.NoError(t, json.Unmarshal(rec.data, &roomEvt))
		assert.Equal(t, model.RoomEventNewMessage, roomEvt.Type)
		assert.Equal(t, "r-tc", roomEvt.RoomID)
		assert.Equal(t, siteID, roomEvt.SiteID)
		require.NotNil(t, roomEvt.Message)
		assert.Equal(t, "tc-reply-1", roomEvt.Message.ID)
		assert.Equal(t, parentMsgID, roomEvt.Message.ThreadParentMessageID)
	}
}

func TestBroadcastWorker_ThreadCreated_SenderExcluded_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	const parentMsgID = "tce-parent-1"
	const siteID = "site-a"
	const sender = "alice"

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-tce", Name: "dev", Type: model.RoomTypeChannel, UserCount: 2, SiteID: siteID,
	})
	require.NoError(t, err)

	// alice is both sender and a subscriber — must NOT appear in fan-out.
	_, err = db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "tce-ts1", ParentMessageID: parentMsgID, RoomID: "r-tce",
			ThreadRoomID: "tr-tce1", UserAccount: sender, UserID: "u-alice", SiteID: siteID,
		},
		model.ThreadSubscription{
			ID: "tce-ts2", ParentMessageID: parentMsgID, RoomID: "r-tce",
			ThreadRoomID: "tr-tce1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
	})
	require.NoError(t, err)

	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub, &fakeRoomKeyProvider{}, false)

	now := time.Now().UTC().Truncate(time.Millisecond)
	evt := model.MessageEvent{
		Event:  model.EventCreated,
		SiteID: siteID,
		Message: model.Message{
			ID:                    "tce-reply-1",
			RoomID:                "r-tce",
			UserID:                "u-alice",
			UserAccount:           sender,
			Content:               "alice replies to own parent",
			CreatedAt:             now,
			ThreadParentMessageID: parentMsgID,
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 1, "sender must be excluded from thread fan-out")
	assert.Equal(t, subject.UserRoomEvent("bob"), records[0].subject)
}

func TestBroadcastWorker_ThreadUpdated_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	const parentMsgID = "tu-parent-1"
	const siteID = "site-a"
	const sender = "alice"

	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-tu", Name: "general", Type: model.RoomTypeChannel, UserCount: 2, SiteID: siteID,
	})
	require.NoError(t, err)

	_, err = db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "tu-ts1", ParentMessageID: parentMsgID, RoomID: "r-tu",
			ThreadRoomID: "tr-tu1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
	})
	require.NoError(t, err)

	seedUsers(t, db)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub, &fakeRoomKeyProvider{}, false)

	now := time.Now().UTC().Truncate(time.Millisecond)
	editedAt := now.Add(time.Minute)
	evt := model.MessageEvent{
		Event:  model.EventUpdated,
		SiteID: siteID,
		Message: model.Message{
			ID:                    "tu-reply-1",
			RoomID:                "r-tu",
			UserID:                "u-alice",
			UserAccount:           sender,
			Content:               "edited thread reply",
			CreatedAt:             now,
			EditedAt:              &editedAt,
			UpdatedAt:             &editedAt,
			ThreadParentMessageID: parentMsgID,
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 1)
	assert.Equal(t, subject.UserRoomEvent("bob"), records[0].subject)

	var editEvt model.EditRoomEvent
	require.NoError(t, json.Unmarshal(records[0].data, &editEvt))
	assert.Equal(t, model.RoomEventMessageEdited, editEvt.Type)
	assert.Equal(t, "r-tu", editEvt.RoomID)
	assert.Equal(t, siteID, editEvt.SiteID)
	assert.Equal(t, "tu-reply-1", editEvt.MessageID)
	assert.Equal(t, "edited thread reply", editEvt.NewContent)
}

func TestBroadcastWorker_ThreadDeleted_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	const parentMsgID = "td-parent-1"
	const siteID = "site-a"
	const sender = "alice"

	// Seed the room — handleThreadDeleted calls GetRoom to use room.SiteID
	// (authoritative) rather than evt.SiteID for the DeleteRoomEvent.
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-td", Type: model.RoomTypeChannel, SiteID: siteID,
	})
	require.NoError(t, err)

	_, err = db.Collection("thread_subscriptions").InsertMany(ctx, []interface{}{
		model.ThreadSubscription{
			ID: "td-ts1", ParentMessageID: parentMsgID, RoomID: "r-td",
			ThreadRoomID: "tr-td1", UserAccount: "bob", UserID: "u-bob", SiteID: siteID,
		},
		model.ThreadSubscription{
			ID: "td-ts2", ParentMessageID: parentMsgID, RoomID: "r-td",
			ThreadRoomID: "tr-td1", UserAccount: "carol", UserID: "u-carol", SiteID: siteID,
		},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub, &fakeRoomKeyProvider{}, false)

	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Minute)
	evtTimestamp := now.UnixMilli()
	evt := model.MessageEvent{
		Event:     model.EventDeleted,
		SiteID:    siteID,
		Timestamp: evtTimestamp,
		Message: model.Message{
			ID:                    "td-reply-1",
			RoomID:                "r-td",
			UserID:                "u-alice",
			UserAccount:           sender,
			Content:               "reply to be deleted",
			CreatedAt:             now,
			UpdatedAt:             &deletedAt,
			ThreadParentMessageID: parentMsgID,
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 2, "two subscribers should receive the delete event")

	var subjects []string
	for _, rec := range records {
		subjects = append(subjects, rec.subject)
	}
	assert.ElementsMatch(t, []string{
		subject.UserRoomEvent("bob"),
		subject.UserRoomEvent("carol"),
	}, subjects)

	for _, rec := range records {
		var delEvt model.DeleteRoomEvent
		require.NoError(t, json.Unmarshal(rec.data, &delEvt))
		assert.Equal(t, model.RoomEventMessageDeleted, delEvt.Type)
		assert.Equal(t, "r-td", delEvt.RoomID)
		assert.Equal(t, siteID, delEvt.SiteID)
		assert.Equal(t, "td-reply-1", delEvt.MessageID)
		assert.Equal(t, sender, delEvt.DeletedBy)
		assert.True(t, delEvt.DeletedAt.Equal(deletedAt))
		assert.Equal(t, evtTimestamp, delEvt.Timestamp, "Timestamp must propagate from evt.Timestamp, not time.Now()")
	}
}

// TestBroadcastWorker_ThreadDeleted_DMRoom_Integration verifies that a thread
// reply deleted in a DM room fans the delete event out to ALL members, even
// when no one subscribed to the thread — DM thread replies are visible to
// every member, so the delete must reach them regardless of thread
// subscription state.
func TestBroadcastWorker_ThreadDeleted_DMRoom_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	const parentMsgID = "tddm-parent-1"
	const siteID = "site-a"
	const sender = "alice"

	// DM room with two members; deliberately NO thread_subscriptions seeded.
	_, err := db.Collection("rooms").InsertOne(ctx, model.Room{
		ID: "r-tddm", Type: model.RoomTypeDM, SiteID: siteID,
		Accounts: []string{"alice", "bob"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	us := userstore.NewMongoStore(db.Collection("users"))
	pub := &recordingPublisher{}
	handler := NewHandler(store, us, pub, &fakeRoomKeyProvider{}, false)

	now := time.Now().UTC().Truncate(time.Millisecond)
	deletedAt := now.Add(time.Minute)
	evt := model.MessageEvent{
		Event:  model.EventDeleted,
		SiteID: siteID,
		Message: model.Message{
			ID:                    "tddm-reply-1",
			RoomID:                "r-tddm",
			UserID:                "u-alice",
			UserAccount:           sender,
			Content:               "dm thread reply to be deleted",
			CreatedAt:             now,
			UpdatedAt:             &deletedAt,
			ThreadParentMessageID: parentMsgID,
			TShow:                 false,
		},
	}
	data, _ := json.Marshal(evt)
	require.NoError(t, handler.HandleMessage(ctx, data))

	records := pub.getRecords()
	require.Len(t, records, 2, "both DM members receive the delete despite no thread subscriptions")

	var subjects []string
	for _, rec := range records {
		subjects = append(subjects, rec.subject)
		var delEvt model.DeleteRoomEvent
		require.NoError(t, json.Unmarshal(rec.data, &delEvt))
		assert.Equal(t, model.RoomEventMessageDeleted, delEvt.Type)
		assert.Equal(t, "r-tddm", delEvt.RoomID)
		assert.Equal(t, "tddm-reply-1", delEvt.MessageID)
	}
	assert.ElementsMatch(t, []string{
		subject.UserRoomEvent("alice"),
		subject.UserRoomEvent("bob"),
	}, subjects)
}

func TestBroadcastWorker_BulkUpdateRoomLastMessage_Integration(t *testing.T) {
	db := setupMongo(t)
	ctx := context.Background()

	_, err := db.Collection("rooms").InsertMany(ctx, []interface{}{
		model.Room{ID: "r-bulk-a", Name: "a", Type: model.RoomTypeChannel, SiteID: "site-a"},
		model.Room{ID: "r-bulk-b", Name: "b", Type: model.RoomTypeChannel, SiteID: "site-a"},
	})
	require.NoError(t, err)

	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))

	t1 := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	updates := map[string]roomLastMsgUpdate{
		"r-bulk-a": {msgID: "msg-a", at: t1},
		"r-bulk-b": {msgID: "msg-b", at: t2, lastMentionAllAt: t2},
	}
	require.NoError(t, store.BulkUpdateRoomLastMessage(ctx, updates))

	var a, b struct {
		LastMsgAt        time.Time `bson:"lastMsgAt"`
		LastMsgID        string    `bson:"lastMsgId"`
		LastMentionAllAt time.Time `bson:"lastMentionAllAt"`
	}
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-bulk-a"}).Decode(&a))
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-bulk-b"}).Decode(&b))

	assert.Equal(t, "msg-a", a.LastMsgID)
	assert.WithinDuration(t, t1, a.LastMsgAt, time.Millisecond)
	assert.True(t, a.LastMentionAllAt.IsZero(), "no mention-all → field stays unset")

	assert.Equal(t, "msg-b", b.LastMsgID)
	assert.WithinDuration(t, t2, b.LastMsgAt, time.Millisecond)
	assert.WithinDuration(t, t2, b.LastMentionAllAt, time.Millisecond)
}

func TestBroadcastWorker_BulkUpdateRoomLastMessage_EmptyIsNoOp_Integration(t *testing.T) {
	db := setupMongo(t)
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), db.Collection("thread_subscriptions"))
	require.NoError(t, store.BulkUpdateRoomLastMessage(context.Background(), nil))
	require.NoError(t, store.BulkUpdateRoomLastMessage(context.Background(), map[string]roomLastMsgUpdate{}))
}
