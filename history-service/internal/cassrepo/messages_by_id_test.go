package cassrepo

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/history-service/internal/models"
)

func TestFetchByIDs_AllFound(t *testing.T) {
	ids := []string{"a", "b", "c"}
	fetch := func(_ context.Context, id string) (*models.Message, error) {
		return &models.Message{MessageID: id}, nil
	}

	got, err := fetchByIDs(context.Background(), ids, 4, fetch)
	require.NoError(t, err)

	gotIDs := make([]string, len(got))
	for i, m := range got {
		gotIDs[i] = m.MessageID
	}
	assert.Equal(t, ids, gotIDs, "results preserved in input order")
}

func TestFetchByIDs_OmitsMissing(t *testing.T) {
	ids := []string{"a", "missing", "c"}
	fetch := func(_ context.Context, id string) (*models.Message, error) {
		if id == "missing" {
			return nil, nil
		}
		return &models.Message{MessageID: id}, nil
	}

	got, err := fetchByIDs(context.Background(), ids, 4, fetch)
	require.NoError(t, err)

	gotIDs := make([]string, len(got))
	for i, m := range got {
		gotIDs[i] = m.MessageID
	}
	assert.Equal(t, []string{"a", "c"}, gotIDs)
}

func TestFetchByIDs_Empty(t *testing.T) {
	var called atomic.Int32
	fetch := func(_ context.Context, _ string) (*models.Message, error) {
		called.Add(1)
		return nil, nil
	}

	got, err := fetchByIDs(context.Background(), nil, 4, fetch)
	require.NoError(t, err)
	assert.Equal(t, []models.Message{}, got)
	assert.Equal(t, int32(0), called.Load(), "fetch must not be called for empty input")
}

func TestFetchByIDs_Error(t *testing.T) {
	sentinel := errors.New("boom")
	fetch := func(_ context.Context, id string) (*models.Message, error) {
		if id == "b" {
			return nil, sentinel
		}
		return &models.Message{MessageID: id}, nil
	}

	got, err := fetchByIDs(context.Background(), []string{"a", "b", "c"}, 4, fetch)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Nil(t, got)
}

func TestFetchByIDs_RespectsConcurrencyLimit(t *testing.T) {
	const limit = 3
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}

	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	fetch := func(_ context.Context, id string) (*models.Message, error) {
		cur := inFlight.Add(1)
		for {
			prev := maxSeen.Load()
			if cur <= prev || maxSeen.CompareAndSwap(prev, cur) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond) // widen the overlap window
		inFlight.Add(-1)
		return &models.Message{MessageID: id}, nil
	}

	got, err := fetchByIDs(context.Background(), ids, limit, fetch)
	require.NoError(t, err)
	assert.Len(t, got, len(ids))
	assert.LessOrEqual(t, int(maxSeen.Load()), limit, "concurrent fetches must not exceed the limit")
	assert.Greater(t, int(maxSeen.Load()), 1, "fetches should actually run concurrently")
}

// guards that index-keyed result writes are race-free; run with -race.
func TestFetchByIDs_ConcurrentRaceSafe(t *testing.T) {
	ids := make([]string, 50)
	for i := range ids {
		ids[i] = string(rune('A' + i%26))
	}
	var wg sync.WaitGroup
	wg.Add(4)
	for i := 0; i < 4; i++ {
		go func() {
			defer wg.Done()
			_, err := fetchByIDs(context.Background(), ids, 8, func(_ context.Context, id string) (*models.Message, error) {
				return &models.Message{MessageID: id}, nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
}
