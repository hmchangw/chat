// data-migration/historical/pkg/checkpoint/store.go
package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Store interface {
	Load(ctx context.Context, jobName string) (lastID string, err error)
	Save(ctx context.Context, jobName, lastID string) error
}

type doc struct {
	ID        string    `bson:"_id"`
	LastID    string    `bson:"lastId"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

type mongoStore struct{ coll *mongo.Collection }

func NewMongoStore(coll *mongo.Collection) Store { return &mongoStore{coll: coll} }

func (s *mongoStore) Load(ctx context.Context, jobName string) (string, error) {
	var d doc
	err := s.coll.FindOne(ctx, bson.M{"_id": jobName}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load checkpoint %s: %w", jobName, err)
	}
	return d.LastID, nil
}

func (s *mongoStore) Save(ctx context.Context, jobName, lastID string) error {
	_, err := s.coll.ReplaceOne(
		ctx,
		bson.M{"_id": jobName},
		doc{ID: jobName, LastID: lastID, UpdatedAt: time.Now().UTC()},
		options.Replace().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("save checkpoint %s: %w", jobName, err)
	}
	return nil
}
