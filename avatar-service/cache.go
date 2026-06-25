package main

import (
	"sync"
	"time"
)

type cacheEntry struct {
	val string
	exp time.Time
}

// ttlCache is a tiny thread-safe cache with a TTL and a hard capacity. When the
// capacity is exceeded it drops all entries (simple bounded behaviour — the
// cache is only an accelerator). Stores positive lookups only.
type ttlCache struct {
	mu  sync.Mutex
	m   map[string]cacheEntry
	cap int
	ttl time.Duration
}

func newTTLCache(capacity int, ttl time.Duration) *ttlCache {
	return &ttlCache{m: make(map[string]cacheEntry), cap: capacity, ttl: ttl}
}

func (c *ttlCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.val, true
}

func (c *ttlCache) Put(key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.cap {
		c.m = make(map[string]cacheEntry, c.cap)
	}
	c.m[key] = cacheEntry{val: val, exp: time.Now().Add(c.ttl)}
}

func (c *ttlCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
