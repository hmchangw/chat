//go:build integration

package service_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/hmchangw/chat/history-service/internal/cassrepo"
	"github.com/hmchangw/chat/history-service/internal/models"
	"github.com/hmchangw/chat/history-service/internal/service"
	"github.com/hmchangw/chat/pkg/natsrouter"
)

// setupCassandra provisions a Cassandra container with the four message
// tables plus the required UDTs. Mirrors the helper in cassrepo/integration_test.go.
func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

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

	// messages_by_room
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_room (
		room_id TEXT, created_at TIMESTAMP, message_id TEXT, thread_room_id TEXT,
		sender FROZEN<"Participant">, target_user FROZEN<"Participant">, msg TEXT,
		mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>,
		file FROZEN<"File">, card FROZEN<"Card">, card_action FROZEN<"CardAction">,
		tshow BOOLEAN, tcount INT, thread_parent_id TEXT, thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>, deleted BOOLEAN,
		type TEXT, sys_msg_data BLOB, site_id TEXT, edited_at TIMESTAMP, updated_at TIMESTAMP,
		PRIMARY KEY ((room_id), created_at, message_id)
	) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`).Exec())

	// messages_by_id
	require.NoError(t, session.Query(`CREATE TABLE IF NOT EXISTS chat_test.messages_by_id (
		message_id TEXT, room_id TEXT, thread_room_id TEXT,
		sender FROZEN<"Participant">, target_user FROZEN<"Participant">, msg TEXT,
		mentions SET<FROZEN<"Participant">>, attachments LIST<BLOB>,
		file FROZEN<"File">, card FROZEN<"Card">, card_action FROZEN<"CardAction">,
		tshow BOOLEAN, tcount INT, thread_parent_id TEXT, thread_parent_created_at TIMESTAMP,
		quoted_parent_message FROZEN<"QuotedParentMessage">, visible_to TEXT, unread BOOLEAN,
		reactions MAP<TEXT, FROZEN<SET<FROZEN<"Participant">>>>, deleted BOOLEAN,
		type TEXT, sys_msg_data BLOB, site_id TEXT, edited_at TIMESTAMP, created_at TIMESTAMP,
		updated_at TIMESTAMP, pinned_at TIMESTAMP, pinned_by FROZEN<"Participant">,
		PRIMARY KEY (message_id, created_at)
	) WITH CLUSTERING ORDER BY (created_at DESC)`).Exec())

	// thread_messages_by_room and pinned_messages_by_room aren't needed for the
	// top-level edit flow exercised here; the cassrepo integration tests cover
	// those branches directly. Keeping the setup minimal reduces container-start time.

	cluster.Keyspace = "chat_test"
	ksSession, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

// recordingPublisher captures every Publish call for assertions.
type recordingPublisher struct {
	mu   sync.Mutex
	sent []recordedMessage
}

type recordedMessage struct {
	Subject string
	Data    []byte
}

func (p *recordingPublisher) Publish(_ context.Context, subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(data))
	copy(cp, data)
	p.sent = append(p.sent, recordedMessage{Subject: subj, Data: cp})
	return nil
}

// alwaysSubscribedRepo stubs SubscriptionRepository so the subscription gate passes.
type alwaysSubscribedRepo struct{}

func (alwaysSubscribedRepo) GetHistorySharedSince(_ context.Context, _, _ string) (*time.Time, bool, error) {
	return nil, true, nil
}

func TestEditMessage_Integration(t *testing.T) {
	session := setupCassandra(t)
	repo := cassrepo.NewRepository(session)
	pub := &recordingPublisher{}
	svc := service.New(repo, alwaysSubscribedRepo{}, pub)

	sender := models.Participant{ID: "u1", Account: "alice"}
	roomID := "r-integ"
	msgID := "m-integ"
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	// Seed the message directly via CQL (bypassing message-worker).
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_id (message_id, room_id, created_at, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		msgID, roomID, createdAt, sender, "original", "",
	).Exec())
	require.NoError(t, session.Query(
		`INSERT INTO messages_by_room (room_id, created_at, message_id, sender, msg, thread_parent_id) VALUES (?, ?, ?, ?, ?, ?)`,
		roomID, createdAt, msgID, sender, "original", "",
	).Exec())

	// Call the handler directly with a prepared natsrouter.Context.
	c := natsrouter.NewContext(map[string]string{"account": "alice", "roomID": roomID})
	resp, err := svc.EditMessage(c, models.EditMessageRequest{
		MessageID: msgID,
		NewMsg:    "edited via integration test",
	})
	require.NoError(t, err)
	assert.Equal(t, msgID, resp.MessageID)
	assert.NotZero(t, resp.EditedAt)

	// Cassandra: both tables should reflect the edit.
	var gotMsg string
	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_id WHERE message_id = ? AND created_at = ?`,
		msgID, createdAt,
	).Scan(&gotMsg))
	assert.Equal(t, "edited via integration test", gotMsg)

	require.NoError(t, session.Query(
		`SELECT msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		roomID, createdAt, msgID,
	).Scan(&gotMsg))
	assert.Equal(t, "edited via integration test", gotMsg)

	// Publisher: exactly one event captured, on the right subject, with the right payload.
	pub.mu.Lock()
	defer pub.mu.Unlock()
	require.Len(t, pub.sent, 1)
	assert.Equal(t, "chat.room."+roomID+".event", pub.sent[0].Subject)

	var evt models.MessageEditedEvent
	require.NoError(t, json.Unmarshal(pub.sent[0].Data, &evt))
	assert.Equal(t, "message_edited", evt.Type)
	assert.Equal(t, roomID, evt.RoomID)
	assert.Equal(t, msgID, evt.MessageID)
	assert.Equal(t, "edited via integration test", evt.NewMsg)
	assert.Equal(t, "alice", evt.EditedBy)
	assert.NotZero(t, evt.Timestamp)
	assert.NotZero(t, evt.EditedAt)
}
