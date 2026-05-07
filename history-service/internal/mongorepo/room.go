package mongorepo

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

const roomsCollection = "rooms"

// RoomRepo reads room metadata from MongoDB.
type RoomRepo struct {
	rooms *Collection[model.Room]
}

// NewRoomRepo creates a RoomRepo backed by the given database.
func NewRoomRepo(db *mongo.Database) *RoomRepo {
	return &RoomRepo{rooms: NewCollection[model.Room](db.Collection(roomsCollection))}
}

// GetRoomTimes returns lastMsgAt (zero time when unset) and createdAt for the given room.
// Returns mongo.ErrNoDocuments wrapped when the room does not exist.
func (r *RoomRepo) GetRoomTimes(ctx context.Context, roomID string) (lastMsgAt, createdAt time.Time, err error) {
	room, err := r.rooms.FindByID(ctx, roomID, WithProjection(bson.M{"lastMsgAt": 1, "createdAt": 1, "_id": 0}))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get room times for %s: %w", roomID, err)
	}
	if room == nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get room times for %s: %w", roomID, mongo.ErrNoDocuments)
	}
	if room.LastMsgAt != nil {
		lastMsgAt = *room.LastMsgAt
	}
	return lastMsgAt, room.CreatedAt, nil
}
