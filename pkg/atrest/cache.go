package atrest

import (
	"crypto/cipher"
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// dekCache is a bounded cache of AES-256-GCM AEADs keyed by roomID,
// backed by HashiCorp's 2Q (TwoQueueCache) algorithm. 2Q protects
// recently-promoted "frequent" entries from being evicted by one-shot
// "recent" scans, which matches our access pattern: a small set of hot
// rooms gets most of the traffic, with a long tail of cold rooms. On
// Zipfian workloads 2Q typically delivers a meaningfully higher hit
// rate than plain LRU at the same capacity.
//
// TTL is layered on top of 2Q for defensive memory retention — cold
// rooms that 2Q hasn't evicted yet are still cleaned up after the TTL
// window. Since the DEK bytes never change for the lifetime of a room
// the TTL isn't required for correctness; it bounds idle memory.
type dekCache struct {
	inner *lru.TwoQueueCache[string, *dekEntry]
	ttl   time.Duration
	now   func() time.Time // injectable for tests
}

type dekEntry struct {
	aead      cipher.AEAD
	expiresAt time.Time
}

// minCacheSize is the smallest capacity 2Q can be built with — its
// ghost-evict LRU is sized as a fraction of capacity and refuses zero.
const minCacheSize = 2

func newDEKCache(capacity int, ttl time.Duration) *dekCache {
	if capacity < minCacheSize {
		capacity = minCacheSize
	}
	inner, err := lru.New2Q[string, *dekEntry](capacity)
	if err != nil {
		// Unreachable: New2Q only errors on size <= 0, which the guard
		// above prevents.
		panic(fmt.Sprintf("atrest: build 2Q cache: %v", err))
	}
	return &dekCache{inner: inner, ttl: ttl, now: time.Now}
}

// get returns the cached AEAD for roomID and whether it was present
// and unexpired. Expired entries are evicted lazily here so cold
// entries don't accumulate indefinitely.
func (c *dekCache) get(roomID string) (cipher.AEAD, bool) {
	e, ok := c.inner.Get(roomID)
	if !ok {
		return nil, false
	}
	if c.now().After(e.expiresAt) {
		c.inner.Remove(roomID)
		return nil, false
	}
	return e.aead, true
}

// set stores aead under roomID, refreshing the TTL.
func (c *dekCache) set(roomID string, aead cipher.AEAD) {
	c.inner.Add(roomID, &dekEntry{aead: aead, expiresAt: c.now().Add(c.ttl)})
}

func (c *dekCache) invalidate(roomID string) {
	c.inner.Remove(roomID)
}
