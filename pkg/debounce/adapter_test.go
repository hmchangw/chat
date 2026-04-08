package debounce

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSortedSetCommander simulates sorted-set behavior in memory.
// It stores member->score mappings and supports injectable per-method errors.
type fakeSortedSetCommander struct {
	store    map[string]float64
	zaddErr  error
	claimErr error
	removeErr error
}

func (f *fakeSortedSetCommander) zadd(_ context.Context, member string, score float64) error {
	if f.zaddErr != nil {
		return f.zaddErr
	}
	if f.store == nil {
		f.store = make(map[string]float64)
	}
	f.store[member] = score
	return nil
}

func (f *fakeSortedSetCommander) claimExpired(_ context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if f.store == nil {
		return nil, nil
	}

	// Collect members whose score <= now (expired).
	var expired []string
	for member, score := range f.store {
		if score <= now {
			expired = append(expired, member)
		}
	}

	// Sort for deterministic output.
	sort.Strings(expired)

	// Apply batch limit.
	if len(expired) > batchSize {
		expired = expired[:batchSize]
	}

	// Update claimed members' scores to processingDeadline.
	for _, member := range expired {
		f.store[member] = processingDeadline
	}

	return expired, nil
}

func (f *fakeSortedSetCommander) removeIfScore(_ context.Context, member string, expectedScore float64) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	if f.store == nil {
		return nil
	}
	score, ok := f.store[member]
	if ok && score == expectedScore {
		delete(f.store, member)
	}
	return nil
}

// newTestAdapter creates a valkeyAdapter backed by the given fake for unit tests.
func newTestAdapter(fake *fakeSortedSetCommander) *valkeyAdapter {
	return &valkeyAdapter{client: fake}
}

func TestValkeyAdapter_Trigger(t *testing.T) {
	deadline := time.UnixMilli(1700000000000)

	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		key         string
		deadline    time.Time
		wantErr     bool
		errContains string
	}{
		{
			name:     "happy path — stores member with deadline as score",
			fake:     &fakeSortedSetCommander{},
			key:      "room:123",
			deadline: deadline,
		},
		{
			name:        "zadd error — returns wrapped error",
			fake:        &fakeSortedSetCommander{zaddErr: errors.New("connection refused")},
			key:         "room:123",
			deadline:    deadline,
			wantErr:     true,
			errContains: "set debounce deadline",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := newTestAdapter(tt.fake)
			err := adapter.Trigger(context.Background(), tt.key, tt.deadline)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			// Verify the member was stored with the correct score (deadline in UnixMilli).
			score, ok := tt.fake.store[tt.key]
			require.True(t, ok, "member should exist in fake store")
			assert.Equal(t, float64(tt.deadline.UnixMilli()), score)
		})
	}
}

