package mongorepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/mongoutil"
)

const threadRoomsCollection = "thread_rooms"

// threadRoomSort: newest activity first, stable secondary sort matching the compound indexes.
var threadRoomSort = bson.D{{Key: "lastMsgAt", Value: -1}, {Key: "threadParentCreatedAt", Value: 1}}

type ThreadRoomRepo struct {
	threadRooms         *mongoutil.Collection[model.ThreadRoom]
	threadSubscriptions *mongo.Collection
}

func NewThreadRoomRepo(db *mongo.Database) *ThreadRoomRepo {
	return &ThreadRoomRepo{
		threadRooms:         mongoutil.NewCollection[model.ThreadRoom](db.Collection(threadRoomsCollection)),
		threadSubscriptions: db.Collection("thread_subscriptions"),
	}
}

// EnsureIndexes creates the compound indexes required by the thread-list queries. Idempotent.
func (r *ThreadRoomRepo) EnsureIndexes(ctx context.Context) error {
	col := r.threadRooms.Raw().Indexes()

	indexes := []mongo.IndexModel{
		// GetThreadRooms: all threads
		{Keys: bson.D{
			{Key: "roomId", Value: 1},
			{Key: "lastMsgAt", Value: -1},
			{Key: "threadParentCreatedAt", Value: 1},
		}},
		// GetFollowingThreadRooms: threads the user has replied to
		{Keys: bson.D{
			{Key: "roomId", Value: 1},
			{Key: "replyAccounts", Value: 1},
			{Key: "lastMsgAt", Value: -1},
			{Key: "threadParentCreatedAt", Value: 1},
		}},
	}

	if _, err := col.CreateMany(ctx, indexes, options.CreateIndexes()); err != nil {
		return fmt.Errorf("ensure thread_rooms indexes: %w", err)
	}

	// Idempotent: same index owned by message-worker; history-service ensures it
	// independently so startup order doesn't affect query performance.
	if _, err := r.threadSubscriptions.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "threadRoomId", Value: 1}, {Key: "userAccount", Value: 1}},
		Options: options.Index().SetUnique(true),
	}); err != nil {
		return fmt.Errorf("ensure thread_subscriptions (threadRoomId,userAccount) index: %w", err)
	}
	return nil
}

func (r *ThreadRoomRepo) GetThreadRooms(ctx context.Context, roomID string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, allThreadsPipeline(roomID, accessSince), req)
	if err != nil {
		return mongoutil.OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying thread rooms: %w", err)
	}
	return page, nil
}

func (r *ThreadRoomRepo) GetFollowingThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, followingThreadsPipeline(roomID, account, accessSince), req)
	if err != nil {
		return mongoutil.OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying following thread rooms: %w", err)
	}
	return page, nil
}

// Unread = subscribed AND lastMsgAt > lastSeenAt.
func (r *ThreadRoomRepo) GetUnreadThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req mongoutil.OffsetPageRequest) (mongoutil.OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, unreadThreadsPipeline(roomID, account, accessSince), req)
	if err != nil {
		return mongoutil.OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying unread thread rooms: %w", err)
	}
	return page, nil
}

// DecrementReplyCount atomically decrements replyCount by 1, guarded by
// replyCount > 0 so it never goes negative, and returns the new count with
// applied=true. When no document matches the guard — a missing thread room, or
// one whose replyCount is absent/zero (e.g. a thread created before replyCount
// existed) — it is a no-op returning (0, false): the caller MUST then skip
// mirroring, otherwise it would stamp a spurious tcount=0 onto a parent that
// still has replies and hide them. Idempotency of the caller (one decrement per
// delete) is guaranteed upstream by the SoftDeleteMessage `IF deleted != true` gate.
func (r *ThreadRoomRepo) DecrementReplyCount(ctx context.Context, threadRoomID string) (int, bool, error) {
	var updated model.ThreadRoom
	err := r.threadRooms.Raw().FindOneAndUpdate(
		ctx,
		bson.M{"_id": threadRoomID, "replyCount": bson.M{"$gt": 0}},
		bson.M{"$inc": bson.M{"replyCount": -1}},
		// Only replyCount is read back; project it to avoid decoding the rest
		// of the document (notably the bounded countedReplies array).
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(bson.M{"replyCount": 1}),
	).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("decrement reply count for thread room %s: %w", threadRoomID, err)
	}
	return updated.ReplyCount, true, nil
}
