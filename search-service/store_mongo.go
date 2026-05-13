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
	apps          *mongo.Collection
	subscriptions *mongo.Collection
	users         *mongo.Collection
	rooms         *mongo.Collection
}

func newMongoStore(db *mongo.Database) *mongoStore {
	return &mongoStore{
		apps:          db.Collection("apps"),
		subscriptions: db.Collection("subscriptions"),
		users:         db.Collection("users"),
		rooms:         db.Collection("rooms"),
	}
}

func (s *mongoStore) SearchAppsByName(
	ctx context.Context,
	nameQuery, account string,
	assistantEnabled *bool,
	offset, limit int,
) ([]model.App, error) {
	pipeline := buildSearchAppsPipeline(nameQuery, account, assistantEnabled, offset, limit)
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

// HydrateSubscriptions fetches the caller's subscription documents for the
// given room IDs from the `subscriptions` collection and projects them into
// []model.SearchSubscription. Documents are matched by `u.account` (caller's
// account) and `roomId` (one of the provided IDs). Missing subscriptions are
// silently omitted. The bson projection maps directly onto model.SearchSubscription
// via the `bson:"..."` tags on that struct.
func (s *mongoStore) HydrateSubscriptions(
	ctx context.Context,
	account string,
	roomIDs []string,
) ([]model.SearchSubscription, error) {
	if len(roomIDs) == 0 {
		return []model.SearchSubscription{}, nil
	}

	filter := bson.D{
		{Key: "u.account", Value: account},
		{Key: "roomId", Value: bson.D{{Key: "$in", Value: roomIDs}}},
	}

	cur, err := s.subscriptions.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("find subscriptions: %w", err)
	}
	defer cur.Close(ctx)

	results := make([]model.SearchSubscription, 0)
	if err := cur.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode subscriptions: %w", err)
	}

	// Reorder results to match the input roomIDs order (Mongo $in does
	// not preserve input order; the caller depends on ES relevance ranking
	// surviving the hydration step). Subscriptions missing from Mongo
	// (e.g. the user left between the ES query and this lookup) are
	// silently omitted.
	byRoomID := make(map[string]model.SearchSubscription, len(results))
	for _, sub := range results {
		byRoomID[sub.RoomID] = sub
	}
	ordered := make([]model.SearchSubscription, 0, len(results))
	for _, roomID := range roomIDs {
		if sub, ok := byRoomID[roomID]; ok {
			ordered = append(ordered, sub)
		}
	}
	return ordered, nil
}

// FindUsersByIDs returns the user documents whose `_id` is in `ids`.
// Order is NOT preserved — Mongo's `$in` returns documents in natural
// collection order, not in the order of the input IDs. Callers that
// need order-preserving lookup should build a `map[string]model.User`
// from the result (as `searchMessages` does for O(1) per-hit enrichment).
// Contrast with `HydrateSubscriptions`, which DOES reorder its results
// to preserve ES relevance ranking.
func (s *mongoStore) FindUsersByIDs(ctx context.Context, ids []string) ([]model.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	filter := bson.M{"_id": bson.M{"$in": ids}}
	cur, err := s.users.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("find users by ids: %w", err)
	}
	defer cur.Close(ctx)

	results := make([]model.User, 0)
	if err := cur.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode users: %w", err)
	}
	return results, nil
}

// FindRoomsByIDs returns the room documents whose `_id` is in `ids`.
// Order is NOT preserved — see `FindUsersByIDs` for rationale.
func (s *mongoStore) FindRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	filter := bson.M{"_id": bson.M{"$in": ids}}
	cur, err := s.rooms.Find(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("find rooms by ids: %w", err)
	}
	defer cur.Close(ctx)

	results := make([]model.Room, 0)
	if err := cur.All(ctx, &results); err != nil {
		return nil, fmt.Errorf("decode rooms: %w", err)
	}
	return results, nil
}
