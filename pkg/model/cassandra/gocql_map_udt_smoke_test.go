//go:build integration

package cassandra

import (
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
)

// TestGocqlMapUDTRoundTrip is the v3-design gate (spec §1 + §8.0). It verifies
// that gocql's reflection-based UDT marshaller can round-trip
// map[ReactionKey]ReactorInfo through a real Cassandra container — read AND
// write — without custom MarshalUDT/UnmarshalUDT methods on the two structs.
//
// If this test fails in CI, the spec (docs/specs/message-reactions.md §1)
// requires adding MarshalUDT/UnmarshalUDT methods to both ReactionKey and
// ReactorInfo before any further v3 production-code work.
func TestGocqlMapUDTRoundTrip(t *testing.T) {
	keyspace, adminSession, host := testutil.CassandraKeyspace(t, "cassandra_map_udt_smoke")

	// Create the two UDTs and a minimal smoke table that mirrors the v3 shape
	// (MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>) without touching any
	// production message tables.
	stmts := []string{
		fmt.Sprintf(`CREATE TYPE IF NOT EXISTS %s.reaction_key (
			emoji        TEXT,
			user_account TEXT
		)`, keyspace),
		fmt.Sprintf(`CREATE TYPE IF NOT EXISTS %s.reactor_info (
			user_id     TEXT,
			eng_name    TEXT,
			chn_name    TEXT,
			account     TEXT,
			reacted_at  TIMESTAMP
		)`, keyspace),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.reaction_smoke (
			message_id TEXT PRIMARY KEY,
			reactions  MAP<FROZEN<reaction_key>, FROZEN<reactor_info>>
		)`, keyspace),
	}
	for _, stmt := range stmts {
		require.NoError(t, adminSession.Query(stmt).Exec())
	}

	// Open a keyspace-pinned session so unqualified table/UDT names resolve.
	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	cluster.DisableInitialHostLookup = true
	cluster.Keyspace = keyspace
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(session.Close)

	// Cassandra TIMESTAMP precision is milliseconds — truncate so the byte
	// comparison after read-back is exact.
	now := time.Now().UTC().Truncate(time.Millisecond)
	want := map[ReactionKey]ReactorInfo{
		{Emoji: "👍", UserAccount: "alice"}: {
			UserID:    "u1",
			EngName:   "Alice",
			ChnName:   "爱丽丝",
			Account:   "alice",
			ReactedAt: now,
		},
		{Emoji: "❤️", UserAccount: "bob"}: {
			UserID:    "u2",
			EngName:   "Bob",
			ChnName:   "鲍勃",
			Account:   "bob",
			ReactedAt: now,
		},
	}

	require.NoError(t,
		session.Query(
			`INSERT INTO reaction_smoke (message_id, reactions) VALUES (?, ?)`,
			"msg-smoke", want,
		).Exec(),
	)

	got := map[ReactionKey]ReactorInfo{}
	iter := session.Query(
		`SELECT reactions FROM reaction_smoke WHERE message_id = ?`,
		"msg-smoke",
	).Iter()
	require.True(t, iter.Scan(&got), "expected one row from reaction_smoke")
	require.NoError(t, iter.Close())

	assert.Equal(t, want, got)
}
