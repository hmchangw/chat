package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/roommetacache"
)

// cachedMetaStore wraps a Store with a roommetacache.Cache for room
// metadata. GetSubscription passes through to the inner store.
type cachedMetaStore struct {
	Store
	cache *roommetacache.Cache
}

func newCachedMetaStore(inner Store, size int, ttl time.Duration) (*cachedMetaStore, error) {
	loader := func(ctx context.Context, roomID string) (roommetacache.Meta, error) {
		return inner.GetRoomMeta(ctx, roomID)
	}
	cache, err := roommetacache.New(size, ttl, loader)
	if err != nil {
		return nil, fmt.Errorf("build room meta cache: %w", err)
	}
	return &cachedMetaStore{Store: inner, cache: cache}, nil
}

func (c *cachedMetaStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return c.cache.Get(ctx, roomID)
}
