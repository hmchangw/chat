package mongorepo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const roomsCollection = "rooms"

// RoomRepo reads a narrow projection of the rooms collection that
// history-service consumes. It never writes to the collection.
type RoomRepo struct {
	coll *mongo.Collection
}

func NewRoomRepo(db *mongo.Database) *RoomRepo {
	return &RoomRepo{coll: db.Collection(roomsCollection)}
}

// GetMinUserLastSeenAt returns the per-room read floor (room.minUserLastSeenAt).
// Returns (nil, nil) when the document is missing OR the field is unset —
// the caller treats both as "no floor".
func (r *RoomRepo) GetMinUserLastSeenAt(ctx context.Context, roomID string) (*time.Time, error) {
	var doc struct {
		MinUserLastSeenAt *time.Time `bson:"minUserLastSeenAt"`
	}
	err := r.coll.FindOne(
		ctx,
		bson.M{"_id": roomID},
		options.FindOne().SetProjection(bson.M{"minUserLastSeenAt": 1, "_id": 0}),
	).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return doc.MinUserLastSeenAt, nil
}
