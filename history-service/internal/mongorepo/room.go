package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

const roomsCollection = "rooms"

// RoomRepo implements room lookups using MongoDB.
type RoomRepo struct {
	rooms *Collection[model.Room]
}

// NewRoomRepo creates a new MongoDB room repository.
func NewRoomRepo(db *mongo.Database) *RoomRepo {
	return &RoomRepo{
		rooms: NewCollection[model.Room](db.Collection(roomsCollection)),
	}
}

// GetRoom returns a room by its ID.
// Returns (nil, nil) when the room does not exist.
func (r *RoomRepo) GetRoom(ctx context.Context, roomID string) (*model.Room, error) {
	return r.rooms.FindByID(ctx, roomID)
}
