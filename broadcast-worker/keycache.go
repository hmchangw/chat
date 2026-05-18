package main

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// keyCacheEntry is a cached key pair with its absolute expiry.
type keyCacheEntry struct {
	key       *roomkeystore.VersionedKeyPair
	expiresAt time.Time
}

// CachedKeyProvider wraps a RoomKeyProvider with an in-process TTL cache.
// Concurrent misses on the same roomID are coalesced via singleflight so
// a hot room never triggers more than one in-flight inner fetch.
//
// Staleness contract: after a key rotation, this cache will keep returning
// the previous version for up to TTL. That is safe as long as TTL is well
// below the roomkeystore grace period — clients still hold the previous
// key during the grace window and can decrypt either version.
//
// Negative results (nil, nil — meaning the room has no provisioned key)
// are deliberately not cached, so a freshly-provisioned key is picked up
// on the next call rather than being shadowed for up to TTL.
type CachedKeyProvider struct {
	inner RoomKeyProvider
	ttl   time.Duration

	mu    sync.RWMutex
	cache map[string]keyCacheEntry

	sf  singleflight.Group
	now func() time.Time
}

// NewCachedKeyProvider returns a cache wrapping inner with the given TTL.
// ttl must be positive; main wires the cache only when configured > 0.
func NewCachedKeyProvider(inner RoomKeyProvider, ttl time.Duration) *CachedKeyProvider {
	return &CachedKeyProvider{
		inner: inner,
		ttl:   ttl,
		cache: make(map[string]keyCacheEntry),
		now:   time.Now,
	}
}

// Get returns the room's current key, serving from cache when fresh and
// falling through to the inner store on miss or expiry.
func (c *CachedKeyProvider) Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	if key, ok := c.lookup(roomID); ok {
		return key, nil
	}

	v, err, _ := c.sf.Do(roomID, func() (any, error) {
		// Re-check under the singleflight gate: another flight may have
		// just populated the cache for this room between our initial
		// miss and this point.
		if key, ok := c.lookup(roomID); ok {
			return key, nil
		}
		key, err := c.inner.Get(ctx, roomID)
		if err != nil {
			return nil, err
		}
		if key == nil {
			return nil, nil
		}
		c.store(roomID, key)
		return key, nil
	})
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	return v.(*roomkeystore.VersionedKeyPair), nil
}

// lookup returns (key, true) when a non-expired entry exists.
func (c *CachedKeyProvider) lookup(roomID string) (*roomkeystore.VersionedKeyPair, bool) {
	c.mu.RLock()
	entry, ok := c.cache[roomID]
	c.mu.RUnlock()
	if !ok {
		return nil, false
	}
	if !c.now().Before(entry.expiresAt) {
		return nil, false
	}
	return entry.key, true
}

func (c *CachedKeyProvider) store(roomID string, key *roomkeystore.VersionedKeyPair) {
	c.mu.Lock()
	c.cache[roomID] = keyCacheEntry{key: key, expiresAt: c.now().Add(c.ttl)}
	c.mu.Unlock()
}
