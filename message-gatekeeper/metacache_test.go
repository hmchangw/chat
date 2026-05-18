package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func TestCachedMetaStore_GetRoomMetaCaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := roommetacache.Meta{ID: "r1", UserCount: 5}
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		got, err := cached.GetRoomMeta(context.Background(), "r1")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestCachedMetaStore_GetSubscriptionPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := &model.Subscription{ID: "s1", RoomID: "r1"}
	inner.EXPECT().GetSubscription(gomock.Any(), "alice", "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	got, err := cached.GetSubscription(context.Background(), "alice", "r1")
	require.NoError(t, err)
	assert.Same(t, want, got)
}
