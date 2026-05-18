package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func TestCachedSubStore_HitMiss(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{
		User:  model.SubscriptionUser{Account: "alice"},
		Roles: []model.Role{model.RoleMember},
	}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	got, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got.User.Account)
	assert.Equal(t, []model.Role{model.RoleMember}, got.Roles)

	// Second call: cache hit, inner not called again.
	got2, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Equal(t, "alice", got2.User.Account)
	assert.Equal(t, []model.Role{model.RoleMember}, got2.Roles)
}

func TestCachedSubStore_NotSubscribedNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, errNotSubscribed).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, errNotSubscribed)
	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, errNotSubscribed)
}

func TestCachedSubStore_TransientErrorNotCached(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	boom := errors.New("transient")
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(nil, boom).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, boom)
	_, err = cached.GetSubscription(context.Background(), "alice", "r1")
	assert.ErrorIs(t, err, boom)
}

func TestCachedSubStore_TTLExpires(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{User: model.SubscriptionUser{Account: "alice"}, Roles: []model.Role{model.RoleMember}}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(2)

	cached, err := newCachedSubStore(inner, 10, 50*time.Millisecond)
	require.NoError(t, err)

	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
	time.Sleep(75 * time.Millisecond)
	_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
}

func TestCachedSubStore_SingleflightDedupsConcurrentMisses(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	var calls atomic.Int32
	gate := make(chan struct{})
	want := &model.Subscription{User: model.SubscriptionUser{Account: "alice"}, Roles: []model.Role{model.RoleMember}}
	inner.EXPECT().
		GetSubscription(gomock.Any(), "alice", "r1").
		DoAndReturn(func(_ context.Context, _, _ string) (*model.Subscription, error) {
			calls.Add(1)
			<-gate
			return want, nil
		}).
		Times(1)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	const N = 20
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = cached.GetSubscription(context.Background(), "alice", "r1")
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())
}

func TestCachedSubStore_GetRoomMetaPassesThrough(t *testing.T) {
	// GetRoomMeta is not cached by the sub cache; it passes through.
	// (Caching of GetRoomMeta is the metacache wrapper's job.)
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(roommetacache.Meta{ID: "r1", UserCount: 1}, nil).Times(2)

	cached, err := newCachedSubStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, _ = cached.GetRoomMeta(context.Background(), "r1")
	_, _ = cached.GetRoomMeta(context.Background(), "r1")
}
