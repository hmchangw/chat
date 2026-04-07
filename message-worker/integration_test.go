//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/cassandra"

	"github.com/hmchangw/chat/pkg/model"
)

func setupCassandra(t *testing.T) *gocql.Session {
	t.Helper()
	ctx := context.Background()
	container, err := cassandra.Run(ctx, "cassandra:5")
	if err != nil {
		t.Fatalf("start cassandra: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.ConnectionHost(ctx)
	if err != nil {
		t.Fatalf("get cassandra host: %v", err)
	}

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	session, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("create cassandra session: %v", err)
	}
	t.Cleanup(func() { session.Close() })

	stmts := []string{
		`CREATE KEYSPACE IF NOT EXISTS message_worker_test WITH replication = {'class': 'SimpleStrategy', 'replication_factor': 1}`,
		`CREATE TYPE IF NOT EXISTS message_worker_test."Participant" (id TEXT, user_name TEXT, eng_name TEXT, company_name TEXT, app_id TEXT, app_name TEXT, is_bot BOOLEAN)`,
		`CREATE TYPE IF NOT EXISTS message_worker_test."File" (id TEXT, name TEXT, type TEXT)`,
		`CREATE TYPE IF NOT EXISTS message_worker_test."Card" (template TEXT, data BLOB)`,
		`CREATE TYPE IF NOT EXISTS message_worker_test."CardAction" (verb TEXT, text TEXT, card_id TEXT, display_text TEXT, hide_exec_log BOOLEAN, card_tmid TEXT, data BLOB)`,
		`CREATE TABLE IF NOT EXISTS message_worker_test.messages_by_room (
			room_id TEXT,
			created_at TIMESTAMP,
			message_id TEXT,
			sender FROZEN<message_worker_test."Participant">,
			target_user FROZEN<message_worker_test."Participant">,
			msg TEXT,
			mentions SET<FROZEN<message_worker_test."Participant">>,
			attachments LIST<BLOB>,
			file FROZEN<message_worker_test."File">,
			card FROZEN<message_worker_test."Card">,
			card_action FROZEN<message_worker_test."CardAction">,
			tshow BOOLEAN,
			thread_parent_created_at TIMESTAMP,
			visible_to TEXT,
			unread BOOLEAN,
			reactions MAP<TEXT, FROZEN<SET<FROZEN<message_worker_test."Participant">>>>,
			deleted BOOLEAN,
			sys_msg_type TEXT,
			sys_msg_data BLOB,
			federate_from TEXT,
			edited_at TIMESTAMP,
			updated_at TIMESTAMP,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`,
	}
	for _, stmt := range stmts {
		if err := session.Query(stmt).Exec(); err != nil {
			t.Fatalf("exec schema statement: %v\nstatement: %s", err, stmt)
		}
	}

	cluster.Keyspace = "message_worker_test"
	ksSession, err := cluster.CreateSession()
	if err != nil {
		t.Fatalf("create keyspace session: %v", err)
	}
	t.Cleanup(func() { ksSession.Close() })
	return ksSession
}

func TestCassandraStore_SaveMessage(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	edited := now.Add(5 * time.Minute)

	msg := model.Message{
		ID:         "m1",
		RoomID:     "r1",
		CreatedAt:  now,
		Sender:     model.Participant{ID: "u1", UserName: "alice"},
		TargetUser: &model.Participant{ID: "u2", UserName: "bob"},
		Content:    "hello world",
		Mentions:   []model.Participant{{ID: "u3", UserName: "charlie"}},
		File:       &model.File{ID: "f1", Name: "doc.pdf", Type: "application/pdf"},
		TShow:      true,
		VisibleTo:  "u1",
		EditedAt:   &edited,
	}
	err := store.SaveMessage(ctx, msg)
	require.NoError(t, err)

	var gotMsg, gotVisibleTo, gotMessageID string
	var gotSender model.Participant
	var gotTShow bool
	err = cassSession.Query(
		`SELECT message_id, sender, msg, tshow, visible_to FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		"r1", now, "m1",
	).Scan(&gotMessageID, &gotSender, &gotMsg, &gotTShow, &gotVisibleTo)
	require.NoError(t, err)
	assert.Equal(t, "m1", gotMessageID)
	assert.Equal(t, "u1", gotSender.ID)
	assert.Equal(t, "alice", gotSender.UserName)
	assert.Equal(t, "hello world", gotMsg)
	assert.True(t, gotTShow)
	assert.Equal(t, "u1", gotVisibleTo)
}

func TestCassandraStore_SaveMessage_Minimal(t *testing.T) {
	cassSession := setupCassandra(t)
	store := NewCassandraStore(cassSession)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	msg := model.Message{
		ID:        "m2",
		RoomID:    "r1",
		CreatedAt: now,
		Sender:    model.Participant{ID: "u1", UserName: "alice"},
		Content:   "hi",
	}
	err := store.SaveMessage(ctx, msg)
	require.NoError(t, err)

	var gotMsg string
	var gotSender model.Participant
	err = cassSession.Query(
		`SELECT sender, msg FROM messages_by_room WHERE room_id = ? AND created_at = ? AND message_id = ?`,
		"r1", now, "m2",
	).Scan(&gotSender, &gotMsg)
	require.NoError(t, err)
	assert.Equal(t, "alice", gotSender.UserName)
	assert.Equal(t, "hi", gotMsg)
}
