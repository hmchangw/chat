package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
)

// mongoStore is the Mongo-backed implementation of MongoStore.
type mongoStore struct {
	apps *mongo.Collection
}

func newMongoStore(db *mongo.Database) *mongoStore {
	return &mongoStore{
		apps: db.Collection("apps"),
	}
}

// ensureIndexes creates the index backing SearchAppsByName's $match on name.
// The match is a case-insensitive substring $regex, which Mongo cannot satisfy
// with tight index bounds — but a {name:1} index still lets the planner scan
// the compact index instead of doing a full collection scan, and supports the
// $lookup/$group stages the spec'd pipeline will add. CreateOne is idempotent
// when the key spec matches.
func (s *mongoStore) ensureIndexes(ctx context.Context) error {
	if _, err := s.apps.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}},
	}); err != nil {
		return fmt.Errorf("ensure apps (name) index: %w", err)
	}
	return nil
}

func (s *mongoStore) SearchAppsByName(
	ctx context.Context,
	query, account string,
	assistantEnabled *bool,
	offset, limit int,
) ([]model.App, error) {
	pipeline := buildSearchAppsPipeline(query, account, assistantEnabled, offset, limit)
	cur, err := s.apps.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, fmt.Errorf("aggregate apps: %w", err)
	}
	defer cur.Close(ctx)

	results := make([]model.App, 0)
	if err := cur.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode apps: %w", err)
	}
	return results, nil
}
