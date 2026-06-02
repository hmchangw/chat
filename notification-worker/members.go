package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
	"golang.org/x/sync/singleflight"

	"github.com/hmchangw/chat/pkg/roomsubcache"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// memberLoader reads the canonical member list for a room; a function type so tests can inject a fake.
type memberLoader func(ctx context.Context, roomID string) ([]roomsubcache.Member, error)

// cachedMemberLookup resolves members via L1 LRU → Valkey → Mongo.
// Single-flight collapses concurrent misses on the same room to one Mongo query.
type cachedMemberLookup struct {
	cache roomsubcache.Cache
	load  memberLoader
	ttl   time.Duration
	sf    singleflight.Group
	l1    *lru.LRU[string, []roomsubcache.Member]
}

// newCachedMemberLookup wires the three-tier lookup; l1Size <= 0 disables the L1.
func newCachedMemberLookup(cache roomsubcache.Cache, load memberLoader, ttl time.Duration, l1Size int, l1TTL time.Duration) *cachedMemberLookup {
	c := &cachedMemberLookup{cache: cache, load: load, ttl: ttl}
	if l1Size > 0 {
		c.l1 = lru.NewLRU[string, []roomsubcache.Member](l1Size, nil, l1TTL)
	}
	return c
}

// GetMembers returns the member list, populating Valkey and L1 on a miss. Callers must not mutate the slice.
func (c *cachedMemberLookup) GetMembers(ctx context.Context, roomID string) ([]roomsubcache.Member, error) {
	if c.l1 != nil {
		if v, ok := c.l1.Get(roomID); ok {
			return v, nil
		}
	}
	members, err, _ := c.sf.Do(roomID, func() (any, error) {
		if c.l1 != nil {
			if v, ok := c.l1.Get(roomID); ok { // double-check inside singleflight
				return v, nil
			}
		}
		got, err := c.cache.Get(ctx, roomID)
		if err == nil {
			c.populateL1(roomID, got)
			return got, nil
		}
		if !errors.Is(err, valkeyutil.ErrCacheMiss) {
			slog.Warn("roomsubcache get failed, falling back to mongo", "error", err, "roomId", roomID)
		}
		loaded, lerr := c.load(ctx, roomID)
		if lerr != nil {
			return nil, fmt.Errorf("load members for room %s: %w", roomID, lerr)
		}
		if setErr := c.cache.Set(ctx, roomID, loaded, c.ttl); setErr != nil {
			slog.Warn("roomsubcache set failed", "error", setErr, "roomId", roomID)
		}
		c.populateL1(roomID, loaded)
		return loaded, nil
	})
	if err != nil {
		return nil, fmt.Errorf("get members for room %s: %w", roomID, err)
	}
	return members.([]roomsubcache.Member), nil
}

// Invalidate drops the room from L1 and Valkey on membership change.
func (c *cachedMemberLookup) Invalidate(ctx context.Context, roomID string) {
	if c.l1 != nil {
		c.l1.Remove(roomID)
	}
	if err := c.cache.Invalidate(ctx, roomID); err != nil {
		slog.Warn("roomsubcache invalidate failed", "error", err, "roomId", roomID)
	}
}

func (c *cachedMemberLookup) populateL1(roomID string, members []roomsubcache.Member) {
	if c.l1 == nil {
		return
	}
	c.l1.Add(roomID, members)
}
