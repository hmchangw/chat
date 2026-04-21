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
		`CREATE TYPE IF NOT EXISTS chat_test."QuotedParentMessage" (message_id TEXT, room_id TEXT, sender FROZEN<"Participant">, created_at TIMESTAMP, msg TEXT, mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>, message_link TEXT)`,
	} {
		require.NoError(t, session.Query(cql).Exec())
	}

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
		room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		thread_room_id TEXT,
		sender FROZEN<"Participant">,
		target_user FROZEN<"Participant">,
		msg TEXT,
		mentions SET<FROZEN<"Participant">>,
		attachments LIST<BLOB>,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		card_action FROZEN<"CardAction">,
		tshow BOOLEAN,
		tcount INT,
		thread_parent_id TEXT,
		thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">,
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

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
		message_id TEXT,
		room_id TEXT,
		thread_room_id TEXT,
		sender FROZEN<"Participant">,
		target_user FROZEN<"Participant">,
		msg TEXT,
		mentions SET<FROZEN<"Participant">>,
		attachments LIST<BLOB>,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		card_action FROZEN<"CardAction">,
		tshow BOOLEAN,
		tcount INT,
		thread_parent_id TEXT,
		thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
		deleted BOOLEAN,
		type TEXT,
		sys_msg_data BLOB,
		site_id TEXT,
		edited_at TIMESTAMP,
		created_at TIMESTAMP,
		updated_at TIMESTAMP,
		pinned_at TIMESTAMP,
		pinned_by FROZEN<"Participant">,
		PRIMARY KEY (message_id, created_at)
	) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec())

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.thread_messages_by_room (
		room_id TEXT,
		thread_room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		thread_parent_id TEXT,
		sender FROZEN<"Participant">,
		target_user FROZEN<"Participant">,
		msg TEXT,
		mentions SET<FROZEN<"Participant">>,
		attachments LIST<BLOB>,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		card_action FROZEN<"CardAction">,
		quoted_parent_message FROZEN<"QuotedParentMessage">,
		visible_to TEXT,
		unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>,
		deleted BOOLEAN,
		type TEXT,
		sys_msg_data BLOB,
		site_id TEXT,
		edited_at TIMESTAMP,
		updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), thread_room_id, created_at, message_id)
	) WITH CLUSTERING ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)`).Exec())

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

func TestRepository_GetMessageByID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "user1"}
	ts := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg) VALUES (?, ?, ?, ?, ?)`,
		"m1", "r1", ts, sender, "hello",
	).Exec())

	msg, err := repo.GetMessageByID(ctx, "m1")
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "m1", msg.MessageID)
	assert.Equal(t, "r1", msg.RoomID)
	assert.Equal(t, "hello", msg.Msg)
}

func TestRepository_GetMessageByID_NotFound(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	msg, err := repo.GetMessageByID(ctx, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestRepository_GetMessagesBefore_ThreadRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "user1"}
	ts := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_room_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"r-thread", ts, "m-thread", sender, "reply", "tr-42",
	).Exec())

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetMessagesBefore(ctx, "r-thread", ts.Add(1*time.Minute), q)
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	assert.Equal(t, "tr-42", page.Data[0].ThreadRoomID)
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
	quotedSender := models.Participant{ID: "u5", Account: "eve"}
	quotedMsg := models.QuotedParentMessage{
		MessageID: "m-quoted", RoomID: "r-full", Sender: quotedSender,
		CreatedAt: ts.Add(-30 * time.Minute), Msg: "original message", MessageLink: "https://chat.example.com/r-full/m-quoted",
	}
	pinnedAt := ts.Add(2 * time.Hour)
	pinnedBy := models.Participant{ID: "u9", Account: "pinner"}

	insertCQL := `INSERT INTO messages_by_id (room_id, created_at, message_id, sender, target_user, msg, mentions, attachments, file, card, card_action, tshow, thread_parent_id, thread_parent_created_at, quoted_parent_message, visible_to, unread, reactions, deleted, type, sys_msg_data, site_id, edited_at, updated_at, thread_room_id, pinned_at, pinned_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertArgs := []any{
		"r-full", ts, "m-full",
		sender, target, "hello world",
		[]models.Participant{mentionUser},
		[][]byte{[]byte("attach1"), []byte("attach2")},
		file, card, cardAction,
		true, "m-parent", threadParent, quotedMsg, "u1", true,
		map[string][]models.Participant{"thumbsup": {reactUser}},
		true, "user_joined", []byte("sys-data"),
		"site-remote", editedAt, updatedAt,
		"N/A", pinnedAt, pinnedBy,
	}
	require.NoError(t, session.Query(insertCQL, insertArgs...).Exec())

	msg, err := repo.GetMessageByID(ctx, "m-full")
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
	assert.Equal(t, "m-parent", msg.ThreadParentID)
	require.NotNil(t, msg.ThreadParentCreatedAt)
	assert.Equal(t, threadParent.UTC(), msg.ThreadParentCreatedAt.UTC())

	// QuotedParentMessage UDT
	require.NotNil(t, msg.QuotedParentMessage)
	assert.Equal(t, "m-quoted", msg.QuotedParentMessage.MessageID)
	assert.Equal(t, "r-full", msg.QuotedParentMessage.RoomID)
	assert.Equal(t, "u5", msg.QuotedParentMessage.Sender.ID)
	assert.Equal(t, "eve", msg.QuotedParentMessage.Sender.Account)
	assert.Equal(t, "original message", msg.QuotedParentMessage.Msg)
	assert.Equal(t, "https://chat.example.com/r-full/m-quoted", msg.QuotedParentMessage.MessageLink)

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

	// messages_by_id extra columns
	assert.Equal(t, "N/A", msg.ThreadRoomID)
	require.NotNil(t, msg.PinnedAt)
	assert.Equal(t, pinnedAt.UTC(), msg.PinnedAt.UTC())
	require.NotNil(t, msg.PinnedBy)
	assert.Equal(t, "u9", msg.PinnedBy.ID)
	assert.Equal(t, "pinner", msg.PinnedBy.Account)
}

func seedThreadMessages(t *testing.T, session *gocql.Session, roomID, threadRoomID, parentID string, base time.Time, count int) {
	t.Helper()
	sender := models.Participant{ID: "u1", Account: "user1"}
	for i := 0; i < count; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		err := session.Query(
			`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, thread_parent_id, sender, msg) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			roomID, threadRoomID, ts, fmt.Sprintf("%s-reply-%d", threadRoomID, i), parentID, sender, fmt.Sprintf("reply-%d", i),
		).Exec()
		require.NoError(t, err)
	}
}

