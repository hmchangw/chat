package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func TestCachedMetaStore_GetRoomMeta_CachesResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	want := roommetacache.Meta{ID: "r1", Type: model.RoomTypeChannel, Name: "r1", SiteID: "site-a", UserCount: 3}
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(want, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	for i := 0; i < 5; i++ {
		got, err := cached.GetRoomMeta(context.Background(), "r1")
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestCachedMetaStore_OtherMethodsPassThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	inner.EXPECT().UpdateRoomLastMessage(gomock.Any(), "r1", "m1", gomock.Any(), false).Return(nil).Times(1)
	inner.EXPECT().ListSubscriptions(gomock.Any(), "r1").Return(nil, nil).Times(1)
	inner.EXPECT().SetSubscriptionMentions(gomock.Any(), "r1", []string{"alice"}).Return(nil).Times(1)
	inner.EXPECT().GetRoom(gomock.Any(), "r1").Return(&model.Room{ID: "r1"}, nil).Times(1)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_ = cached.UpdateRoomLastMessage(context.Background(), "r1", "m1", time.Now(), false)
	_, _ = cached.ListSubscriptions(context.Background(), "r1")
	_ = cached.SetSubscriptionMentions(context.Background(), "r1", []string{"alice"})
	_, _ = cached.GetRoom(context.Background(), "r1")
}

func TestCachedMetaStore_LoaderErrorReturned(t *testing.T) {
	ctrl := gomock.NewController(t)
	inner := NewMockStore(ctrl)

	wantErr := errors.New("boom")
	inner.EXPECT().GetRoomMeta(gomock.Any(), "r1").Return(roommetacache.Meta{}, wantErr).Times(2)

	cached, err := newCachedMetaStore(inner, 10, time.Minute)
	require.NoError(t, err)

	_, err = cached.GetRoomMeta(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
	_, err = cached.GetRoomMeta(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
}
