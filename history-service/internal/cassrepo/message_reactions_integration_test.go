//go:build integration

package cassrepo

import (
	"context"
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
