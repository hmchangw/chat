package atrest

import (
	"container/list"
	"sync"
	"time"
)

// dekCache is an LRU keyed by roomID holding unwrapped DEK bytes.
// Capacity is the primary cap; TTL is a soft retention bound for
// cold rooms in long-lived processes.
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
	dek       []byte
	expiresAt time.Time
}

func newDEKCache(capacity int, ttl time.Duration) *dekCache {
	return &dekCache{
		capacity: capacity,
		ttl:      ttl,
		ll:       list.New(),
		index:    make(map[string]*list.Element, capacity),
		now:      time.Now,
	}
}

func (c *dekCache) get(roomID string) ([]byte, bool) {
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
	return e.dek, true
}

func (c *dekCache) set(roomID string, dek []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	exp := c.now().Add(c.ttl)
	if el, ok := c.index[roomID]; ok {
		e := el.Value.(*dekEntry)
		e.dek = dek
		e.expiresAt = exp
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&dekEntry{roomID: roomID, dek: dek, expiresAt: exp})
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
