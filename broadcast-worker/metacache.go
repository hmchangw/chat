package main

import (
	"context"
	"time"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

// cachedMetaStore wraps a Store with a roommetacache.Cache in front of
// GetRoomMeta. All other Store methods pass through to the inner store via
// the embedded Store field.
type cachedMetaStore struct {
	Store
	cache *roommetacache.Wrapper[Store]
}

func newCachedMetaStore(inner Store, size int, ttl time.Duration, rec *cachestats.Recorder) (*cachedMetaStore, error) {
	w, err := roommetacache.WrapStore(inner, size, ttl, rec)
	if err != nil {
		return nil, err
	}
	return &cachedMetaStore{Store: w.S, cache: w}, nil
}

// GetRoomMeta serves from the cache, falling through to the inner store on miss.
func (c *cachedMetaStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return c.cache.GetRoomMeta(ctx, roomID)
}

// Len lets the owning service register a chat_cache_size gauge in
// main.go via cachestats. See cachestats.Stats.Register.
func (c *cachedMetaStore) Len() int { return c.cache.Len() }
