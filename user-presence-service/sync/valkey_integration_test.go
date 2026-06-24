//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestValkeyIDMap_RefreshResolveFresh(t *testing.T) {
	client := testutil.StartValkeyCluster(t)
	ctx := context.Background()
	idm := newValkeyIDMap(client)

	fresh, err := idm.Fresh(ctx)
	require.NoError(t, err)
	assert.False(t, fresh)

	require.NoError(t, idm.Refresh(ctx, map[string]string{"alice": "ida", "bob": "idb"}, time.Hour))

	fresh, err = idm.Fresh(ctx)
	require.NoError(t, err)
	assert.True(t, fresh)

	got, err := idm.Resolve(ctx, []string{"alice", "carol"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"alice": "ida"}, got)
}

func TestValkeyInCallIndex_AddMembersRemove(t *testing.T) {
	client := testutil.StartValkeyCluster(t)
	ctx := context.Background()
	idx := newValkeyInCallIndex(client)

	require.NoError(t, idx.Add(ctx, "alice"))
	require.NoError(t, idx.Add(ctx, "bob"))
	m, err := idx.Members(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"alice", "bob"}, m)

	require.NoError(t, idx.Remove(ctx, "alice"))
	m, err = idx.Members(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"bob"}, m)
}
