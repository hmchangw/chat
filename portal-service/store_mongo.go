package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Compile-time interface compliance (also marks the method set used for lint).
var _ DirectoryStore = (*mongoDirectoryStore)(nil)

type mongoDirectoryStore struct {
	coll *mongo.Collection
}

func newMongoDirectoryStore(db *mongo.Database, collection string) *mongoDirectoryStore {
	return &mongoDirectoryStore{coll: db.Collection(collection)}
}

func (s *mongoDirectoryStore) LoadAll(ctx context.Context) ([]directoryRecord, error) {
	cur, err := s.coll.Find(ctx, bson.D{})
	if err != nil {
		return nil, fmt.Errorf("find directory records: %w", err)
	}
	var recs []directoryRecord
	if err := cur.All(ctx, &recs); err != nil {
		return nil, fmt.Errorf("decode directory records: %w", err)
	}
	return recs, nil
}
