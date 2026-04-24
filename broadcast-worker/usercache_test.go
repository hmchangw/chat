package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/userstore"
)

// fakeUserStore is a minimal userstore.UserStore that records calls and
// returns preconfigured users. Tests assert call counts to confirm cache
// hits do not reach the inner store.
type fakeUserStore struct {
	mu        sync.Mutex
	calls     [][]string
	byAccount map[string]model.User
	err       error
}

func newFakeUserStore(users ...model.User) *fakeUserStore {
	f := &fakeUserStore{byAccount: make(map[string]model.User, len(users))}
	for i := range users {
		f.byAccount[users[i].Account] = users[i]
	}
	return f
}

func (f *fakeUserStore) FindUserByID(_ context.Context, _ string) (*model.User, error) {
	return nil, errors.New("unused in these tests")
}

func (f *fakeUserStore) FindUsersByAccounts(_ context.Context, accounts []string) ([]model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), accounts...))
	if f.err != nil {
		return nil, f.err
	}
	out := make([]model.User, 0, len(accounts))
	for _, a := range accounts {
		if u, ok := f.byAccount[a]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeUserStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeUserStore) lastCall() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// sentinel to prove the returned type implements the userstore.UserStore interface.
var _ userstore.UserStore = (*CachedUserStore)(nil)

func TestNewCachedUserStore_ConstructsEmpty(t *testing.T) {
	inner := newFakeUserStore()
	c := NewCachedUserStore(inner, 10, time.Minute)
	require.NotNil(t, c)
	// A fresh cache doesn't call inner until asked.
	assert.Equal(t, 0, inner.callCount())
	assert.Nil(t, inner.lastCall())
}

func TestCachedUserStore_MissCallsInner(t *testing.T) {
	alice := model.User{ID: "u1", Account: "alice", EngName: "Alice"}
	inner := newFakeUserStore(alice)
	c := NewCachedUserStore(inner, 10, time.Minute)

	users, err := c.FindUsersByAccounts(context.Background(), []string{"alice"})
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, alice, users[0])
	assert.Equal(t, 1, inner.callCount(), "miss should call inner")
	assert.Equal(t, []string{"alice"}, inner.lastCall())
}

func TestCachedUserStore_HitServedFromCache(t *testing.T) {
	alice := model.User{ID: "u1", Account: "alice", EngName: "Alice"}
	inner := newFakeUserStore(alice)
	c := NewCachedUserStore(inner, 10, time.Minute)

	_, _ = c.FindUsersByAccounts(context.Background(), []string{"alice"}) // prime
	users, err := c.FindUsersByAccounts(context.Background(), []string{"alice"})
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, alice, users[0])
	assert.Equal(t, 1, inner.callCount(), "hit should not call inner")
}

func TestCachedUserStore_PartialHitCallsInnerWithOnlyMissing(t *testing.T) {
	alice := model.User{ID: "u1", Account: "alice"}
	bob := model.User{ID: "u2", Account: "bob"}
	inner := newFakeUserStore(alice, bob)
	c := NewCachedUserStore(inner, 10, time.Minute)

	_, _ = c.FindUsersByAccounts(context.Background(), []string{"alice"}) // prime alice only

	users, err := c.FindUsersByAccounts(context.Background(), []string{"alice", "bob"})
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, 2, inner.callCount(), "partial hit still calls inner for misses")
	assert.Equal(t, []string{"bob"}, inner.lastCall(), "inner called only with missing accounts")
}

func TestCachedUserStore_EmptyInputReturnsNil(t *testing.T) {
	inner := newFakeUserStore()
	c := NewCachedUserStore(inner, 10, time.Minute)

	users, err := c.FindUsersByAccounts(context.Background(), nil)
	require.NoError(t, err)
	assert.Nil(t, users)
	assert.Equal(t, 0, inner.callCount())
}

func TestCachedUserStore_MissingUserNotCached(t *testing.T) {
	inner := newFakeUserStore() // no users registered
	c := NewCachedUserStore(inner, 10, time.Minute)

	// First call: inner returns no users for "ghost".
	users, err := c.FindUsersByAccounts(context.Background(), []string{"ghost"})
	require.NoError(t, err)
	assert.Empty(t, users)

	// Add ghost later to simulate the user being created.
	inner.byAccount["ghost"] = model.User{ID: "u-ghost", Account: "ghost"}

	// Second call: the negative result must NOT be cached — inner must be called again.
	users2, err := c.FindUsersByAccounts(context.Background(), []string{"ghost"})
	require.NoError(t, err)
	require.Len(t, users2, 1)
	assert.Equal(t, 2, inner.callCount(), "missing accounts must not be cached as negatives")
}

func TestCachedUserStore_InnerErrorPropagated(t *testing.T) {
	inner := newFakeUserStore()
	inner.err = errors.New("boom")
	c := NewCachedUserStore(inner, 10, time.Minute)

	_, err := c.FindUsersByAccounts(context.Background(), []string{"alice"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
