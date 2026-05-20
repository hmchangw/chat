package main

import (
	"context"
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/model"
)

// cachedSubscription is the projection of model.Subscription that
// gatekeeper actually reads on the hot path. Caching only these fields
// (vs the full doc) keeps the cache tight and makes the contract
// explicit at the cache boundary. ID is the user's entity ID (used by
// the handler to populate msg.UserID on the canonical message event).
type cachedSubscription struct {
	ID      string
	Account string
	Roles   []model.Role
}

type subKey struct {
	roomID  string
	account string
}

// cachedSubStore wraps a Store with an LRU+TTL cache of subscription
// lookups. Negative results (errNotSubscribed) and transient errors
// are NOT cached — see the spec for rationale. GetRoomMeta passes
// through unchanged.
type cachedSubStore struct {
	Store
	lru *lru.LRU[subKey, cachedSubscription]
	sf  singleflight.Group
	rec *cachestats.Recorder
}

func newCachedSubStore(inner Store, size int, ttl time.Duration, rec *cachestats.Recorder) (*cachedSubStore, error) {
	if size <= 0 {
		return nil, fmt.Errorf("subcache: size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("subcache: ttl must be positive, got %v", ttl)
	}
	return &cachedSubStore{
		Store: inner,
		lru:   lru.NewLRU[subKey, cachedSubscription](size, nil, ttl),
		rec:   rec,
	}, nil
}

func (c *cachedSubStore) GetSubscription(ctx context.Context, account, roomID string) (*model.Subscription, error) {
	key := subKey{roomID: roomID, account: account}
	if v, ok := c.lru.Get(key); ok {
		c.rec.Hit()
		return fromCached(v), nil
	}
	// Miss counts every caller whose outer-LRU lookup found no
	// entry, not every distinct loader invocation. Under singleflight
	// contention these can diverge: N concurrent callers for the same
	// missing key produce N misses but a single inner-store call.
	c.rec.Miss()

	// roomIDs and accounts are validated as UUID/base62/email-style strings that
	// cannot contain NUL bytes; the \x00 separator therefore makes the key
	// collision-free across any (roomID, account) split.
	sfKey := roomID + "\x00" + account
	v, err, _ := c.sf.Do(sfKey, func() (interface{}, error) {
		if cached, ok := c.lru.Get(key); ok {
			return cached, nil
		}
		sub, err := c.Store.GetSubscription(ctx, account, roomID)
		if err != nil {
			// Do not cache errNotSubscribed or transient errors — see spec.
			return nil, err
		}
		projected := cachedSubscription{
			ID:      sub.User.ID,
			Account: sub.User.Account,
			Roles:   append([]model.Role(nil), sub.Roles...),
		}
		c.lru.Add(key, projected)
		return projected, nil
	})
	if err != nil {
		return nil, err
	}
	return fromCached(v.(cachedSubscription)), nil
}

// fromCached builds a partial *model.Subscription from the cached
// projection. Only the fields gatekeeper reads (User.ID, User.Account,
// Roles) are populated; other fields are zero. This is intentional —
// the cache contract is the projection, not the full doc.
//
// The Roles slice is not defensively copied: the slice is already owned
// by the cache (copied at cache-write in GetSubscription), and the only
// consumer (canBypassLargeRoomCap) range-reads without mutation.
func fromCached(c cachedSubscription) *model.Subscription {
	return &model.Subscription{
		User:  model.SubscriptionUser{ID: c.ID, Account: c.Account},
		Roles: c.Roles,
	}
}

// Len lets the owning service register a chat_cache_size gauge in
// main.go via cachestats. See cachestats.Stats.Register.
func (c *cachedSubStore) Len() int { return c.lru.Len() }
