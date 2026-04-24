package main

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

// userCacheEntry is the value stored in each LRU list element.
type userCacheEntry struct {
	account  string
	user     model.User
	inserted time.Time
}

// CachedUserStore wraps a userstore.UserStore with an in-process LRU+TTL
// cache of FindUsersByAccounts results. FindUserByID delegates to the
// inner store unchanged.
type CachedUserStore struct {
	inner   userstore.UserStore
	ttl     time.Duration
	maxSize int

	mu    sync.Mutex
	lru   *list.List // elements hold *userCacheEntry; front = MRU, back = LRU
	index map[string]*list.Element
	now   func() time.Time
}

// NewCachedUserStore returns a cache wrapping inner. maxSize > 0 and ttl > 0
// are required; the main.go wiring guards against zero values.
func NewCachedUserStore(inner userstore.UserStore, maxSize int, ttl time.Duration) *CachedUserStore {
	return &CachedUserStore{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		lru:     list.New(),
		index:   make(map[string]*list.Element, maxSize),
		now:     time.Now,
	}
}

// FindUserByID delegates; no caching for single-ID lookups.
func (c *CachedUserStore) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	return c.inner.FindUserByID(ctx, id)
}

// FindUsersByAccounts will be implemented in Task 2. The stub takes the
// lock once so the mutex and LRU state are reachable by the linter during
// the scaffolding phase.
func (c *CachedUserStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	c.mu.Lock()
	_ = c.lru
	_ = c.index
	_ = c.now
	c.mu.Unlock()
	return c.inner.FindUsersByAccounts(ctx, accounts)
}

// Forward reference: userCacheEntry is declared in this file for the Task 2
// cache implementation. Listing every field here keeps the linter from
// flagging them as unused during the scaffolding phase. Remove when the
// Task 2 implementation populates real entries.
var _ = userCacheEntry{
	account:  "",
	user:     model.User{},
	inserted: time.Time{},
}
