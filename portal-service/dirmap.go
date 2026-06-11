package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// dirMap is the bulk-loaded account directory, atomically swapped on Reload.
// A lookup miss is terminal (account_not_ready) until the next reload.
type dirMap struct {
	mu   sync.Mutex // serializes Reload; Lookup stays lock-free
	recs atomic.Pointer[map[string]directoryRecord]
}

func newDirMap() *dirMap {
	d := &dirMap{}
	empty := map[string]directoryRecord{}
	d.recs.Store(&empty)
	return d
}

func (d *dirMap) Lookup(account string) (directoryRecord, bool) {
	m := *d.recs.Load()
	rec, ok := m[account]
	return rec, ok
}

func (d *dirMap) Len() int {
	return len(*d.recs.Load())
}

// Reload bulk-loads the directory and atomically swaps the map. On failure
// the previous map keeps serving.
func (d *dirMap) Reload(ctx context.Context, store DirectoryStore) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	recs, err := store.LoadAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("load directory: %w", err)
	}
	m := make(map[string]directoryRecord, len(recs))
	for _, r := range recs {
		m[r.Account] = r
	}
	d.recs.Store(&m)
	return len(m), nil
}
