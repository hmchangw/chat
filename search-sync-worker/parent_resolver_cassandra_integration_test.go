//go:build integration

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
)

// setupParentResolverCassandra provisions a keyspace-scoped session with a
// minimal messages_by_id table (only the columns the resolver reads).
func setupParentResolverCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	keyspace, adminSession, host := testutil.CassandraKeyspace(t, "search_sync_parent")
	require.NoError(t, adminSession.Query(fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s.messages_by_id (
			message_id TEXT,
			created_at TIMESTAMP,
			PRIMARY KEY (message_id)
		)`, keyspace)).Exec())

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	cluster.DisableInitialHostLookup = true
	cluster.Keyspace = keyspace
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(session.Close)
	return session
}

func TestCassParentResolver_GetMessageCreatedAt_Integration(t *testing.T) {
	ctx := context.Background()
	session := setupParentResolverCassandra(t)
	resolver := newCassParentResolver(session)

	createdAt := time.Date(2026, 3, 10, 8, 15, 0, 0, time.UTC)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, created_at) VALUES (?, ?)`,
		"parent-1", createdAt,
	).WithContext(ctx).Exec())

	t.Run("found returns the authoritative createdAt", func(t *testing.T) {
		got, found, err := resolver.GetMessageCreatedAt(ctx, "parent-1")
		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, got.Equal(createdAt), "got %v want %v", got, createdAt)
	})

	t.Run("missing message returns found=false, no error", func(t *testing.T) {
		_, found, err := resolver.GetMessageCreatedAt(ctx, "does-not-exist")
		require.NoError(t, err)
		assert.False(t, found)
	})
}
