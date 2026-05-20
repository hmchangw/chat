package main

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// roomKeyStore is the narrow consumer interface for room-key seeding.
type roomKeyStore interface {
	Set(ctx context.Context, roomID string, pair roomkeystore.RoomKeyPair) (int, error)
	Delete(ctx context.Context, roomID string) error
}

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
func Seed(ctx context.Context, db *mongo.Database, f *Fixtures) error {
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
		return fmt.Errorf("seed users: %w", err)
	}
	if err := insertDocs(ctx, db.Collection("rooms"), f.Rooms); err != nil {
		return fmt.Errorf("seed rooms: %w", err)
	}
	if err := insertDocs(ctx, db.Collection("subscriptions"), f.Subscriptions); err != nil {
		return fmt.Errorf("seed subscriptions: %w", err)
	}

	subsIdx := db.Collection("subscriptions")
	if _, err := subsIdx.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "roomId", Value: 1}}},
		{Keys: bson.D{{Key: "u.account", Value: 1}}},
		{Keys: bson.D{{Key: "u.account", Value: 1}, {Key: "roomId", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create subscription indexes: %w", err)
	}

	// broadcast-worker and message-gatekeeper look up users by account
	// (not _id) during enrichment — index it to avoid a COLLSCAN per message.
	usersIdx := db.Collection("users")
	if _, err := usersIdx.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "account", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("create user indexes: %w", err)
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

// SeedRoomKeys writes each room keypair into the keystore. Harmless when
// broadcast-worker runs with ENCRYPTION_ENABLED=false.
func SeedRoomKeys(ctx context.Context, keys roomKeyStore, roomKeys map[string]roomkeystore.RoomKeyPair) error {
	for roomID, pair := range roomKeys {
		if _, err := keys.Set(ctx, roomID, pair); err != nil {
			return fmt.Errorf("set room key %s: %w", roomID, err)
		}
	}
	return nil
}

// SeedSearchSyncACL publishes one synthetic OutboxMemberAdded event per unique
// (account, roomID) tuple in the fixture subscriptions onto the local INBOX
// subject. search-sync-worker's `user-room-sync` consumer fans these out into
// one ES user-room doc per account, granting the search-sync-lag scenario the
// ACL gate that search-service's `search.messages` handler terms-lookups
// against.
//
// This is the seed-time entry point: it lets operators pay the 35s ES-refresh
// wait once (during `loadgen seed --preset=search-read --with-search-sync-acl`)
// instead of on every `loadgen run --scenario=search-sync-lag`. The wire
// output is byte-identical (modulo the publish-site Timestamp) to the legacy
// Run-time bootstrap in scenario_searchsync.go — operators who don't migrate
// will see the Run-time bootstrap publish the same events again, and the
// painless LWW logic in search-sync-worker treats the redundant write as a
// no-op.
//
// Returns the number of unique pairs actually published so callers can log
// positive confirmation. Honors ctx cancellation between publishes so a
// shutdown during a large-fixture seed doesn't sit publishing into a dying
// connection.
//
// NOTE: This helper does NOT wait for ES refresh. The caller (dispatch.runSeed)
// is responsible for the post-publish wait — separating "publish" from "wait"
// keeps the helper unit-testable without a live ES.
func SeedSearchSyncACL(
	ctx context.Context,
	publisher Publisher,
	siteID string,
	subs []model.Subscription,
) (int, error) {
	return bootstrapSearchSyncACL(ctx, publisher, siteID, subs)
}

// TeardownRoomKeys deletes the keypairs written by SeedRoomKeys.
func TeardownRoomKeys(ctx context.Context, keys roomKeyStore, roomIDs []string) error {
	for _, roomID := range roomIDs {
		if err := keys.Delete(ctx, roomID); err != nil {
			return fmt.Errorf("delete room key %s: %w", roomID, err)
		}
	}
	return nil
}
