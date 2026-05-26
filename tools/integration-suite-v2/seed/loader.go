// Package seed materialises the integration-suite-v2 test world into
// Mongo. The embedded JSON files in this directory are the source of
// truth; LoadAll drops the relevant collections and re-inserts the
// seeded documents fresh, so every scenario starts from a byte-identical
// baseline.
//
// The fixture-cast catalog (catalogs/fixture-cast.yaml) annotates these
// documents with role tags so scenarios can pick fixtures by predicate.
// Edit the JSON files in this directory and the cast YAML in the same
// PR (see README.md for the drift-discipline rationale).
package seed

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

//go:embed users.json
var usersJSON []byte

//go:embed rooms.json
var roomsJSON []byte

//go:embed subscriptions.json
var subscriptionsJSON []byte

//go:embed room-keys.json
var roomKeysJSON []byte

// Decoded returns the parsed seed documents. Used by LoadAll and by
// tests that want to verify the seed shape without touching Mongo.
func Decoded() (users []model.User, rooms []model.Room, subs []model.Subscription, err error) {
	if err = json.Unmarshal(usersJSON, &users); err != nil {
		return nil, nil, nil, fmt.Errorf("decode users.json: %w", err)
	}
	if err = json.Unmarshal(roomsJSON, &rooms); err != nil {
		return nil, nil, nil, fmt.Errorf("decode rooms.json: %w", err)
	}
	if err = json.Unmarshal(subscriptionsJSON, &subs); err != nil {
		return nil, nil, nil, fmt.Errorf("decode subscriptions.json: %w", err)
	}
	return users, rooms, subs, nil
}

// DecodedRoomKeys returns the list of room IDs that should have a
// fresh Valkey room key seeded at suite startup. Used by pure
// room-worker scenarios that publish canonical events directly
// (bypassing room-service's key-generation path) — they need a
// pre-existing key for the gate at room-worker/handler.go:1106-1114.
func DecodedRoomKeys() ([]string, error) {
	var ids []string
	if err := json.Unmarshal(roomKeysJSON, &ids); err != nil {
		return nil, fmt.Errorf("decode room-keys.json: %w", err)
	}
	return ids, nil
}

// LoadAll drops the users / rooms / subscriptions collections and
// re-inserts the seeded documents. Idempotent — every invocation
// produces byte-identical world state. Safe to call at the start of
// every scenario test.
func LoadAll(ctx context.Context, db *mongo.Database) error {
	users, rooms, subs, err := Decoded()
	if err != nil {
		return err
	}
	if err := dropAndInsert(ctx, db.Collection("users"), users); err != nil {
		return fmt.Errorf("seed users: %w", err)
	}
	if err := dropAndInsert(ctx, db.Collection("rooms"), rooms); err != nil {
		return fmt.Errorf("seed rooms: %w", err)
	}
	if err := dropAndInsert(ctx, db.Collection("subscriptions"), subs); err != nil {
		return fmt.Errorf("seed subscriptions: %w", err)
	}
	return nil
}

// LoadRoomKeys generates a fresh keypair for every room ID in
// room-keys.json and writes them to the supplied Valkey-backed store.
// Idempotent: re-running overwrites the existing keys, which is fine
// because pure room-worker scenarios never read the key's contents —
// only its presence. Intended to be called once at suite startup,
// NOT per-scenario (keys are stable across scenarios; Mongo is the
// only per-scenario wiped store).
func LoadRoomKeys(ctx context.Context, store roomkeystore.RoomKeyStore) error {
	ids, err := DecodedRoomKeys()
	if err != nil {
		return err
	}
	for _, id := range ids {
		pair, err := roomkeystore.GenerateKeyPair()
		if err != nil {
			return fmt.Errorf("seed room key %s: generate: %w", id, err)
		}
		if _, err := store.Set(ctx, id, pair); err != nil {
			return fmt.Errorf("seed room key %s: set: %w", id, err)
		}
	}
	return nil
}

func dropAndInsert[T any](ctx context.Context, coll *mongo.Collection, items []T) error {
	if err := coll.Drop(ctx); err != nil {
		return fmt.Errorf("drop %s: %w", coll.Name(), err)
	}
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
