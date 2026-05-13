package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/hmchangw/chat/pkg/model"
)

//go:generate mockgen -source=store.go -destination=mock_store_test.go -package=main

type SearchStore interface {
	Search(ctx context.Context, indices []string, body json.RawMessage) (json.RawMessage, error)
	GetUserRoomDoc(ctx context.Context, account string) (UserRoomDoc, bool, error)
}

// RestrictedRoomCache stores only the restricted-rooms map (rid → HSS
// millis). The unrestricted rooms[] array is always resolved via ES
// terms-lookup at query time, so no local copy is needed.
type RestrictedRoomCache interface {
	GetRestricted(ctx context.Context, account string) (map[string]int64, bool, error)
	SetRestricted(ctx context.Context, account string, rooms map[string]int64, ttl time.Duration) error
}

// UserRoomDoc mirrors the subset of the user-room ES doc that
// search-service reads. Fields must stay in sync with the upsert shape
// in search-sync-worker/user_room.go userRoomUpsertDoc.
type UserRoomDoc struct {
	UserAccount     string           `json:"userAccount"`
	Rooms           []string         `json:"rooms"`
	RestrictedRooms map[string]int64 `json:"restrictedRooms"`
}

// MongoStore is the Mongo-backed store interface for search-service.
type MongoStore interface {
	SearchAppsByName(
		ctx context.Context,
		nameQuery, account string,
		assistantEnabled *bool,
		offset, limit int,
	) ([]model.App, error)

	// HydrateSubscriptions fetches the caller's Subscription documents for
	// the given room IDs and returns them as SearchSubscription projections.
	// The returned slice preserves the ordering of roomIDs. Room IDs for
	// which no subscription exists in Mongo are silently omitted (the user
	// may have left the room between the ES query and the Mongo fetch).
	HydrateSubscriptions(
		ctx context.Context,
		account string,
		roomIDs []string,
	) ([]model.SearchSubscription, error)

	// FindUsersByIDs fetches user documents from the `users` collection
	// whose `_id` is in `ids`. Missing IDs are silently absent from the
	// result — callers must tolerate a shorter slice.
	FindUsersByIDs(ctx context.Context, ids []string) ([]model.User, error)

	// FindRoomsByIDs fetches room documents from the `rooms` collection
	// whose `_id` is in `ids`. Missing IDs are silently absent from the
	// result.
	FindRoomsByIDs(ctx context.Context, ids []string) ([]model.Room, error)
}

// SearchUsersClient is the outbound HTTP interface for user search.
// It wraps the third-party HR endpoint; the handler tests inject a fake
// implementation so no real HTTP call is needed in unit tests.
type SearchUsersClient interface {
	SearchUsers(ctx context.Context, query string) ([]model.SearchUser, error)
}
