package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// cacheLoadTimeout bounds a single full scan of the hr_employee collection.
const cacheLoadTimeout = time.Minute

// directoryCache is the in-memory account → employee directory. Entries have
// no TTL — the whole map is swapped wholesale by Load (at startup and on the
// periodic refresh; the backing hr_employee collection is rewritten by a
// daily HR cron).
type directoryCache struct {
	mu      sync.RWMutex
	entries map[string]employee
	loaded  bool
}

func newDirectoryCache() *directoryCache {
	return &directoryCache{}
}

// Get returns the directory entry for account.
func (c *directoryCache) Get(account string) (employee, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[account]
	return e, ok
}

// Ready reports whether the cache holds directory data — the /readyz signal.
// An empty directory is not ready: the portal cannot resolve any account.
func (c *directoryCache) Ready() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded && len(c.entries) > 0
}

// Load reads the full employee directory and swaps it in. A corrupt snapshot
// (duplicate accounts, or empty after a prior successful load — the HR cron
// may be mid-rewrite) is rejected; on any error the previous entries keep
// serving and the refresh loop retries.
func (c *directoryCache) Load(ctx context.Context, store DirectoryStore) error {
	ctx, cancel := context.WithTimeout(ctx, cacheLoadTimeout)
	defer cancel()
	emps, err := store.ListEmployees(ctx)
	if err != nil {
		return fmt.Errorf("list employee directory: %w", err)
	}
	if c.Ready() && len(emps) == 0 {
		return fmt.Errorf("refresh employee directory: empty snapshot after a successful load")
	}
	if err := c.replace(emps); err != nil {
		return fmt.Errorf("refresh employee directory: %w", err)
	}
	slog.Info("directory cache loaded", "entries", len(emps))
	return nil
}

// replace publishes a new snapshot, refusing one with duplicate accounts —
// last-write-wins would route the account by Mongo cursor order.
func (c *directoryCache) replace(emps []employee) error {
	entries := make(map[string]employee, len(emps))
	for _, e := range emps {
		if _, dup := entries[e.Account]; dup {
			return fmt.Errorf("duplicate account %q", e.Account)
		}
		entries[e.Account] = e
	}
	c.mu.Lock()
	c.entries = entries
	c.loaded = true
	c.mu.Unlock()
	return nil
}

// RefreshLoop populates the cache immediately, then refreshes it every
// refreshEvery. A failed attempt is retried after retryEvery instead, so a
// transient Mongo failure does not leave the portal stale until the next
// scheduled refresh. Returns when ctx is cancelled.
func (c *directoryCache) RefreshLoop(ctx context.Context, store DirectoryStore, refreshEvery, retryEvery time.Duration) {
	for {
		wait := refreshEvery
		if err := c.Load(ctx, store); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("refresh directory cache", "error", err)
			wait = retryEvery
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
