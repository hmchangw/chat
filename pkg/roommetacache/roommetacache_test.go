package roommetacache_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roommetacache"
)

func makeMeta(id string) roommetacache.Meta {
	return roommetacache.Meta{
		ID:        id,
		Type:      model.RoomTypeChannel,
		Name:      "room " + id,
		SiteID:    "site-a",
		UserCount: 7,
	}
}

func TestCache_GetMissThenHit(t *testing.T) {
	var loaderCalls atomic.Int32
	loader := func(_ context.Context, roomID string) (roommetacache.Meta, error) {
		loaderCalls.Add(1)
		return makeMeta(roomID), nil
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	// First call: miss, loader runs.
	got, err := c.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, makeMeta("r1"), got)
	assert.Equal(t, int32(1), loaderCalls.Load(), "loader should run on miss")

	// Second call: hit, loader does NOT run again.
	got2, err := c.Get(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, makeMeta("r1"), got2)
	assert.Equal(t, int32(1), loaderCalls.Load(), "loader should not run on hit")

	stats := c.Stats()
	assert.Equal(t, uint64(1), stats.Hits)
	assert.Equal(t, uint64(1), stats.Misses)
	assert.Equal(t, uint64(0), stats.LoadErrors)
}

func TestCache_LoaderErrorNotCached(t *testing.T) {
	var calls atomic.Int32
	wantErr := errors.New("boom")
	loader := func(_ context.Context, _ string) (roommetacache.Meta, error) {
		calls.Add(1)
		return roommetacache.Meta{}, wantErr
	}
	c, err := roommetacache.New(10, time.Minute, loader)
	require.NoError(t, err)

	_, err = c.Get(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)
	_, err = c.Get(context.Background(), "r1")
	assert.ErrorIs(t, err, wantErr)

	assert.Equal(t, int32(2), calls.Load(), "errors should not be cached; loader must run again")

	stats := c.Stats()
	assert.Equal(t, uint64(2), stats.Misses)
	assert.Equal(t, uint64(2), stats.LoadErrors)
}
