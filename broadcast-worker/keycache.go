package main

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// roomKeyCacheEntry is the value stored in each LRU list element.
type roomKeyCacheEntry struct {
	roomID   string
	key      *roomkeystore.VersionedKeyPair
	inserted time.Time
}

// CachedRoomKeyProvider wraps a RoomKeyProvider with an in-process LRU+TTL
// cache. The hot path (per-message encrypt) used to issue one Valkey HGETALL
// per message; with this cache the same room's key is served from memory
// until the TTL elapses or the entry is evicted.
//
// Cache staleness is bounded by safety: a rotated-out key version is still
// valid against the recipient's previous-key slot (see roomkeystore grace
// period), so a cached entry served past rotation just produces a message
// stamped with the older version, which clients decrypt against their stored
// previous key. The TTL should be short relative to the grace period so a
// rotation propagates quickly under steady load.
type CachedRoomKeyProvider struct {
	inner   RoomKeyProvider
	ttl     time.Duration
	maxSize int

	mu    sync.Mutex
	lru   *list.List // elements hold *roomKeyCacheEntry; front = MRU, back = LRU
	index map[string]*list.Element
	now   func() time.Time
}

// NewCachedRoomKeyProvider returns a cache wrapping inner. maxSize and ttl
// must both be positive.
func NewCachedRoomKeyProvider(inner RoomKeyProvider, maxSize int, ttl time.Duration) *CachedRoomKeyProvider {
	return &CachedRoomKeyProvider{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		lru:     list.New(),
		index:   make(map[string]*list.Element, maxSize),
		now:     time.Now,
	}
}

// Get returns the cached versioned key for roomID, or fetches from inner on
// miss / stale. Absent keys (inner returns (nil, nil)) and errors are NOT
// cached — caching either would mask the room having its first key
// provisioned, or a transient Valkey failure, for the full TTL.
func (c *CachedRoomKeyProvider) Get(ctx context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	now := c.now()

	c.mu.Lock()
	if elem, ok := c.index[roomID]; ok {
		entry := elem.Value.(*roomKeyCacheEntry)
		if now.Sub(entry.inserted) < c.ttl {
			if elem != c.lru.Front() {
				c.lru.MoveToFront(elem)
			}
			key := entry.key
			c.mu.Unlock()
			return key, nil
		}
		// Stale; drop now so a concurrent writer doesn't collide and the
		// fresh fetch below repopulates cleanly.
		c.lru.Remove(elem)
		delete(c.index, roomID)
	}
	c.mu.Unlock()

	fresh, err := c.inner.Get(ctx, roomID)
	if err != nil {
		return nil, fmt.Errorf("cached get room key for room %s: %w", roomID, err)
	}
	if fresh == nil {
		// Don't cache misses — see comment on Get.
		return nil, nil
	}

	c.mu.Lock()
	c.addLocked(roomID, fresh, now)
	c.mu.Unlock()
	return fresh, nil
}

// addLocked inserts or refreshes a cache entry. The caller must hold c.mu.
func (c *CachedRoomKeyProvider) addLocked(roomID string, key *roomkeystore.VersionedKeyPair, now time.Time) {
	if existing, ok := c.index[roomID]; ok {
		existing.Value = &roomKeyCacheEntry{roomID: roomID, key: key, inserted: now}
		c.lru.MoveToFront(existing)
		return
	}
	entry := &roomKeyCacheEntry{roomID: roomID, key: key, inserted: now}
	elem := c.lru.PushFront(entry)
	c.index[roomID] = elem
	if c.lru.Len() > c.maxSize {
		if lruElem := c.lru.Back(); lruElem != nil {
			lruEntry := lruElem.Value.(*roomKeyCacheEntry)
			c.lru.Remove(lruElem)
			delete(c.index, lruEntry.roomID)
		}
	}
}
