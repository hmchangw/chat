package userstore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/model"
)

// Cache is an LRU+TTL cache over UserStore.FindUserByID. Mirrors the shape of
// pkg/roommetacache so services that need both can pick the same env-knob and
// loader conventions. Concurrent misses for the same userID collapse to a
// single backend call via singleflight; ErrUserNotFound propagates unwrapped
// so callers can branch on it.
//
// Why pod-local in-memory (not Valkey-backed L2 like pkg/roomsubcache):
// entries are tiny (~500 B/user), per-pod working set caps at ~5 MB for 10K
// active senders, writes are rare (display-name changes are admin events), and
// there is only one consumer today (message-gatekeeper). Same profile as
// pkg/roommetacache — Valkey overhead (network hop, serialization, error
// handling) buys nothing at this size. Revisit if (a) gatekeeper pod count
// grows past ~20, (b) entry size jumps (e.g. embedded avatars), or (c) a
// second consumer arrives and cross-service sharing starts to matter.
type Cache struct {
	lru   *lru.LRU[string, *model.User]
	store UserStore
	sf    singleflight.Group

	hits     atomic.Uint64
	misses   atomic.Uint64
	loadErrs atomic.Uint64
}

// Stats is a snapshot of the cache's counters.
type Stats struct {
	Hits, Misses, LoadErrors uint64
	Size                     int
}

// NewCache returns a Cache fronting the given UserStore. size and ttl must both
// be positive; store must be non-nil.
func NewCache(store UserStore, size int, ttl time.Duration) (*Cache, error) {
	if store == nil {
		return nil, fmt.Errorf("userstore: store must not be nil")
	}
	if size <= 0 {
		return nil, fmt.Errorf("userstore: cache size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("userstore: cache ttl must be positive, got %v", ttl)
	}
	return &Cache{
		lru:   lru.NewLRU[string, *model.User](size, nil, ttl),
		store: store,
	}, nil
}

// FindUserByID serves from the LRU when hot, falls through to the underlying
// store on miss. ErrUserNotFound is returned verbatim so the caller can choose
// to fail-open (the cache does not store negative results).
func (c *Cache) FindUserByID(ctx context.Context, id string) (*model.User, error) {
	if v, ok := c.lru.Get(id); ok {
		c.hits.Add(1)
		return v, nil
	}
	c.misses.Add(1)
	v, err, _ := c.sf.Do(id, func() (interface{}, error) {
		if cached, ok := c.lru.Get(id); ok {
			return cached, nil
		}
		u, err := c.store.FindUserByID(ctx, id)
		if err != nil {
			return nil, err
		}
		c.lru.Add(id, u)
		return u, nil
	})
	if err != nil {
		c.loadErrs.Add(1)
		if errors.Is(err, ErrUserNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("find cached user %q: %w", id, err)
	}
	return v.(*model.User), nil
}

// FindUsersByAccounts is not cached and delegates directly to the underlying
// store. Callers that want hot-path-cached lookups should drive them via
// FindUserByID (by ID, post-resolution).
func (c *Cache) FindUsersByAccounts(ctx context.Context, accounts []string) ([]model.User, error) {
	return c.store.FindUsersByAccounts(ctx, accounts)
}

// Stats returns a snapshot of cache counters.
func (c *Cache) Stats() Stats {
	return Stats{
		Hits:       c.hits.Load(),
		Misses:     c.misses.Load(),
		LoadErrors: c.loadErrs.Load(),
		Size:       c.lru.Len(),
	}
}

// Invalidate removes any cached entry for userID. No-op if absent.
func (c *Cache) Invalidate(userID string) { c.lru.Remove(userID) }
