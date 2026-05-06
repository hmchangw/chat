package mongorepo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const roomsCollection = "rooms"

// RoomRepo reads room metadata from MongoDB.
type RoomRepo struct {
	rooms *mongo.Collection
}

// NewRoomRepo creates a RoomRepo backed by the given database.
func NewRoomRepo(db *mongo.Database) *RoomRepo {
	return &RoomRepo{rooms: db.Collection(roomsCollection)}
}

// roomTimes is a projection-only struct used by GetRoomTimes.
type roomTimes struct {
	LastMsgAt *time.Time `bson:"lastMsgAt"`
	CreatedAt time.Time  `bson:"createdAt"`
}

// GetRoomTimes returns lastMsgAt (zero time when unset) and createdAt for the given room.
// Returns mongo.ErrNoDocuments wrapped when the room does not exist.
func (r *RoomRepo) GetRoomTimes(ctx context.Context, roomID string) (lastMsgAt, createdAt time.Time, err error) {
	opts := options.FindOne().SetProjection(bson.M{"lastMsgAt": 1, "createdAt": 1, "_id": 0})
	var rt roomTimes
	if err := r.rooms.FindOne(ctx, bson.M{"_id": roomID}, opts).Decode(&rt); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get room times for %s: %w", roomID, err)
	}
	if rt.LastMsgAt != nil {
		lastMsgAt = *rt.LastMsgAt
	}
	return lastMsgAt, rt.CreatedAt, nil
}
