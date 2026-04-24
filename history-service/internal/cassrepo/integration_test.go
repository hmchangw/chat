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
		PRIMARY KEY ((room_id), thread_room_id, created_at, message_id)
	) WITH CLUSTERING ORDER BY (thread_room_id ASC, created_at DESC, message_id DESC)`).Exec())

	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.pinned_messages_by_room (
		room_id TEXT,
		created_at TIMESTAMP,
		message_id TEXT,
		sender FROZEN<"Participant">,
		msg TEXT,
		file FROZEN<"File">,
		card FROZEN<"Card">,
		deleted BOOLEAN,
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

func TestRepository_UpdateMessageContent_TopLevel(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-top"
	msgID := "m-top"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message in both tables (ThreadParentID == "").
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// messages_by_id updated
	var gotMsg string
	var gotEditedAt time.Time
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	// messages_by_room updated
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	// thread_messages_by_room must NOT have a phantom row for this message
	var threadCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM thread_messages_by_room WHERE room_id = ?`,
		roomID,
	).Scan(&threadCount))
	assert.Equal(t, 0, threadCount, "top-level edit must not write to thread_messages_by_room")
}

func TestRepository_UpdateMessageContent_ThreadReply(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-thread"
	threadRoomID := "thread-1"
	parentID := "m-parent"
	msgID := "m-reply"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a thread reply in messages_by_id and thread_messages_by_room.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_room_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", parentID, threadRoomID,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, createdAt, msgID, sender, "original", parentID,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: parentID,
		ThreadRoomID:   threadRoomID,
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// messages_by_id updated
	var gotMsg string
	var gotEditedAt time.Time
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	// thread_messages_by_room updated (verify with the full PK including thread_room_id)
	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, threadRoomID, createdAt, msgID,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg)
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	// messages_by_room must NOT have a phantom row for this thread reply
	var roomCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&roomCount))
	assert.Equal(t, 0, roomCount, "thread-reply edit must not write to messages_by_room")
}

func TestRepository_UpdateMessageContent_Pinned(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-pin"
	msgID := "m-pin"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	pinnedAt := createdAt.Add(10 * time.Second)

	// Seed a top-level pinned message in all three tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, pinned_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "", pinnedAt,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO pinned_messages_by_room (room_id, created_at, message_id, sender, msg) VALUES (?, ?, ?, ?, ?)`,
		roomID, pinnedAt, msgID, sender, "original",
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
		PinnedAt:       &pinnedAt,
	}
	editedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.UpdateMessageContent(ctx, msg, "edited", editedAt))

	// All three affected tables updated
	var gotMsg string
	var gotEditedAt time.Time

	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg, "messages_by_id should reflect the edit")
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg, "messages_by_room should reflect the edit")
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)

	require.NoError(t, session.Query(
		`SELECT msg, edited_at FROM pinned_messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, pinnedAt, msgID,
	).Scan(&gotMsg, &gotEditedAt))
	assert.Equal(t, "edited", gotMsg, "pinned_messages_by_room should reflect the edit")
	assert.WithinDuration(t, editedAt, gotEditedAt, time.Second)
}

func TestRepository_SoftDeleteMessage_TopLevel(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-top"
	msgID := "m-del-top"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed a top-level message in both tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "", false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "", false,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
	}
	deletedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// messages_by_id: deleted = true, msg retained, updated_at advanced
	var gotDeleted bool
	var gotMsg string
	var gotUpdatedAt time.Time
	require.NoError(t, session.Query(
		`SELECT deleted, msg, updated_at FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotDeleted, &gotMsg, &gotUpdatedAt))
	assert.True(t, gotDeleted, "messages_by_id.deleted should be true")
	assert.Equal(t, "original", gotMsg, "msg content must be preserved")
	assert.WithinDuration(t, deletedAt, gotUpdatedAt, time.Second)

	// messages_by_room: same assertions
	require.NoError(t, session.Query(
		`SELECT deleted, msg, updated_at FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotDeleted, &gotMsg, &gotUpdatedAt))
	assert.True(t, gotDeleted)
	assert.Equal(t, "original", gotMsg, "msg content must be preserved")
	assert.WithinDuration(t, deletedAt, gotUpdatedAt, time.Second)

	// thread_messages_by_room must have no phantom row
	var threadCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM thread_messages_by_room WHERE room_id = ?`,
		roomID,
	).Scan(&threadCount))
	assert.Equal(t, 0, threadCount, "top-level soft-delete must not write to thread_messages_by_room")
}

