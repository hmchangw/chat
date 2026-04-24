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

func (f *fakeUserStore) lastCall() []string { //nolint:unused
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
}
