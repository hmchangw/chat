package mongorepo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/model"
)

const threadRoomsCollection = "threadRooms"

// threadRoomSort: newest activity first, stable secondary sort matching the compound indexes.
var threadRoomSort = bson.D{{Key: "lastMsgAt", Value: -1}, {Key: "threadParentCreatedAt", Value: 1}}

type ThreadRoomRepo struct {
	threadRooms *Collection[model.ThreadRoom]
}

func NewThreadRoomRepo(db *mongo.Database) *ThreadRoomRepo {
	return &ThreadRoomRepo{
		threadRooms: NewCollection[model.ThreadRoom](db.Collection(threadRoomsCollection)),
	}
}

// EnsureIndexes creates the indexes required by the three thread-list queries.
// Safe to call on every startup — MongoDB ignores identical existing indexes.
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
	return nil
}

// GetThreadRooms returns paginated thread rooms, sorted by newest activity.
// When accessSince is non-nil, only threads whose parent was created at or after that time are included.
func (r *ThreadRoomRepo) GetThreadRooms(ctx context.Context, roomID string, accessSince *time.Time, req OffsetPageRequest) (OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, allThreadsPipeline(roomID, accessSince), req)
	if err != nil {
		return OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying thread rooms: %w", err)
	}
	return page, nil
}

// GetFollowingThreadRooms returns paginated thread rooms where account appears in ReplyAccounts.
// When accessSince is non-nil, only threads whose parent was created at or after that time are included.
func (r *ThreadRoomRepo) GetFollowingThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req OffsetPageRequest) (OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, followingThreadsPipeline(roomID, account, accessSince), req)
	if err != nil {
		return OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying following thread rooms: %w", err)
	}
	return page, nil
}

// GetUnreadThreadRooms returns thread rooms with unread activity for account.
// A thread is unread when the user has a threadSubscription AND lastMsgAt > sub.lastSeenAt.
func (r *ThreadRoomRepo) GetUnreadThreadRooms(ctx context.Context, roomID, account string, accessSince *time.Time, req OffsetPageRequest) (OffsetPage[model.ThreadRoom], error) {
	page, err := r.threadRooms.AggregatePaged(ctx, unreadThreadsPipeline(roomID, account, accessSince), req)
	if err != nil {
		return OffsetPage[model.ThreadRoom]{}, fmt.Errorf("querying unread thread rooms: %w", err)
	}
	return page, nil
}
