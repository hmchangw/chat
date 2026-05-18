// Package roommetacache provides a process-local LRU+TTL cache for room
// metadata that is read on the per-message hot path of multiple services.
//
// The cached fields (Type, Name, SiteID, UserCount) change rarely; reading
// them from MongoDB on every published message produces measurable wasted
// load. This package centralizes the cache so message-gatekeeper and
// broadcast-worker share a uniform shape and behavior.
//
// Freshness is TTL-bounded. There is no active invalidation in v1 — see
// the spec at
// docs/superpowers/specs/2026-05-18-message-pipeline-mongo-caching-design.md
// for rationale.
package roommetacache

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/model"
)

// Meta is the cached projection of a room document. Both consumers
// (gatekeeper and broadcast-worker) use these four fields and nothing
// else from the room.
type Meta struct {
	ID        string
	Type      model.RoomType
	Name      string
	SiteID    string
	UserCount int
}

// Loader fetches a fresh Meta for the given roomID. The cache calls
// Loader on miss; a non-nil error short-circuits the cache (the error
// is returned to the caller and nothing is cached).
type Loader func(ctx context.Context, roomID string) (Meta, error)

// Cache is an LRU+TTL cache of room Meta values, deduped via
// singleflight on miss.
type Cache struct {
	lru    *lru.LRU[string, Meta]
	loader Loader
	sf     singleflight.Group

	hits     atomic.Uint64
	misses   atomic.Uint64
	loadErrs atomic.Uint64
}

// Stats is a snapshot of the cache's hit/miss counters.
type Stats struct {
	Hits, Misses, LoadErrors uint64
	Size                     int
}

// New constructs a Cache with the given capacity, TTL, and loader.
// size and ttl must both be positive; loader must be non-nil.
func New(size int, ttl time.Duration, loader Loader) (*Cache, error) {
	if size <= 0 {
		return nil, fmt.Errorf("roommetacache: size must be positive, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("roommetacache: ttl must be positive, got %v", ttl)
	}
	if loader == nil {
		return nil, fmt.Errorf("roommetacache: loader must not be nil")
	}
	return &Cache{
		lru:    lru.NewLRU[string, Meta](size, nil, ttl),
		loader: loader,
	}, nil
}
