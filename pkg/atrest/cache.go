package atrest

import (
	"container/list"
	"crypto/cipher"
	"sync"
	"time"
)

// dekCache is an LRU keyed by roomID holding the AES-GCM AEAD constructed
// from the unwrapped DEK. Caching the AEAD (not just the DEK bytes) lets
// batch reads / writes for the same room skip the AES key schedule and
// GHASH setup that NewCipher+NewGCM would otherwise repeat per call.
//
// Capacity is the primary cap; TTL is a soft retention bound for cold
// rooms in long-lived processes. TTL is checked lazily on get; expired
// entries persist in the index until accessed or evicted.
type dekCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	ll       *list.List
	index    map[string]*list.Element
	now      func() time.Time // injectable for tests
}

type dekEntry struct {
	roomID    string
	aead      cipher.AEAD
	expiresAt time.Time
}

func newDEKCache(capacity int, ttl time.Duration) *dekCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &dekCache{
		capacity: capacity,
		ttl:      ttl,
		ll:       list.New(),
		index:    make(map[string]*list.Element, capacity),
		now:      time.Now,
	}
}

func (c *dekCache) get(roomID string) (cipher.AEAD, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.index[roomID]
	if !ok {
		return nil, false
	}
	e := el.Value.(*dekEntry)
	if c.now().After(e.expiresAt) {
		c.ll.Remove(el)
		delete(c.index, roomID)
		return nil, false
	}
	c.ll.MoveToFront(el)
	return e.aead, true
}

// set stores the AEAD under roomID, refreshing the TTL.
func (c *dekCache) set(roomID string, aead cipher.AEAD) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(c.ttl)
	if el, ok := c.index[roomID]; ok {
		e := el.Value.(*dekEntry)
		e.aead = aead
		e.expiresAt = exp
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&dekEntry{roomID: roomID, aead: aead, expiresAt: exp})
	c.index[roomID] = el
	if c.ll.Len() > c.capacity {
		oldest := c.ll.Back()
		if oldest != nil {
			c.ll.Remove(oldest)
			delete(c.index, oldest.Value.(*dekEntry).roomID)
		}
	}
}

func (c *dekCache) invalidate(roomID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.index[roomID]; ok {
		c.ll.Remove(el)
		delete(c.index, roomID)
	}
}
