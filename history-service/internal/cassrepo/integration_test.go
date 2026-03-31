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

	"github.com/hmchangw/chat/history-service/internal/models"
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

	for _, cql := range []string{
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, user_name TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN)`,
		`CREATE TYPE IF NOT EXISTS chat_test."File" (id TEXT, name TEXT, type TEXT)`,
		`CREATE TYPE IF NOT EXISTS chat_test."Card" (template TEXT, data BLOB)`,
		`CREATE TYPE IF NOT EXISTS chat_test."CardAction" (verb TEXT, text TEXT, card_id TEXT, display_text TEXT, hide_exec_log BOOLEAN, card_tmid TEXT, data BLOB)`,
	} {
		require.NoError(t, session.Query(cql).Exec())
	}

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
		room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		sender FROZEN<"Participant">,
		target_user FROZEN<"Participant">,
		msg TEXT,
		mentions SET<FROZEN<"Participant">>,
		attachments LIST<BLOB>,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		card_action FROZEN<"CardAction">,
		tshow BOOLEAN,
		thread_parent_created_at TIMESTAMP,
		visible_to TEXT,
		unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
		deleted BOOLEAN,
		sys_msg_type TEXT,
		sys_msg_data BLOB,
		federate_from TEXT,
		edited_at TIMESTAMP,
		updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), created_at, message_id)
	) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`).Exec())

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func seedMessages(t *testing.T, session *gocql.Session, roomID string, base time.Time, count int) {
	t.Helper()
	sender := models.Participant{ID: "u1", UserName: "user1"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(
			`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
			roomID, ts, fmt.Sprintf("m%d", i), sender, fmt.Sprintf("msg-%d", i),
		).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetMessagesBefore(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page, err := repo.GetMessagesBefore(ctx, "r1", base.Add(10*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3)
	assert.True(t, page.Data[0].CreatedAt.After(page.Data[1].CreatedAt))
}

func TestRepository_GetMessagesBetweenAsc_Exclusive(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesBetweenAsc(ctx, "r1", base.Add(1*time.Minute), base.Add(4*time.Minute), false, q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)                                           // m2 (2min), m3 (3min) — excludes 1min and 4min
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt)) // ASC order
}

func TestRepository_GetMessagesBetweenAsc_Inclusive(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	// inclusive upper bound: includes m4 (4min)
	page, err := repo.GetMessagesBetweenAsc(ctx, "r1", base.Add(1*time.Minute), base.Add(4*time.Minute), true, q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3) // m2, m3, m4
	assert.Equal(t, "m4", page.Data[2].MessageID)
}

func TestRepository_GetMessagesBetweenDesc(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesBetweenDesc(ctx, "r1", base.Add(1*time.Minute), base.Add(4*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)                                          // m2 (2min), m3 (3min) — excludes 1min and 4min
	assert.True(t, page.Data[0].CreatedAt.After(page.Data[1].CreatedAt)) // DESC order
}

func TestRepository_GetMessagesAfter(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesAfter(ctx, "r1", base.Add(2*time.Minute), q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 2)                                           // m3 (3min), m4 (4min) — strictly after 2min
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt)) // ASC order
}

func TestRepository_GetAllMessagesAsc(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 5)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page, err := repo.GetAllMessagesAsc(ctx, "r1", q)
	require.NoError(t, err)
	assert.Len(t, page.Data, 3)
	assert.True(t, page.Data[0].CreatedAt.Before(page.Data[1].CreatedAt)) // ASC order
	assert.True(t, page.HasNext)
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
	assert.Equal(t, "m1", msg.MessageID)
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
