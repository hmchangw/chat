//go:build integration

package cassrepo

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/msgbucket"
)

func newReactionsRepo(t *testing.T) (*Repository, *gocql.Session) {
	t.Helper()
	session := setupCassandra(t)
	return NewRepository(session, msgbucket.New(24*time.Hour), 365, 50), session
}

func TestRepository_GetReactionsByMessageID_Found(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()
	const msgID = "msg-found"
	alice := cassandra.Participant{ID: "u1", EngName: "Alice", Account: "alice"}
	bob := cassandra.Participant{ID: "u2", EngName: "Bob", Account: "bob"}

	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		msgID, "👍", []cassandra.Participant{alice, bob},
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		msgID, "❤️", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []cassandra.Participant{alice, bob}, got["👍"])
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["❤️"])
}

func TestRepository_GetReactionsByMessageID_NotFound(t *testing.T) {
	repo, _ := newReactionsRepo(t)

	got, err := repo.GetReactionsByMessageID(context.Background(), "msg-does-not-exist")
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got, "must return empty map, not nil")
}

func TestRepository_GetReactionsByMessageIDs_Happy(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", EngName: "Alice", Account: "alice"}
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m1", "👍", []cassandra.Participant{alice},
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m2", "❤️", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m2", "m3"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m1"]["👍"])
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m2"]["❤️"])
	_, exists := got["m3"]
	assert.False(t, exists, "messages without reactions must be omitted from result map")
}

func TestRepository_GetReactionsByMessageIDs_EmptyAndNilInput(t *testing.T) {
	repo, _ := newReactionsRepo(t)
	ctx := context.Background()

	got, err := repo.GetReactionsByMessageIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)

	got, err = repo.GetReactionsByMessageIDs(ctx, []string{})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.NotNil(t, got)
}

func TestRepository_GetReactionsByMessageIDs_DeduplicatesInput(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	require.NoError(t, session.Query(
		`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
		"m1", "👍", []cassandra.Participant{alice},
	).Exec())

	got, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m1", "m1"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.ElementsMatch(t, []cassandra.Participant{alice}, got["m1"]["👍"])
}

func TestRepository_GetReactionsByMessageIDs_LargeFanOut(t *testing.T) {
	repo, session := newReactionsRepo(t)
	ctx := context.Background()

	alice := cassandra.Participant{ID: "u1", Account: "alice"}
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = fmt.Sprintf("bulk-%03d", i)
		require.NoError(t, session.Query(
			`INSERT INTO message_reactions (message_id, emoji, users) VALUES (?, ?, ?)`,
			ids[i], "👍", []cassandra.Participant{alice},
		).Exec())
	}

	got, err := repo.GetReactionsByMessageIDs(ctx, ids)
	require.NoError(t, err)
	assert.Len(t, got, 100)
}

func TestRepository_GetReactionsByMessageIDs_ContextCancellation(t *testing.T) {
	repo, _ := newReactionsRepo(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before call

	_, err := repo.GetReactionsByMessageIDs(ctx, []string{"m1", "m2", "m3"})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