func TestRepository_SoftDeleteMessage_ThreadReply(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-thread"
	threadRoomID := "thread-del-1"
	parentID := "m-del-parent"
	parentCreatedAt := time.Now().UTC().Truncate(time.Millisecond)
	replyID := "m-del-reply"
	replyCreatedAt := parentCreatedAt.Add(10 * time.Second)

	// Seed the parent (so the thread_parent_created_at reference is real and
	// Task 7's tcount decrement has a target row to work with later).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, deleted, tcount) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		parentID, roomID, parentCreatedAt, sender, "parent", "", false, 1,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted, tcount) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, parentCreatedAt, parentID, sender, "parent", "", false, 1,
	).Exec())

	// Seed the thread reply in messages_by_id and thread_messages_by_room.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, thread_parent_created_at, thread_room_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		replyID, roomID, replyCreatedAt, sender, "reply", parentID, parentCreatedAt, threadRoomID, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO thread_messages_by_room (room_id, thread_room_id, created_at, message_id, sender, msg, thread_parent_id, thread_parent_created_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		roomID, threadRoomID, replyCreatedAt, replyID, sender, "reply", parentID, parentCreatedAt, false,
	).Exec())

	parentCreatedAtPtr := parentCreatedAt
	msg := &models.Message{
		MessageID:             replyID,
		RoomID:                roomID,
		CreatedAt:             replyCreatedAt,
		Sender:                sender,
		ThreadParentID:        parentID,
		ThreadParentCreatedAt: &parentCreatedAtPtr,
		ThreadRoomID:          threadRoomID,
	}
	deletedAt := replyCreatedAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// messages_by_id: reply now deleted
	var gotDeleted bool
	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		replyID, replyCreatedAt,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted)

	// thread_messages_by_room: reply now deleted (full PK including thread_room_id)
	require.NoError(t, session.Query(
		`SELECT deleted FROM thread_messages_by_room WHERE room_id = ? AND thread_room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, threadRoomID, replyCreatedAt, replyID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted)

	// messages_by_room must NOT have a phantom row for this thread reply
	var roomCount int
	require.NoError(t, session.Query(
		`SELECT COUNT(*) FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, replyCreatedAt, replyID,
	).Scan(&roomCount))
	assert.Equal(t, 0, roomCount, "thread-reply soft-delete must not write to messages_by_room")

	// Parent's tcount should still be 1 — tcount decrement is added in Task 7.
	var gotTcount int
	require.NoError(t, session.Query(
		`SELECT tcount FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, parentCreatedAt, parentID,
	).Scan(&gotTcount))
	assert.Equal(t, 1, gotTcount, "tcount decrement is not yet implemented — expected unchanged")
}

func TestRepository_SoftDeleteMessage_Pinned(t *testing.T) {
	session := setupCassandra(t)
	repo := NewRepository(session)
	ctx := context.Background()

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "room-del-pin"
	msgID := "m-del-pin"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	pinnedAt := createdAt.Add(10 * time.Second)

	// Seed a top-level pinned message in all three tables.
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id, pinned_at, deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "content", "", pinnedAt, false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "content", "", false,
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO pinned_messages_by_room (room_id, created_at, message_id, sender, msg, deleted) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, pinnedAt, msgID, sender, "content", false,
	).Exec())

	msg := &models.Message{
		MessageID:      msgID,
		RoomID:         roomID,
		CreatedAt:      createdAt,
		Sender:         sender,
		ThreadParentID: "",
		PinnedAt:       &pinnedAt,
	}
	deletedAt := createdAt.Add(time.Minute)
	require.NoError(t, repo.SoftDeleteMessage(ctx, msg, deletedAt))

	// All three tables should reflect deleted = true
	var gotDeleted bool

	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "messages_by_id should be deleted")

	require.NoError(t, session.Query(
		`SELECT deleted FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "messages_by_room should be deleted")

	require.NoError(t, session.Query(
		`SELECT deleted FROM pinned_messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, pinnedAt, msgID,
	).Scan(&gotDeleted))
	assert.True(t, gotDeleted, "pinned_messages_by_room should be deleted")
}
