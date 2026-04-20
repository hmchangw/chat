package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(i int) *int { return &i }

func TestCasIncrement(t *testing.T) {
	tests := []struct {
		name       string
		maxRetries int
		initial    *int
		updates    []func(newVal int, expected *int) (bool, *int, error)
		wantErr    bool
		wantCalls  int
	}{
		{
			name:       "first attempt succeeds — nil initial increments to 1",
			maxRetries: 5,
			initial:    nil,
			updates: []func(int, *int) (bool, *int, error){
				func(newVal int, expected *int) (bool, *int, error) {
					assert.Equal(t, 1, newVal)
					assert.Nil(t, expected)
					return true, nil, nil
				},
			},
			wantCalls: 1,
		},
		{
			name:       "first attempt succeeds — non-nil initial increments by 1",
			maxRetries: 5,
			initial:    intPtr(3),
			updates: []func(int, *int) (bool, *int, error){
				func(newVal int, expected *int) (bool, *int, error) {
					assert.Equal(t, 4, newVal)
					assert.Equal(t, intPtr(3), expected)
					return true, nil, nil
				},
			},
			wantCalls: 1,
		},
		{
			name:       "one conflict then success — retries with current value",
			maxRetries: 5,
			initial:    intPtr(3),
			updates: []func(int, *int) (bool, *int, error){
				// concurrent writer bumped it to 5
				func(newVal int, expected *int) (bool, *int, error) {
					assert.Equal(t, 4, newVal)
					return false, intPtr(5), nil
				},
				func(newVal int, expected *int) (bool, *int, error) {
					assert.Equal(t, 6, newVal)
					assert.Equal(t, intPtr(5), expected)
					return true, nil, nil
				},
			},
			wantCalls: 2,
		},
		{
			name:       "retries exhausted — returns error after maxRetries attempts",
			maxRetries: 3,
			initial:    nil,
			updates: []func(int, *int) (bool, *int, error){
				func(int, *int) (bool, *int, error) { return false, intPtr(1), nil },
				func(int, *int) (bool, *int, error) { return false, intPtr(2), nil },
				func(int, *int) (bool, *int, error) { return false, intPtr(3), nil },
			},
			wantErr:   true,
			wantCalls: 3,
		},
		{
			name:       "update error — returned immediately without further retries",
			maxRetries: 5,
			initial:    nil,
			updates: []func(int, *int) (bool, *int, error){
				func(int, *int) (bool, *int, error) {
					return false, nil, errors.New("cassandra: write timeout")
				},
			},
			wantErr:   true,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := 0
			update := func(newVal int, expected *int) (bool, *int, error) {
				require.Less(t, idx, len(tt.updates), "unexpected extra call to update")
				fn := tt.updates[idx]
				idx++
				return fn(newVal, expected)
			}

			err := casIncrement(tt.maxRetries, tt.initial, update)

			assert.Equal(t, tt.wantCalls, idx, "number of update calls")
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