func TestRepository_GetThreadMessages_IsolatesByThreadRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 3)
	seedThreadMessages(t, session, "r-A", "tr-2", "m-parent-2", base, 5)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	page1, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, page1.Data, 3)
	for _, m := range page1.Data {
		assert.Equal(t, "tr-1", m.ThreadRoomID)
		assert.Equal(t, "m-parent-1", m.ThreadParentID)
	}

	page2, err := repo.GetThreadMessages(ctx, "r-A", "tr-2", q)
	require.NoError(t, err)
	assert.Len(t, page2.Data, 5)
	for _, m := range page2.Data {
		assert.Equal(t, "tr-2", m.ThreadRoomID)
	}
}

func TestRepository_GetThreadMessages_IsolatesByRoomID(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-A", base, 2)
	seedThreadMessages(t, session, "r-B", "tr-1", "m-parent-B", base, 4)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	pageA, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, pageA.Data, 2)
	for _, m := range pageA.Data {
		assert.Equal(t, "r-A", m.RoomID)
	}

	pageB, err := repo.GetThreadMessages(ctx, "r-B", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, pageB.Data, 4)
	for _, m := range pageB.Data {
		assert.Equal(t, "r-B", m.RoomID)
	}
}

func TestRepository_GetThreadMessages_OrdersDescByCreatedAt(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 4)

	q, err := ParsePageRequest("", 100)
	require.NoError(t, err)

	page, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	require.Len(t, page.Data, 4)
	for i := 0; i < len(page.Data)-1; i++ {
		assert.True(t, page.Data[i].CreatedAt.After(page.Data[i+1].CreatedAt),
			"expected DESC order at index %d", i)
	}
}

func TestRepository_GetThreadMessages_Pagination(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()
	base := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	seedThreadMessages(t, session, "r-A", "tr-1", "m-parent-1", base, 7)

	q, err := ParsePageRequest("", 3)
	require.NoError(t, err)

	page1, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q)
	require.NoError(t, err)
	assert.Len(t, page1.Data, 3)
	assert.True(t, page1.HasNext)
	require.NotEmpty(t, page1.NextCursor)

	q2, err := ParsePageRequest(page1.NextCursor, 3)
	require.NoError(t, err)
	page2, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q2)
	require.NoError(t, err)
	assert.Len(t, page2.Data, 3)

	q3, err := ParsePageRequest(page2.NextCursor, 3)
	require.NoError(t, err)
	page3, err := repo.GetThreadMessages(ctx, "r-A", "tr-1", q3)
	require.NoError(t, err)
	assert.Len(t, page3.Data, 1)
	assert.False(t, page3.HasNext)

	// No overlap, no gaps.
	seen := map[string]bool{}
	for _, m := range page1.Data {
		seen[m.MessageID] = true
	}
	for _, m := range page2.Data {
		assert.False(t, seen[m.MessageID], "page2 overlaps page1: %s", m.MessageID)
		seen[m.MessageID] = true
	}
	for _, m := range page3.Data {
		assert.False(t, seen[m.MessageID], "page3 overlaps earlier pages: %s", m.MessageID)
		seen[m.MessageID] = true
	}
	assert.Len(t, seen, 7)
}

func TestRepository_GetThreadMessages_EmptyWhenThreadUnknown(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetThreadMessages(ctx, "r-A", "tr-nonexistent", q)
	require.NoError(t, err)
	assert.Empty(t, page.Data)
	assert.False(t, page.HasNext)
	assert.Empty(t, page.NextCursor)
}

