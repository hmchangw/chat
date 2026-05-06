package atrest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

//go:generate mockgen -destination=mock_dek_store_test.go -package=atrest -source=dek_store.go DEKStore

// DEKStore persists wrapped DEK records, one per room.
type DEKStore interface {
	// Get returns nil, nil if the row does not exist.
	Get(ctx context.Context, roomID string) (*RoomDataKey, error)
	// Upsert inserts the row only if absent (first-writer-wins).
	Upsert(ctx context.Context, key RoomDataKey) error
	// Replace fully overwrites the row. Used by KEK rotation.
	Replace(ctx context.Context, key RoomDataKey) error
}

// CollectionName is the canonical Mongo collection name for the DEK store.
const CollectionName = "room_data_keys"

type mongoDEKStore struct {
	coll *mongo.Collection
}

// NewMongoDEKStore returns a DEKStore backed by the given collection.
// Callers typically pass db.Collection(CollectionName).
func NewMongoDEKStore(coll *mongo.Collection) DEKStore {
	return &mongoDEKStore{coll: coll}
}

func (s *mongoDEKStore) Get(ctx context.Context, roomID string) (*RoomDataKey, error) {
	var out RoomDataKey
	err := s.coll.FindOne(ctx, bson.M{"_id": roomID}).Decode(&out)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, fmt.Errorf("dek store get: %w", err)
	}
	return &out, nil
}

func (s *mongoDEKStore) Upsert(ctx context.Context, key RoomDataKey) error { //nolint:gocritic // hugeParam: key is passed by value to satisfy the DEKStore interface
	if key.CreatedAt.IsZero() {
		key.CreatedAt = time.Now().UTC()
	}
	_, err := s.coll.UpdateOne(ctx,
		bson.M{"_id": key.ID},
		bson.M{"$setOnInsert": bson.M{
			"wrappedDEK": key.WrappedDEK,
			"wrapNonce":  key.WrapNonce,
			"kekVersion": key.KEKVersion,
			"createdAt":  key.CreatedAt,
		}},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("dek store upsert: %w", err)
	}
	return nil
}

func (s *mongoDEKStore) Replace(ctx context.Context, key RoomDataKey) error { //nolint:gocritic // hugeParam: key is passed by value to satisfy the DEKStore interface
	_, err := s.coll.ReplaceOne(ctx, bson.M{"_id": key.ID}, key, options.Replace().SetUpsert(false))
	if err != nil {
		return fmt.Errorf("dek store replace: %w", err)
	}
	return nil
}
