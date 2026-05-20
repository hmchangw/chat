package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/cachestats"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// valkeyCache keys are namespaced under `searchservice:restrictedrooms:`
// so the cache can coexist with other services on the same Valkey.
type valkeyCache struct {
	client valkeyutil.Client
	rec    *cachestats.Recorder
}

func newValkeyCache(client valkeyutil.Client, rec *cachestats.Recorder) *valkeyCache {
	return &valkeyCache{client: client, rec: rec}
}

func restrictedKey(account string) string {
	return fmt.Sprintf("searchservice:restrictedrooms:%s", account)
}

// GetRestricted returns the cached restricted-rooms map for `account`.
// hit=false covers both "key absent" and "transport failure" — the handler
// treats both as cache misses and falls through to ES. Transport-level
// errors are returned via `err` so the handler can log them for visibility;
// hit=true implies err==nil.
func (c *valkeyCache) GetRestricted(ctx context.Context, account string) (map[string]int64, bool, error) {
	var rooms map[string]int64
	err := valkeyutil.GetJSON(ctx, c.client, restrictedKey(account), &rooms)
	if errors.Is(err, valkeyutil.ErrCacheMiss) {
		c.rec.Miss()
		return nil, false, nil
	}
	if err != nil {
		// Transport errors are logged at the call site; we deliberately
		// do not move either counter so hit-rate is not skewed during
		// Valkey outages.
		return nil, false, fmt.Errorf("cache get restricted: %w", err)
	}
	if rooms == nil {
		rooms = map[string]int64{}
	}
	c.rec.Hit()
	return rooms, true, nil
}

func (c *valkeyCache) SetRestricted(ctx context.Context, account string, rooms map[string]int64, ttl time.Duration) error {
	if rooms == nil {
		rooms = map[string]int64{}
	}
	if err := valkeyutil.SetJSONWithTTL(ctx, c.client, restrictedKey(account), rooms, ttl); err != nil {
		return fmt.Errorf("cache set restricted: %w", err)
	}
	return nil
}