func TestRepository_GetThreadMessages_ColumnScan(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	editedAt := ts.Add(5 * time.Minute)
	updatedAt := ts.Add(10 * time.Minute)

	sender := models.Participant{ID: "u1", EngName: "Alice", CompanyName: "Acme", AppID: "app1", AppName: "MyApp", IsBot: false, Account: "alice"}
	target := models.Participant{ID: "u2", Account: "bob"}
	mentionUser := models.Participant{ID: "u3", Account: "charlie"}
	reactUser := models.Participant{ID: "u4", Account: "dave"}
	file := models.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"}
	card := models.Card{Template: "approval", Data: []byte("card-data")}
	cardAction := models.CardAction{Verb: "approve", Text: "Approve", CardID: "c1", DisplayText: "Click", HideExecLog: true, CardTmID: "tm1", Data: []byte("action-data")}
	quotedSender := models.Participant{ID: "u5", Account: "eve"}
	quotedMsg := models.QuotedParentMessage{
		MessageID: "m-quoted", RoomID: "r-thread-full", Sender: quotedSender,
		CreatedAt: ts.Add(-30 * time.Minute), Msg: "original message", MessageLink: "https://chat.example.com/r-thread-full/m-quoted",
	}

	insertCQL := `INSERT INTO thread_messages_by_room (
        room_id, thread_room_id, created_at, message_id, thread_parent_id,
        sender, target_user, msg, mentions, attachments, file, card, card_action,
        quoted_parent_message, visible_to, unread, reactions, deleted,
        type, sys_msg_data, site_id, edited_at, updated_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	insertArgs := []any{
		"r-thread-full", "tr-full", ts, "m-reply-full", "m-thread-parent",
		sender, target, "thread reply body",
		[]models.Participant{mentionUser},
		[][]byte{[]byte("attach1"), []byte("attach2")},
		file, card, cardAction,
		quotedMsg, "u1", true,
		map[string][]models.Participant{"thumbsup": {reactUser}},
		true, "user_joined", []byte("sys-data"),
		"site-remote", editedAt, updatedAt,
	}
	require.NoError(t, session.Query(insertCQL, insertArgs...).Exec())

	q, err := ParsePageRequest("", 10)
	require.NoError(t, err)

	page, err := repo.GetThreadMessages(ctx, "r-thread-full", "tr-full", q)
	require.NoError(t, err)
	require.Len(t, page.Data, 1)
	msg := page.Data[0]

	// Primary key + thread linkage
	assert.Equal(t, "r-thread-full", msg.RoomID)
	assert.Equal(t, "tr-full", msg.ThreadRoomID)
	assert.Equal(t, ts.UTC(), msg.CreatedAt.UTC())
	assert.Equal(t, "m-reply-full", msg.MessageID)
	assert.Equal(t, "m-thread-parent", msg.ThreadParentID)

	// Sender UDT
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
	assert.Equal(t, "thread reply body", msg.Msg)

	// Mentions
	require.Len(t, msg.Mentions, 1)
	assert.Equal(t, "u3", msg.Mentions[0].ID)
	assert.Equal(t, "charlie", msg.Mentions[0].Account)

	// Attachments
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

	// QuotedParentMessage UDT
	require.NotNil(t, msg.QuotedParentMessage)
	assert.Equal(t, "m-quoted", msg.QuotedParentMessage.MessageID)
	assert.Equal(t, "r-thread-full", msg.QuotedParentMessage.RoomID)
	assert.Equal(t, "u5", msg.QuotedParentMessage.Sender.ID)
	assert.Equal(t, "eve", msg.QuotedParentMessage.Sender.Account)
	assert.Equal(t, "original message", msg.QuotedParentMessage.Msg)
	assert.Equal(t, "https://chat.example.com/r-thread-full/m-quoted", msg.QuotedParentMessage.MessageLink)

	// Scalars
	assert.Equal(t, "u1", msg.VisibleTo)
	assert.True(t, msg.Unread)
	assert.True(t, msg.Deleted)
	assert.Equal(t, "user_joined", msg.Type)
	assert.Equal(t, []byte("sys-data"), msg.SysMsgData)
	assert.Equal(t, "site-remote", msg.SiteID)

	// Reactions (MAP<TEXT, FROZEN<SET<FROZEN<Participant>>>>)
	require.Contains(t, msg.Reactions, "thumbsup")
	require.Len(t, msg.Reactions["thumbsup"], 1)
	assert.Equal(t, "u4", msg.Reactions["thumbsup"][0].ID)
	assert.Equal(t, "dave", msg.Reactions["thumbsup"][0].Account)

	// Timestamps
	require.NotNil(t, msg.EditedAt)
	assert.Equal(t, editedAt.UTC(), msg.EditedAt.UTC())
	require.NotNil(t, msg.UpdatedAt)
	assert.Equal(t, updatedAt.UTC(), msg.UpdatedAt.UTC())

	// Columns that DON'T exist on thread_messages_by_room must remain at zero value.
	assert.False(t, msg.TShow)
	assert.Equal(t, 0, msg.TCount)
	assert.Nil(t, msg.ThreadParentCreatedAt)
	assert.Nil(t, msg.PinnedAt)
	assert.Nil(t, msg.PinnedBy)
}
