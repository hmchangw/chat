// Package roomsubcache caches the member list of a room in Valkey so
// fan-out workers (e.g. notification-worker) can avoid a Mongo round-trip
// for every published message.
//
// The cache stores only the fields a fan-out path actually needs —
// {ID, Account} per member — not the full model.Subscription document.
// Entries are written with a caller-supplied TTL and are not actively
// invalidated; staleness is bounded by the TTL. An Invalidate method is
// provided so a future room-membership event listener can evict eagerly
// without changing this package.
package roomsubcache

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// Member is the projection of model.Subscription that fan-out callers need:
// the user's stable ID (for sender-skip checks) and account (for routing).
type Member struct {
	ID      string `json:"id"`
	Account string `json:"account"`
}

// Cache stores and retrieves a room's member list.
//
// Get returns valkeyutil.ErrCacheMiss when the room has no cached entry.
// An empty (non-nil) slice is a valid cache hit and must not be confused
// with a miss — callers can negative-cache empty rooms by Set-ing nil.
type Cache interface {
	Get(ctx context.Context, roomID string) ([]Member, error)
	Set(ctx context.Context, roomID string, members []Member, ttl time.Duration) error
	Invalidate(ctx context.Context, roomID string) error
}

type valkeyCache struct {
	client valkeyutil.Client
}

// NewValkeyCache returns a Cache backed by the given Valkey client.
func NewValkeyCache(client valkeyutil.Client) Cache {
	return &valkeyCache{client: client}
}

func cacheKey(roomID string) string {
	return "room:" + roomID + ":subs"
}

// Get returns the cached member list for roomID. On absence it returns
// a wrapped valkeyutil.ErrCacheMiss — callers should branch with
// errors.Is. An empty cached value is a hit and returns a non-nil empty
// slice, distinguishable from a miss.
func (c *valkeyCache) Get(ctx context.Context, roomID string) ([]Member, error) {
	// Default to an empty slice so an empty cache hit stays non-nil and
	// is distinguishable from a miss (which returns ErrCacheMiss instead).
	members := []Member{}
	if err := valkeyutil.GetJSON(ctx, c.client, cacheKey(roomID), &members); err != nil {
		return nil, fmt.Errorf("get cached subscriptions for room %s: %w", roomID, err)
	}
	return members, nil
}

// Set stores members under roomID with the given TTL. A nil members
// slice is stored as an empty list (so Get returns []Member{} rather
// than nil on the next read), which doubles as a negative cache for
// empty/deleted rooms. A ttl of 0 stores the entry without expiry —
// callers who want bounded staleness must pass a non-zero TTL.
func (c *valkeyCache) Set(ctx context.Context, roomID string, members []Member, ttl time.Duration) error {
	// Marshal nil as an empty list so Get returns []Member{} rather than nil.
	if members == nil {
		members = []Member{}
	}
	if err := valkeyutil.SetJSONWithTTL(ctx, c.client, cacheKey(roomID), members, ttl); err != nil {
		return fmt.Errorf("set cached subscriptions for room %s: %w", roomID, err)
	}
	return nil
}

// Invalidate removes the cached entry for roomID. Intended for a future
// membership-change event listener; not called by the cache itself,
// which relies on TTL expiry.
func (c *valkeyCache) Invalidate(ctx context.Context, roomID string) error {
	if err := c.client.Del(ctx, cacheKey(roomID)); err != nil {
		return fmt.Errorf("invalidate cached subscriptions for room %s: %w", roomID, err)
	}
	return nil
}
