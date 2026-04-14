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
		`CREATE TYPE IF NOT EXISTS chat_test."Participant" (id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN, account TEXT)`,
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
		type TEXT,
		sys_msg_data BLOB,
		site_id TEXT,
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
	sender := models.Participant{ID: "u1", Account: "user1"}
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

func TestRepository_GetMessage(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	seedMessages(t, session, "r1", base, 3)

	msg, err := repo.GetMessage(ctx, "r1", base.Add(1*time.Minute), "m1")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "m1", msg.MessageID)
	assert.Equal(t, "r1", msg.RoomID)
}

func TestRepository_GetMessage_NotFound(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	msg, err := repo.GetMessage(ctx, "r1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestRepository_FullRow_AllColumns(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	ts := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	editedAt := ts.Add(5 * time.Minute)
	updatedAt := ts.Add(10 * time.Minute)
	threadParent := ts.Add(-1 * time.Hour)

	sender := models.Participant{ID: "u1", EngName: "Alice", CompanyName: "Acme", AppID: "app1", AppName: "MyApp", IsBot: false, Account: "alice"}
	target := models.Participant{ID: "u2", Account: "bob"}
	mentionUser := models.Participant{ID: "u3", Account: "charlie"}
	reactUser := models.Participant{ID: "u4", Account: "dave"}
	file := models.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"}
	card := models.Card{Template: "approval", Data: []byte("card-data")}
	cardAction := models.CardAction{Verb: "approve", Text: "Approve", CardID: "c1", DisplayText: "Click", HideExecLog: true, CardTmID: "tm1", Data: []byte("action-data")}

	err := session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, target_user, msg, mentions, attachments, file, card, card_action, tshow, thread_parent_created_at, visible_to, unread, reactions, deleted, type, sys_msg_data, site_id, edited_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"r-full", ts, "m-full",
		sender, target, "hello world",
		[]models.Participant{mentionUser},
		[][]byte{[]byte("attach1"), []byte("attach2")},
		file, card, cardAction,
		true, threadParent, "u1", true,
		map[string][]models.Participant{"thumbsup": {reactUser}},
		true, "user_joined", []byte("sys-data"),
		"site-remote", editedAt, updatedAt,
	).Exec()
	require.NoError(t, err)

	msg, err := repo.GetMessage(ctx, "r-full", ts, "m-full")
	require.NoError(t, err)
	require.NotNil(t, msg)

	// Primary key fields
	assert.Equal(t, "r-full", msg.RoomID)
	assert.Equal(t, ts.UTC(), msg.CreatedAt.UTC())
	assert.Equal(t, "m-full", msg.MessageID)

	// Sender UDT (all fields)
	assert.Equal(t, "u1", msg.Sender.ID)
	assert.Equal(t, "alice", msg.Sender.Account)
	assert.Equal(t, "Alice", msg.Sender.EngName)
	assert.Equal(t, "Acme", msg.Sender.CompanyName)
	assert.Equal(t, "app1", msg.Sender.AppID)
	assert.Equal(t, "MyApp", msg.Sender.AppName)
	assert.False(t, msg.Sender.IsBot)

	// Target user UDT
	require.NotNil(t, msg.TargetUser)
	assert.Equal(t, "u2", msg.TargetUser.ID)
	assert.Equal(t, "bob", msg.TargetUser.Account)

	// Text
	assert.Equal(t, "hello world", msg.Msg)

	// Mentions (SET<FROZEN<Participant>>)
	require.Len(t, msg.Mentions, 1)
	assert.Equal(t, "u3", msg.Mentions[0].ID)
	assert.Equal(t, "charlie", msg.Mentions[0].Account)

	// Attachments (LIST<BLOB>)
	require.Len(t, msg.Attachments, 2)
	assert.Equal(t, []byte("attach1"), msg.Attachments[0])
	assert.Equal(t, []byte("attach2"), msg.Attachments[1])

	// File UDT
	require.NotNil(t, msg.File)
	assert.Equal(t, "f1", msg.File.ID)
	assert.Equal(t, "doc.pdf", msg.File.Name)
	assert.Equal(t, "application/pdf", msg.File.Type)

	// Card UDT
	require.NotNil(t, msg.Card)
	assert.Equal(t, "approval", msg.Card.Template)
	assert.Equal(t, []byte("card-data"), msg.Card.Data)

	// CardAction UDT
	require.NotNil(t, msg.CardAction)
	assert.Equal(t, "approve", msg.CardAction.Verb)
	assert.Equal(t, "Approve", msg.CardAction.Text)
	assert.Equal(t, "c1", msg.CardAction.CardID)
	assert.Equal(t, "Click", msg.CardAction.DisplayText)
	assert.True(t, msg.CardAction.HideExecLog)
	assert.Equal(t, "tm1", msg.CardAction.CardTmID)
	assert.Equal(t, []byte("action-data"), msg.CardAction.Data)

	// Boolean/string fields
	assert.True(t, msg.TShow)
	require.NotNil(t, msg.ThreadParentCreatedAt)
	assert.Equal(t, threadParent.UTC(), msg.ThreadParentCreatedAt.UTC())
	assert.Equal(t, "u1", msg.VisibleTo)
	assert.True(t, msg.Unread)
	assert.True(t, msg.Deleted)
	assert.Equal(t, "user_joined", msg.Type)
	assert.Equal(t, []byte("sys-data"), msg.SysMsgData)
	assert.Equal(t, "site-remote", msg.SiteID)

	// Timestamps
	require.NotNil(t, msg.EditedAt)
	assert.Equal(t, editedAt.UTC(), msg.EditedAt.UTC())
	require.NotNil(t, msg.UpdatedAt)
	assert.Equal(t, updatedAt.UTC(), msg.UpdatedAt.UTC())

	// Reactions (MAP<TEXT, FROZEN<SET<FROZEN<Participant>>>>)
	require.Contains(t, msg.Reactions, "thumbsup")
	require.Len(t, msg.Reactions["thumbsup"], 1)
	assert.Equal(t, "u4", msg.Reactions["thumbsup"][0].ID)
	assert.Equal(t, "dave", msg.Reactions["thumbsup"][0].Account)
}
