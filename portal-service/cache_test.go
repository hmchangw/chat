package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

var (
	aliceEmployee = employee{
		Account:    "alice",
		EmployeeID: "E001",
		SiteID:     "site-a",
		NATSURL:    "wss://nats-3.site-a.example.com",
	}
	bobEmployee = employee{
		Account:    "bob",
		EmployeeID: "E002",
		SiteID:     "site-b",
		NATSURL:    "wss://nats.site-b.example.com",
	}
)

func TestDirectoryCache_EmptyUntilLoaded(t *testing.T) {
	cache := newDirectoryCache()

	assert.False(t, cache.Ready())
	_, ok := cache.Get("alice")
	assert.False(t, ok)
}

func TestDirectoryCache_Load(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{aliceEmployee, bobEmployee}, nil)

	cache := newDirectoryCache()
	require.NoError(t, cache.Load(context.Background(), store))

	assert.True(t, cache.Ready())
	got, ok := cache.Get("alice")
	require.True(t, ok)
	assert.Equal(t, aliceEmployee, got)
	got, ok = cache.Get("bob")
	require.True(t, ok)
	assert.Equal(t, bobEmployee, got)
	_, ok = cache.Get("mallory")
	assert.False(t, ok)
}

func TestDirectoryCache_LoadError(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().ListEmployees(gomock.Any()).Return(nil, errors.New("mongo down"))

	cache := newDirectoryCache()
	require.Error(t, cache.Load(context.Background(), store))
	assert.False(t, cache.Ready())
}

func TestDirectoryCache_LoadErrorKeepsPreviousEntries(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{aliceEmployee}, nil),
		store.EXPECT().ListEmployees(gomock.Any()).Return(nil, errors.New("mongo down")),
	)

	cache := newDirectoryCache()
	require.NoError(t, cache.Load(context.Background(), store))
	require.Error(t, cache.Load(context.Background(), store))

	assert.True(t, cache.Ready(), "a failed refresh must keep serving the previous data")
	_, ok := cache.Get("alice")
	assert.True(t, ok)
}

func TestDirectoryCache_EmptyLoadIsNotReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{}, nil)

	cache := newDirectoryCache()
	require.NoError(t, cache.Load(context.Background(), store))

	assert.False(t, cache.Ready(), "an empty directory must not report ready")
}

func TestDirectoryCache_EmptyRefreshAfterReadyKeepsPrevious(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	gomock.InOrder(
		store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{aliceEmployee}, nil),
		// The daily HR cron rewrites hr_employee; a refresh racing it can see zero rows.
		store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{}, nil),
	)

	cache := newDirectoryCache()
	require.NoError(t, cache.Load(context.Background(), store))
	require.Error(t, cache.Load(context.Background(), store),
		"an empty snapshot after a successful load is a failed refresh")

	assert.True(t, cache.Ready(), "the previous snapshot must keep serving")
	_, ok := cache.Get("alice")
	assert.True(t, ok)
}

func TestDirectoryCache_DuplicateAccountKeepsPrevious(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	dupAlice := aliceEmployee
	dupAlice.SiteID = "site-b"
	gomock.InOrder(
		store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{aliceEmployee}, nil),
		store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{bobEmployee, dupAlice, aliceEmployee}, nil),
	)

	cache := newDirectoryCache()
	require.NoError(t, cache.Load(context.Background(), store))
	require.Error(t, cache.Load(context.Background(), store),
		"a snapshot with duplicate accounts must be rejected, not published last-write-wins")

	got, ok := cache.Get("alice")
	require.True(t, ok)
	assert.Equal(t, aliceEmployee, got, "the previous snapshot must keep serving")
	_, ok = cache.Get("bob")
	assert.False(t, ok, "no row of a rejected snapshot may be published")
}

func TestDirectoryCache_DuplicateAccountAtStartupNotReady(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	store.EXPECT().ListEmployees(gomock.Any()).Return([]employee{aliceEmployee, aliceEmployee}, nil)

	cache := newDirectoryCache()
	require.Error(t, cache.Load(context.Background(), store))

	assert.False(t, cache.Ready())
	_, ok := cache.Get("alice")
	assert.False(t, ok)
}

func TestDirectoryCache_RefreshLoop(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockDirectoryStore(ctrl)
	loads := make(chan struct{}, 2)
	gomock.InOrder(
		// First attempt fails — the loop must retry on the short interval.
		store.EXPECT().ListEmployees(gomock.Any()).DoAndReturn(func(context.Context) ([]employee, error) {
			loads <- struct{}{}
			return nil, errors.New("mongo down")
		}),
		store.EXPECT().ListEmployees(gomock.Any()).DoAndReturn(func(context.Context) ([]employee, error) {
			loads <- struct{}{}
			return []employee{aliceEmployee}, nil
		}),
	)

	cache := newDirectoryCache()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.RefreshLoop(ctx, store, time.Hour, time.Millisecond)
	}()

	<-loads
	<-loads
	require.Eventually(t, cache.Ready, time.Second, time.Millisecond)
	_, ok := cache.Get("alice")
	assert.True(t, ok)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RefreshLoop did not stop on context cancel")
	}
}
