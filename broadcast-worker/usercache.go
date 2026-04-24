package main

import (
	"container/list"
	"context"
	"fmt"
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

// FindUsersByAccounts returns users for the requested accounts, serving
// cache hits without calling the inner store. Cache misses are forwarded
// in a single batched inner call. Missing users are not cached as
// negatives — an account the inner store didn't return is simply absent
// and will be re-fetched next time.
func (c *CachedUserStore) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	if len(accounts) == 0 {
		return nil, nil
	}

	now := c.now()

	c.mu.Lock()
	hits := make([]model.User, 0, len(accounts))
	missing := make([]string, 0, len(accounts))
	for _, account := range accounts {
		elem, ok := c.index[account]
		if !ok {
			missing = append(missing, account)
			continue
		}
		entry := elem.Value.(*userCacheEntry)
		if now.Sub(entry.inserted) >= c.ttl {
			// Stale; treat as miss. Drop entry now so a concurrent writer
			// doesn't collide; the inner result (or its absence) will
			// repopulate below.
			c.lru.Remove(elem)
			delete(c.index, account)
			missing = append(missing, account)
			continue
		}
		c.lru.MoveToFront(elem)
		hits = append(hits, entry.user)
	}
	c.mu.Unlock()

	if len(missing) == 0 {
		return hits, nil
	}

	fresh, err := c.inner.FindUsersByAccounts(ctx, missing)
	if err != nil {
		// Return partial hits plus the wrapped error so callers can log and continue.
		return hits, fmt.Errorf("cached find users by accounts: %w", err)
	}

	c.mu.Lock()
	for i := range fresh {
		u := fresh[i]
		if existing, ok := c.index[u.Account]; ok {
			// Concurrent race: another goroutine populated the same account.
			// Refresh in place and move to front.
			existing.Value = &userCacheEntry{account: u.Account, user: u, inserted: now}
			c.lru.MoveToFront(existing)
			continue
		}
		entry := &userCacheEntry{account: u.Account, user: u, inserted: now}
		elem := c.lru.PushFront(entry)
		c.index[u.Account] = elem
		if c.lru.Len() > c.maxSize {
			lruElem := c.lru.Back()
			if lruElem != nil {
				lruEntry := lruElem.Value.(*userCacheEntry)
				c.lru.Remove(lruElem)
				delete(c.index, lruEntry.account)
			}
		}
	}
	c.mu.Unlock()

	return append(hits, fresh...), nil
}
