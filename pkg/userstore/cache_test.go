package userstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

type fakeStore struct {
	users     map[string]*model.User
	callCount int
	err       error
}

func (f *fakeStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	f.callCount++
	if f.err != nil {
		return nil, f.err
	}
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, userstore.ErrUserNotFound
}

func (f *fakeStore) FindUsersByAccounts(context.Context, []string) ([]model.User, error) {
	return nil, nil
}

func TestNewCache_RejectsInvalidArgs(t *testing.T) {
	_, err := userstore.NewCache(nil, 100, time.Minute)
	require.Error(t, err)

	_, err = userstore.NewCache(&fakeStore{}, 0, time.Minute)
	require.Error(t, err)

	_, err = userstore.NewCache(&fakeStore{}, 100, 0)
	require.Error(t, err)
}

func TestCache_MissThenHit(t *testing.T) {
	store := &fakeStore{users: map[string]*model.User{
		"u1": {ID: "u1", Account: "alice", EngName: "Alice"},
	}}
	c, err := userstore.NewCache(store, 100, time.Minute)
	require.NoError(t, err)

	got, err := c.FindUserByID(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, "Alice", got.EngName)

	got2, err := c.FindUserByID(context.Background(), "u1")
	require.NoError(t, err)
	assert.Equal(t, "Alice", got2.EngName)
	assert.Equal(t, 1, store.callCount, "second FindUserByID must be served from cache")

	stats := c.Stats()
	assert.Equal(t, uint64(1), stats.Hits)
	assert.Equal(t, uint64(1), stats.Misses)
	assert.Equal(t, 1, stats.Size)
}

func TestCache_NotFoundReturnsUnwrappedErrUserNotFound(t *testing.T) {
	c, err := userstore.NewCache(&fakeStore{users: map[string]*model.User{}}, 100, time.Minute)
	require.NoError(t, err)

	_, err = c.FindUserByID(context.Background(), "ghost")
	require.Error(t, err)
	assert.True(t, errors.Is(err, userstore.ErrUserNotFound),
		"ErrUserNotFound must propagate so callers can branch on it without parsing strings")
}

func TestCache_GenericErrorWrappedWithContext(t *testing.T) {
	c, err := userstore.NewCache(&fakeStore{err: errors.New("mongo timeout")}, 100, time.Minute)
	require.NoError(t, err)

	_, err = c.FindUserByID(context.Background(), "u1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "find cached user")
}

func TestCache_LoaderErrorNotCached(t *testing.T) {
	store := &fakeStore{err: errors.New("transient")}
	c, err := userstore.NewCache(store, 100, time.Minute)
	require.NoError(t, err)

	_, err1 := c.FindUserByID(context.Background(), "u1")
	require.Error(t, err1)
	_, err2 := c.FindUserByID(context.Background(), "u1")
	require.Error(t, err2)
	assert.Equal(t, 2, store.callCount, "errors must not be cached")
}

func TestCache_TTLExpires(t *testing.T) {
	store := &fakeStore{users: map[string]*model.User{
		"u1": {ID: "u1", Account: "alice"},
	}}
	c, err := userstore.NewCache(store, 100, 30*time.Millisecond)
	require.NoError(t, err)

	_, _ = c.FindUserByID(context.Background(), "u1")
	time.Sleep(60 * time.Millisecond)
	_, _ = c.FindUserByID(context.Background(), "u1")
	assert.GreaterOrEqual(t, store.callCount, 2, "TTL-expired entry must reload")
}

func TestCache_Invalidate(t *testing.T) {
	store := &fakeStore{users: map[string]*model.User{
		"u1": {ID: "u1", Account: "alice"},
	}}
	c, err := userstore.NewCache(store, 100, time.Minute)
	require.NoError(t, err)

	_, _ = c.FindUserByID(context.Background(), "u1")
	require.Equal(t, 1, store.callCount)
	c.Invalidate("u1")
	_, _ = c.FindUserByID(context.Background(), "u1")
	assert.Equal(t, 2, store.callCount, "invalidated entry must reload")
}

func TestCache_FindUsersByAccountsPassThrough(t *testing.T) {
	store := &fakeStore{}
	c, err := userstore.NewCache(store, 100, time.Minute)
	require.NoError(t, err)

	_, err = c.FindUsersByAccounts(context.Background(), []string{"alice"})
	require.NoError(t, err)
	// Bulk lookups bypass the cache; the test just verifies the delegation compiles and runs.
}
