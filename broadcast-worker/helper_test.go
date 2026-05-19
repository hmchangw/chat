package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestDedupedAccounts(t *testing.T) {
	cases := []struct {
		name     string
		sender   string
		mentions []string
		want     []string
	}{
		{name: "no mentions", sender: "alice", want: []string{"alice"}},
		{name: "mentions exclude sender", sender: "alice", mentions: []string{"bob", "carol"}, want: []string{"alice", "bob", "carol"}},
		{name: "sender appears in mentions", sender: "alice", mentions: []string{"alice", "bob"}, want: []string{"alice", "bob"}},
		{name: "duplicate mentions", sender: "alice", mentions: []string{"bob", "bob", "carol"}, want: []string{"alice", "bob", "carol"}},
		{name: "preserves mention order", sender: "alice", mentions: []string{"carol", "bob"}, want: []string{"alice", "carol", "bob"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, dedupedAccounts(tc.sender, tc.mentions))
		})
	}
}

func TestMemoLookup(t *testing.T) {
	users := map[string]model.User{
		"alice": {ID: "u-alice", Account: "alice"},
		"bob":   {ID: "u-bob", Account: "bob"},
	}
	lookup := memoLookup(users)

	t.Run("returns known users in request order", func(t *testing.T) {
		got, err := lookup(context.Background(), []string{"bob", "alice"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "bob", got[0].Account)
		assert.Equal(t, "alice", got[1].Account)
	})

	t.Run("omits unknown accounts", func(t *testing.T) {
		got, err := lookup(context.Background(), []string{"alice", "ghost", "bob"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "alice", got[0].Account)
		assert.Equal(t, "bob", got[1].Account)
	})

	t.Run("empty input returns empty slice", func(t *testing.T) {
		got, err := lookup(context.Background(), nil)
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("empty map returns empty slice", func(t *testing.T) {
		got, err := memoLookup(nil)(context.Background(), []string{"alice"})
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
