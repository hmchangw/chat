package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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

	if len(f.Users) > 0 {
		docs := make([]interface{}, len(f.Users))
		for i := range f.Users {
			docs[i] = f.Users[i]
		}
		if _, err := db.Collection("users").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert users: %w", err)
		}
	}
	if len(f.Rooms) > 0 {
		docs := make([]interface{}, len(f.Rooms))
		for i := range f.Rooms {
			docs[i] = f.Rooms[i]
		}
		if _, err := db.Collection("rooms").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert rooms: %w", err)
		}
	}
	if len(f.Subscriptions) > 0 {
		docs := make([]interface{}, len(f.Subscriptions))
		for i := range f.Subscriptions {
			docs[i] = f.Subscriptions[i]
		}
		if _, err := db.Collection("subscriptions").InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("insert subscriptions: %w", err)
		}
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
