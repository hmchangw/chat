package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestDirMap_LookupOnFreshMapMisses(t *testing.T) {
	d := newDirMap()
	_, ok := d.Lookup("alice")
	assert.False(t, ok)
	assert.Equal(t, 0, d.Len())
}

func TestDirMap_ReloadPopulatesAndLookupHits(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", EmployeeID: "E001", SiteID: "site-a"},
		{Account: "bob", EmployeeID: "E002", SiteID: "site-b"},
	}, nil)

	d := newDirMap()
	n, err := d.Reload(context.Background(), store)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Equal(t, 2, d.Len())

	rec, ok := d.Lookup("alice")
	require.True(t, ok)
	assert.Equal(t, "site-a", rec.SiteID)
	assert.Equal(t, "E001", rec.EmployeeID)

	_, ok = d.Lookup("charlie")
	assert.False(t, ok)
}

func TestDirMap_ReloadFailureKeepsOldMap(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
		}, nil),
		store.EXPECT().LoadAll(gomock.Any()).Return(nil, fmt.Errorf("mongo down")),
	)

	d := newDirMap()
	_, err := d.Reload(context.Background(), store)
	require.NoError(t, err)

	_, err = d.Reload(context.Background(), store)
	require.Error(t, err)

	// Old map still serves.
	rec, ok := d.Lookup("alice")
	require.True(t, ok)
	assert.Equal(t, "site-a", rec.SiteID)
}

func TestDirMap_ReloadReplacesRemovedAccounts(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
			{Account: "bob", SiteID: "site-b"},
		}, nil),
		store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
			{Account: "alice", SiteID: "site-a"},
		}, nil),
	)

	d := newDirMap()
	_, err := d.Reload(context.Background(), store)
	require.NoError(t, err)
	_, err = d.Reload(context.Background(), store)
	require.NoError(t, err)

	_, ok := d.Lookup("bob") // removed by the second sync
	assert.False(t, ok)
	assert.Equal(t, 1, d.Len())
}

// Concurrent lookups during a swap must be race-free (validated by -race,
// which `make test` always enables).
func TestDirMap_ConcurrentLookupAndReload(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().LoadAll(gomock.Any()).Return([]directoryRecord{
		{Account: "alice", SiteID: "site-a"},
	}, nil).AnyTimes()

	d := newDirMap()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = d.Reload(context.Background(), store) }()
		go func() { defer wg.Done(); _, _ = d.Lookup("alice") }()
	}
	wg.Wait()
}
