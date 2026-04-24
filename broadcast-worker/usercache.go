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

// _ is a forward-reference placeholder proving userCacheEntry is intentionally
// declared now and wired in Task 2. Remove when FindUsersByAccounts starts
// using userCacheEntry directly.
var _ *userCacheEntry

func init() {
	// Suppress unused field warnings for userCacheEntry fields that will be
	// used in Task 2. These blanks can be removed once FindUsersByAccounts
	// uses them.
	var e userCacheEntry
	_ = e.account
	_ = e.user
	_ = e.inserted
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
	c := &CachedUserStore{
		inner:   inner,
		ttl:     ttl,
		maxSize: maxSize,
		lru:     list.New(),
		index:   make(map[string]*list.Element, maxSize),
		now:     time.Now,
	}
	// Blank refs for fields that will be used in Task 2.
	_ = &c.mu
	_ = c.lru
	return c
}

// FindUserByID delegates; no caching for single-ID lookups.
func (c *CachedUserStore) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	return c.inner.FindUserByID(ctx, id)
}

// FindUsersByAccounts will be implemented in Task 2.
func (c *CachedUserStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	return c.inner.FindUsersByAccounts(ctx, accounts)
}
