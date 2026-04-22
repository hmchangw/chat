package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func insertDocs[T any](ctx context.Context, coll *mongo.Collection, items []T) error {
	if len(items) == 0 {
		return nil
	}
	docs := make([]interface{}, len(items))
	for i := range items {
		docs[i] = items[i]
	}
	if _, err := coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert into %s: %w", coll.Name(), err)
	}
	return nil
}

// Seed drops and repopulates users/rooms/subscriptions in db from fixtures.
// Idempotent: safe to rerun.
func Seed(ctx context.Context, db *mongo.Database, f Fixtures) error {
	if err := db.Collection("users").Drop(ctx); err != nil {
		return fmt.Errorf("drop users: %w", err)
	}
	if err := db.Collection("rooms").Drop(ctx); err != nil {
		return fmt.Errorf("drop rooms: %w", err)
	}
	if err := db.Collection("subscriptions").Drop(ctx); err != nil {
		return fmt.Errorf("drop subscriptions: %w", err)
	}

	if err := insertDocs(ctx, db.Collection("users"), f.Users); err != nil {
		return err
	}
	if err := insertDocs(ctx, db.Collection("rooms"), f.Rooms); err != nil {
		return err
	}
	if err := insertDocs(ctx, db.Collection("subscriptions"), f.Subscriptions); err != nil {
		return err
	}

	subsIdx := db.Collection("subscriptions")
	if _, err := subsIdx.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "roomId", Value: 1}}},
		{Keys: bson.D{{Key: "u.account", Value: 1}}},
		{Keys: bson.D{{Key: "u.account", Value: 1}, {Key: "roomId", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create subscription indexes: %w", err)
	}
	return nil
}

// Teardown drops the three seeded collections without repopulating.
func Teardown(ctx context.Context, db *mongo.Database) error {
	for _, c := range []string{"users", "rooms", "subscriptions"} {
		if err := db.Collection(c).Drop(ctx); err != nil {
			return fmt.Errorf("drop %s: %w", c, err)
		}
	}
	return nil
}