func TestValkeyAdapter_Claim(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	processingTimeout := 30 * time.Second
	processingDeadlineMs := float64(now.Add(processingTimeout).UnixMilli())

	tests := []struct {
		name              string
		fake              *fakeSortedSetCommander
		now               time.Time
		processingTimeout time.Duration
		batchSize         int
		wantEntries       []ClaimedEntry
		wantErr           bool
		errContains       string
	}{
		{
			name: "happy path — claims expired entries",
			fake: &fakeSortedSetCommander{
				store: map[string]float64{
					"key-a": float64(now.Add(-10 * time.Second).UnixMilli()), // expired
					"key-b": float64(now.Add(-5 * time.Second).UnixMilli()),  // expired
					"key-c": float64(now.Add(10 * time.Second).UnixMilli()),  // not expired
				},
			},
			now:               now,
			processingTimeout: processingTimeout,
			batchSize:         10,
			wantEntries: []ClaimedEntry{
				{Key: "key-a", ClaimedScore: processingDeadlineMs},
				{Key: "key-b", ClaimedScore: processingDeadlineMs},
			},
		},
		{
			name:              "no expired entries — returns empty slice",
			fake:              &fakeSortedSetCommander{store: map[string]float64{"key-a": float64(now.Add(10 * time.Second).UnixMilli())}},
			now:               now,
			processingTimeout: processingTimeout,
			batchSize:         10,
			wantEntries:       nil,
		},
		{
			name: "batch limit — returns at most batchSize entries",
			fake: &fakeSortedSetCommander{
				store: map[string]float64{
					"key-a": float64(now.Add(-10 * time.Second).UnixMilli()),
					"key-b": float64(now.Add(-5 * time.Second).UnixMilli()),
					"key-c": float64(now.Add(-1 * time.Second).UnixMilli()),
				},
			},
			now:               now,
			processingTimeout: processingTimeout,
			batchSize:         2,
			wantEntries: []ClaimedEntry{
				{Key: "key-a", ClaimedScore: processingDeadlineMs},
				{Key: "key-b", ClaimedScore: processingDeadlineMs},
			},
		},
		{
			name:              "claim error — returns wrapped error",
			fake:              &fakeSortedSetCommander{claimErr: errors.New("timeout")},
			now:               now,
			processingTimeout: processingTimeout,
			batchSize:         10,
			wantErr:           true,
			errContains:       "claim expired entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := newTestAdapter(tt.fake)
			entries, err := adapter.Claim(context.Background(), tt.now, tt.processingTimeout, tt.batchSize)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantEntries, entries)

			// Verify that claimed entries' scores in the store are updated to processingDeadline.
			for _, e := range entries {
				score, ok := tt.fake.store[e.Key]
				require.True(t, ok, "claimed member %s should still exist in store", e.Key)
				assert.Equal(t, processingDeadlineMs, score, "claimed member %s score should be updated to processingDeadline", e.Key)
			}
		})
	}
}

func TestValkeyAdapter_Remove(t *testing.T) {
	tests := []struct {
		name         string
		fake         *fakeSortedSetCommander
		key          string
		claimedScore float64
		wantRemoved  bool
		wantErr      bool
		errContains  string
	}{
		{
			name:         "happy path — removes when score matches",
			fake:         &fakeSortedSetCommander{store: map[string]float64{"key-a": 1700000000000}},
			key:          "key-a",
			claimedScore: 1700000000000,
			wantRemoved:  true,
		},
		{
			name:         "score mismatch — does not remove",
			fake:         &fakeSortedSetCommander{store: map[string]float64{"key-a": 9999999999999}},
			key:          "key-a",
			claimedScore: 1700000000000,
			wantRemoved:  false,
		},
		{
			name:         "remove error — returns wrapped error",
			fake:         &fakeSortedSetCommander{removeErr: errors.New("connection lost")},
			key:          "key-a",
			claimedScore: 1700000000000,
			wantErr:      true,
			errContains:  "conditional remove",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := newTestAdapter(tt.fake)
			err := adapter.Remove(context.Background(), tt.key, tt.claimedScore)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			_, exists := tt.fake.store[tt.key]
			if tt.wantRemoved {
				assert.False(t, exists, "member should be removed from store")
			} else {
				assert.True(t, exists, "member should still exist in store when score mismatches")
			}
		})
	}
}

func TestValkeyAdapter_Requeue(t *testing.T) {
	deadline := time.UnixMilli(1700000060000)

	tests := []struct {
		name        string
		fake        *fakeSortedSetCommander
		key         string
		deadline    time.Time
		wantErr     bool
		errContains string
	}{
		{
			name:     "happy path — sets new deadline",
			fake:     &fakeSortedSetCommander{store: map[string]float64{"key-a": 1700000000000}},
			key:      "key-a",
			deadline: deadline,
		},
		{
			name:        "zadd error — returns wrapped error",
			fake:        &fakeSortedSetCommander{zaddErr: errors.New("write error")},
			key:         "key-a",
			deadline:    deadline,
			wantErr:     true,
			errContains: "requeue debounce entry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := newTestAdapter(tt.fake)
			err := adapter.Requeue(context.Background(), tt.key, tt.deadline)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			// Verify the member was updated with the new deadline score.
			score, ok := tt.fake.store[tt.key]
			require.True(t, ok, "member should exist in fake store")
			assert.Equal(t, float64(tt.deadline.UnixMilli()), score)
		})
	}
}
