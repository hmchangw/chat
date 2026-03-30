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
	"github.com/testcontainers/testcontainers-go/modules/cassandra"
)

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	require.NoError(t, err)

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { session.Close() })

	require.NoError(t, session.Query(`CREATE KEYSPACE IF NOT EXISTS chat_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`).Exec())
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages (room_id text, created_at timestamp, id text, user_id text, content text, PRIMARY KEY (room_id, created_at)) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec())

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(`INSERT INTO messages (room_id, created_at, id, user_id, content) VALUES (?, ?, ?, ?, ?)`,
			roomID, ts, fmt.Sprintf("m%d", i), "u1", fmt.Sprintf("msg-%d", i)).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetMessagesBefore(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParseQuery("", 3)
	require.NoError(t, err)

	page, err := repo.GetMessagesBefore(ctx, "r1", base, base.Add(10*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3)
	assert.True(t, page.Data[0].CreatedAt.After(page.Data[1].CreatedAt))
}

func TestRepository_GetMessagesAfter(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParseQuery("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesAfter(ctx, "r1", base.Add(2*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt))
}

func TestRepository_GetMessageByID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 3)

	msg, err := repo.GetMessageByID(ctx, "r1", "m1")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "m1", msg.ID)
	assert.Equal(t, "r1", msg.RoomID)
}

func TestRepository_GetMessageByID_NotFound(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	msg, err := repo.GetMessageByID(ctx, "r1", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestRepository_GetSurroundingMessages(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 10)

	before, after, err := repo.GetSurroundingMessages(ctx, "r1", "m5", 6)
	require.NoError(t, err)
	assert.NotEmpty(t, before)
	assert.NotEmpty(t, after)
}
